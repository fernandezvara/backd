package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

func orderDoc(t *testing.T, f *rulesFixture, id string) storage.Document {
	t.Helper()
	c, _ := f.reg.Collection("acme", "app", "orders")
	d, err := f.store.Repository(c).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("order %s: %v", id, err)
	}
	return d
}

// Erasing: a tombstone at once, then a worker applies the collection policies.
func TestEraseUser(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	seedOrder(t, f, "o1", f.adaID, map[string]any{"buyer": "Ada Lovelace", "phone": "555"})
	seedOrder(t, f, "o2", f.adaID, map[string]any{"buyer": "Ada Lovelace"})
	seedOrder(t, f, "o3", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"ada@example.com", "bob@example.com"}, "paid_by": f.adaID})
	seedOrder(t, f, "o4", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"bob@example.com"}, "paid_by": f.bobID})

	// What is held about her elsewhere: a link, a queued email, a session.
	token, _, _ := f.svc.NewEmailToken(ctx, "reset-password", f.adaID, "", time.Hour)
	if _, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: "reset-password", UserID: f.adaID, Address: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}

	code, out := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.adaID, "")
	if code != 202 {
		t.Fatalf("erase: %d %v", code, out)
	}
	jobID := out["id"].(string)

	// At once: the tombstone, and the rest of what was held about her.
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/_auth/me", ""); code != 401 {
		t.Errorf("her session: %d", code)
	}
	if _, err := f.svc.PeekEmailToken(ctx, token, "reset-password"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("her email token: %v", err)
	}
	if jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Origin: "backd:email.reset-password"}); len(jobs) != 0 {
		t.Errorf("her queued email: %+v", jobs)
	}
	u, _ := f.svc.UserByID(ctx, f.adaID)
	if u.ErasedAt.IsZero() || u.Email != "erased-"+f.adaID+"@erased.invalid" || !u.Disabled || len(u.Roles) != 0 {
		t.Errorf("tombstone: %+v", u)
	}
	if _, _, err := f.svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("login: %v", err)
	}
	// The documents are still untouched until a worker runs.
	if orderDoc(t, f, "o1")["buyer"] != "Ada Lovelace" {
		t.Error("documents changed before the worker ran")
	}
	if _, out = f.as(t, f.key, "GET", "/v1/acme/_admin/users/"+f.adaID+"/owned", ""); out["user"].(map[string]any)["status"] != "erased" {
		t.Errorf("owned of an erased user: %v", out)
	}

	if !w.RunOnce(ctx) {
		t.Fatal("no erase job to run")
	}
	// Her documents are anonymized, and she is out of the other users' documents.
	o1 := orderDoc(t, f, "o1")
	meta := o1["_meta"].(map[string]any)
	if o1["buyer"] != "Erased customer" || o1["phone"] != nil || meta["owner"] != nil || meta["updated_by"] != storage.ErasedBy {
		t.Errorf("o1: %#v", o1)
	}
	if _, has := o1["phone"]; has {
		t.Errorf("phone should be removed: %#v", o1)
	}
	o3 := orderDoc(t, f, "o3")
	if m := o3["members"].([]any); len(m) != 1 || m[0] != "bob@example.com" {
		t.Errorf("o3 members: %v", o3["members"])
	}
	if _, has := o3["paid_by"]; has {
		t.Errorf("o3 paid_by: %#v", o3)
	}
	if o4 := orderDoc(t, f, "o4"); o4["paid_by"] != f.bobID || o4["_meta"].(map[string]any)["version"] != int64(1) {
		t.Errorf("o4 should be untouched: %#v", o4)
	}

	// The job ended ok, forgot the email, and the audit trail has the counts.
	job, _, _ := f.svc.GetJob(ctx, jobID)
	if job.Status != auth.JobDone || job.Result == nil || job.Result.Status != "ok" || job.Erase.Email != "" {
		t.Errorf("job: %+v / %+v", job, job.Erase)
	}
	if job.Erase.Counts["app/orders/anonymized"] != 2 || job.Erase.Counts["app/orders/pulled"] != 1 || job.Erase.Counts["app/orders/cleared"] != 1 {
		t.Errorf("counts: %v", job.Erase.Counts)
	}
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditUserErased})
	if len(recs) != 1 || recs[0].Target != "user:"+f.adaID || recs[0].Details["needs_attention"] != nil || strings.Contains(jsonString(recs[0].Details), "ada@example.com") {
		t.Errorf("audit: %+v", recs)
	}
	if w.RunOnce(ctx) {
		t.Error("the job ran twice")
	}
	// Erasing her again: nothing is left to do.
	if code, _ := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.adaID, ""); code != 409 {
		t.Errorf("erase again: %d", code)
	}
}

// A job that keeps failing ends failed and visible, and repeating the delete
// resumes it with the email it still holds.
func TestEraseFailureAndResume(t *testing.T) {
	f, w := newVerifyFixture(t)
	ctx := context.Background()
	seedOrder(t, f, "o1", f.adaID, map[string]any{"buyer": "Ada"})
	seedOrder(t, f, "o3", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"ada@example.com"}})
	f.store.fail = errors.New("database unreachable")

	_, out := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.adaID, "")
	jobID := out["id"].(string)
	for range eraseAttempts {
		w.RunOnce(ctx)
		*f.clock = f.clock.Add(time.Hour) // past the wait before the next attempt
	}
	job, _, _ := f.svc.GetJob(ctx, jobID)
	if job.Status != auth.JobDone || job.Result.Status == "ok" || !strings.Contains(job.Result.Message, "database unreachable") || job.Erase.Email != "ada@example.com" {
		t.Fatalf("a job that kept failing: %+v / %+v", job, job.Erase)
	}
	recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditUserErased})
	if len(recs) != 1 || recs[0].Details["needs_attention"] != true {
		t.Errorf("audit: %+v", recs)
	}
	if _, out := f.as(t, f.key, "GET", "/v1/acme/_admin/jobs?origin=backd:account.erase", ""); out["items"].([]any)[0].(map[string]any)["result"].(map[string]any)["status"] == "ok" {
		t.Errorf("the failed job should show as failed: %v", out)
	}

	// The database is back: repeating the delete resumes the same job.
	f.store.fail = nil
	code, out := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.adaID, "")
	if code != 202 || out["id"] != jobID || out["status"] != "queued" {
		t.Fatalf("resume: %d %v", code, out)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("the resumed job didn't run")
	}
	job, _, _ = f.svc.GetJob(ctx, jobID)
	if job.Result.Status != "ok" || job.Erase.Email != "" || job.Erase.Counts["app/orders/anonymized"] != 1 || job.Erase.Counts["app/orders/pulled"] != 1 {
		t.Errorf("after resuming: %+v / %+v", job, job.Erase)
	}
	if m := orderDoc(t, f, "o3")["members"].([]any); len(m) != 0 {
		t.Errorf("members after resuming: %v", m)
	}
}
