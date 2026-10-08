package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/imaging"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestWhichVersionsARegenerationMakesAgain(t *testing.T) {
	f := &registry.FileField{Versions: []registry.Version{
		{Name: "thumb", Params: imaging.Params{MaxWidth: 10, MaxHeight: 10}},
		{Name: "big", Params: imaging.Params{MaxWidth: 100}, Writable: true},
		{Name: "mark", Writable: true},
	}}
	thumb, _ := f.Version("thumb")
	big, _ := f.Version("big")
	image := func(versions map[string]any) map[string]any {
		return map[string]any{"id": "fl_1", "type": "image/png", "versions": versions}
	}
	ready := func(fp string, extra ...any) map[string]any {
		m := map[string]any{"status": "ready", "fingerprint": fp}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i].(string)] = extra[i+1]
		}
		return m
	}
	names := func(ts []imageTarget) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.name)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name        string
		file        map[string]any
		only        string
		missingOnly bool
		want        string
	}{
		{"all current", image(map[string]any{"thumb": ready(thumb.Fingerprint()), "big": ready(big.Fingerprint()), "mark": map[string]any{"status": "empty"}}), "", false, ""},
		{"a changed declaration", image(map[string]any{"thumb": ready("old"), "big": ready(big.Fingerprint())}), "", false, "thumb"},
		{"both changed", image(map[string]any{"thumb": ready("old"), "big": ready("old")}), "", false, "thumb,big"},
		{"only one asked for", image(map[string]any{"thumb": ready("old"), "big": ready("old")}), "big", false, "big"},
		{"a version added later", image(map[string]any{"big": ready(big.Fingerprint())}), "", false, "thumb"},
		{"a file from before versions existed", image(nil), "", false, "thumb,big"},
		{"missing only leaves a changed copy", image(map[string]any{"thumb": ready("old")}), "", true, "big"},
		{"a function's own copy is left alone", image(map[string]any{"thumb": ready("old", "custom", true), "big": ready(big.Fingerprint())}), "", false, ""},
		{"failed is tried again", image(map[string]any{"thumb": map[string]any{"status": "failed", "reason": "timeout"}, "big": ready(big.Fingerprint())}), "", false, "thumb"},
		{"pending is made", image(map[string]any{"thumb": map[string]any{"status": "pending"}, "big": ready(big.Fingerprint())}), "", false, "thumb"},
		{"failed is left when only missing", image(map[string]any{"thumb": map[string]any{"status": "failed"}, "big": ready(big.Fingerprint())}), "", true, ""},
		{"skipped stays skipped", image(map[string]any{"thumb": map[string]any{"status": "skipped", "reason": "unsupported_format"}, "big": ready(big.Fingerprint())}), "", false, ""},
		{"not a picture", map[string]any{"id": "fl_2", "type": "text/plain"}, "", false, ""},
	} {
		if got := names(staleTargets(f, tc.only, tc.file, tc.missingOnly)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// declared changes what the picture field declares for thumb, as an edit of collection.yaml would.
func declareThumb(t *testing.T, f *filesFixture, maxWidth int) {
	t.Helper()
	c, _ := f.reg.Collection("acme", "app", "library")
	pic := c.Files["picture"]
	for i := range pic.Versions {
		if pic.Versions[i].Name == "thumb" {
			pic.Versions[i].Params.MaxWidth, pic.Versions[i].Params.MaxHeight = maxWidth, maxWidth
		}
	}
}

func (f *filesFixture) regenerate(t *testing.T, cred, body string) (int, map[string]any) {
	t.Helper()
	rec, out := f.doRaw(t, "POST", "/v1/acme/_admin/files/versions/regenerate", []byte(body), map[string]string{"Authorization": "Bearer " + cred, "Content-Type": "application/json"})
	return rec.Code, out
}

const regenerateField = `"database": "app", "collection": "library", "field": "picture"`

func TestRegeneratingMakesTheStaleVersionsAgain(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	docID, fileID := f.madePicture(t, w, "picture")
	runImageJobs(t, w)
	state := func(name string) map[string]any {
		return versionsOf(t, f.readDoc(t, docID)["picture"].(map[string]any))[name].(map[string]any)
	}
	oldThumb, oldBig := state("thumb"), state("big")
	if oldThumb["width"] != float64(10) {
		t.Fatalf("thumb before: %v", oldThumb)
	}
	// A function's own copy of big, and a picture from before versions existed.
	runWorker(t, w)
	if rec, _ := f.doRaw(t, "POST", "/v1/acme/app/library/"+docID+"/_files/picture/"+fileID+"/versions/big", []byte(`{"max_width": 4}`), map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}); rec.Code != 200 {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	old := f.newDoc(t, "library")
	oldFile := "fl_old0000000000000001"
	f.seedFile(t, "library", old, "picture", map[string]any{"id": oldFile, "name": "old.png", "size": int64(100), "type": "image/png", "sha256": strings.Repeat("0", 64), "uploaded_at": "2026-10-06T10:00:00Z"})
	f.s3.Put("t/acme/app/library/"+oldFile, realPNG(t, 20, 10))
	text := f.newDoc(t, "library") // a file that isn't a picture is not touched
	f.seedFile(t, "library", text, "picture", map[string]any{"id": "fl_text0000000000000001", "name": "t.txt", "size": int64(3), "type": "text/plain", "sha256": strings.Repeat("0", 64), "uploaded_at": "2026-10-06T10:00:00Z"})

	// The declaration changes: thumb is 6 pixels now.
	declareThumb(t, f, 6)
	version := f.readDoc(t, docID)["_meta"].(map[string]any)["version"]
	code, out := f.regenerate(t, f.key, `{`+regenerateField+`, "version": "thumb"}`)
	if code != 202 || out["id"] == nil || out["status"] != "queued" || out["version"] != "thumb" {
		t.Fatalf("start: %d %v", code, out)
	}
	if code, again := f.regenerate(t, f.key, `{`+regenerateField+`}`); code != 409 || again["error"].(map[string]any)["code"] != "regenerate_running" {
		t.Errorf("a second one for the field: %d %v", code, again)
	}
	jobID := out["id"].(string)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if j, _, _ := f.svc.GetJob(context.Background(), jobID); j.Status == auth.JobDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	job, _, _ := f.svc.GetJob(context.Background(), jobID)
	if job.Status != auth.JobDone || job.Result == nil || job.Result.Status != "ok" {
		t.Fatalf("the job: %+v", job)
	}
	var summary map[string]int
	_ = json.Unmarshal(job.Result.Output, &summary)
	if summary["files"] != 2 || summary["made"] != 2 || summary["failed"] != 0 {
		t.Errorf("summary: %v", summary)
	}
	if len(job.Steps) == 0 || job.Steps[len(job.Steps)-1].Status != auth.StepDone {
		t.Errorf("steps: %+v", job.Steps)
	}

	// thumb is remade at the new size, with a new fingerprint and id; the document's version stayed.
	nt := state("thumb")
	if nt["width"] != float64(6) || nt["id"] == oldThumb["id"] || nt["fingerprint"] == oldThumb["fingerprint"] {
		t.Errorf("thumb after: %v", nt)
	}
	if f.readDoc(t, docID)["_meta"].(map[string]any)["version"] != version {
		t.Error("the document's version changed")
	}
	// big, which a function generated with its own parameters, is left alone.
	if nb := state("big"); nb["custom"] != true || nb["width"] != float64(4) || nb["id"] == oldBig["id"] && false {
		t.Errorf("big after: %v", nb)
	}
	// The picture from before versions existed has both now (only thumb was asked for).
	oldDoc := versionsOf(t, f.readDoc(t, old)["picture"].(map[string]any))
	if th, _ := oldDoc["thumb"].(map[string]any); th["status"] != "ready" || th["width"] != float64(6) {
		t.Errorf("the old picture: %v", oldDoc)
	}
	if oldDoc["big"] != nil {
		t.Errorf("a version that wasn't asked for was made: %v", oldDoc["big"])
	}
	if got := f.readDoc(t, text)["picture"].(map[string]any); got["versions"] != nil {
		t.Errorf("a text file was given versions: %v", got)
	}
	// Nothing is stale now, so another run does nothing; --missing-only adds only what's missing.
	code, out = f.regenerate(t, f.key, `{`+regenerateField+`, "missing_only": true}`)
	if code != 202 {
		t.Fatalf("second start: %d %v", code, out)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if j, _, _ := f.svc.GetJob(context.Background(), out["id"].(string)); j.Status == auth.JobDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	oldDoc = versionsOf(t, f.readDoc(t, old)["picture"].(map[string]any))
	if big, _ := oldDoc["big"].(map[string]any); big["status"] != "ready" {
		t.Errorf("missing-only didn't make the missing version: %v", oldDoc)
	}
	if state("thumb")["id"] != nt["id"] {
		t.Error("missing-only made a copy again")
	}
}

func TestRegenerationRequestsAreChecked(t *testing.T) {
	f := newFilesFixture(t)
	for name, tc := range map[string]struct {
		cred, body string
		want       int
	}{
		"no credentials":           {"", `{` + regenerateField + `}`, 401},
		"an ordinary user":         {f.ada, `{` + regenerateField + `}`, 403},
		"missing fields":           {f.key, `{}`, 400},
		"an unknown parameter":     {f.key, `{` + regenerateField + `, "speed": 3}`, 400},
		"not JSON":                 {f.key, `nope`, 400},
		"a rate out of range":      {f.key, `{` + regenerateField + `, "rate": 0}`, 400},
		"no such field":            {f.key, `{"database": "app", "collection": "library", "field": "nope"}`, 404},
		"no such collection":       {f.key, `{"database": "app", "collection": "nope", "field": "picture"}`, 404},
		"a field with no versions": {f.key, `{"database": "app", "collection": "library", "field": "avatar"}`, 400},
		"a version not declared":   {f.key, `{` + regenerateField + `, "version": "nope"}`, 404},
		"a function-only version":  {f.key, `{` + regenerateField + `, "version": "mark"}`, 400},
	} {
		if code, out := f.regenerate(t, tc.cred, tc.body); code != tc.want {
			t.Errorf("%s: %d %v, want %d", name, code, out, tc.want)
		}
	}
	// It is audited.
	f.regenerate(t, f.key, `{`+regenerateField+`}`)
	if recs, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditVersionsRegenerate}); len(recs) != 1 {
		t.Errorf("audit: %+v", recs)
	}
}

func TestACancelledRegenerationStops(t *testing.T) {
	f := newFilesFixture(t)
	code, out := f.regenerate(t, f.key, `{`+regenerateField+`}`)
	if code != 202 {
		t.Fatal(code, out)
	}
	rec, got := f.doRaw(t, "POST", "/v1/acme/_admin/jobs/"+out["id"].(string)+"/cancel", nil, map[string]string{"Authorization": "Bearer " + f.key})
	if rec.Code != 200 || got["status"] != "done" {
		t.Errorf("cancel: %d %v", rec.Code, got)
	}
	// And it can be started again.
	if code, _ := f.regenerate(t, f.key, `{`+regenerateField+`}`); code != 202 {
		t.Errorf("start again: %d", code)
	}
}

func TestAKilledRegenerationResumes(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	var ids []string
	for i := 0; i < 3; i++ {
		d, _ := f.madePicture(t, w, "picture")
		ids = append(ids, d)
	}
	runImageJobs(t, w)
	declareThumb(t, f, 7)
	_, out := f.regenerate(t, f.key, `{`+regenerateField+`}`)
	jobID := out["id"].(string)

	// A worker claims it and dies; nothing was made.
	if _, found, err := f.svc.ClaimJob(context.Background(), "dead-worker"); err != nil || !found {
		t.Fatalf("claim: %v %v", found, err)
	}
	if w.RunOnce(context.Background()) {
		t.Fatal("a leased job was run")
	}
	*f.clock = f.clock.Add(time.Hour)
	// Another takes it over; the thumbs of all three are remade. A killed run that had
	// done one of them would find it current and skip it.
	one := versionsOf(t, f.readDoc(t, ids[0])["picture"].(map[string]any))["thumb"].(map[string]any)["id"]
	if !w.RunOnce(context.Background()) {
		t.Fatal("the lapsed job was not taken over")
	}
	job, _, _ := f.svc.GetJob(context.Background(), jobID)
	if job.Status != auth.JobDone || job.Result.Status != "ok" || job.Attempts != 2 {
		t.Fatalf("job: %+v", job)
	}
	for _, d := range ids {
		th := versionsOf(t, f.readDoc(t, d)["picture"].(map[string]any))["thumb"].(map[string]any)
		if th["width"] != float64(7) {
			t.Errorf("%s: %v", d, th)
		}
	}
	if versionsOf(t, f.readDoc(t, ids[0])["picture"].(map[string]any))["thumb"].(map[string]any)["id"] == one {
		t.Error("thumb of the first picture wasn't remade")
	}
	// Running it again finds everything current.
	_, out = f.regenerate(t, f.key, `{`+regenerateField+`}`)
	if !w.RunOnce(context.Background()) {
		t.Fatal("no job")
	}
	again, _, _ := f.svc.GetJob(context.Background(), out["id"].(string))
	var summary map[string]int
	_ = json.Unmarshal(again.Result.Output, &summary)
	if summary["files"] != 0 || summary["made"] != 0 {
		t.Errorf("second run: %v", summary)
	}
}

// A worker that starts looks at what the field declares and logs the files that need it.
func TestAWorkerReportsDeclaredVersionsThatChanged(t *testing.T) {
	f := newFilesFixture(t)
	var buf bytes.Buffer
	w := newTestWorker(t, f.rulesFixture)
	w.log = slog.New(slog.NewJSONHandler(&buf, nil))
	docID, _ := f.madePicture(t, w, "picture")
	runImageJobs(t, w)
	_ = docID

	// The first time every declaration is new; the pictures were made by it, so none is stale.
	w.CheckImageDeclarations(context.Background())
	if !strings.Contains(buf.String(), "image version new") || strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("first look: %s", buf.String())
	}
	declared, _ := f.svc.ImageDeclarations(context.Background())
	if declared["app/library.picture/thumb"] == "" || declared["app/library.picture/big"] == "" || declared["app/library.picture/mark"] != "" { // function-only versions have no parameters to fingerprint
		t.Errorf("recorded: %v", declared)
	}
	// Nothing changed: it says nothing and reads no document.
	buf.Reset()
	w.CheckImageDeclarations(context.Background())
	if buf.Len() != 0 {
		t.Errorf("unchanged: %s", buf.String())
	}
	// A declaration changes: the pictures made with the old parameters are counted.
	declareThumb(t, f, 6)
	w.CheckImageDeclarations(context.Background())
	out := buf.String()
	if !strings.Contains(out, "image version changed") || !strings.Contains(out, `"files":1`) || !strings.Contains(out, "backd versions regenerate") || !strings.Contains(out, `"level":"WARN"`) {
		t.Errorf("after the change: %s", out)
	}
	// And it is said once.
	buf.Reset()
	w.CheckImageDeclarations(context.Background())
	if buf.Len() != 0 {
		t.Errorf("said twice: %s", buf.String())
	}
}
