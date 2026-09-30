package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
)

// concurrencyFixture is acme/app with three functions ("a", "b", "c"),
// each with its own per-function concurrency limit, in a realm with
// functions.max_concurrency set. Auth disabled: concurrency limits apply
// to every caller, not just authenticated ones.
func concurrencyFixture(t *testing.T, funcConcurrency, realmMax int, runner FunctionRunner) *fixture {
	t.Helper()
	root := t.TempDir()
	fn := "acme/app/" + registry.FunctionsDir
	realmYAML := "auth: disabled\n"
	if realmMax > 0 {
		realmYAML += "functions:\n  max_concurrency: " + strconv.Itoa(realmMax) + "\n"
	}
	files := map[string]string{"acme/realm.yaml": realmYAML}
	for _, name := range []string{"a", "b", "c"} {
		files[fn+"/"+name+"/function.yaml"] = "concurrency: " + strconv.Itoa(funcConcurrency) + "\n"
		files[fn+"/"+name+"/index.js"] = ""
	}
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, filepath.Join(root, fn))
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Functions = runner
		c.CallbackURL = "http://backd-internal:8081"
		c.ExecutorToken = executorToken
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// blockingRunner runs one call at a time per test's direction: each
// Invoke signals started, then waits for release.
type blockingRunner struct {
	started chan string // function name
	release chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan string, 10), release: make(chan struct{})}
}

func (b *blockingRunner) Invoke(_ context.Context, req executor.InvokeRequest) (executor.Result, error) {
	b.started <- req.Function
	<-b.release
	return executor.Result{Status: executor.StatusOK, Output: json.RawMessage("null")}, nil
}

func TestFunctionConcurrencyPerFunction(t *testing.T) {
	runner := newBlockingRunner()
	f := concurrencyFixture(t, 1, 0, runner) // per-function: 1, realm: unlimited

	go f.doH(t, "POST", "/v1/acme/app/_func/a", "{}", nil)
	<-runner.started // the first call holds function a's one slot

	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/a", "{}", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second call to a: %d %v", rec.Code, out)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}

	// A different function has its own, unaffected, slot.
	go f.doH(t, "POST", "/v1/acme/app/_func/b", "{}", nil)
	<-runner.started

	close(runner.release)
}

// TestFunctionConcurrencyWebhook503 proves a full concurrency limit
// answers 503 for a webhook function, not 429 (roadmap F13, completing
// F8's original design: webhook senders like Stripe retry on 5xx).
func TestFunctionConcurrencyWebhook503(t *testing.T) {
	runner := newBlockingRunner()
	root := t.TempDir()
	fn := "acme/app/" + registry.FunctionsDir
	files := map[string]string{
		"acme/realm.yaml":          "auth: disabled\n",
		fn + "/hook/function.yaml": "mode: webhook\nconcurrency: 1\n",
		fn + "/hook/index.js":      "",
	}
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, filepath.Join(root, fn))
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Functions = runner
		c.CallbackURL = "http://backd-internal:8081"
		c.ExecutorToken = executorToken
	})

	go f.doH(t, "POST", "/v1/acme/app/_func/hook", "{}", map[string]string{"Content-Type": "text/plain"})
	<-runner.started

	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/hook", "{}", map[string]string{"Content-Type": "text/plain"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("full webhook concurrency: %d %v", rec.Code, out)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	close(runner.release)
}

func TestFunctionConcurrencyPerRealm(t *testing.T) {
	runner := newBlockingRunner()
	f := concurrencyFixture(t, 5, 2, runner) // per-function: 5 (generous), realm: 2

	go f.doH(t, "POST", "/v1/acme/app/_func/a", "{}", nil)
	<-runner.started
	go f.doH(t, "POST", "/v1/acme/app/_func/b", "{}", nil)
	<-runner.started

	// The realm's 2 slots are both taken (by different functions, so
	// per-function limits alone wouldn't catch this).
	rec, out := f.doH(t, "POST", "/v1/acme/app/_func/c", "{}", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third call: %d %v", rec.Code, out)
	}

	close(runner.release)
}
