package functions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// deno returns the deno binary to test with, skipping the test when there
// is none (the dockerized test image has it).
func deno(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("DENO")
	if bin == "" {
		bin = "deno"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("deno not found; skipping the build tests (they run in the dockerized test image)")
	}
	// A deno of another version fails rather than skips: the test image
	// must follow registry.DenoVersion (and a bump of one needs the other).
	if v, err := DenoVersion(context.Background(), bin); err != nil || v != registry.DenoVersion {
		t.Fatalf("deno %s (%v), want %s: update docker/test/Dockerfile and registry.DenoVersion together", v, err, registry.DenoVersion)
	}
	return bin
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuild(t *testing.T) {
	bin := deno(t)
	ctx := context.Background()
	root := t.TempDir()
	fn := "shop/app/" + registry.FunctionsDir + "/"
	write(t, root, map[string]string{
		"shop/realm.yaml":          "",
		fn + "deno.json":           `{"imports": {}}`,
		fn + "lib/money.ts":        "export const cents = (n: number): number => Math.round(n * 100);\n",
		fn + "total/function.yaml": "",
		fn + "total/index.ts":      "import { cents } from \"../lib/money.ts\";\nexport default (ctx: { input: { amount: number } }) => ({ cents: cents(ctx.input.amount) });\n",
		fn + "hello/function.yaml": "",
		fn + "hello/index.js":      "export default () => ({ hello: 'world' });\n",
	})
	load := func() *registry.Registry {
		t.Helper()
		reg, err := registry.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	var logged []string
	opts := Options{Deno: bin, Log: func(f string, a ...any) { logged = append(logged, f) }}
	if err := Build(ctx, load(), opts); err != nil {
		t.Fatal(err)
	}
	reg := load()
	if err := reg.CheckBundles(); err != nil {
		t.Fatalf("fresh build refused: %v", err)
	}
	if len(logged) != 2 {
		t.Errorf("logged %d bundles", len(logged))
	}
	fns := reg.Realms["shop"].Databases["app"].Functions
	m, err := registry.ReadManifest(fns.Dir)
	if err != nil {
		t.Fatal(err)
	}
	total, err := os.ReadFile(fns.BundlePath(m, "total"))
	if err != nil {
		t.Fatal(err)
	}
	// One self-contained file: lib/ is inlined, TypeScript is gone.
	if !strings.Contains(string(total), "Math.round") || strings.Contains(string(total), "import ") || strings.Contains(string(total), ": number") {
		t.Errorf("bundle not self-contained JavaScript:\n%s", total)
	}

	// Rebuilding unchanged sources gives the same bundles (reproducible).
	before := m
	if err := Build(ctx, reg, opts); err != nil {
		t.Fatal(err)
	}
	if after, _ := registry.ReadManifest(fns.Dir); after.Functions["total"] != before.Functions["total"] || after.Source != before.Source {
		t.Errorf("rebuild changed the manifest: %+v → %+v", before, after)
	}

	// A change in lib/ makes the build stale; rebuilding replaces the old
	// bundle and removes it.
	write(t, root, map[string]string{fn + "lib/money.ts": "export const cents = (n: number): number => Math.floor(n * 100);\n"})
	if err := load().CheckBundles(); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale build accepted: %v", err)
	}
	if err := Build(ctx, load(), opts); err != nil {
		t.Fatal(err)
	}
	if err := load().CheckBundles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fns.BundlePath(before, "total")); !os.IsNotExist(err) {
		t.Errorf("old bundle not removed: %v", err)
	}

	// A function that doesn't bundle fails the build and keeps the last
	// good manifest.
	good, _ := registry.ReadManifest(fns.Dir)
	write(t, root, map[string]string{fn + "hello/index.js": "export default (\n"})
	err = Build(ctx, load(), opts)
	if err == nil || !strings.Contains(err.Error(), "shop/app: function hello: deno bundle failed") {
		t.Fatalf("broken function: %v", err)
	}
	if kept, _ := registry.ReadManifest(fns.Dir); kept.Source != good.Source {
		t.Error("a failed build replaced the manifest")
	}

	// --check type-checks TypeScript.
	write(t, root, map[string]string{
		fn + "hello/index.js": "export default () => 1;\n",
		fn + "total/index.ts": "export default (): number => \"not a number\";\n",
	})
	if err := Build(ctx, load(), opts); err != nil {
		t.Errorf("without --check a type error must still bundle: %v", err)
	}
	opts.Check = true
	if err := Build(ctx, load(), opts); err == nil || !strings.Contains(err.Error(), "function total") {
		t.Errorf("--check didn't catch the type error: %v", err)
	}
}

func TestBuildWrongDeno(t *testing.T) {
	root := t.TempDir()
	fn := "shop/app/" + registry.FunctionsDir + "/"
	write(t, root, map[string]string{
		"shop/realm.yaml":          "",
		fn + "hello/function.yaml": "",
		fn + "hello/index.js":      "export default () => 1;\n",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	// A fake deno reporting another version.
	fake := filepath.Join(t.TempDir(), "deno")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'deno 1.46.3 (stable, release, x86_64-unknown-linux-gnu)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = Build(context.Background(), reg, Options{Deno: fake})
	if err == nil || !strings.Contains(err.Error(), "bundles functions with Deno "+registry.DenoVersion+", but "+fake+" is 1.46.3") {
		t.Errorf("wrong version: %v", err)
	}
	err = Build(context.Background(), reg, Options{Deno: filepath.Join(t.TempDir(), "missing")})
	if err == nil || !strings.Contains(err.Error(), "install Deno "+registry.DenoVersion) {
		t.Errorf("missing deno: %v", err)
	}
	// Nothing to build: deno isn't needed.
	empty, _ := registry.Load(t.TempDir())
	if err := Build(context.Background(), empty, Options{Deno: "/nonexistent"}); err != nil {
		t.Errorf("no functions: %v", err)
	}
}

func TestWatch(t *testing.T) {
	bin := deno(t)
	root := t.TempDir()
	fn := "shop/app/" + registry.FunctionsDir + "/"
	write(t, root, map[string]string{
		"shop/realm.yaml":          "",
		fn + "hello/function.yaml": "",
		fn + "hello/index.js":      "export default () => ({ hello: 'world' });\n",
	})
	load := func() *registry.Registry {
		t.Helper()
		reg, err := registry.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	reg := load()
	if err := Build(context.Background(), reg, Options{Deno: bin}); err != nil {
		t.Fatal(err)
	}
	fns := reg.Realms["shop"].Databases["app"].Functions
	first, err := registry.ReadManifest(fns.Dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logged []string
	var mu sync.Mutex
	go Watch(ctx, load(), Options{Deno: bin, Log: func(f string, a ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(f, a...))
		mu.Unlock()
	}}, 20*time.Millisecond)

	// Watch shouldn't rebuild sources that haven't changed.
	time.Sleep(80 * time.Millisecond)
	if unchanged, _ := registry.ReadManifest(fns.Dir); unchanged.Functions["hello"] != first.Functions["hello"] {
		t.Fatal("watch rebuilt without a source change")
	}

	// A change is picked up and the manifest is updated in place.
	write(t, root, map[string]string{fn + "hello/index.js": "export default () => ({ hello: 'watched' });\n"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		m, _ := registry.ReadManifest(fns.Dir)
		if m.Functions["hello"] != first.Functions["hello"] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watch did not rebuild the changed source in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	rebuilt, err := os.ReadFile(fns.BundlePath(mustManifest(t, fns.Dir), "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rebuilt), "watched") {
		t.Errorf("rebuilt bundle doesn't reflect the change:\n%s", rebuilt)
	}

	// A broken source is logged and retried, not fatal to the loop.
	write(t, root, map[string]string{fn + "hello/index.js": "export default (\n"})
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range logged {
			if strings.Contains(l, "rebuilding shop/app failed") {
				return true
			}
		}
		return false
	}
	deadline = time.Now().Add(5 * time.Second)
	for !failed() {
		if time.Now().After(deadline) {
			t.Fatal("watch didn't log the broken source")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustManifest(t *testing.T, dir string) registry.Manifest {
	t.Helper()
	m, err := registry.ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWatchWrongDeno(t *testing.T) {
	root := t.TempDir()
	fn := "shop/app/" + registry.FunctionsDir + "/"
	write(t, root, map[string]string{
		"shop/realm.yaml":          "",
		fn + "hello/function.yaml": "",
		fn + "hello/index.js":      "export default () => 1;\n",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "deno")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'deno 1.46.3 (stable, release, x86_64-unknown-linux-gnu)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var logged []string
	done := make(chan struct{})
	go func() {
		Watch(context.Background(), reg, Options{Deno: fake, Log: func(f string, a ...any) {
			logged = append(logged, fmt.Sprintf(f, a...))
		}}, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch didn't return for a wrong deno version")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "1.46.3") {
		t.Errorf("logged = %v", logged)
	}
}
