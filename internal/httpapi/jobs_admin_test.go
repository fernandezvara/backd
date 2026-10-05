package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// The admin listing of jobs (roadmap F19): every job of the realm, newest
// first, filterable by function, status and origin, and never carrying a
// job's input or output.
func TestAdminJobsListing(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "jobs-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := bearer(adminKey)

	*f.clock = time.Date(2026, 9, 29, 3, 0, 20, 0, time.UTC)
	if rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{"secret": "input-value"}`, bearer(f.ada)); rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	w.EnqueueDue(ctx) // the "nightly" schedule is due at 03:00
	w.RunOnce(ctx)    // runs the older of the two

	list := func(query string) map[string]any {
		t.Helper()
		rec, out := f.doH(t, "GET", "/v1/acme/_admin/jobs"+query, "", key)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET jobs%s: %d %v", query, rec.Code, out)
		}
		return out
	}
	items := func(query string) []map[string]any {
		var res []map[string]any
		for _, it := range list(query)["items"].([]any) {
			res = append(res, it.(map[string]any))
		}
		return res
	}

	all := items("")
	if len(all) != 2 {
		t.Fatalf("want the called job and the cron run, got %v", all)
	}
	for _, it := range all {
		for _, banned := range []string{"input", "output"} {
			if _, ok := it[banned]; ok {
				t.Errorf("a listed job must not carry %q: %v", banned, it)
			}
		}
	}
	if got := items("?scheduled=true"); len(got) != 1 || got[0]["function"] != "app/nightly" || got[0]["scheduled"] != true {
		t.Errorf("scheduled=true: %v", got)
	}
	if got := items("?scheduled=false&function=app/job"); len(got) != 1 || got[0]["scheduled"] != false {
		t.Errorf("scheduled=false: %v", got)
	}
	done := items("?status=done")
	if len(done) != 1 {
		t.Fatalf("status=done: %v", done)
	}
	res, _ := done[0]["result"].(map[string]any)
	if res["status"] != "ok" || done[0]["completed_at"] == nil || done[0]["attempts"] != float64(1) {
		t.Errorf("done job: %v", done[0])
	}
	if got := items("?status=queued"); len(got) != 1 || got[0]["result"] != nil || got[0]["completed_at"] != nil {
		t.Errorf("status=queued: %v", got)
	}
	if got := items("?function=app/nothing"); len(got) != 0 {
		t.Errorf("unknown function: %v", got)
	}

	page := list("?limit=1")
	if len(page["items"].([]any)) != 1 || page["has_more"] != true {
		t.Errorf("limit=1: %v", page)
	}
	if got := items("?until=2026-09-29T03:00:00Z"); len(got) != 0 {
		t.Errorf("until before both: %v", got)
	}

	for _, bad := range []string{"?status=paused", "?function=job", "?scheduled=maybe", "?since=yesterday", "?limit=0", "?bogus=1"} {
		if rec, _ := f.doH(t, "GET", "/v1/acme/_admin/jobs"+bad, "", key); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}
	if rec, _ := f.doH(t, "GET", "/v1/acme/_admin/jobs", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", "/v1/acme/_admin/jobs", "", bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin user: %d", rec.Code)
	}
}

// Cancel and re-run (roadmap f1): an administrator ends a job that hasn't
// finished, and queues a finished function job again; both are audited, both
// need the functions area, and neither touches backd's own jobs.
func TestAdminCancelAndRerunJobs(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "jobs-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := bearer(adminKey)
	jobs := admin + "/jobs/"

	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{"day": "2026-10-01"}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	id := out["id"].(string)

	// A queued job is cancelled at once; the answer is the job as the listing shows it.
	rec, got := f.doH(t, "POST", jobs+id+"/cancel", "", key)
	res, _ := got["result"].(map[string]any)
	if rec.Code != http.StatusOK || got["status"] != "done" || res["status"] != "cancelled" || got["completed_at"] == nil {
		t.Fatalf("cancel: %d %v", rec.Code, got)
	}
	if w.RunOnce(ctx) {
		t.Error("a worker claimed a cancelled job")
	}
	// Twice, unknown, and not a thing to cancel any more: refusals say why.
	if rec, got := f.doH(t, "POST", jobs+id+"/cancel", "", key); rec.Code != http.StatusConflict || errCode(got) != "conflict" {
		t.Errorf("cancel again: %d %v", rec.Code, got)
	}
	if rec, got := f.doH(t, "POST", jobs+"nope/cancel", "", key); rec.Code != http.StatusNotFound || errCode(got) != "not_found" {
		t.Errorf("cancel unknown: %d %v", rec.Code, got)
	}

	// Re-run: a new job, same function, input and caller; the original keeps its result.
	rec, again := f.doH(t, "POST", jobs+id+"/rerun", "", key)
	newID, _ := again["id"].(string)
	if rec.Code != http.StatusAccepted || newID == "" || newID == id || again["status"] != "queued" || again["rerun_of"] != id || again["origin"] != "admin" {
		t.Fatalf("rerun: %d %v", rec.Code, again)
	}
	if rec.Header().Get("Location") != "/v1/acme/app/_jobs/"+newID {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	if !w.RunOnce(ctx) {
		t.Fatal("the re-run wasn't claimed")
	}
	req := f.runner.last()
	if string(req.Envelope.Input) != `{"day": "2026-10-01"}` {
		t.Errorf("the re-run's input: %s", req.Envelope.Input)
	}
	var user map[string]any
	_ = json.Unmarshal(req.Envelope.User, &user)
	if user["id"] != f.adaID {
		t.Errorf("the re-run acts as the original caller: %v", user)
	}
	// Its own result is stored, readable by that caller; the cancelled one stays cancelled.
	if code, out := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+newID, ""); code != 200 || out["status"] != "done" || out["result"].(map[string]any)["status"] != "ok" {
		t.Errorf("the re-run's result: %d %v", code, out)
	}
	if code, out := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, ""); code != 200 || out["result"].(map[string]any)["status"] != "cancelled" {
		t.Errorf("the original: %d %v", code, out)
	}
	// A job still waiting can't be re-run.
	_, pending := f.doH(t, "POST", "/v1/acme/app/_func/job", `{}`, bearer(f.ada))
	if rec, got := f.doH(t, "POST", jobs+pending["id"].(string)+"/rerun", "", key); rec.Code != http.StatusConflict || errCode(got) != "conflict" {
		t.Errorf("rerun an unfinished job: %d %v", rec.Code, got)
	}

	// backd's own jobs (emails, erasures) are neither cancelled nor re-run.
	mail, err := f.svc.EnqueueJob(ctx, auth.Job{Email: &auth.EmailJob{Kind: "welcome", UserID: f.adaID}})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"cancel", "rerun"} {
		if rec, got := f.doH(t, "POST", jobs+mail.ID+"/"+op, "", key); rec.Code != http.StatusConflict {
			t.Errorf("%s an email job: %d %v", op, rec.Code, got)
		}
	}

	// The audit trail records who, never any input.
	records, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditJobCancel})
	if len(records) != 1 || records[0].Target != "job:"+id || records[0].Actor != "key:jobs-admin" {
		t.Errorf("cancel audit: %+v", records)
	}
	records, _, _ = f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditJobRerun})
	if len(records) != 1 || records[0].Details["new_job"] != newID || records[0].Details["function"] != "app/job" {
		t.Errorf("rerun audit: %+v", records)
	}
}
