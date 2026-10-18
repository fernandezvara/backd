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

func TestGoldenShopDart(t *testing.T) {
	reg, err := registry.Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(reg, Options{Realm: "shop", Lang: "dart"})
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "shop.types.dart")
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
	// The fingerprint is shared with the TypeScript output: it is what both
	// clients send in X-Backd-Types for the stale check.
	ts, err := Generate(reg, Options{Realm: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Info.Fingerprint != ts.Info.Fingerprint {
		t.Error("dart and ts fingerprints differ: realm.json isn't shared")
	}
	if !strings.Contains(out.Types, "final class MainOrders {") ||
		!strings.Contains(out.Types, "extension ShopRealmAccess on BackdClient<ShopRealm>") {
		t.Error("types.dart is missing the document class or the typed accessor")
	}
}

// TestDartAnalyzes runs `dart analyze` on the generated file when a Dart SDK
// is installed (the Dart client package is, at clients/dart).
func TestDartAnalyzes(t *testing.T) {
	dart, err := exec.LookPath("dart")
	if err != nil {
		t.Skip("dart SDK not installed")
	}
	reg, err := registry.Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(reg, Options{Realm: "shop", Lang: "dart"})
	if err != nil {
		t.Fatal(err)
	}
	// The file needs the backd package to analyze against: link it into the
	// client's workspace so package:backd resolves.
	pkg := filepath.Join("..", "..", "clients", "dart")
	generated := filepath.Join(pkg, "lib", "src", "generated_test_shop.dart")
	if err := os.WriteFile(generated, []byte(out.Types), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(generated)
	cmd := exec.Command(dart, "analyze", "--fatal-warnings", generated)
	cmd.Dir = pkg
	if res, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("dart analyze %s:\n%s", err, res)
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

// tscPath returns the TypeScript compiler of the JavaScript client, or skips the test when
// it (or node, which it runs on) isn't installed.
func tscPath(t *testing.T) string {
	t.Helper()
	tsc, err := filepath.Abs("../../clients/js/node_modules/.bin/tsc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tsc); err != nil {
		t.Skip("tsc isn't installed (npm ci in clients/js)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node isn't installed")
	}
	return tsc
}

// TestExamplesTypeCheck generates the types of every example realm and checks them with the
// TypeScript compiler of the JavaScript client, when it is installed.
func TestExamplesTypeCheck(t *testing.T) {
	tsc := tscPath(t)
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

// TestFunctionTypes type-checks fixtures against the generated backd.d.ts: the good one must
// pass, and each bad one must fail.
func TestFunctionTypes(t *testing.T) {
	tsc := tscPath(t)
	reg, err := registry.Load("testdata")
	if err != nil {
		t.Fatal(err)
	}
	out, err := GenerateFunctions(reg, "shop", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backd.d.ts"), []byte(out.Types), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(name string) (string, error) {
		src, err := os.ReadFile(filepath.Join("fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, src, 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := exec.Command(tsc, "--ignoreConfig", "--noEmit", "--strict", "--target", "es2022", "--moduleResolution", "bundler", "--module", "esnext", filepath.Join(dir, "backd.d.ts"), f).CombinedOutput()
		return string(b), err
	}
	if b, err := check("pass.ts"); err != nil {
		t.Errorf("pass.ts doesn't type-check:\n%s", b)
	}
	entries, err := os.ReadDir(filepath.Join("fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "fail_") {
			continue
		}
		if b, err := check(e.Name()); err == nil {
			t.Errorf("%s type-checks, it must not", e.Name())
		} else if !strings.Contains(b, e.Name()) {
			t.Errorf("%s fails somewhere else:\n%s", e.Name(), b)
		}
	}
}

// A realm with organizations names their roles, and a function with admin access gets
// ctx.admin.orgs; a realm without them has neither.
func TestOrganizationTypes(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("acme/realm.yaml", "organizations:\n  roles:\n    owner: {manage: org}\n    editor: {}\n  team_roles:\n    manager: {}\n    delegate: {}\n")
	write("acme/main/projects/schema.json", `{"type": "object", "properties": {"title": {"type": "string"}}}`)
	write("acme/main/projects/collection.yaml", "organization: required\non_org_delete:\n  action: delete\nrules:\n  read: inOrg(document._meta.org)\n")
	write("acme/main/tickets/schema.json", `{"type": "object", "properties": {"title": {"type": "string"}}}`)
	write("acme/main/tickets/collection.yaml", "organization: required\non_org_delete:\n  action: delete\nteam: required\non_team_delete:\n  action: keep\nrules:\n  read: inTeam(document._meta.team)\n")
	write("acme/main/_functions/provision/function.yaml", "admin: true\ninvoke: \"true\"\n")
	write("acme/main/_functions/provision/index.ts", "export default () => null\n")
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(reg, Options{Realm: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Types, `org_roles: "editor" | "owner";`) {
		t.Errorf("no org_roles:\n%s", out.Types)
	}
	if !strings.Contains(out.Types, `team_roles: "delegate" | "manager";`) {
		t.Errorf("no team_roles:\n%s", out.Types)
	}
	// _meta.team is queryable only where the collection has teams.
	if n := strings.Count(out.Types, `"_meta.team"?:`); n != 1 {
		t.Errorf("_meta.team in %d where types, want 1 (tickets)", n)
	}
	if !strings.Contains(out.Types, `"_meta.org"`) {
		t.Errorf("_meta.org is not queryable")
	}
	fns, err := GenerateFunctions(reg, "acme", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fns.Types, "provision: { webhook: false; admin: true; orgs: true;") {
		t.Errorf("flags:\n%s", fns.Types)
	}
	// Without organizations there are neither.
	write("acme/realm.yaml", "signup: open\n")
	write("acme/main/projects/collection.yaml", "rules:\n  read: user != nil\n")
	write("acme/main/tickets/collection.yaml", "rules:\n  read: user != nil\n")
	reg, err = registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out, _ = Generate(reg, Options{Realm: "acme"})
	if strings.Contains(out.Types, "org_roles") || strings.Contains(out.Types, "team_roles") {
		t.Error("org_roles or team_roles in a realm without organizations")
	}
}

// A collection with triggers gets the input type of its trigger functions; one without has
// `trigger: never`. The type-checks (tsc, when it is installed) cover that it compiles.
func TestTriggerInputTypes(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("acme/realm.yaml", "signup: open\n")
	write("acme/main/orders/schema.json", `{"type": "object", "properties": {"status": {"type": "string"}, "total": {"type": "number"}}}`)
	write("acme/main/orders/collection.yaml", "triggers:\n  on_paid:\n    events: [created, updated]\n    when: \"document.status == 'paid'\"\n    function: main/on-paid\n")
	write("acme/main/notes/schema.json", `{"type": "object", "properties": {"title": {"type": "string"}}}`)
	write("acme/main/_functions/on-paid/function.yaml", "internal: true\nmode: async\n")
	write("acme/main/_functions/on-paid/index.ts", "export default () => null\n")
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(reg, Options{Realm: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export type MainOrdersTriggerInput = {",
		`collection: "orders";`,
		`event: "created" | "updated" | "removed";`,
		"before?: Partial<MainOrders> | null;",
		"after?: Partial<MainOrders> | null;",
		"orders: { doc: MainOrders;",
		"trigger: MainOrdersTriggerInput }",
		"notes: { doc: MainNotes;",
		"trigger: never }",
	} {
		if !strings.Contains(out.Types, want) {
			t.Errorf("missing %q in:\n%s", want, out.Types)
		}
	}
	if strings.Contains(out.Types, "MainNotesTriggerInput") {
		t.Error("a collection without triggers got an input type")
	}
	if tsc, err := exec.LookPath("tsc"); err == nil {
		f := filepath.Join(t.TempDir(), "acme.d.ts")
		if err := os.WriteFile(f, []byte(out.Types), 0o644); err != nil {
			t.Fatal(err)
		}
		if b, err := exec.Command(tsc, "--ignoreConfig", "--noEmit", "--strict", f).CombinedOutput(); err != nil {
			t.Errorf("tsc: %v\n%s", err, b)
		}
	}
}

// A function a trigger runs has ctx.admin in its types, without declaring admin: true.
func TestTriggerFunctionsAreTypedWithAdminAccess(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("acme/realm.yaml", "signup: open\n")
	write("acme/main/orders/schema.json", `{"type": "object", "properties": {"status": {"type": "string"}}}`)
	write("acme/main/orders/collection.yaml", "triggers:\n  on_paid:\n    events: [created]\n    function: main/on-paid\n")
	write("acme/main/_functions/on-paid/function.yaml", "internal: true\nmode: async\n")
	write("acme/main/_functions/on-paid/index.ts", "export default () => null\n")
	write("acme/main/_functions/other/function.yaml", "internal: true\nmode: async\n")
	write("acme/main/_functions/other/index.ts", "export default () => null\n")
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	fns, err := GenerateFunctions(reg, "acme", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fns.Types, "on_paid: { webhook: false; admin: true;") && !strings.Contains(fns.Types, `"on-paid": { webhook: false; admin: true;`) {
		t.Errorf("the trigger's function has no admin access in its types:\n%s", fns.Types)
	}
	if strings.Contains(fns.Types, "other: { webhook: false; admin: true;") {
		t.Error("a function no trigger runs got admin access")
	}
}
