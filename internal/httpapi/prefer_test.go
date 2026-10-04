package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrefersAsync(t *testing.T) {
	for _, tc := range []struct {
		prefer []string
		want   bool
	}{
		{nil, false},
		{[]string{"respond-async"}, true},
		{[]string{"RESPOND-ASYNC"}, true},
		{[]string{"wait=5, respond-async"}, true},
		{[]string{"respond-async; x=1"}, true},
		{[]string{"return=minimal", "respond-async"}, true},
		{[]string{"wait=5"}, false},
		{[]string{"respond-asynchronously"}, false},
		{[]string{""}, false},
	} {
		r := httptest.NewRequest("POST", "/", nil)
		for _, p := range tc.prefer {
			r.Header.Add("Prefer", p)
		}
		if got := prefersAsync(r); got != tc.want {
			t.Errorf("Prefer %q: %v, want %v", tc.prefer, got, tc.want)
		}
	}
}

// A sync function is answered inline; with `Prefer: respond-async` the call
// becomes a job (202 and its location), run later by a worker like any async
// job, and the answer says the preference was applied.
func TestPreferRespondAsyncOnASyncFunction(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	ctx := context.Background()
	const echo = "/v1/acme/app/_func/echo"
	prefer := func(token string) map[string]string {
		h := jsonAs(f.ada)
		h["Prefer"] = token
		return h
	}

	// Without the header nothing changes.
	rec, out := f.doH(t, "POST", echo, `{"n": 1}`, jsonAs(f.ada))
	if rec.Code != http.StatusOK || rec.Header().Get("Preference-Applied") != "" {
		t.Fatalf("plain call: %d %v %v", rec.Code, rec.Header(), out)
	}

	// With it, a job: queued, nothing ran inline.
	calls := len(f.runner.reqs)
	rec, out = f.doH(t, "POST", echo, `{"n": 2}`, prefer("respond-async"))
	if rec.Code != http.StatusAccepted || rec.Header().Get("Preference-Applied") != "respond-async" {
		t.Fatalf("respond-async: %d %v %v", rec.Code, rec.Header(), out)
	}
	id, _ := out["id"].(string)
	if out["status"] != "queued" || rec.Header().Get("Location") != "/v1/acme/app/_jobs/"+id || len(f.runner.reqs) != calls {
		t.Errorf("job not queued: %v (ran %d times)", out, len(f.runner.reqs)-calls)
	}
	// The caller polls it like any job; nobody else reads it.
	if code, got := f.as(t, f.bob, "GET", "/v1/acme/app/_jobs/"+id, ""); code != http.StatusNotFound {
		t.Errorf("another user reads the job: %d %v", code, got)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("the worker found no job")
	}
	req := f.runner.last()
	if req.Function != "acme/app/echo" || string(req.Envelope.Input) != `{"n": 2}` || req.Envelope.Mode != "sync" {
		t.Errorf("executor request: %+v", req)
	}
	code, got := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/"+id, "")
	result, _ := got["result"].(map[string]any)
	if code != http.StatusOK || got["status"] != "done" || result["status"] != "ok" {
		t.Errorf("job after the worker: %d %v", code, got)
	}
	// One attempt: a failure is the result, not retried.
	rec, out = f.doH(t, "POST", echo, `{"n": 3}`, prefer("wait=5, respond-async"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second job: %d %v", rec.Code, out)
	}

	// The checks come first: input that doesn't fit is still a 400.
	if rec, out := f.doH(t, "POST", "/v1/acme/app/_func/typed", `{"n": "x"}`, prefer("respond-async")); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid input with the preference: %d %v", rec.Code, out)
	}
	// A caller who may not call it is refused, preference or not.
	if rec, _ := f.doH(t, "POST", echo, `{}`, map[string]string{"Authorization": "Bearer " + f.carl, "Content-Type": "application/json", "Prefer": "respond-async"}); rec.Code != http.StatusForbidden {
		t.Errorf("a caller the invoke rule refuses: %d", rec.Code)
	}
}

// An async function is always a job; the header only gets acknowledged. A
// webhook function ignores it (the sender waits for the function's response).
func TestPreferWithAsyncAndWebhookFunctions(t *testing.T) {
	f := newRulesFixture(t)
	h := jsonAs(f.ada)
	h["Prefer"] = "respond-async"
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/job", `{"n": 1}`, h)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Preference-Applied") != "respond-async" {
		t.Errorf("async function: %d %v %v", rec.Code, rec.Header(), out)
	}
	rec, _ = f.doH(t, "POST", "/v1/acme/app/_func/job", `{"n": 1}`, jsonAs(f.ada))
	if rec.Code != http.StatusAccepted || rec.Header().Get("Preference-Applied") != "" {
		t.Errorf("async function without the header: %d %v", rec.Code, rec.Header())
	}
	hook := map[string]string{"Prefer": "respond-async", "Content-Type": "text/plain"}
	rec, _ = f.doH(t, "POST", "/v1/acme/app/_func/hook", "raw body", hook)
	if rec.Code == http.StatusAccepted || rec.Header().Get("Preference-Applied") != "" {
		t.Errorf("a webhook function must ignore the preference: %d %v", rec.Code, rec.Header())
	}
}

// The idempotency key binds the call to its first outcome, whichever way the
// caller preferred it the second time.
func TestPreferWithIdempotencyKey(t *testing.T) {
	f := newRulesFixture(t)
	const billed = "/v1/acme/app/_func/billed"
	first := jsonAs(f.ada)
	first["Idempotency-Key"] = "k1"
	first["Prefer"] = "respond-async"
	rec, out := f.doH(t, "POST", billed, `{"n": 1}`, first)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first call: %d %v", rec.Code, out)
	}
	id := out["id"]

	again := jsonAs(f.ada) // no Prefer this time
	again["Idempotency-Key"] = "k1"
	rec, out = f.doH(t, "POST", billed, `{"n": 1}`, again)
	if rec.Code != http.StatusAccepted || out["id"] != id || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay without the preference: %d %v %v, want the same job", rec.Code, rec.Header(), out)
	}
	if rec, _ := f.doH(t, "POST", billed, `{"n": 2}`, again); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("same key, other input: %d", rec.Code)
	}

	// A call answered inline replays inline, even when asked for a job later.
	inline := jsonAs(f.ada)
	inline["Idempotency-Key"] = "k2"
	rec, out = f.doH(t, "POST", billed, `{"n": 5}`, inline)
	if rec.Code != http.StatusOK {
		t.Fatalf("inline call: %d %v", rec.Code, out)
	}
	inline["Prefer"] = "respond-async"
	if rec, out = f.doH(t, "POST", billed, `{"n": 5}`, inline); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay of an inline call: %d %v %v", rec.Code, rec.Header(), out)
	}
}
