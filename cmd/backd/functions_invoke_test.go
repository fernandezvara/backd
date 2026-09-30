package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
)

// fakeFuncRunner stands in for the executor in `backd functions invoke` tests.
type fakeFuncRunner struct {
	mu      sync.Mutex
	last    executor.InvokeRequest
	handle  func(executor.InvokeRequest) (executor.Result, error)
	invoked bool
}

func (f *fakeFuncRunner) Invoke(_ context.Context, req executor.InvokeRequest) (executor.Result, error) {
	f.mu.Lock()
	f.last, f.invoked = req, true
	f.mu.Unlock()
	if f.handle != nil {
		return f.handle(req)
	}
	return executor.Result{Status: executor.StatusOK, Output: req.Envelope.Input}, nil
}

func (f *fakeFuncRunner) lastRequest() executor.InvokeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// writeFuncManifest writes a one-function manifest as `backd functions
// build` would, so the handler's index finds a built bundle.
func writeFuncManifest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, registry.BuildDir), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("export default () => null;\n")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	file := name + "-" + hash[:8] + ".js"
	if err := os.WriteFile(filepath.Join(dir, registry.BuildDir, file), data, 0o644); err != nil {
		t.Fatal(err)
	}
	m := registry.Manifest{Deno: registry.DenoVersion, Functions: map[string]registry.ManifestBundle{name: {Bundle: file, SHA256: hash}}}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, registry.BuildDir, registry.ManifestFile), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFunctionsInvoke(t *testing.T) {
	fnDir := "acme/app/" + registry.FunctionsDir
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":              "roles:\n  ops:\n    admin: true\n",
		fnDir + "/hello/function.yaml": "invoke: \"false\"\n", // API keys bypass this
		fnDir + "/hello/index.js":      "",
	})
	writeFuncManifest(t, filepath.Join(root, fnDir), "hello")
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	pw := "dev-p4ssw0rd!"
	if _, err := svc.Create(ctx, "admin@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "dev@example.com", &pw); err != nil {
		t.Fatal(err)
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}

	runner := &fakeFuncRunner{handle: func(req executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{
			Status:     executor.StatusOK,
			Output:     req.Envelope.Input,
			Logs:       []executor.LogLine{{Level: "log", Line: "hello from the function"}},
			DurationMS: 7,
		}, nil
	}}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		Functions:     runner,
		Dev:           true,
		CallbackURL:   "http://backd-internal:8081",
		ExecutorToken: "test-executor-token-0123456789ab",
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()

	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}

	input := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(input, []byte(`{"n": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A plain call: the API key bypasses the invoke rule ("false"); dev
	// mode's diagnostic headers surface as logs and timing, on stderr
	// (stdout stays the function's own output, safe to pipe on).
	code, out, errOut := c.run("", "functions", "invoke", "--function", "acme/app/hello", "--input", input, "--url", srv.URL)
	if code != 0 || !strings.Contains(out, `"n": 1`) {
		t.Fatalf("code %d, stdout %q", code, out)
	}
	if !strings.Contains(errOut, "hello from the function") || !strings.Contains(errOut, "executor: 7ms") {
		t.Errorf("missing dev diagnostics on stderr: %s", errOut)
	}

	// --as resolves the email to a user id and sets X-Backd-On-Behalf-Of;
	// the function sees that user, not the API key.
	c.expect(0, `"n": 1`, "", "functions", "invoke", "--function", "acme/app/hello", "--input", input, "--as", "dev@example.com", "--url", srv.URL)
	var user struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(runner.lastRequest().Envelope.User, &user); err != nil || user.Email != "dev@example.com" {
		t.Errorf("Envelope.User = %s", runner.lastRequest().Envelope.User)
	}

	// An unknown --as email is refused clearly, without calling the function.
	runner.mu.Lock()
	runner.invoked = false
	runner.mu.Unlock()
	c.expect(1, "user not found", "", "functions", "invoke", "--function", "acme/app/hello", "--as", "ghost@example.com", "--url", srv.URL)
	runner.mu.Lock()
	invoked := runner.invoked
	runner.mu.Unlock()
	if invoked {
		t.Error("the function was called despite the unresolved --as")
	}

	// Without --input the body is null.
	c.expect(0, "null", "", "functions", "invoke", "--function", "acme/app/hello", "--url", srv.URL)

	// A function error still surfaces logs and timing.
	runner.handle = func(req executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{
			Status:        executor.StatusFunctionError,
			FunctionError: &executor.FunctionError{Status: 409, Code: "conflict", Message: "nope"},
			Logs:          []executor.LogLine{{Level: "error", Line: "boom"}},
			DurationMS:    3,
		}, nil
	}
	code, _, errOut = c.run("", "functions", "invoke", "--function", "acme/app/hello", "--url", srv.URL)
	if code != 1 || !strings.Contains(errOut, "409 conflict") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(errOut, "boom") {
		t.Errorf("missing logs on a function error: %s", errOut)
	}
}
