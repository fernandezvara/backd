package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage/storagetest"
)

const exportsPath = "/v1/acme/_auth/me/export"

// exportFixture is a realm that exports its users' data, with its storage on the in-memory S3 server.
type exportFixture struct {
	*rulesFixture
	w  *Worker
	s3 *storagetest.Server
}

func newExportFixture(t *testing.T) *exportFixture {
	t.Helper()
	f, w := newVerifyFixture(t)
	s3 := storagetest.New(t, "files")
	f.svc.Realm = "acme"
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Cache = auth.NewSecretCache()
	st := f.reg.Realms["acme"].Settings.Storage
	st.Endpoint, st.Bucket, st.HTTP = s3.URL, "files", true
	for _, n := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY", "PUBLIC_ACCESS_KEY", "PUBLIC_SECRET_KEY"} {
		if err := f.svc.SetSecret(context.Background(), "", n, "v-"+n, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	return &exportFixture{rulesFixture: f, w: w, s3: s3}
}

func (f *exportFixture) enable() {
	f.reg.Realms["acme"].Settings.Compliance = registry.ComplianceSettings{Export: true, ExportPage: "https://app.acme.example/my-data", ExportRetention: 3 * 24 * time.Hour}
}

// openZip reads the archive an export response (or the storage) holds.
func openZip(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, zf := range zr.File {
		rc, _ := zf.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[zf.Name] = b
	}
	return out
}

func TestUserAsksForTheirExport(t *testing.T) {
	f := newExportFixture(t)
	ctx := context.Background()

	// A realm that does not export says so, whoever asks.
	if code, out := f.as(t, f.ada, "POST", exportsPath, ""); code != http.StatusNotFound {
		t.Fatalf("export off: %d %v", code, out)
	}
	f.enable()

	// It needs a session and a fresh proof of who is asking.
	if rec, _ := f.doH(t, "POST", exportsPath, "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec, out := f.doH(t, "POST", exportsPath, "", bearer(f.ada)); rec.Code != http.StatusUnauthorized || errCode(out) != "reauth_required" {
		t.Errorf("without a proof: %d %v", rec.Code, out)
	}

	seedDoc(t, f.rulesFixture, "contacts", "k1", f.adaID, map[string]any{"title": "Ada's card", "body": "likes tea", "internal_note": "STAFF-ONLY"})
	code, out := f.as(t, f.ada, "POST", exportsPath, "")
	if code != http.StatusAccepted || out["status"] != "queued" || out["id"] == nil {
		t.Fatalf("ask: %d %v", code, out)
	}
	id := out["id"].(string)

	// One a day: the second answers when to come back.
	rec, again := f.doH(t, "POST", exportsPath, "", map[string]string{"Authorization": "Bearer " + f.ada, reauthHeader: f.freshProof(t, f.ada)})
	if rec.Code != http.StatusTooManyRequests || errCode(again) != "too_many_requests" || rec.Header().Get("Retry-After") == "" {
		t.Errorf("second ask: %d %v %q", rec.Code, again, rec.Header().Get("Retry-After"))
	}

	// Nothing to download until the job has run.
	if code, out := f.as(t, f.ada, "GET", exportsPath+"s/"+id+"/download", ""); code != http.StatusConflict {
		t.Errorf("download while queued: %d %v", code, out)
	}
	if !f.w.RunOnce(ctx) {
		t.Fatal("no export job to run")
	}
	code, got := f.as(t, f.ada, "GET", exportsPath+"s/"+id, "")
	if code != 200 || got["status"] != "ready" || got["size"] == nil || got["expires_at"] == nil {
		t.Fatalf("after the job: %d %v", code, got)
	}
	counts := got["counts"].(map[string]any)
	if counts["app/contacts.jsonl"] != float64(1) || counts["_account/account"] != float64(1) {
		t.Errorf("counts = %v", counts)
	}
	if rec, list := f.doH(t, "GET", exportsPath+"s", "", bearer(f.ada)); rec.Code != 200 || len(list["items"].([]any)) != 1 {
		t.Errorf("list: %d %v", rec.Code, list)
	}

	// The ready email links the app's page with the export, never the file.
	in := deliver(t, f.rulesFixture, f.w)
	if in.Kind != "data-export-ready" || in.To[0].Email != "ada@example.com" {
		t.Fatalf("email: %+v", in)
	}
	link, _ := in.Data["link"].(string)
	if u, err := url.Parse(link); err != nil || u.Host != "app.acme.example" || u.Path != "/my-data" || u.Query().Get("export") != id {
		t.Errorf("link = %q", link)
	}
	if strings.Contains(in.Text, ".zip") || !strings.Contains(in.Text, link) {
		t.Errorf("text = %q", in.Text)
	}

	// Download: a link to the storage that works for a few minutes, as the redirect or, for a
	// page that fetches the API, as JSON.
	rec, _ = f.doH(t, "GET", exportsPath+"s/"+id+"/download", "", bearer(f.ada))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, f.s3.URL) || !strings.Contains(loc, "X-Amz-Expires=300") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	rec, asJSON := f.doH(t, "GET", exportsPath+"s/"+id+"/download", "", map[string]string{"Authorization": "Bearer " + f.ada, "Accept": "application/json"})
	if rec.Code != 200 || !strings.HasPrefix(asJSON["url"].(string), f.s3.URL) || asJSON["expires_at"] == nil {
		t.Errorf("download as JSON: %d %v", rec.Code, asJSON)
	}
	resp, err := http.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "acme-your-data-") {
		t.Fatalf("the signed link: %d %v", resp.StatusCode, resp.Header)
	}
	pkg := openZip(t, raw)
	if !strings.Contains(string(pkg["app/contacts.jsonl"]), "likes tea") || strings.Contains(string(pkg["app/contacts.jsonl"]), "STAFF-ONLY") {
		t.Errorf("contacts = %s", pkg["app/contacts.jsonl"])
	}
	if pkg["README.html"] == nil || pkg["manifest.json"] == nil || pkg["_account/account.jsonl"] == nil {
		t.Errorf("parts = %v", len(pkg))
	}
	if !strings.Contains(string(pkg["README.html"]), "3 days") {
		t.Errorf("README does not say how long it is kept")
	}

	// Another user can neither see it nor download it.
	if code, _ := f.as(t, f.bob, "GET", exportsPath+"s/"+id, ""); code != http.StatusNotFound {
		t.Errorf("bob reads it: %d", code)
	}
	if code, _ := f.as(t, f.bob, "GET", exportsPath+"s/"+id+"/download", ""); code != http.StatusNotFound {
		t.Errorf("bob downloads it: %d", code)
	}
	if rec, bobs := f.doH(t, "GET", exportsPath+"s", "", bearer(f.bob)); rec.Code != 200 || len(bobs["items"].([]any)) != 0 {
		t.Errorf("bob's list: %v", bobs)
	}

	// The trail: asked, built, downloaded.
	var actions []string
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Target: "user:" + f.adaID})
	for _, r := range recs {
		if strings.HasPrefix(r.Action, "user.data_export") {
			actions = append(actions, r.Action)
		}
	}
	for _, want := range []string{auth.AuditDataExportRequested, auth.AuditDataExported, auth.AuditDataExportDownloaded} {
		found := false
		for _, a := range actions {
			found = found || a == want
		}
		if !found {
			t.Errorf("audit lacks %s (have %v)", want, actions)
		}
	}
	if e, _ := f.svc.UserExport(ctx, f.adaID, id); e.Downloads != 2 {
		t.Errorf("downloads = %d", e.Downloads)
	}

	// After the retention the package is deleted and the record says so; she may ask again.
	*f.clock = f.clock.Add(4 * 24 * time.Hour)
	f.w.ExportsDue(ctx)
	if e, _ := f.svc.UserExport(ctx, f.adaID, id); e.Status != auth.ExportExpired || e.Key != "" {
		t.Errorf("after the retention: %+v", e)
	}
	if code, _ := f.as(t, f.ada, "GET", exportsPath+"s/"+id+"/download", ""); code != http.StatusConflict {
		t.Errorf("download an expired export: %d", code)
	}
	f.w.FilesDue(ctx)
	if keys := f.s3.Keys(); len(keys) != 0 {
		t.Errorf("the package is still in the storage: %v", keys)
	}
	delete(f.proofs, f.ada) // the proof of days ago is no proof now
	if code, out := f.as(t, f.ada, "POST", exportsPath, ""); code != http.StatusAccepted {
		t.Errorf("ask again after the day: %d %v", code, out)
	}
}

// A realm whose clients cannot reach its storage gets the package through backd.
func TestUserDownloadsTheirExportThroughBackdWhenFilesAreProxied(t *testing.T) {
	f := newExportFixture(t)
	f.enable()
	f.reg.Realms["acme"].Settings.Storage.Download = registry.DownloadProxy
	code, out := f.as(t, f.ada, "POST", exportsPath, "")
	if code != http.StatusAccepted {
		t.Fatalf("ask: %d %v", code, out)
	}
	if !f.w.RunOnce(context.Background()) {
		t.Fatal("no job")
	}
	rec, _ := f.doH(t, "GET", exportsPath+"s/"+out["id"].(string)+"/download", "", bearer(f.ada))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("proxied: %d %v", rec.Code, rec.Header())
	}
	if pkg := openZip(t, rec.Body.Bytes()); pkg["_account/account.jsonl"] == nil {
		t.Errorf("parts = %d", len(pkg))
	}
}

// A job that cannot reach the storage is tried again; when it keeps failing the record says so and
// the user may ask again at once.
func TestExportFailsWhenTheStorageNeverAnswers(t *testing.T) {
	f := newExportFixture(t)
	f.enable()
	ctx := context.Background()
	code, out := f.as(t, f.ada, "POST", exportsPath, "")
	if code != http.StatusAccepted {
		t.Fatalf("ask: %d %v", code, out)
	}
	id := out["id"].(string)
	f.svc.Cipher = nil // the storage keys can no longer be read
	for i := range exportAttempts {
		if !f.w.RunOnce(ctx) {
			t.Fatalf("attempt %d: no job to run", i+1)
		}
		e, _ := f.svc.UserExport(ctx, f.adaID, id)
		if i < exportAttempts-1 && e.Status != auth.ExportQueued {
			t.Fatalf("attempt %d left the export %s", i+1, e.Status)
		}
		*f.clock = f.clock.Add(time.Hour) // past the wait before the retry
	}
	e, _ := f.svc.UserExport(ctx, f.adaID, id)
	if e.Status != auth.ExportFailed || e.Error == "" || !e.FinishedAt.After(e.CreatedAt) {
		t.Fatalf("the record: %+v", e)
	}
	// The user is told it failed, not why; and a failure does not count against the day.
	code, got := f.as(t, f.ada, "GET", exportsPath+"s/"+id, "")
	if code != 200 || got["status"] != "failed" || got["error"] != nil {
		t.Errorf("the user's view: %d %v", code, got)
	}
	delete(f.proofs, f.ada)
	if code, out := f.as(t, f.ada, "POST", exportsPath, ""); code != http.StatusAccepted {
		t.Errorf("ask again after a failure: %d %v", code, out)
	}
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditDataExportFailed})
	if len(recs) != 1 {
		t.Errorf("failure audit: %+v", recs)
	}
}

// An export whose job an administrator cancelled is shown as failed, and does not keep the user from asking again.
func TestCancelledExportDoesNotBlockTheUser(t *testing.T) {
	f := newExportFixture(t)
	f.enable()
	ctx := context.Background()
	code, out := f.as(t, f.ada, "POST", exportsPath, "")
	if code != http.StatusAccepted {
		t.Fatalf("ask: %d %v", code, out)
	}
	id := out["id"].(string)
	rec, err := f.svc.UserExport(ctx, f.adaID, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CancelJob(ctx, rec.JobID); err != nil {
		t.Fatal(err)
	}
	code, got := f.as(t, f.ada, "GET", exportsPath+"s/"+id, "")
	if code != 200 || got["status"] != "failed" {
		t.Fatalf("after the cancel: %d %v", code, got)
	}
	if code, out := f.as(t, f.ada, "POST", exportsPath, ""); code != http.StatusAccepted {
		t.Errorf("ask again: %d %v", code, out)
	}
}
