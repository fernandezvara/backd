package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

const reloadSchema = `{"type":"object","properties":{"title":{"type":"string"}%s},"additionalProperties":false}`

func reloadFixture(t *testing.T) (*reloader, string, *[]reloadEvent, *int) {
	t.Helper()
	dir := writeConfig(t, map[string]string{
		"app/realm.yaml":                 "auth: disabled\n",
		"app/main/notes/schema.json":     strings.Replace(reloadSchema, "%s", "", 1),
		"app/main/notes/collection.yaml": "{}\n",
	})
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := reg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		cfg:         settings.Settings{ConfigDir: dir, Dev: true},
		log:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		reg:         reg,
		fingerprint: fp,
	}
	pub := &swapHandler{}
	pub.Store(http.NotFoundHandler())
	r := newReloader(a, pub, nil)
	applied := 0
	r.apply = func(context.Context, *app) error { applied++; return nil }
	r.build = func(context.Context, *registry.Registry) error { return nil }
	var events []reloadEvent
	r.onReload = append(r.onReload, func(e reloadEvent) { events = append(events, e) })
	return r, dir, &events, &applied
}

// edit rewrites a file and bumps its modification time, whatever the file system's resolution.
func edit(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Duration(len(content)+1) * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func TestReloadAppliesAValidChange(t *testing.T) {
	r, dir, events, applied := reloadFixture(t)
	before := r.app.fingerprint
	edit(t, filepath.Join(dir, "app/main/notes/schema.json"), strings.Replace(reloadSchema, "%s", `,"done":{"type":"boolean"}`, 1))
	r.Poll(context.Background())
	if r.app.fingerprint == before {
		t.Fatal("the running config was not replaced")
	}
	if *applied != 1 {
		t.Errorf("provisioned %d times, want 1", *applied)
	}
	if len(*events) != 1 || len((*events)[0].Realms) != 1 || (*events)[0].Realms[0] != "app" ||
		len((*events)[0].Files) != 1 || (*events)[0].Files[0] != "app/main/notes/schema.json" {
		t.Errorf("events = %+v", *events)
	}
	if _, ok := r.app.reg.Realms["app"].Databases["main"].Collections["notes"].RawSchema["properties"].(map[string]any)["done"]; !ok {
		t.Errorf("the new schema is not the one running")
	}
	if len(r.Errors()) != 0 {
		t.Errorf("errors = %v", r.Errors())
	}
}

func TestReloadKeepsThePreviousConfigOnAnInvalidChange(t *testing.T) {
	r, dir, events, applied := reloadFixture(t)
	before := r.app
	path := filepath.Join(dir, "app/main/notes/schema.json")
	edit(t, path, `{ not json`)
	r.Poll(context.Background())
	if r.app != before || len(*events) != 0 || *applied != 0 {
		t.Fatalf("an invalid config was applied (events %v, applied %d)", *events, *applied)
	}
	if errs := r.Errors(); len(errs) == 0 || !strings.Contains(strings.Join(errs, "\n"), "schema.json") {
		t.Errorf("errors = %v, want one naming schema.json", errs)
	}
	// Not retried until a file changes again.
	r.Poll(context.Background())
	if len(r.Errors()) == 0 {
		t.Error("the error was cleared without a change")
	}
	// Fixing the file reloads and clears the error.
	edit(t, path, strings.Replace(reloadSchema, "%s", `,"n":{"type":"integer"}`, 1))
	r.Poll(context.Background())
	if r.app == before || len(r.Errors()) != 0 || len(*events) != 1 {
		t.Errorf("fix not applied: errors %v, events %d", r.Errors(), len(*events))
	}
}

func TestReloadRapidChangesLandOnTheLastOne(t *testing.T) {
	r, dir, events, _ := reloadFixture(t)
	path := filepath.Join(dir, "app/main/notes/schema.json")
	for _, extra := range []string{`,"a":{"type":"string"}`, `,"b":{"type":"string"}`, `,"c":{"type":"string"}`} {
		edit(t, path, strings.Replace(reloadSchema, "%s", extra, 1))
	}
	r.Poll(context.Background())
	if len(*events) != 1 {
		t.Fatalf("%d reloads for changes made between two polls, want 1", len(*events))
	}
	if _, ok := r.app.reg.Realms["app"].Databases["main"].Collections["notes"].RawSchema["properties"].(map[string]any)["c"]; !ok {
		t.Error("not the last version")
	}
}

func TestReloadIgnoresFilesBackdDoesNotRead(t *testing.T) {
	r, dir, events, applied := reloadFixture(t)
	edit(t, filepath.Join(dir, "app/main/notes/README.txt"), "notes")
	r.Poll(context.Background())
	if len(*events) != 0 || *applied != 0 {
		t.Errorf("a stray file reloaded the config: %v", *events)
	}
}

func TestSwapHandlerServesTheLatest(t *testing.T) {
	s := &swapHandler{}
	s.Store(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("one")) }))
	s.Store(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("two")) }))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "two" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestReloadWritesFunctionTypes(t *testing.T) {
	r, dir, _, _ := reloadFixture(t)
	fn := filepath.Join(dir, "app/main/_functions/hello")
	if err := os.MkdirAll(fn, 0o755); err != nil {
		t.Fatal(err)
	}
	edit(t, filepath.Join(fn, "function.yaml"), "mode: sync\n")
	edit(t, filepath.Join(fn, "index.ts"), "export default () => 1;\n")
	writeFuncManifest(t, filepath.Join(dir, "app/main/_functions"), "hello")
	r.Poll(context.Background())
	if len(r.Errors()) != 0 {
		t.Fatalf("reload failed: %v", r.Errors())
	}
	types, err := os.ReadFile(filepath.Join(dir, "app/main/_functions/backd.d.ts"))
	if err != nil {
		t.Fatalf("backd.d.ts not written: %v", err)
	}
	if !strings.Contains(string(types), "defineFunction") || !strings.Contains(string(types), "hello") {
		t.Errorf("backd.d.ts:\n%s", types)
	}
	// Writing it is not a change: no second reload.
	before := r.app
	r.Poll(context.Background())
	if r.app != before {
		t.Error("the generated file caused another reload")
	}
}

// Outside dev mode there is no swap handler, and the listener must serve the app's own: a nil
// *swapHandler inside an interface is not nil and would panic on the first request.
func TestServedByFallsBackToTheHandler(t *testing.T) {
	own := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("own")) })
	var none *swapHandler
	rec := httptest.NewRecorder()
	servedBy(own, none).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "own" {
		t.Errorf("body = %q", rec.Body.String())
	}
	swap := &swapHandler{}
	swap.Store(http.NotFoundHandler())
	rec = httptest.NewRecorder()
	servedBy(own, swap).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("dev mode didn't serve the swap handler: %d", rec.Code)
	}
}

func followFixture(t *testing.T) (*app, string) {
	t.Helper()
	dir := writeConfig(t, map[string]string{
		"app/realm.yaml":                 "auth: disabled\n",
		"app/main/notes/schema.json":     strings.Replace(reloadSchema, "%s", "", 1),
		"app/main/notes/collection.yaml": "{}\n",
	})
	cfg := settings.Settings{ConfigDir: dir, Dev: true}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	reg, fp, err := loadConfig(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return &app{cfg: cfg, log: log, reg: reg, fingerprint: fp}, dir
}

// The worker follows CONFIG_DIR in dev mode: a valid change gives the app for the new config, a
// change that doesn't load is retried until it does, and files that don't change the config are
// not a reload.
func TestWorkerFollowsTheConfig(t *testing.T) {
	a, dir := followFixture(t)
	schema := filepath.Join(dir, "app/main/notes/schema.json")
	got := make(chan *app, 1)
	ctx := t.Context()
	go func() { got <- followConfig(ctx, a, 10*time.Millisecond) }()

	// A stray file: the same config, no reload.
	edit(t, filepath.Join(dir, "app/notes.txt"), "just a note")
	select {
	case <-got:
		t.Fatal("a file the config doesn't read caused a reload")
	case <-time.After(150 * time.Millisecond):
	}
	// An invalid change is not applied...
	edit(t, schema, `{ not json`)
	select {
	case <-got:
		t.Fatal("an invalid config was applied")
	case <-time.After(150 * time.Millisecond):
	}
	// ...and fixing the file, without another change in between, is picked up.
	edit(t, schema, strings.Replace(reloadSchema, "%s", `,"done":{"type":"boolean"}`, 1))
	select {
	case next := <-got:
		if next == nil || next.fingerprint == a.fingerprint {
			t.Fatalf("next = %+v", next)
		}
		if _, ok := next.reg.Realms["app"].Databases["main"].Collections["notes"].RawSchema["properties"].(map[string]any)["done"]; !ok {
			t.Error("the new schema is not the one returned")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the valid change was not applied")
	}
}

// A config that stays invalid until something else catches up (the API building a bundle) is
// retried on its own: the worker applies it as soon as it loads, with no further file change.
func TestWorkerRetriesAConfigThatDoesNotLoadYet(t *testing.T) {
	a, dir := followFixture(t)
	got := make(chan *app, 1)
	ctx := t.Context()
	go func() { got <- followConfig(ctx, a, 10*time.Millisecond) }()
	// A reference to a collection that does not exist yet.
	edit(t, filepath.Join(dir, "app/main/notes/collection.yaml"), "references:\n  later: {field: title, collection: later}\n")
	select {
	case <-got:
		t.Fatal("applied a config that does not load")
	case <-time.After(200 * time.Millisecond):
	}
	// The collection appears, without touching collection.yaml again.
	if err := os.MkdirAll(filepath.Join(dir, "app/main/later"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app/main/later/schema.json"), []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-got:
		if next == nil || next.fingerprint == a.fingerprint {
			t.Fatalf("next = %+v", next)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the config was not applied once it loaded")
	}
}

func TestWorkerFollowStopsWithItsContext(t *testing.T) {
	a, _ := followFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *app, 1)
	go func() { done <- followConfig(ctx, a, 10*time.Millisecond) }()
	cancel()
	select {
	case next := <-done:
		if next != nil {
			t.Fatal("an app after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop")
	}
}
