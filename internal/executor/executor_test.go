//go:build linux

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const token = "0123456789abcdef0123456789abcdef-executor"

// fixture is an executor plus a fake backd serving bundles (and recording
// callbacks). Tests are skipped when deno isn't installed; the dockerized
// test image has it.
type fixture struct {
	t       *testing.T
	ex      *Executor
	backd   *httptest.Server
	mu      sync.Mutex
	bundles map[string][]byte
	calls   []*http.Request
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	deno := os.Getenv("DENO")
	if deno == "" {
		deno = "deno"
	}
	if _, err := exec.LookPath(deno); err != nil {
		t.Skip("deno not found; skipping executor tests (they run in the dockerized test image)")
	}
	f := &fixture{t: t, bundles: map[string][]byte{}}
	f.backd = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if hash, ok := strings.CutPrefix(r.URL.Path, "/_internal/functions/"); ok {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			w.Write(f.bundles[hash])
			return
		}
		f.calls = append(f.calls, r.Clone(context.Background()))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items": [{"id": "n1", "title": "hello"}], "limit": 20, "skip": 0, "has_more": false}`))
	}))
	t.Cleanup(f.backd.Close)
	ex, err := New(Config{Deno: deno, Dir: t.TempDir(), Token: token, MaxProcesses: 8})
	if err != nil {
		t.Fatal(err)
	}
	f.ex = ex
	return f
}

// request makes an invocation of code (a JavaScript module) with defaults.
func (f *fixture) request(code string) InvokeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256([]byte(code))
	hash := hex.EncodeToString(sum[:])
	f.bundles[hash] = []byte(code)
	return InvokeRequest{
		Function:  "shop/app/test",
		Bundle:    Bundle{SHA256: hash, URL: f.backd.URL + "/_internal/functions/" + hash},
		TimeoutMS: 5000, MemoryMB: 64, MaxOutput: 1 << 20,
		Envelope: Envelope{Input: json.RawMessage(`{"n": 2}`), RequestID: "req1"},
	}
}

func (f *fixture) invoke(req InvokeRequest) Result {
	f.t.Helper()
	return f.ex.Invoke(context.Background(), req)
}

func TestInvoke(t *testing.T) {
	f := newFixture(t)

	res := f.invoke(f.request(`export default (ctx) => ({ doubled: ctx.input.n * 2, user: ctx.user, rid: ctx.requestId });`))
	if res.Status != StatusOK || string(res.Output) != `{"doubled":4,"user":null,"rid":"req1"}` {
		t.Fatalf("ok: %+v", res)
	}

	req := f.request(`export default async (ctx) => { console.log("key is", ctx.secrets.KEY); console.warn({ a: 1 }); return null; };`)
	req.Envelope.Secrets = map[string]string{"KEY": "sk_live_secret"}
	res = f.invoke(req)
	if res.Status != StatusOK || string(res.Output) != "null" || len(res.Logs) != 2 ||
		res.Logs[0] != (LogLine{Level: "log", Line: "key is ***"}) || res.Logs[1] != (LogLine{Level: "warn", Line: `{"a":1}`}) {
		t.Errorf("logs: %+v", res)
	}

	res = f.invoke(f.request(`export default (ctx) => { throw ctx.error(404, "not_found", "no such order", { id: "o1" }); };`))
	if fe := res.FunctionError; res.Status != StatusFunctionError || fe == nil || fe.Status != 404 || fe.Code != "not_found" ||
		fe.Message != "no such order" || string(fe.Details) != `{"id":"o1"}` {
		t.Errorf("function error: %+v %+v", res, res.FunctionError)
	}
	res = f.invoke(f.request(`export default () => { throw new Error("boom"); };`))
	if res.Status != StatusError || !strings.Contains(res.Message, "boom") {
		t.Errorf("thrown: %+v", res)
	}
	res = f.invoke(f.request(`export default (ctx) => { throw ctx.error(500, "x", "y"); };`))
	if res.Status != StatusError || !strings.Contains(res.Message, "status must be a 4xx code") {
		t.Errorf("5xx function error: %+v", res)
	}
	res = f.invoke(f.request(`export const notDefault = 1;`))
	if res.Status != StatusError || !strings.Contains(res.Message, "default export is not a function") {
		t.Errorf("no default export: %+v", res)
	}
}

// TestInvokeWebhook proves the runner shapes ctx and the result
// differently for webhook mode (roadmap F13): ctx.request carries the
// raw body and headers, and the handler's return value becomes the
// Result's Webhook field, not Output.
func TestInvokeWebhook(t *testing.T) {
	f := newFixture(t)

	req := f.request(`export default (ctx) => ({
		status: 201,
		body: JSON.stringify({ echoed: ctx.request.body, sig: ctx.request.headers["stripe-signature"] }),
		headers: { "Content-Type": "application/json" },
	});`)
	req.Envelope.Input = nil
	req.Envelope.Mode = "webhook"
	req.Envelope.Webhook = &WebhookRequest{Body: `{"type":"charge.succeeded"}`, Headers: http.Header{"Stripe-Signature": {"t=1,v1=abc"}}}

	res := f.invoke(req)
	if res.Status != StatusOK || res.Webhook == nil {
		t.Fatalf("webhook result: %+v", res)
	}
	if res.Webhook.Status != 201 || res.Webhook.Headers["Content-Type"] != "application/json" {
		t.Errorf("webhook response: %+v", res.Webhook)
	}
	want := `{"echoed":"{\"type\":\"charge.succeeded\"}","sig":"t=1,v1=abc"}`
	if res.Webhook.Body != want {
		t.Errorf("webhook body = %s, want %s", res.Webhook.Body, want)
	}

	// A handler that doesn't return a proper {status, body} still
	// answers something usable: defaults fill in, they don't crash.
	req2 := f.request(`export default () => ({});`)
	req2.Envelope.Mode = "webhook"
	req2.Envelope.Webhook = &WebhookRequest{Body: "x"}
	res2 := f.invoke(req2)
	if res2.Status != StatusOK || res2.Webhook == nil || res2.Webhook.Status != 200 || res2.Webhook.Body != "" {
		t.Errorf("default webhook response: %+v", res2.Webhook)
	}
}

func TestInvokeLimits(t *testing.T) {
	f := newFixture(t)

	req := f.request(`export default () => new Promise((r) => setTimeout(r, 10000));`)
	req.TimeoutMS = 500
	start := time.Now()
	if res := f.invoke(req); res.Status != StatusTimeout || time.Since(start) > 3*time.Second {
		t.Errorf("timeout: %+v after %s", res, time.Since(start))
	}

	// The caller going away stops the function too.
	req = f.request(`export default () => new Promise((r) => setTimeout(r, 10000));`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start = time.Now()
	if res := f.ex.Invoke(ctx, req); res.Status != StatusTimeout || time.Since(start) > 3*time.Second {
		t.Errorf("cancelled: %+v after %s", res, time.Since(start))
	}

	// Heap and buffers both count.
	for _, code := range []string{
		`export default () => { const a = []; for (let i = 0; i < 400; i++) a.push(new Array(131072).fill(i + 0.5)); return a.length; };`,
		`export default async () => { const a = []; for (let i = 0; i < 600; i++) { a.push(new Uint8Array(1 << 20).fill(1)); await null; } return a.length; };`,
	} {
		req = f.request(code)
		req.TimeoutMS = 20000
		if res := f.invoke(req); res.Status != StatusMemory {
			t.Errorf("memory: %+v", res)
		}
	}

	req = f.request(`export default () => { const end = Date.now() + 10000; let x = 0; while (Date.now() < end) x += Math.sqrt(x + 1); return x; };`)
	req.TimeoutMS = 1000 // the CPU limit follows the timeout; the wall clock stops it first
	if res := f.invoke(req); res.Status != StatusTimeout && res.Status != StatusCPU {
		t.Errorf("spin: %+v", res)
	}

	req = f.request(`export default () => "x".repeat(4096);`)
	req.MaxOutput = 1024
	if res := f.invoke(req); res.Status != StatusOutputTooBig || res.Output != nil {
		t.Errorf("output: %s %s", res.Status, res.Message)
	}
	req = f.request(`export default () => "x".repeat(1000);`)
	req.MaxOutput = 1024
	if res := f.invoke(req); res.Status != StatusOK {
		t.Errorf("output within the limit: %s %s", res.Status, res.Message)
	}
}

// Functions can't read the environment or files, run processes, use FFI,
// write the protocol, or reach hosts they don't list.
func TestInvokeSandbox(t *testing.T) {
	f := newFixture(t)
	t.Setenv("EXECUTOR_SECRET_ENV", "must-not-leak")
	code := `
async function attempt(f) { try { await f(); return "ALLOWED"; } catch (e) { return e.name; } }
export default async () => ({
  env: await attempt(() => Deno.env.get("HOME")),
  read: await attempt(() => Deno.readTextFile("/etc/passwd")),
  write: await attempt(() => Deno.writeTextFile("/tmp/x", "x")),
  run: await attempt(() => new Deno.Command("sh").output()),
  ffi: await attempt(() => Deno.dlopen("libc.so.6", {})),
  net: await attempt(() => fetch("https://example.com/", { signal: AbortSignal.timeout(2000) })),
  // --cached-only refuses any fetch not already cached at build time,
  // including a bundle's own dynamic import of a remote module (F4): it
  // never reaches the network, whatever --allow-net would say.
  dynamicImport: await attempt(() => import("https://example.com/mod.js")),
  stdout: await attempt(() => Object.defineProperty(Deno, "stdout", { value: 1 })),
});`
	res := f.invoke(f.request(code))
	var got map[string]string
	if res.Status != StatusOK || json.Unmarshal(res.Output, &got) != nil {
		t.Fatalf("sandbox probe: %+v", res)
	}
	for k, v := range got {
		if v == "ALLOWED" {
			t.Errorf("%s allowed", k)
		}
	}
}

func TestInvokeBundlesAndCallbacks(t *testing.T) {
	f := newFixture(t)

	// A bundle that doesn't match its hash is refused.
	req := f.request(`export default () => 1;`)
	f.mu.Lock()
	f.bundles[req.Bundle.SHA256] = []byte(`export default () => 2;`)
	f.mu.Unlock()
	if res := f.invoke(req); res.Status != StatusBundle || !strings.Contains(res.Message, "doesn't match its hash") {
		t.Errorf("tampered bundle: %+v", res)
	}
	req.Bundle.SHA256 = "../../etc/passwd"
	if res := f.invoke(req); res.Status != StatusBundle {
		t.Errorf("bad hash: %+v", res)
	}

	// ctx.db is the JS client, calling back with the caller's token; the
	// callback host is reachable, nothing else.
	req = f.request(`export default async (ctx) => {
		const page = await ctx.db("app").collection("notes").list();
		return { first: page.items[0].title, admin: ctx.admin !== undefined };
	};`)
	req.Envelope.Callback = &Callback{URL: f.backd.URL, Realm: "shop", Token: "bdf_caller"}
	res := f.invoke(req)
	if res.Status != StatusOK || string(res.Output) != `{"first":"hello","admin":false}` {
		t.Fatalf("callback: %+v", res)
	}
	f.mu.Lock()
	call := f.calls[len(f.calls)-1]
	f.mu.Unlock()
	if call.URL.Path != "/v1/shop/app/notes" || call.Header.Get("Authorization") != "Bearer bdf_caller" || call.Header.Get("X-Request-ID") != "req1" {
		t.Errorf("callback request: %s %v", call.URL.Path, call.Header)
	}

	req = f.request(`export default async (ctx) => { await ctx.admin.db("app").collection("notes").list(); return typeof ctx.db; };`)
	req.Envelope.Callback = &Callback{URL: f.backd.URL, Realm: "shop", AdminToken: "bdf_admin"}
	if res := f.invoke(req); res.Status != StatusOK || string(res.Output) != `"undefined"` {
		t.Errorf("admin callback: %+v", res)
	}
	f.mu.Lock()
	if got := f.calls[len(f.calls)-1].Header.Get("Authorization"); got != "Bearer bdf_admin" {
		t.Errorf("admin token: %q", got)
	}
	f.mu.Unlock()
}

func TestHandler(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.ex.Handler())
	defer srv.Close()
	body, _ := json.Marshal(f.request(`export default () => "hi";`))
	post := func(auth string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/invoke", strings.NewReader(string(body)))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := post(""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", res.StatusCode)
	}
	if res := post("Bearer wrong"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", res.StatusCode)
	}
	res := post("Bearer " + token)
	var r Result
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&r) != nil || string(r.Output) != `"hi"` {
		t.Errorf("invoke: %d %+v", res.StatusCode, r)
	}
	if _, err := New(Config{Dir: t.TempDir(), Token: "short"}); err == nil {
		t.Error("short token accepted")
	}
}
