package functions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

func mustManifest(t *testing.T, dir string) registry.Manifest {
	t.Helper()
	m, err := registry.ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A function written with defineFunction bundles with no setup in the project, whether its
// deno.json has other imports, none, or maps the module itself; the generated types don't
// make the build stale.
func TestBuildDefineFunction(t *testing.T) {
	bin := deno(t)
	ctx := context.Background()
	for name, denoJSON := range map[string]string{
		"imports":  `{"imports": {"x": "./lib/x.ts"}}`,
		"none":     "",
		"explicit": `{"imports": {"@backd/functions": "./.backd/functions.js"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			fn := "shop/app/" + registry.FunctionsDir + "/"
			files := map[string]string{
				"shop/realm.yaml":          "",
				fn + "lib/x.ts":            "export const x = 7;\n",
				fn + "typed/function.yaml": "",
				fn + "typed/index.ts":      "import { defineFunction } from \"@backd/functions\";\nimport { x } from \"../lib/x.ts\";\nexport default defineFunction(\"app/typed\", () => ({ x }));\n",
			}
			if denoJSON != "" {
				files[fn+"deno.json"] = denoJSON
			}
			write(t, root, files)
			reg, err := registry.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := WriteTypes(reg, "", "", false); err != nil {
				t.Fatal(err)
			}
			if err := Build(ctx, reg, Options{Deno: bin}); err != nil {
				t.Fatal(err)
			}
			reg, err = registry.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := reg.CheckBundles(); err != nil {
				t.Errorf("fresh build refused: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "shop/app", registry.FunctionsDir, registry.TypesFile)); err != nil {
				t.Errorf("backd.d.ts: %v", err)
			}
		})
	}
}
