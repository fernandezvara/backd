package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
)

// putSteps sends PUT /v1/acme/_job/steps on the internal listener with a
// callback token naming the job attempt.
func (f *rulesFixture) putSteps(t *testing.T, claims auth.CallbackClaims, body string) *httptest.ResponseRecorder {
	t.Helper()
	claims.Realm, claims.Expires = "acme", f.clock.Add(time.Minute)
	req := httptest.NewRequest("PUT", "/v1/acme/_job/steps", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+auth.SignCallback(f.callbackKey, claims))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	return rec
}

func stepBody(names ...string) string {
	var steps []map[string]any
	for i, n := range names {
		steps = append(steps, map[string]any{
			"n": i + 1, "name": n, "status": "running", "started_at": "2026-10-06T10:00:00.000Z", "ended_at": nil, "duration_ms": nil,
			"current": 3, "total": 10, "message": "m-" + n, "updated_at": "2026-10-06T10:00:01.000Z",
		})
	}
	b, _ := json.Marshal(map[string]any{"steps": steps, "omitted": 0})
	return string(b)
}

// A running job's steps are stored live, only for the attempt that is running,
// and every way of reading the job shows them.
func TestJobStepsAreReportedLive(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "steps-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
		// The run reports from inside: the callback is what the runner would use.
		if !req.Envelope.Callback.Progress {
			t.Error("a job's callback must say it reports progress")
		}
		return executor.Result{Status: executor.StatusOK}, nil
	})
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	id = out["id"].(string)
	job, found, _ := f.svc.Store.GetJob(ctx, id)
	if !found {
		t.Fatal("no job")
	}
	claimed, ok, _ := f.svc.ClaimJob(ctx, "w1")
	if !ok || claimed.Attempts != 1 {
		t.Fatalf("claim: %+v %v", claimed, ok)
	}
	claims := auth.CallbackClaims{Function: "app/job", Job: job.ID, Attempt: 1}

	if rec := f.putSteps(t, claims, stepBody("load", "save")); rec.Code != http.StatusNoContent {
		t.Fatalf("put steps: %d %s", rec.Code, rec.Body)
	}
	// What the caller sees reading the job.
	code, got := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, "")
	steps, _ := got["steps"].([]any)
	if code != 200 || len(steps) != 2 || steps[1].(map[string]any)["name"] != "save" || steps[1].(map[string]any)["status"] != "running" || steps[0].(map[string]any)["total"] != float64(10) {
		t.Fatalf("job: %d %v", code, got)
	}
	// What an administrator sees: the current step in the listing, every step in the detail.
	_, list := f.doH(t, "GET", admin+"/jobs?function=app/job", "", bearer(adminKey))
	prog := list["items"].([]any)[0].(map[string]any)["progress"].(map[string]any)
	if prog["name"] != "save" || prog["step"] != float64(2) || prog["current"] != float64(3) || prog["message"] != "m-save" {
		t.Errorf("progress: %v", prog)
	}
	rec, detail := f.doH(t, "GET", admin+"/jobs/"+id, "", bearer(adminKey))
	if ds, _ := detail["steps"].([]any); rec.Code != 200 || len(ds) != 2 {
		t.Errorf("detail: %d %v", rec.Code, detail)
	}

	// Only the running attempt may write; others change nothing or are refused.
	f.putSteps(t, auth.CallbackClaims{Function: "app/job", Job: job.ID, Attempt: 2}, stepBody("stale"))
	if cur, _, _ := f.svc.Store.GetJob(ctx, id); len(cur.Steps) != 2 {
		t.Errorf("a write for another attempt changed the steps: %+v", cur.Steps)
	}
	for name, c := range map[string]auth.CallbackClaims{
		"a sync call (no job)": {Function: "app/echo"},
		"an admin token":       {Function: "app/job", Job: job.ID, Attempt: 1, Admin: true},
	} {
		if rec := f.putSteps(t, c, stepBody("x")); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := f.putSteps(t, claims, `{"steps": "nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad body: %d", rec.Code)
	}
	// The public listener has no such route.
	req := httptest.NewRequest("PUT", "/v1/acme/_job/steps", strings.NewReader(stepBody("x")))
	req.Header.Set("Authorization", "Bearer "+auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/job", Job: job.ID, Attempt: 1, Expires: f.clock.Add(time.Minute)}))
	req.Header.Set("Content-Type", "application/json")
	pub := httptest.NewRecorder()
	f.h.ServeHTTP(pub, req)
	if pub.Code == http.StatusNoContent {
		t.Errorf("the public listener accepted it")
	}
}

// When an attempt ends its steps are closed by how it ended, stored on the job
// and in the invocation record next to the logs; a run killed before it could
// send its result keeps the steps it reported live; a retry starts again.
func TestAttemptEndClosesTheSteps(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f)
	ctx := context.Background()
	fns := f.reg.Realms["acme"].Databases["app"].Functions.Functions
	fns["flaky"].Retry = nil
	run := func(res executor.Result, live string) auth.Job {
		t.Helper()
		f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
			if live != "" { // what the runner pushed before it was killed
				c := req.Envelope.Callback
				claims, err := auth.VerifyCallback(f.callbackKey, c.Token, *f.clock)
				if err != nil {
					t.Fatal(err)
				}
				if rec := f.putSteps(t, auth.CallbackClaims{Function: claims.Function, Job: claims.Job, Attempt: claims.Attempt}, stepBody(strings.Split(live, ",")...)); rec.Code != 204 {
					t.Fatalf("live put: %d", rec.Code)
				}
			}
			return res, nil
		})
		rec, out := f.doH(t, "POST", "/v1/acme/app/_func/flaky", `{}`, bearer(f.ada))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("enqueue: %d %v", rec.Code, out)
		}
		w.RunOnce(ctx)
		j, _, _ := f.svc.Store.GetJob(ctx, out["id"].(string))
		return j
	}
	invocation := func(jobID string) auth.InvocationRecord {
		t.Helper()
		recs, _, _ := f.svc.Invocations(ctx, auth.InvocationFilter{Function: "app/flaky"})
		for _, r := range recs {
			if r.JobID == jobID {
				return r
			}
		}
		t.Fatalf("no invocation for %s", jobID)
		return auth.InvocationRecord{}
	}
	started := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	ended := started.Add(3 * time.Second)
	dur := int64(3000)
	reported := []executor.Step{
		{N: 1, Name: "load", Status: "done", StartedAt: started, EndedAt: &ended, DurationMS: &dur, Current: 10, UpdatedAt: ended},
		{N: 2, Name: "save", Status: "running", StartedAt: ended, Current: 4, UpdatedAt: ended},
	}

	// The runner's result carries the steps; the one running is closed by the outcome.
	j := run(executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 422, Code: "bad"}, Steps: reported}, "")
	if len(j.Steps) != 2 || j.Steps[0].Status != auth.StepDone || j.Steps[1].Status != auth.StepFailed || j.Steps[1].EndedAt.IsZero() {
		t.Errorf("a failed attempt: %+v", j.Steps)
	}
	if inv := invocation(j.ID); len(inv.Steps) != 2 || inv.Steps[1].Status != auth.StepFailed || inv.Steps[1].Current != 4 {
		t.Errorf("its invocation record: %+v", inv.Steps)
	}
	j = run(executor.Result{Status: executor.StatusOK, Steps: reported}, "")
	if j.Steps[1].Status != auth.StepDone {
		t.Errorf("an attempt that succeeded: %+v", j.Steps)
	}

	// A killed run sends no result: the steps it reported live are kept, closed as timed out.
	j = run(executor.Result{Status: executor.StatusTimeout, Message: "stopped"}, "load,save,send")
	if len(j.Steps) != 3 || j.Steps[2].Name != "send" || j.Steps[2].Status != auth.StepTimedOut {
		t.Errorf("a timed out attempt: %+v", j.Steps)
	}
	if inv := invocation(j.ID); len(inv.Steps) != 3 || inv.Steps[2].Status != auth.StepTimedOut {
		t.Errorf("its record: %+v", inv.Steps)
	}

	// Without a step, nothing is stored.
	if j = run(executor.Result{Status: executor.StatusOK}, ""); len(j.Steps) != 0 {
		t.Errorf("steps from nowhere: %+v", j.Steps)
	}
}

// A retry starts with no steps, and each attempt's own are kept in its record.
func TestRetryStartsWithNoSteps(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f)
	ctx := context.Background()
	started := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	step := func(name string) []executor.Step {
		return []executor.Step{{N: 1, Name: name, Status: "running", StartedAt: started, UpdatedAt: started}}
	}
	attempt := 0
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		attempt++
		if attempt == 1 {
			return executor.Result{Status: executor.StatusError, Message: "boom", Steps: step("first")}, nil
		}
		return executor.Result{Status: executor.StatusOK, Steps: step("second")}, nil
	})
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/flaky", `{}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d", rec.Code)
	}
	id := out["id"].(string)
	w.RunOnce(ctx)
	if j, _, _ := f.svc.Store.GetJob(ctx, id); j.Status != auth.JobQueued || len(j.Steps) != 1 || j.Steps[0].Name != "first" || j.Steps[0].Status != auth.StepFailed {
		t.Fatalf("after the first attempt: %+v", j)
	}
	*f.clock = f.clock.Add(time.Minute)
	w.RunOnce(ctx)
	j, _, _ := f.svc.Store.GetJob(ctx, id)
	if len(j.Steps) != 1 || j.Steps[0].Name != "second" || j.Steps[0].Status != auth.StepDone {
		t.Errorf("after the second: %+v", j.Steps)
	}
	recs, _, _ := f.svc.Invocations(ctx, auth.InvocationFilter{Function: "app/flaky"})
	names := map[string]bool{}
	for _, r := range recs {
		for _, s := range r.Steps {
			names[s.Name] = true
		}
	}
	if len(recs) != 2 || !names["first"] || !names["second"] {
		t.Errorf("each attempt keeps its own steps: %+v", recs)
	}
}

// Cancelling a job closes the step it was on as cancelled.
func TestCancelClosesTheCurrentStep(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "cancel-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/flaky", `{}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d", rec.Code)
	}
	id := out["id"].(string)
	claimed, _, _ := f.svc.ClaimJob(ctx, "w1")
	if rec := f.putSteps(t, auth.CallbackClaims{Function: "app/flaky", Job: claimed.ID, Attempt: 1}, stepBody("a", "b")); rec.Code != 204 {
		t.Fatalf("put: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", admin+"/jobs/"+id+"/cancel", "", bearer(adminKey)); rec.Code != 200 {
		t.Fatalf("cancel: %d", rec.Code)
	}
	j, _, _ := f.svc.Store.GetJob(ctx, id)
	// Only the step running last is closed; the earlier ones stay as reported.
	if len(j.Steps) != 2 || j.Steps[1].Status != auth.StepCancelled || j.Steps[1].EndedAt.IsZero() {
		t.Errorf("after the cancel: %+v", j.Steps)
	}
}
