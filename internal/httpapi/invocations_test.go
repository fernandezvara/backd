package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
)

func TestFunctionInvocationRecorded(t *testing.T) {
	f := newRulesFixture(t)
	f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{
			Status:     executor.StatusOK,
			Output:     req.Envelope.Input,
			Logs:       []executor.LogLine{{Level: "log", Line: "hi"}},
			DurationMS: 12,
		}, nil
	})

	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/typed", `{"n": 1}`, bearer(f.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("call: %d %v", rec.Code, out)
	}

	recs, more, err := f.svc.Invocations(context.Background(), auth.InvocationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if more || len(recs) != 1 {
		t.Fatalf("recs = %v, more = %v", recs, more)
	}
	r := recs[0]
	if r.Function != "app/typed" {
		t.Errorf("function = %q", r.Function)
	}
	if r.Actor != "user:"+f.adaID {
		t.Errorf("actor = %q, want user:%s", r.Actor, f.adaID)
	}
	if r.Mode != "sync" {
		t.Errorf("mode = %q", r.Mode)
	}
	if r.Status != executor.StatusOK {
		t.Errorf("status = %q", r.Status)
	}
	if r.Code != "" {
		t.Errorf("code = %q, want empty for a successful call", r.Code)
	}
	if r.DurationMS != 12 {
		t.Errorf("duration_ms = %d", r.DurationMS)
	}
	if r.RequestID == "" {
		t.Error("no request id")
	}
	if len(r.Logs) != 1 || r.Logs[0].Line != "hi" {
		t.Errorf("logs = %v", r.Logs)
	}
	if r.ExpiresAt.Before(r.At) {
		t.Errorf("expires_at %v is before at %v", r.ExpiresAt, r.At)
	}
}

// A function error is recorded with its own code.
func TestFunctionInvocationRecordedOnFunctionError(t *testing.T) {
	f := newRulesFixture(t)
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{
			Status:        executor.StatusFunctionError,
			FunctionError: &executor.FunctionError{Status: 409, Code: "out_of_stock", Message: "no stock"},
			DurationMS:    3,
		}, nil
	})
	if rec, out := f.doH(t, "POST", "/v1/acme/app/_func/echo", `{}`, bearer(f.ada)); rec.Code != 409 {
		t.Fatalf("call: %d %v", rec.Code, out)
	}
	recs, _, err := f.svc.Invocations(context.Background(), auth.InvocationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Status != executor.StatusFunctionError || recs[0].Code != "out_of_stock" {
		t.Errorf("recs = %+v", recs)
	}
}

// Anonymous calls are recorded too, with "anonymous" as the actor.
func TestFunctionInvocationRecordedAnonymous(t *testing.T) {
	f := newRulesFixture(t)
	if rec, out := f.doH(t, "POST", "/v1/acme/app/_func/typed", `{"n": 1}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("call: %d %v", rec.Code, out)
	}
	recs, _, err := f.svc.Invocations(context.Background(), auth.InvocationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Actor != "anonymous" {
		t.Errorf("recs = %+v", recs)
	}
}
