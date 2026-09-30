package httpapi

import (
	"context"
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
