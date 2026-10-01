package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
)

func newTestWorker(t *testing.T, f *rulesFixture) *Worker {
	t.Helper()
	var buf bytes.Buffer
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(buf.String())
		}
	})
	return NewWorker(Config{
		Registry: f.reg,
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return f.svc
			}
			return nil
		},
		Functions:   f.runner,
		CallbackURL: "http://backd-internal:8081",
		CallbackKey: f.callbackKey,
		Now:         func() time.Time { return *f.clock },
		Log:         slog.New(slog.NewJSONHandler(&buf, nil)),
	}, "test-worker")
}

// TestWorkerRunsJob proves the whole async path end to end (roadmap
// F11): enqueue over HTTP, a worker claims and runs it through the
// executor with the right envelope and callback, the result is stored,
// and reading it back shows "done" with the output — and an invocation
// history record with this job's id.
func TestWorkerRunsJob(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()

	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{"n": 5}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	id := out["id"].(string)

	if ran := w.RunOnce(ctx); !ran {
		t.Fatal("RunOnce found nothing to claim")
	}
	req := f.runner.last()
	if req.Function != "acme/app/job" || string(req.Envelope.Input) != `{"n": 5}` {
		t.Errorf("executor request: %+v", req)
	}
	var user map[string]any
	json.Unmarshal(req.Envelope.User, &user)
	if user["id"] != f.adaID {
		t.Errorf("envelope user: %v", user)
	}
	claims, err := auth.VerifyCallback(f.callbackKey, req.Envelope.Callback.Token, *f.clock)
	if err != nil || claims.UserID != f.adaID {
		t.Errorf("callback token: %+v %v", claims, err)
	}

	if code, out := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, ""); code != 200 || out["status"] != "done" {
		t.Fatalf("get after run: %d %v", code, out)
	} else {
		result := out["result"].(map[string]any)
		if result["status"] != "ok" || result["output"] == nil {
			t.Errorf("result: %v", result)
		}
	}

	recs, _, err := f.svc.Invocations(ctx, auth.InvocationFilter{Function: "app/job"})
	if err != nil || len(recs) != 1 || recs[0].JobID != id || recs[0].Mode != "async" || recs[0].Status != "ok" {
		t.Errorf("invocation history: %+v %v", recs, err)
	}

	// Nothing left to claim.
	if w.RunOnce(ctx) {
		t.Error("RunOnce claimed a second time with an empty queue")
	}
}

// TestWorkerLeavesBusyJobsForRetry proves a busy executor doesn't
// consume a job: it's left running, claimable again once its lease
// expires (the roadmap's at-least-once semantics), not marked done.
func TestWorkerLeavesBusyJobsForRetry(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()

	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusBusy}, nil
	})
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	id := out["id"].(string)

	if !w.RunOnce(ctx) {
		t.Fatal("RunOnce found nothing to claim")
	}
	if code, out := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, ""); code != 200 || out["status"] != "running" || out["result"] != nil {
		t.Errorf("busy job must stay running, not done: %d %v", code, out)
	}
}

// TestWorkerRecordsFunctionError proves a function's own error reaches
// the job's result with its code and message, the same shape a sync
// call's error response carries.
func TestWorkerRecordsFunctionError(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()

	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 409, Code: "out_of_stock", Message: "no stock"}}, nil
	})
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	w.RunOnce(ctx)

	_, out = f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, "")
	result := out["result"].(map[string]any)
	if result["status"] != "function_error" || result["code"] != "out_of_stock" || result["message"] != "no stock" || result["http_status"] != float64(409) {
		t.Errorf("result: %v", result)
	}
}

// TestWorkerRecordsOtherEndReasons proves a job's stored result maps
// every terminal executor.Status to the same code/message/http_status a
// sync call would answer with for that reason (roadmap F14, so a client
// can treat a job's outcome uniformly with a sync call's) — never the
// raw executor Message verbatim, which may hold a stack trace.
func TestWorkerRecordsOtherEndReasons(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()

	for _, tt := range []struct {
		name       string
		res        executor.Result
		wantStatus string
		wantHTTP   float64
		wantCode   string
	}{
		{"timeout", executor.Result{Status: executor.StatusTimeout, Message: "stopped after 15m0s"}, "timeout", 504, "function_timeout"},
		{"output too big", executor.Result{Status: executor.StatusOutputTooBig, Message: "output of 999 bytes over max_output"}, "output_too_large", 500, "invalid_output"},
		{"crash", executor.Result{Status: executor.StatusCrash, Message: "the process ended without a result"}, "crash", 500, "function_failed"},
	} {
		f.runner.set(func(executor.InvokeRequest) (executor.Result, error) { return tt.res, nil })
		rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{}`, bearer(f.ada))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: enqueue %d %v", tt.name, rec.Code, out)
		}
		id := out["id"].(string)
		w.RunOnce(ctx)

		_, out = f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, "")
		result := out["result"].(map[string]any)
		if result["status"] != tt.wantStatus || result["http_status"] != tt.wantHTTP || result["code"] != tt.wantCode {
			t.Errorf("%s: result = %v", tt.name, result)
		}
		if msg, _ := result["message"].(string); strings.Contains(msg, tt.res.Message) && tt.res.Message != "" {
			t.Errorf("%s: raw executor message reached the caller: %v", tt.name, result["message"])
		}
	}
}

// TestWorkerRunsScheduledFunction proves cron runs (roadmap F18): a due
// schedule creates exactly one job however many workers (or ticks) try,
// the run acts as the function itself with admin access, and its
// history shows the function as actor.
func TestWorkerRunsScheduledFunction(t *testing.T) {
	f := newRulesFixture(t)
	w1, w2 := newTestWorker(t, f), newTestWorker(t, f)
	ctx := context.Background()

	*f.clock = time.Date(2026, 9, 29, 3, 0, 20, 0, time.UTC)
	w1.EnqueueDue(ctx)
	w2.EnqueueDue(ctx)
	w1.EnqueueDue(ctx)
	*f.clock = f.clock.Add(45 * time.Second)
	w2.EnqueueDue(ctx)

	if !w1.RunOnce(ctx) {
		t.Fatal("no scheduled job to claim")
	}
	if w2.RunOnce(ctx) {
		t.Fatal("more than one job was created for one scheduled time")
	}
	req := f.runner.last()
	if req.Function != "acme/app/nightly" || req.Envelope.Callback.AdminToken == "" || req.Envelope.User != nil {
		t.Errorf("executor request: %+v", req)
	}
	claims, err := auth.VerifyCallback(f.callbackKey, req.Envelope.Callback.AdminToken, *f.clock)
	if err != nil || !claims.Admin || claims.Function != "app/nightly" {
		t.Errorf("admin callback: %+v %v", claims, err)
	}
	id := auth.ScheduledJobID("app", "nightly", time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC))
	if job, ok, err := f.svc.GetJob(ctx, id); err != nil || !ok || job.Status != auth.JobDone || !job.Scheduled {
		t.Errorf("job %s: %+v %v %v", id, job, ok, err)
	}
	recs, _, err := f.svc.Invocations(ctx, auth.InvocationFilter{Function: "app/nightly"})
	if err != nil || len(recs) != 1 || recs[0].Actor != "func:acme/app/nightly" || recs[0].JobID != id {
		t.Errorf("history: %+v %v", recs, err)
	}

	// The next day's run is a new job; a tick too long after the scheduled
	// time skips it instead of replaying.
	*f.clock = time.Date(2026, 9, 30, 3, 0, 5, 0, time.UTC)
	w1.EnqueueDue(ctx)
	if !w1.RunOnce(ctx) {
		t.Error("next day's run was not created")
	}
	*f.clock = time.Date(2026, 10, 1, 3, 20, 0, 0, time.UTC)
	w1.EnqueueDue(ctx)
	if w1.RunOnce(ctx) {
		t.Error("a run missed by 20 minutes was replayed")
	}
}

// TestWorkerRetries proves an async function with a retry policy is tried
// again after the function threw, with growing waits, until it succeeds or
// the attempts run out; and that an answer the function chose isn't retried.
func TestWorkerRetries(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock } // the queue's clock, so waits can be crossed
	w := newTestWorker(t, f)
	ctx := context.Background()
	failing := func(status string) func(executor.InvokeRequest) (executor.Result, error) {
		return func(executor.InvokeRequest) (executor.Result, error) {
			return executor.Result{Status: status, Message: "boom"}, nil
		}
	}
	enqueue := func() string {
		t.Helper()
		rec, out := f.doH(t, "POST", "/v1/acme/app/_func/flaky", `{}`, bearer(f.ada))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("enqueue: %d %v", rec.Code, out)
		}
		return out["id"].(string)
	}
	job := func(id string) map[string]any {
		t.Helper()
		code, out := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, "")
		if code != 200 {
			t.Fatalf("get job: %d %v", code, out)
		}
		return out
	}

	// Every attempt throws: waits of 1m and 2m, then the job ends as failed.
	f.runner.set(failing(executor.StatusError))
	id := enqueue()
	w.RunOnce(ctx)
	j := job(id)
	if j["status"] != "queued" || j["attempts"] != float64(1) || j["result"] != nil || j["next_attempt_at"] != formatTime(f.clock.Add(time.Minute)) {
		t.Fatalf("after the first failure: %v", j)
	}
	if w.RunOnce(ctx) {
		t.Error("a retry was claimed before its wait was over")
	}
	*f.clock = f.clock.Add(time.Minute)
	if !w.RunOnce(ctx) {
		t.Fatal("the retry wasn't claimed once its wait was over")
	}
	j = job(id)
	if j["status"] != "queued" || j["attempts"] != float64(2) || j["next_attempt_at"] != formatTime(f.clock.Add(2*time.Minute)) {
		t.Fatalf("after the second failure: %v", j)
	}
	*f.clock = f.clock.Add(2 * time.Minute)
	if !w.RunOnce(ctx) {
		t.Fatal("the last attempt wasn't claimed")
	}
	j = job(id)
	if r, _ := j["result"].(map[string]any); j["status"] != "done" || j["attempts"] != float64(3) || j["next_attempt_at"] != nil || r["status"] != "error" {
		t.Fatalf("after the last attempt: %v", j)
	}
	if w.RunOnce(ctx) {
		t.Error("a finished job was claimed again")
	}
	// Each attempt is its own record in the history, all with the job's id.
	recs, _, _ := f.svc.Invocations(ctx, auth.InvocationFilter{Function: "app/flaky"})
	if len(recs) != 3 {
		t.Fatalf("invocation records: %d", len(recs))
	}
	for _, r := range recs {
		if r.JobID != id || r.Status != "error" {
			t.Errorf("record: %+v", r)
		}
	}

	// It succeeds on the second attempt: done, ok, nothing more to run.
	calls := 0
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		calls++
		if calls == 1 {
			return executor.Result{Status: executor.StatusTimeout}, nil
		}
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`{"ok":true}`)}, nil
	})
	id = enqueue()
	w.RunOnce(ctx)
	*f.clock = f.clock.Add(time.Minute)
	w.RunOnce(ctx)
	if j = job(id); j["status"] != "done" || j["attempts"] != float64(2) || j["result"].(map[string]any)["status"] != "ok" {
		t.Errorf("success on a retry: %v", j)
	}

	// What the function chose (ctx.error), or that would repeat, isn't retried.
	for name, res := range map[string]executor.Result{
		"function error": {Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 409, Code: "conflict", Message: "no"}},
		"output too big": {Status: executor.StatusOutputTooBig},
	} {
		f.runner.set(func(executor.InvokeRequest) (executor.Result, error) { return res, nil })
		id = enqueue()
		w.RunOnce(ctx)
		if j = job(id); j["status"] != "done" || j["attempts"] != float64(1) {
			t.Errorf("%s was retried: %v", name, j)
		}
	}

	// A worker lost mid-run isn't a failure: the lease expires and the job is
	// claimed again, and the attempt that never finished doesn't use one up.
	f.runner.set(failing(executor.StatusError))
	id = enqueue()
	if _, found, _ := f.svc.ClaimJob(ctx, "lost-worker"); !found {
		t.Fatal("couldn't claim the job")
	}
	*f.clock = f.clock.Add(time.Hour)
	for i, want := range []string{"queued", "queued", "done"} {
		if !w.RunOnce(ctx) {
			t.Fatalf("attempt %d wasn't claimed", i)
		}
		if got := job(id)["status"]; got != want {
			t.Fatalf("attempt %d: status %v, want %s", i, got, want)
		}
		*f.clock = f.clock.Add(time.Hour)
	}
}
