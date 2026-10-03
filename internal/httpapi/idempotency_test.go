package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/metrics"
)

func idemHeaders(cred, key string) map[string]string {
	h := bearer(cred)
	h["Content-Type"] = "application/json"
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return h
}

// TestIdempotencyRequired proves idempotency: required refuses calls
// with no key, and accepts calls with one (roadmap F12).
func TestIdempotencyRequired(t *testing.T) {
	f := newRulesFixture(t)
	const billed = "/v1/acme/app/_func/billed"

	rec, out := f.doH(t, "POST", billed, `{}`, idemHeaders(f.ada, ""))
	if rec.Code != 400 || out["error"].(map[string]any)["code"] != "idempotency_key_required" {
		t.Fatalf("no key: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", billed, `{}`, idemHeaders(f.ada, "order-42"))
	if rec.Code != 200 {
		t.Fatalf("with key: %d %v", rec.Code, out)
	}
}

func TestIdempotencyInvalidKeyFormat(t *testing.T) {
	f := newRulesFixture(t)
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/echo", `{}`, idemHeaders(f.ada, "has a space"))
	if rec.Code != 400 || out["error"].(map[string]any)["code"] != "invalid_header" {
		t.Fatalf("bad key format: %d %v", rec.Code, out)
	}
}

// TestIdempotencySyncReplay proves a repeated key with the same input
// replays the stored response instead of calling the executor again,
// and that different input with the same key is refused.
func TestIdempotencySyncReplay(t *testing.T) {
	f := newRulesFixture(t)
	const echo = "/v1/acme/app/_func/echo"

	rec1, out1 := f.doH(t, "POST", echo, `{"n": 1}`, idemHeaders(f.ada, "call-1"))
	if rec1.Code != 200 || out1["n"] != float64(1) {
		t.Fatalf("first call: %d %v", rec1.Code, out1)
	}
	if calls := len(f.runner.reqs); calls != 1 {
		t.Fatalf("executor called %d times after first call", calls)
	}

	rec2, out2 := f.doH(t, "POST", echo, `{"n": 1}`, idemHeaders(f.ada, "call-1"))
	if rec2.Code != 200 || out2["n"] != float64(1) {
		t.Fatalf("replay: %d %v", rec2.Code, out2)
	}
	if calls := len(f.runner.reqs); calls != 1 {
		t.Errorf("executor called again on replay: %d calls total", calls)
	}

	rec3, out3 := f.doH(t, "POST", echo, `{"n": 2}`, idemHeaders(f.ada, "call-1"))
	if rec3.Code != 422 || out3["error"].(map[string]any)["code"] != "idempotency_key_reused" {
		t.Fatalf("different input, same key: %d %v", rec3.Code, out3)
	}
}

// TestIdempotencyFunctionErrorReplay proves a function error is
// replayed with its original status, code and message.
func TestIdempotencyFunctionErrorReplay(t *testing.T) {
	f := newRulesFixture(t)
	const echo = "/v1/acme/app/_func/echo"
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 409, Code: "out_of_stock", Message: "no stock"}}, nil
	})
	rec1, out1 := f.doH(t, "POST", echo, `{}`, idemHeaders(f.ada, "charge-1"))
	if rec1.Code != 409 || out1["error"].(map[string]any)["code"] != "out_of_stock" {
		t.Fatalf("first call: %d %v", rec1.Code, out1)
	}
	f.runner.set(nil) // if replay calls the executor again, this would answer 200 {"n":...} instead
	rec2, out2 := f.doH(t, "POST", echo, `{}`, idemHeaders(f.ada, "charge-1"))
	if rec2.Code != 409 || out2["error"].(map[string]any)["code"] != "out_of_stock" || out2["error"].(map[string]any)["message"] != "no stock" {
		t.Fatalf("replay: %d %v", rec2.Code, out2)
	}
}

// TestIdempotencyScopedPerCaller proves two different callers using the
// same client-chosen key never collide.
func TestIdempotencyScopedPerCaller(t *testing.T) {
	f := newRulesFixture(t)
	const echo = "/v1/acme/app/_func/echo"
	rec1, out1 := f.doH(t, "POST", echo, `{"n": 1}`, idemHeaders(f.ada, "shared-key"))
	rec2, out2 := f.doH(t, "POST", echo, `{"n": 1}`, idemHeaders(f.bob, "shared-key"))
	if rec1.Code != 200 || rec2.Code != 200 || out1["n"] != float64(1) || out2["n"] != float64(1) {
		t.Fatalf("two callers, same key: %d %v / %d %v", rec1.Code, out1, rec2.Code, out2)
	}
	if calls := len(f.runner.reqs); calls != 2 {
		t.Errorf("executor called %d times, want 2 (one per caller)", calls)
	}
}

// TestIdempotencyAsyncReplay proves a repeated key on an async function
// returns the same job id instead of enqueuing a second one.
func TestIdempotencyAsyncReplay(t *testing.T) {
	f := newRulesFixture(t)
	const job = "/v1/acme/app/_func/job"
	rec1, out1 := f.doH(t, "POST", job, `{}`, idemHeaders(f.ada, "job-key-1"))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first enqueue: %d %v", rec1.Code, out1)
	}
	id1 := out1["id"].(string)

	rec2, out2 := f.doH(t, "POST", job, `{}`, idemHeaders(f.ada, "job-key-1"))
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("replay enqueue: %d %v", rec2.Code, out2)
	}
	if out2["id"] != id1 {
		t.Errorf("replay returned a different job id: %v, want %v", out2["id"], id1)
	}
}

// TestIdempotencyConcurrentInProgress proves a second call with the
// same key while the first is still running answers 409, and that the
// first call's own eventual completion is unaffected.
func TestIdempotencyConcurrentInProgress(t *testing.T) {
	f := newRulesFixture(t)
	const echo = "/v1/acme/app/_func/echo"
	started, release := make(chan struct{}), make(chan struct{})
	f.runner.set(func(req executor.InvokeRequest) (executor.Result, error) {
		close(started)
		<-release
		return executor.Result{Status: executor.StatusOK, Output: req.Envelope.Input}, nil
	})
	done := make(chan struct{})
	go func() {
		rec, _ := f.doH(t, "POST", echo, `{"n": 9}`, idemHeaders(f.ada, "in-flight"))
		if rec.Code != 200 {
			t.Errorf("first call: %d", rec.Code)
		}
		close(done)
	}()
	<-started
	rec, out := f.doH(t, "POST", echo, `{"n": 9}`, idemHeaders(f.ada, "in-flight"))
	if rec.Code != 409 || out["error"].(map[string]any)["code"] != "request_in_progress" {
		t.Errorf("concurrent call: %d %v", rec.Code, out)
	}
	close(release)
	<-done
}

// TestIdempotencyReleasedOnTransientFailure proves a claim doesn't get
// stuck "running" forever when the call itself never really happened
// (the function's concurrency limit was full): a retry with the same
// key afterward must be able to actually run.
func TestIdempotencyReleasedOnTransientFailure(t *testing.T) {
	f := newRulesFixture(t)
	const limited = "/v1/acme/app/_func/limited"
	started, release, holderDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		close(started)
		<-release
		return executor.Result{Status: executor.StatusOK, Output: []byte("null")}, nil
	})
	go func() {
		f.doH(t, "POST", limited, `{}`, bearer(f.ada))
		close(holderDone)
	}()
	<-started
	rec, out := f.doH(t, "POST", limited, `{}`, idemHeaders(f.ada, "retry-me"))
	if rec.Code != 429 {
		t.Fatalf("concurrency limit: %d %v", rec.Code, out)
	}
	close(release)
	<-holderDone // the concurrency slot (and, with it, this test's claim) is released

	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Output: []byte(`{"ok":true}`)}, nil
	})
	rec, out = f.doH(t, "POST", limited, `{}`, idemHeaders(f.ada, "retry-me"))
	if rec.Code != 200 || out["ok"] != true {
		t.Errorf("retry after a released claim: %d %v", rec.Code, out)
	}
}

// TestFunctionAndSessionMetrics checks the metrics of calls, replays,
// refusals and sessions, and that none of them carries who called.
func TestFunctionAndSessionMetrics(t *testing.T) {
	m := metrics.New("dev", "x")
	f := newRulesFixture(t, func(c *Config) { c.Metrics = m })
	f.svc.Metrics, f.svc.Realm = m, "acme"
	const echo = "/v1/acme/app/_func/echo"

	f.doH(t, "POST", echo, `{"n": 1}`, idemHeaders(f.ada, "call-1"))
	f.doH(t, "POST", echo, `{"n": 1}`, idemHeaders(f.ada, "call-1")) // a replay
	f.doH(t, "POST", echo, `{"n": 2}`, idemHeaders(f.ada, "call-1")) // the key with other input
	f.doH(t, "POST", "/v1/acme/_auth/logout", ``, bearer(f.bob))     // a session ends

	var body strings.Builder
	rec := httptest.NewRecorder()
	m.Handler("").ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body.WriteString(rec.Body.String())
	got := body.String()
	for _, want := range []string{
		`backd_function_invocations_total{function="app/echo",mode="sync",realm="acme",status="ok"} 1`,
		`backd_function_idempotent_replays_total{function="app/echo",realm="acme"} 1`,
		`backd_function_refusals_total{function="app/echo",realm="acme",reason="idempotency_reused"} 1`,
		`backd_function_duration_seconds_count{function="app/echo",realm="acme"} 1`,
		`backd_sessions_ended_total{realm="acme",reason="logout"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, grepBackd(got))
		}
	}
	assertNoSensitiveMetrics(t, got)
	for _, leak := range []string{"ada@example.com", f.adaID, f.bobID, f.ada, "call-1"} {
		if strings.Contains(got, leak) {
			t.Errorf("the metrics contain %q", leak)
		}
	}
}

func grepBackd(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "backd_") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

var sensitiveInMetrics = regexp.MustCompile(`@|bd[a-z]_|[0-9a-v]{20}|\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

// assertNoSensitiveMetrics fails when the metrics text holds anything that
// looks like an email address, a token or key, an id or an IPv4 address.
func assertNoSensitiveMetrics(t *testing.T, body string) {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "go_") || strings.HasPrefix(l, "process_") {
			continue
		}
		if m := sensitiveInMetrics.FindString(l); m != "" {
			t.Errorf("a metric holds %q: %s", m, l)
		}
	}
}
