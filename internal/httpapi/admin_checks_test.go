package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAdminSchemaChecks(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "check-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := bearer(adminKey)
	seedNotes(f, 30, map[int]string{4: "type", 9: "missing"}, map[int]bool{9: true})

	// Nothing has been checked yet: every collection is listed with a null report.
	rec, list := f.doH(t, "GET", admin+"/data-checks", "", key)
	items, _ := list["items"].([]any)
	if rec.Code != http.StatusOK || list["running"] != nil || len(items) == 0 || items[0].(map[string]any)["report"] != nil {
		t.Fatalf("list: %d %v", rec.Code, list)
	}
	if rec, _ := f.doH(t, "GET", admin+"/data-checks/app/notes", "", key); rec.Code != http.StatusNotFound {
		t.Errorf("a report that doesn't exist: %d", rec.Code)
	}

	// Bad requests.
	for name, body := range map[string]string{
		"unknown collection": `{"database": "app", "collection": "ghost"}`,
		"unknown database":   `{"database": "nope"}`,
	} {
		if rec, _ := f.doH(t, "POST", admin+"/data-checks", body, key); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	for name, body := range map[string]string{
		"limit too big":    `{"database": "app", "collection": "notes", "limit": 5000}`,
		"limit zero":       `{"database": "app", "collection": "notes", "limit": 0}`,
		"collection no db": `{"collection": "notes"}`,
		"unknown field":    `{"database": "app", "bogus": 1}`,
		"not an object":    `[1]`,
	} {
		if rec, _ := f.doH(t, "POST", admin+"/data-checks", body, key); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}

	// Start one: 202 with the job, how many documents it will read, and where to follow it.
	rec, started := f.doH(t, "POST", admin+"/data-checks", `{"database": "app", "collection": "notes"}`, key)
	id, _ := started["id"].(string)
	if rec.Code != http.StatusAccepted || id == "" || started["scope"] != "app/notes" || started["estimated_documents"] != float64(30) || started["limit"] != float64(100) || rec.Header().Get("Location") != admin+"/jobs/"+id {
		t.Fatalf("start: %d %v %v", rec.Code, started, rec.Header())
	}
	// A second one while it is queued or running is refused with the running job's id.
	rec, refused := f.doH(t, "POST", admin+"/data-checks", `{}`, key)
	errBody, _ := refused["error"].(map[string]any)
	if rec.Code != http.StatusConflict || errBody["code"] != "check_running" || len(errBody["details"].([]any)) != 1 || errBody["details"].([]any)[0].(map[string]any)["reason"] != id {
		t.Fatalf("a second start: %d %v", rec.Code, refused)
	}
	_, list = f.doH(t, "GET", admin+"/data-checks", "", key)
	if run, _ := list["running"].(map[string]any); run == nil || run["id"] != id || run["status"] != "queued" || run["function"] != "app/notes" {
		t.Errorf("running: %v", list["running"])
	}

	// A worker runs it, and the report can be read.
	if !w.RunOnce(ctx) {
		t.Fatal("no worker claimed the check")
	}
	rec, full := f.doH(t, "GET", admin+"/data-checks/app/notes", "", key)
	docs, _ := full["documents"].([]any)
	if rec.Code != http.StatusOK || full["scanned"] != float64(30) || full["invalid"] != float64(2) || full["complete"] != true || full["stopped_by"] != nil || len(docs) != 2 || full["job_id"] != id {
		t.Fatalf("report: %d %v", rec.Code, full)
	}
	first := docs[0].(map[string]any)
	if first["id"] != "n00004" || first["deleted"] != false || first["problems"].([]any)[0].(map[string]any)["path"] != "title" {
		t.Errorf("a document: %v", first)
	}
	if d := docs[1].(map[string]any); d["deleted"] != true {
		t.Errorf("the trashed document: %v", d)
	}
	_, list = f.doH(t, "GET", admin+"/data-checks", "", key)
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		if m["collection"] == "notes" && (m["report"] == nil || m["report"].(map[string]any)["invalid"] != float64(2) || m["report"].(map[string]any)["documents"] != nil) {
			t.Errorf("the listing: %v", m)
		}
	}
	if list["running"] != nil {
		t.Errorf("still running: %v", list["running"])
	}
	// The job is in the job list, with its origin and scope, and the start was audited.
	_, jobs := f.doH(t, "GET", admin+"/jobs?origin="+auth.CheckOrigin, "", key)
	if its, _ := jobs["items"].([]any); len(its) != 1 || its[0].(map[string]any)["function"] != "app/notes" {
		t.Errorf("jobs: %v", jobs)
	}
	if a := f.auditActions(t, "data.check"); len(a) != 1 || a[0]["target"] != "data:app/notes" {
		t.Errorf("audit: %v", a)
	}

	// A check can be cancelled through the jobs route, by whoever may read the data.
	_, again := f.doH(t, "POST", admin+"/data-checks", `{"database": "app"}`, key)
	if rec, got := f.doH(t, "POST", admin+"/jobs/"+again["id"].(string)+"/cancel", "", key); rec.Code != http.StatusOK || got["result"].(map[string]any)["status"] != "cancelled" {
		t.Errorf("cancel: %d %v", rec.Code, got)
	}
	if rec, _ := f.doH(t, "POST", admin+"/data-checks", `{"database": "app", "collection": "notes"}`, key); rec.Code != http.StatusAccepted {
		t.Errorf("after the cancel: %d", rec.Code)
	}
}

// The data area opens schema checks to those who can read it: a read-only
// administrator may start and cancel them (and read the report), one who holds
// only the functions area may not, and neither may a caller with no admin role.
func TestSchemaChecksNeedTheDataArea(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	start := func(who map[string]string) (*httptest.ResponseRecorder, map[string]any) {
		return f.doH(t, "POST", admin+"/data-checks", `{"database": "app", "collection": "notes"}`, who)
	}
	if rec, _ := start(nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("nobody: %d", rec.Code)
	}
	if rec, _ := start(bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("a user with no admin role: %d", rec.Code)
	}

	f.bobHolds(t, "runner") // admin: [functions]
	if rec, _ := start(bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a functions administrator: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", admin+"/data-checks", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a functions administrator reading: %d", rec.Code)
	}

	f.bobHolds(t, "viewer") // admin: read
	if rec, _ := start(bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a read-only administrator without admin.read_access.data: %d", rec.Code)
	}
	f.svc.Settings.ReadAccess.Data = true // realm.yaml's admin.read_access.data
	rec, started := start(bearer(f.bob))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a read-only administrator: %d %v", rec.Code, started)
	}
	if rec, _ := f.doH(t, "GET", admin+"/data-checks", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("a read-only administrator reading: %d", rec.Code)
	}
	// They may cancel the check, but not an ordinary function's job.
	other, _ := f.svc.EnqueueJob(ctx, auth.Job{Database: "app", Function: "job", CallerActor: "user:x", TimeoutMS: 1000})
	if rec, _ := f.doH(t, "POST", admin+"/jobs/"+other.ID+"/cancel", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("cancelling a function's job: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", admin+"/jobs/"+started["id"].(string)+"/cancel", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("cancelling the check: %d", rec.Code)
	}
}
