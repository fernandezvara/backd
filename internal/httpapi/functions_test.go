package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
)

// fnDir is the rules fixture's functions project.
const fnDir = "acme/app/" + registry.FunctionsDir + "/"

const executorToken = "executor-token-0123456789abcdef0123"

// writeManifest writes a build of every function in dir, as
// `backd functions build` would (the bundles' content doesn't matter here).
func writeManifest(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	m := registry.Manifest{Deno: registry.DenoVersion, Functions: map[string]registry.ManifestBundle{}}
	os.MkdirAll(filepath.Join(dir, registry.BuildDir), 0o755)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data := []byte("export default () => '" + e.Name() + "';\n")
		sum := sha256.Sum256(data)
		file := e.Name() + ".js"
		os.WriteFile(filepath.Join(dir, registry.BuildDir, file), data, 0o644)
		m.Functions[e.Name()] = registry.ManifestBundle{Bundle: file, SHA256: hex.EncodeToString(sum[:])}
	}
	m.Source, _ = registry.SourceHash(dir)
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, registry.BuildDir, registry.ManifestFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRunner stands in for the executor: it records invocations and, by
// default, returns the input as the output.
type fakeRunner struct {
	mu     sync.Mutex
	reqs   []executor.InvokeRequest
	handle func(executor.InvokeRequest) (executor.Result, error)
}

func (f *fakeRunner) Invoke(_ context.Context, req executor.InvokeRequest) (executor.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if f.handle != nil {
		return f.handle(req)
	}
	return executor.Result{Status: executor.StatusOK, Output: req.Envelope.Input}, nil
}

func (f *fakeRunner) last() executor.InvokeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeRunner) set(h func(executor.InvokeRequest) (executor.Result, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handle = h
}

func TestFunctionCalls(t *testing.T) {
	f := newRulesFixture(t)
	const echo = "/v1/acme/app/_func/echo"
	call := func(cred, path, body string, hdr ...string) (int, map[string]any, http.Header) {
		t.Helper()
		h := map[string]string{"Content-Type": "application/json"}
		if cred != "" {
			h["Authorization"] = "Bearer " + cred
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			h[hdr[i]] = hdr[i+1]
		}
		rec, out := f.doH(t, "POST", path, body, h)
		return rec.Code, out, rec.Header()
	}

	// Invoke rules: echo is for verified users; API keys always may call.
	for _, tt := range []struct {
		name, cred string
		want       int
	}{
		{"anonymous", "", 401}, {"unverified user", f.carl, 403}, {"verified user", f.ada, 200}, {"API key", f.key, 200},
	} {
		if code, out, _ := call(tt.cred, echo, `{"n": 1}`); code != tt.want {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
	}

	// The invocation: limits, bundle, envelope with ctx.user and a callback
	// token that acts as the caller.
	code, out, hdr := call(f.ada, echo, `{"n": 2}`, "Idempotency-Key", "idem-1", "X-Request-ID", "req-7")
	if code != 200 || out["n"] != float64(2) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("echo: %d %v %v", code, out, hdr)
	}
	req := f.runner.last()
	if req.Function != "acme/app/echo" || req.TimeoutMS != 10000 || req.MemoryMB != 128 || req.MaxOutput != 1<<20 ||
		!strings.HasPrefix(req.Bundle.URL, "http://backd-internal:8081/_internal/functions/") || len(req.Bundle.SHA256) != 64 {
		t.Errorf("request: %+v", req)
	}
	env := req.Envelope
	var user map[string]any
	json.Unmarshal(env.User, &user)
	if user["id"] != f.adaID || user["email"] != "ada@example.com" || env.IdempotencyKey != "idem-1" || env.RequestID != "req-7" || string(env.Input) != `{"n": 2}` {
		t.Errorf("envelope: %+v user %v", env, user)
	}
	if env.Callback == nil || env.Callback.URL != "http://backd-internal:8081" || env.Callback.Realm != "acme" || env.Callback.AdminToken != "" {
		t.Fatalf("callback: %+v", env.Callback)
	}
	claims, err := auth.VerifyCallback(f.callbackKey, env.Callback.Token, *f.clock)
	if err != nil || claims.UserID != f.adaID || claims.Function != "app/echo" || claims.Admin || !claims.Expires.Equal(f.clock.Add(15*time.Second)) {
		t.Errorf("callback token: %+v %v", claims, err)
	}
	// An API key acting on behalf of a user: ctx.user is that user.
	call(f.key, echo, `null`, onBehalfHeader, f.bobID)
	if env := f.runner.last().Envelope; !strings.Contains(string(env.User), f.bobID) {
		t.Errorf("on behalf: %s", env.User)
	}
	// admin: true adds a full-access token.
	call(f.ada, "/v1/acme/app/_func/admin", `{}`)
	if cb := f.runner.last().Envelope.Callback; cb.AdminToken == "" {
		t.Error("admin function without an admin token")
	} else if c, err := auth.VerifyCallback(f.callbackKey, cb.AdminToken, *f.clock); err != nil || !c.Admin || c.UserID != "" {
		t.Errorf("admin token: %+v %v", c, err)
	}

	// Input and output schemas.
	if code, out, _ := call(f.ada, "/v1/acme/app/_func/typed", `{"n": "two"}`); code != 400 || out["error"].(map[string]any)["code"] != "validation_error" {
		t.Errorf("bad input: %d %v", code, out)
	}
	if code, _, _ := call(f.ada, "/v1/acme/app/_func/typed", `{"n": 2}`); code != 200 {
		t.Errorf("good input: %d", code)
	}
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`[1, 2]`)}, nil
	})
	if code, out, _ := call(f.ada, "/v1/acme/app/_func/typed", `{"n": 2}`); code != 500 || out["error"].(map[string]any)["code"] != "invalid_output" {
		t.Errorf("bad output: %d %v", code, out)
	}

	// How results map to answers.
	errCode := func(out map[string]any) string {
		e, _ := out["error"].(map[string]any)
		c, _ := e["code"].(string)
		return c
	}
	for _, tt := range []struct {
		name   string
		res    executor.Result
		err    error
		status int
		code   string
	}{
		{"function error", executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 409, Code: "out_of_stock", Message: "no stock", Details: json.RawMessage(`[{"path": "items[0]", "reason": "sold out"}]`)}}, nil, 409, "out_of_stock"},
		{"function error with a 5xx", executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 500, Code: "x", Message: "y"}}, nil, 500, "function_failed"},
		{"function error with a bad code", executor.Result{Status: executor.StatusFunctionError, FunctionError: &executor.FunctionError{Status: 400, Code: "Bad Code", Message: "y"}}, nil, 500, "function_failed"},
		{"thrown", executor.Result{Status: executor.StatusError, Message: "TypeError: x is undefined\n    at handler"}, nil, 500, "function_failed"},
		{"memory", executor.Result{Status: executor.StatusMemory}, nil, 500, "function_failed"},
		{"timeout", executor.Result{Status: executor.StatusTimeout}, nil, 504, "function_timeout"},
		{"output too large", executor.Result{Status: executor.StatusOutputTooBig}, nil, 500, "invalid_output"},
		{"busy", executor.Result{Status: executor.StatusBusy}, nil, 503, "unavailable"},
		{"executor down", executor.Result{}, errors.New("connection refused"), 503, "unavailable"},
	} {
		f.runner.set(func(executor.InvokeRequest) (executor.Result, error) { return tt.res, tt.err })
		code, out, hdr := call(f.ada, echo, `{}`)
		if code != tt.status || errCode(out) != tt.code {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
		if tt.name == "function error" {
			if d, _ := out["error"].(map[string]any)["details"].([]any); len(d) != 1 || out["error"].(map[string]any)["message"] != "no stock" {
				t.Errorf("function error details: %v", out)
			}
		}
		if tt.name == "busy" && hdr.Get("Retry-After") != "1" {
			t.Error("busy without Retry-After")
		}
		if tt.name == "thrown" && strings.Contains(strings.Join(func() []string { b, _ := json.Marshal(out); return []string{string(b)} }(), ""), "TypeError") {
			t.Error("stack trace reached the caller")
		}
	}
	if !strings.Contains(f.log.String(), `"msg":"function failed"`) || !strings.Contains(f.log.String(), "TypeError: x is undefined") {
		t.Error("failures aren't logged")
	}
	f.runner.set(nil)

	// Everything else.
	for _, tt := range []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"unknown function", "/v1/acme/app/_func/nope", `{}`, 404, "not_found"},
		{"unknown database", "/v1/acme/nope/_func/echo", `{}`, 404, "not_found"},
		{"secrets, not yet", "/v1/acme/app/_func/keyed", `{}`, 500, "secret_missing"},
		{"invalid JSON", echo, `{`, 400, "invalid_json"},
	} {
		if code, out, _ := call(f.ada, tt.path, tt.body); code != tt.status || errCode(out) != tt.code {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
	}
	if code, out, _ := call(f.ada, echo, ``); code != 200 || out != nil {
		t.Errorf("empty body is null: %d %v", code, out)
	}
	rec, _ := f.doH(t, "POST", echo, `{}`, map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "text/plain"})
	if rec.Code != 415 {
		t.Errorf("content type: %d", rec.Code)
	}
	// Callback tokens don't work on the public listener.
	tok := auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "acme", Function: "app/echo", Admin: true, Expires: f.clock.Add(time.Minute)})
	if code, _ := f.as(t, tok, "GET", "/v1/acme/app/notes", ""); code != 401 {
		t.Errorf("callback token on the public listener: %d", code)
	}
}

// TestFunctionAsync proves enqueuing (roadmap F11): a 202 with a job id
// and Location, and that only the job's own caller (or an API key) can
// read it back. Running the job is a worker's job (see worker_test.go).
func TestFunctionAsync(t *testing.T) {
	f := newRulesFixture(t)
	const job = "/v1/acme/app/_func/job"

	rec, out := f.doH(t, "POST", job, `{"n": 1}`, bearer(f.ada))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", rec.Code, out)
	}
	id, _ := out["id"].(string)
	if id == "" || out["function"] != "app/job" || out["status"] != "queued" || out["result"] != nil || out["created_at"] == nil {
		t.Fatalf("enqueue body: %v", out)
	}
	wantLoc := "/v1/acme/app/_jobs/" + id
	if loc := rec.Header().Get("Location"); loc != wantLoc {
		t.Errorf("Location = %q, want %q", loc, wantLoc)
	}
	if len(f.runner.reqs) != 0 {
		t.Error("enqueue must not call the executor; a worker does")
	}

	jobPath := "/v1/acme/app/_jobs/" + id
	if code, out := f.as(t, f.ada, "GET", jobPath, ""); code != 200 || out["status"] != "queued" {
		t.Errorf("owner reads their job: %d %v", code, out)
	}
	if code, _ := f.as(t, f.bob, "GET", jobPath, ""); code != 404 {
		t.Errorf("another user reads someone else's job: %d, want 404", code)
	}
	if code, _ := f.as(t, f.key, "GET", jobPath, ""); code != 200 {
		t.Errorf("an API key reads any job: %d, want 200", code)
	}
	if code, _ := f.as(t, "", "GET", jobPath, ""); code != 401 {
		t.Errorf("anonymous read: %d, want 401", code)
	}
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/app/_jobs/nonexistent", ""); code != 404 {
		t.Errorf("unknown job id: %d, want 404", code)
	}
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/nope/_jobs/"+id, ""); code != 404 {
		t.Errorf("wrong database: %d, want 404", code)
	}
}

// TestFunctionSecrets proves a declared secret reaches ctx.secrets
// (Envelope.Secrets), keyed exactly as function.yaml declares it, once
// it's configured and set — and stays secret_missing until then.
func TestFunctionSecrets(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()

	cipher, err := auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Cipher = cipher
	f.svc.Cache = auth.NewSecretCache()

	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_func/keyed", `{}`); code != 500 || errCode(out) != "secret_missing" {
		t.Fatalf("before it's set: %d %v", code, out)
	}

	if err := f.svc.SetSecret(ctx, "app", "KEY", "sk_live_topsecret", "test"); err != nil {
		t.Fatal(err)
	}
	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_func/keyed", `{}`); code != 200 {
		t.Fatalf("after it's set: %d %v", code, out)
	}
	if got := f.runner.last().Envelope.Secrets; got["KEY"] != "sk_live_topsecret" {
		t.Errorf("Envelope.Secrets = %+v", got)
	}
}

// writeDevManifest (re)writes a one-function manifest whose bundle content
// (and so its sha256) is content, simulating a rebuild.
func writeDevManifest(t *testing.T, dir, content string) string {
	t.Helper()
	buildDir := filepath.Join(dir, registry.BuildDir)
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	file := "hello-" + hash[:8] + ".js"
	if err := os.WriteFile(filepath.Join(buildDir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := registry.Manifest{Deno: registry.DenoVersion, Functions: map[string]registry.ManifestBundle{"hello": {Bundle: file, SHA256: hash}}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, registry.ManifestFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return hash
}

// TestFunctionsDevReindex checks BACKD_DEV's promise: a background rebuild
// (which only ever changes .build/manifest.json; sources aren't read here)
// is served without a restart, unlike the normal once-per-process cache.
func TestFunctionsDevReindex(t *testing.T) {
	for _, dev := range []bool{false, true} {
		t.Run(fmt.Sprintf("dev=%v", dev), func(t *testing.T) {
			root := t.TempDir()
			fn := filepath.Join(root, "acme", "app", registry.FunctionsDir)
			if err := os.MkdirAll(filepath.Join(fn, "hello"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "acme", "realm.yaml"), []byte("auth: disabled\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fn, "hello", "function.yaml"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fn, "hello", "index.js"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			first := writeDevManifest(t, fn, "v1")
			reg, err := registry.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{}
			f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
				c.Functions = runner
				c.Dev = dev
				c.CallbackURL = "http://backd-internal:8081"
				c.ExecutorToken = executorToken
			})
			if rec, _ := f.do(t, "POST", "/v1/acme/app/_func/hello", "{}"); rec.Code != 200 {
				t.Fatalf("first call: %d", rec.Code)
			}
			if got := runner.last().Bundle.SHA256; got != first {
				t.Fatalf("first call bundle = %s, want %s", got, first)
			}

			second := writeDevManifest(t, fn, "v2")
			if rec, _ := f.do(t, "POST", "/v1/acme/app/_func/hello", "{}"); rec.Code != 200 {
				t.Fatalf("second call: %d", rec.Code)
			}
			got := runner.last().Bundle.SHA256
			if dev && got != second {
				t.Errorf("dev mode: bundle = %s, want the rebuilt %s", got, second)
			}
			if !dev && got != first {
				t.Errorf("non-dev: bundle = %s, want the cached %s (no restart happened)", got, first)
			}
		})
	}
}

// The internal listener: bundles for the executor, and data for functions
// calling back with callback tokens (acting as their caller, or as
// themselves with admin access).
func TestInternalListener(t *testing.T) {
	f := newRulesFixture(t)
	do := func(method, path, body, token string, hdr ...string) (int, map[string]any, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Body.Bytes()
	}

	// Bundles, by hash, for the executor only.
	f.doH(t, "POST", "/v1/acme/app/_func/echo", `{}`, map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"})
	b := f.runner.last().Bundle
	path := strings.TrimPrefix(b.URL, "http://backd-internal:8081")
	if code, _, body := do("GET", path, "", executorToken); code != 200 || !strings.Contains(string(body), "'echo'") {
		t.Errorf("bundle: %d %q", code, body)
	} else if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != b.SHA256 {
		t.Error("bundle doesn't match its hash")
	}
	for _, tt := range []struct {
		path, token string
		want        int
	}{
		{path, "", 401}, {path, "wrong", 401}, {path, f.key, 401},
		{"/_internal/functions/" + strings.Repeat("0", 64), executorToken, 404},
	} {
		if code, _, _ := do("GET", tt.path, "", tt.token); code != tt.want {
			t.Errorf("GET %s with %q: %d, want %d", tt.path, tt.token, code, tt.want)
		}
	}

	token := func(c auth.CallbackClaims) string {
		c.Realm, c.Function = "acme", "app/echo"
		if c.Expires.IsZero() {
			c.Expires = f.clock.Add(time.Minute)
		}
		return auth.SignCallback(f.callbackKey, c)
	}
	asAda := token(auth.CallbackClaims{UserID: f.adaID})
	admin := token(auth.CallbackClaims{Admin: true})

	// Acting as ada: her rules and ownership apply (notes: signed-in users).
	code, created, _ := do("POST", "/v1/acme/app/notes", `{"title": "from a function"}`, asAda)
	if code != 201 {
		t.Fatalf("create as ada: %d %v", code, created)
	}
	meta := created["_meta"].(map[string]any)
	if meta["owner"] != f.adaID || meta["created_by"] != "user:"+f.adaID {
		t.Errorf("ownership as ada: %v", meta)
	}
	if code, _, _ := do("POST", "/v1/acme/app/private", `{"title": "x"}`, asAda); code != 403 {
		t.Errorf("rules still apply: %d", code)
	}
	// Anonymous caller: rules see no user.
	if code, _, _ := do("GET", "/v1/acme/app/notes", "", token(auth.CallbackClaims{})); code != 401 {
		t.Errorf("anonymous callback: %d", code)
	}
	// Admin: full access, recorded as the function.
	code, adm, _ := do("POST", "/v1/acme/app/private", `{"title": "by the function"}`, admin)
	if code != 201 || adm["_meta"].(map[string]any)["created_by"] != "func:acme/app/echo" || adm["_meta"].(map[string]any)["owner"] != nil {
		t.Errorf("admin write: %d %v", code, adm)
	}
	if !strings.Contains(f.log.String(), `"actor":"func:acme/app/echo as user:`+f.adaID+`"`) || !strings.Contains(f.log.String(), `"actor":"func:acme/app/echo"`) {
		t.Error("callbacks not logged with the function as actor")
	}

	// Only valid, unexpired callback tokens for this realm; nothing else.
	for _, tt := range []struct {
		name, token string
		hdr         []string
		want        int
	}{
		{"no token", "", nil, 401},
		{"session", f.ada, nil, 401},
		{"API key", f.key, nil, 401},
		{"expired", token(auth.CallbackClaims{UserID: f.adaID, Expires: f.clock.Add(-time.Second)}), nil, 401},
		{"forged", strings.TrimSuffix(asAda, asAda[len(asAda)-4:]) + "AAAA", nil, 401},
		{"other key", auth.SignCallback([]byte("another-key-0123456789abcdef012345"), auth.CallbackClaims{Realm: "acme", Function: "app/echo", Admin: true, Expires: f.clock.Add(time.Minute)}), nil, 401},
		{"other realm", auth.SignCallback(f.callbackKey, auth.CallbackClaims{Realm: "other", Function: "app/echo", Admin: true, Expires: f.clock.Add(time.Minute)}), nil, 401},
		{"on behalf", admin, []string{onBehalfHeader, f.bobID}, 400},
	} {
		if code, _, _ := do("GET", "/v1/acme/app/notes", "", tt.token, tt.hdr...); code != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, code, tt.want)
		}
	}
	// A user disabled during the invocation loses access at once.
	if err := f.svc.SetDisabled(context.Background(), "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := do("GET", "/v1/acme/app/notes", "", asAda); code != 401 {
		t.Errorf("disabled user: %d", code)
	}
	// Nothing but data and bundles on the internal listener.
	for _, p := range []string{"/v1/acme/_auth/me", "/v1/acme/_admin/users"} {
		if code, _, _ := do("GET", p, "", admin); code != 404 {
			t.Errorf("GET %s on the internal listener: %d", p, code)
		}
	}
	if code, _, _ := do("POST", "/v1/acme/app/_func/echo", `{}`, admin); code != 404 && code != 405 {
		t.Errorf("functions calling functions: %d", code)
	}
}

// An internal function has no HTTP route: every caller gets the 404 of a
// function that doesn't exist, and nothing reaches the executor.
func TestInternalFunctionHasNoRoute(t *testing.T) {
	f := newRulesFixture(t)
	before := len(f.runner.reqs)
	for _, tt := range []struct{ name, cred string }{
		{"anonymous", ""}, {"user", f.ada}, {"api key", f.key},
	} {
		h := map[string]string{"Content-Type": "application/json"}
		if tt.cred != "" {
			h["Authorization"] = "Bearer " + tt.cred
		}
		rec, out := f.doH(t, "POST", "/v1/acme/app/_func/cleanup", `{}`, h)
		_, missing := f.doH(t, "POST", "/v1/acme/app/_func/nothing_here", `{}`, h)
		if rec.Code != 404 || out["error"].(map[string]any)["code"] != "not_found" {
			t.Errorf("%s: %d %v", tt.name, rec.Code, out)
		}
		if out["error"].(map[string]any)["message"] != missing["error"].(map[string]any)["message"] {
			t.Errorf("%s: an internal function answers differently from a missing one: %v / %v", tt.name, out, missing)
		}
	}
	if len(f.runner.reqs) != before {
		t.Error("an internal function reached the executor")
	}
}
