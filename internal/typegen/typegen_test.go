package typegen

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func generate(t *testing.T, dir, realm string) *Output {
	t.Helper()
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(reg, Options{Realm: realm})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenShop(t *testing.T) {
	out := generate(t, "testdata", "shop")
	golden := filepath.Join("testdata", "shop.types.d.ts")
	if *update {
		if err := os.WriteFile(golden, []byte(out.Types), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != out.Types {
		t.Errorf("generated types differ from %s (run go test -update):\n%s", golden, out.Types)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], `keyword "not"`) {
		t.Errorf("warnings = %v", out.Warnings)
	}
	if out.Info.Fingerprint == "" || out.Info.Entries["field main/orders status"] == "" {
		t.Errorf("info = %+v", out.Info)
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	a, b := generate(t, "testdata", "shop"), generate(t, "testdata", "shop")
	if a.Types != b.Types || a.Info.Fingerprint != b.Info.Fingerprint {
		t.Error("two runs differ")
	}
}

func TestUnknownRealmAndDatabase(t *testing.T) {
	reg, err := registry.Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(reg, Options{Realm: "nope"}); err == nil {
		t.Error("unknown realm accepted")
	}
	if _, err := Generate(reg, Options{Realm: "shop", Database: "nope"}); err == nil {
		t.Error("unknown database accepted")
	}
}

func TestDiff(t *testing.T) {
	old := Info{Entries: map[string]string{
		"collection main/orders":    "",
		"field main/orders total":   "number",
		"field main/orders note":    "string",
		"function main/place input": "{ a: string }",
		"collection main/gone":      "",
	}}
	now := Info{Entries: map[string]string{
		"collection main/orders":    "",
		"field main/orders total":   "number | null",
		"field main/orders done":    "boolean",
		"function main/place input": "{ a: string }",
		"collection main/new":       "",
	}}
	got := strings.Join(Diff(old, now), "\n")
	for _, want := range []string{
		"+ field done of main/orders: boolean",
		"- field note of main/orders",
		"~ field total of main/orders: number → number | null",
		"- collection main/gone",
		"+ collection main/new",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diff misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "function") {
		t.Errorf("unchanged function reported:\n%s", got)
	}
	if len(Diff(old, old)) != 0 {
		t.Error("identical generations differ")
	}
}

// TestExamplesTypeCheck generates the types of every example realm and checks them with the
// TypeScript compiler of the JavaScript client, when it is installed.
func TestExamplesTypeCheck(t *testing.T) {
	tsc, err := filepath.Abs("../../clients/js/node_modules/.bin/tsc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tsc); err != nil {
		t.Skip("tsc isn't installed (npm ci in clients/js)")
	}
	dir := t.TempDir()
	var files []string
	for _, ex := range []string{"shelf", "workshop", "shop", "blog", "expenses"} {
		root := filepath.Join("..", "..", "examples", "config", ex)
		reg, err := registry.Load(filepath.Dir(root))
		if err != nil {
			t.Skipf("examples don't load together: %v", err)
		}
		if _, ok := reg.Realms[ex]; !ok {
			continue
		}
		out, err := Generate(reg, Options{Realm: ex})
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, ex+".d.ts")
		if err := os.WriteFile(f, []byte(out.Types), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Skip("no example realm found")
	}
	args := append([]string{"--ignoreConfig", "--noEmit", "--strict"}, files...)
	if b, err := exec.Command(tsc, args...).CombinedOutput(); err != nil {
		t.Errorf("tsc: %v\n%s", err, b)
	}
}
