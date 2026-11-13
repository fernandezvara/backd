package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/devreport"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

func TestDevPreflight(t *testing.T) {
	withFns := cmdtest.Write(t, map[string]string{"app/main/_functions/hello/function.yaml": "mode: sync\n"})
	base := settings.Settings{Dev: true, HTTPAddr: "127.0.0.1:8080", ConfigDir: withFns, Deno: "/nonexistent/deno"}
	if _, err := devDeno(base); err == nil || !strings.Contains(err.Error(), "backd-executor") {
		t.Errorf("no Deno with a function to build: %v", err)
	}
	if err := devPreflight(base, "worker", Options{}); err == nil || !strings.Contains(err.Error(), "BACKD_EXECUTOR_URL") {
		t.Errorf("worker without an executor: %v", err)
	}
	noFns := base
	noFns.ConfigDir = t.TempDir()
	if err := devPreflight(noFns, "serve", Options{}); err != nil {
		t.Errorf("no functions, no Deno needed: %v", err)
	}
	if err := devPreflight(noFns, "serve", Options{WithWorker: true}); err == nil {
		t.Error("--with-worker without an executor was accepted")
	}
	off := noFns
	off.HTTPAddr = "0.0.0.0:8080"
	if err := devPreflight(off, "serve", Options{}); err == nil || !strings.Contains(err.Error(), "localhost") {
		t.Errorf("an address off the machine: %v", err)
	}
}

func TestWaitForConfigLoadsOnTheFirstFixingChange(t *testing.T) {
	defer func(d time.Duration) { devPoll = d }(devPoll)
	devPoll = 10 * time.Millisecond
	dir := cmdtest.Write(t, map[string]string{"app/realm.yaml": "x\n"})
	var buf cmdtest.LockedBuffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	attempts := 0
	load := func() (*registry.Registry, string, error) {
		attempts++
		data, _ := os.ReadFile(filepath.Join(dir, "app/realm.yaml"))
		if string(data) != "fixed\n" {
			return nil, "", errors.New("still broken")
		}
		return &registry.Registry{}, "fp", nil
	}
	done := make(chan string)
	go func() {
		_, fp, err := waitForConfig(context.Background(), dir, log, boardOf(dir, errors.New("broken")), load, false)
		if err != nil {
			fp = err.Error()
		}
		done <- fp
	}()
	time.Sleep(50 * time.Millisecond)
	if attempts != 0 {
		t.Errorf("tried %d times without a change", attempts)
	}
	edit(t, filepath.Join(dir, "app/realm.yaml"), "still bad\n")
	time.Sleep(50 * time.Millisecond)
	edit(t, filepath.Join(dir, "app/realm.yaml"), "fixed\n")
	select {
	case fp := <-done:
		if fp != "fp" {
			t.Errorf("waitForConfig = %q", fp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fixing change was not loaded")
	}
	// One record at the start, and one again for a change that still fails (the poll may
	// also catch the file half written: that is a change too).
	if !strings.Contains(buf.String(), `"error":"broken"`) || !strings.Contains(buf.String(), `"error":"still broken"`) {
		t.Errorf("records:\n%s", buf.String())
	}
}

// Production is unchanged: an invalid config, or a stale bundle, exits at startup.
func TestRunOutsideDevModeExitsOnAConfigError(t *testing.T) {
	broken := cmdtest.Write(t, map[string]string{"app/realm.yaml": "auth:\n  strategies:\n    password: [password]\nbogus: 1\n"})
	stale := cmdtest.Write(t, map[string]string{
		"app/realm.yaml":                          "auth: disabled\n",
		"app/main/notes/schema.json":              "{}",
		"app/main/_functions/hello/function.yaml": "mode: sync\n",
		"app/main/_functions/hello/index.ts":      "export default () => 1;\n",
	})
	for name, dir := range map[string]string{"invalid": broken, "stale bundle": stale} {
		env := map[string]string{"CONFIG_DIR": dir, "MONGO_URI": "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=100"}
		err := Run(context.Background(), "serve", func(k string) string { return env[k] }, io.Discard, Options{})
		if err == nil || !strings.Contains(err.Error(), "invalid config") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// End to end, on MongoDB with Deno: a dev-mode serve with a function that doesn't build
// starts anyway, answers /healthz but not /readyz, and comes up on the save that fixes it,
// building the bundle itself.
func TestDevServeWaitsForAConfigThatLoads(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("deno not found")
	}
	defer func(d time.Duration) { devPoll = d }(devPoll)
	devPoll = 50 * time.Millisecond
	realm := "t" + xid.New().String()
	root := cmdtest.Write(t, map[string]string{
		realm + "/realm.yaml":                          "auth: disabled\n",
		realm + "/main/notes/schema.json":              "{}",
		realm + "/main/_functions/hello/function.yaml": "mode: sync\n",
		realm + "/main/_functions/hello/index.ts":      "export default () => {\n  const x y = 1;\n};\n",
	})
	t.Cleanup(func() {
		if client, err := mongodb.Connect(context.Background(), uri); err == nil {
			_ = client.Database(realm + "__main").Drop(context.Background())
			_ = client.Disconnect(context.Background())
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "HTTP_ADDR": addr, "BACKD_DEV": "true", "LOG_LEVEL": "error"}
	var logs cmdtest.LockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "serve", func(k string) string { return env[k] }, &logs, Options{}) }()

	get := func(path string) int {
		res, err := http.Get("http://" + addr + path)
		if err != nil {
			return 0
		}
		res.Body.Close()
		return res.StatusCode
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			select {
			case err := <-done:
				t.Fatalf("serve ended while waiting for %s: %v\n%s", what, err, logs.String())
			default:
			}
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s:\n%s", what, logs.String())
	}
	waitFor("/healthz", func() bool { return get("/healthz") == http.StatusOK })
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz before the config loads = %d", code)
	}
	waitFor("the build error in the log", func() bool { return strings.Contains(logs.String(), `"kind":"build"`) })
	if !strings.Contains(logs.String(), `"output":`) {
		t.Errorf("no Deno output in the report:\n%s", logs.String())
	}
	read := func(path string) (int, string) {
		res, err := http.Get("http://" + addr + path)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := read("/v1/" + realm + "/main/notes"); code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"config_not_loaded"`) || !strings.Contains(body, `"code":"build"`) {
		t.Errorf("a data route before the config loads = %d %s", code, body)
	}
	if code, body := read("/"); code != http.StatusServiceUnavailable || !strings.Contains(body, "deno bundle failed") || !strings.Contains(body, "_functions/hello") {
		t.Errorf("the root page before the config loads = %d:\n%s", code, body)
	}
	edit(t, filepath.Join(root, realm, "main/_functions/hello/index.ts"), "export default () => 1;\n")
	waitFor("/readyz", func() bool { return get("/readyz") == http.StatusOK })
	if _, err := registry.ReadManifest(filepath.Join(root, realm, "main/_functions")); err != nil {
		t.Errorf("the bundle was not built: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("serve didn't stop")
	}
}

// Without Deno, dev mode still starts on bundles built elsewhere (a stack with its own build
// step), and says a changed function can't be built again.
func TestDevDenoWithFreshBundles(t *testing.T) {
	dir := cmdtest.Write(t, map[string]string{
		"app/realm.yaml":                          "auth: disabled\n",
		"app/main/notes/schema.json":              "{}",
		"app/main/_functions/hello/function.yaml": "mode: sync\n",
		"app/main/_functions/hello/index.ts":      "export default () => 1;\n",
	})
	cmdtest.WriteFuncManifest(t, filepath.Join(dir, "app/main/_functions"), "hello")
	cfg := settings.Settings{Dev: true, ConfigDir: dir, Deno: "/nonexistent/deno"}
	if warning, err := devDeno(cfg); err != nil || !strings.Contains(warning, "can't be built again") {
		t.Errorf("fresh bundles without Deno: %q, %v", warning, err)
	}
	if warning, err := devDeno(settings.Settings{Dev: true, ConfigDir: t.TempDir(), Deno: "/nonexistent/deno"}); err != nil || warning != "" {
		t.Errorf("no functions: %q, %v", warning, err)
	}
}

func boardOf(dir string, err error) *problemBoard {
	b := &problemBoard{}
	b.set(devreport.Of(err, dir))
	return b
}
