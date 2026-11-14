package typegen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

// copyTree copies the testdata realm into a temp dir, to write into.
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func loadVersions(t *testing.T, root string) *registry.Registry {
	t.Helper()
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestSchemaTypesGolden(t *testing.T) {
	reg := loadVersions(t, "testdata")
	c, _ := reg.Collection("versions", "main", "assets")
	got, warnings, ok, err := SchemaTypes(c)
	if err != nil || !ok || len(warnings) != 0 {
		t.Fatalf("SchemaTypes: ok=%v warnings=%v err=%v", ok, warnings, err)
	}
	golden := filepath.Join("testdata", "versions.schema.d.ts")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("schema.d.ts differs from %s (run go test -update):\n%s", golden, got)
	}

	// A collection with a plain schema.json has none.
	shop, _ := reg.Collection("shop", "main", "orders")
	if _, _, ok, _ := SchemaTypes(shop); ok {
		t.Error("an unversioned collection has a schema.d.ts")
	}
}

func TestSchemaFilesWrittenAndChecked(t *testing.T) {
	root := copyTree(t, "testdata")
	reg := loadVersions(t, root)
	states, err := CheckSchemaFiles(reg, "versions", "")
	if err != nil || len(states) != 1 || !states[0].Missing {
		t.Fatalf("before writing: %+v %v", states, err)
	}
	written, _, err := WriteSchemaFiles(reg, "versions", "")
	if err != nil || len(written) != 1 || !strings.HasSuffix(written[0], filepath.Join("main", "assets", "schema.d.ts")) {
		t.Fatalf("written: %v %v", written, err)
	}
	if states, _ := CheckSchemaFiles(reg, "versions", ""); states[0].Missing || states[0].Stale {
		t.Errorf("after writing: %+v", states[0])
	}
	if again, _, _ := WriteSchemaFiles(reg, "versions", ""); len(again) != 0 {
		t.Errorf("a second write changed %v", again)
	}
	// Editing a schema makes it stale; the other realm's collections are not looked at.
	path := filepath.Join(root, "versions", "main", "assets", "schema.v3.json")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"tags"`, `"labels"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	reg = loadVersions(t, root)
	if states, _ := CheckSchemaFiles(reg, "versions", ""); !states[0].Stale {
		t.Errorf("after an edit: %+v", states[0])
	}
	if states, _ := CheckSchemaFiles(reg, "shop", ""); len(states) != 0 {
		t.Errorf("a realm without versions: %+v", states)
	}
}

// The migrations type-check against the generated schema.d.ts, and a wrong one does not.
func TestSchemaTypesTypeCheckTheMigrations(t *testing.T) {
	deno, err := exec.LookPath("deno")
	if err != nil {
		t.Skip("deno isn't installed")
	}
	root := copyTree(t, "testdata")
	reg := loadVersions(t, root)
	if _, _, err := WriteSchemaFiles(reg, "versions", ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "versions", "main", "assets")
	check := func() (string, error) {
		cmd := exec.CommandContext(context.Background(), deno, "check", "--no-lock", "--no-config", "schema.v2.ts", "schema.v3.ts")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := check(); err != nil {
		t.Fatalf("the migrations should type-check:\n%s", out)
	}
	// A migration that leaves out a field the next version requires is refused.
	if err := os.WriteFile(filepath.Join(dir, "schema.v3.ts"), []byte("import type { V2, V3 } from \"./schema.d.ts\";\nexport default function (doc: V2): V3 {\n  return { title: doc.title };\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := check(); err == nil || !strings.Contains(out, "visibility") {
		t.Errorf("a migration missing a required field passed:\n%s", out)
	}
}

func TestRealmInfoListsSchemaVersions(t *testing.T) {
	reg := loadVersions(t, "testdata")
	out, err := Generate(reg, Options{Realm: "versions"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Info.SchemaVersions) != 1 || out.Info.SchemaVersions["main/assets"] != 3 || out.Info.Entries["schema_version main/assets"] != "3" {
		t.Errorf("info = %+v", out.Info)
	}
	if shop, _ := Generate(reg, Options{Realm: "shop"}); shop.Info.SchemaVersions != nil {
		t.Errorf("a realm without versions: %v", shop.Info.SchemaVersions)
	}
	// A new version shows in --diff.
	next := out.Info
	next.Entries = map[string]string{"schema_version main/assets": "4"}
	next.SchemaVersions = map[string]int{"main/assets": 4}
	if d := Diff(out.Info, next); len(d) == 0 || !strings.Contains(strings.Join(d, "\n"), "~ schema version of main/assets: 3 → 4") {
		t.Errorf("diff = %v", d)
	}
}

func TestGoldenVersionedTypes(t *testing.T) {
	reg := loadVersions(t, "testdata")
	out, err := Generate(reg, Options{Realm: "versions"})
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "versions.types.d.ts")
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
	for _, want := range []string{
		"export type MainAssetsV1 = { id: string; _meta: Meta & { schema_version: 1 } }",
		"export type MainAssetsV3 = { id: string; _meta: Meta & { schema_version: 3 } }",
		"export type MainAssets = MainAssetsV1 | MainAssetsV2 | MainAssetsV3;",
	} {
		if !strings.Contains(out.Types, want) {
			t.Errorf("types lack %q", want)
		}
	}
	// What a client writes is the current version only.
	if !strings.Contains(out.Types, "export type MainAssetsInput = { description?: string; owner_note?: MainAssetsOwnerNote") && !strings.Contains(out.Types, "export type MainAssetsInput = {") {
		t.Error("no MainAssetsInput")
	}
	if strings.Contains(out.Types, "MainAssetsInput = { body") {
		t.Error("the write input still has the dropped field")
	}
}

// A document is assignable to the union only with the fields of its own version, and the write
// input is the current version. (TypeScript can't narrow a union by a property of _meta, which is
// why the generated `upgrade` takes the whole union rather than a switch.)
func TestVersionedTypesAreChecked(t *testing.T) {
	deno, err := exec.LookPath("deno")
	if err != nil {
		t.Skip("deno isn't installed")
	}
	reg := loadVersions(t, "testdata")
	out, err := Generate(reg, Options{Realm: "versions"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("types.d.ts", out.Types)
	const good = `import type { MainAssets, MainAssetsV1, MainAssetsV3, MainAssetsInput } from "./types.d.ts";
const when = "2026-01-01T00:00:00.000Z" as MainAssetsV1["_meta"]["created_at"];
export const v1: MainAssets = { id: "a", _meta: { created_at: when, updated_at: when, schema_version: 1 }, title: "t", body: "old" };
export const v3: MainAssets = { id: "b", _meta: { created_at: when, updated_at: when, schema_version: 3 }, title: "t", visibility: "private" };
export const input: MainAssetsInput = { title: "t", visibility: "team" };
export function stamp(x: MainAssets): 1 | 2 | 3 | undefined { return x._meta.schema_version; }
export const asV3: MainAssetsV3 = v3 as MainAssetsV3;
`
	write("good.ts", good)
	check := func(file string) (string, error) {
		cmd := exec.Command(deno, "check", "--no-lock", "--no-config", file)
		cmd.Dir = dir
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	if b, err := check("good.ts"); err != nil {
		t.Fatalf("good.ts doesn't type-check:\n%s", b)
	}
	for name, src := range map[string]string{
		"v3-missing-visibility": `import type { MainAssetsV3 } from "./types.d.ts";
export const x: MainAssetsV3 = { id: "x", _meta: { created_at: "" as any, updated_at: "" as any, schema_version: 3 }, title: "t" };`,
		"input-with-dropped-field": `import type { MainAssetsInput } from "./types.d.ts";
export const x: MainAssetsInput = { title: "t", visibility: "team", body: "gone" };`,
		"unknown-version": `import type { MainAssets } from "./types.d.ts";
export const x: MainAssets = { id: "x", _meta: { created_at: "" as any, updated_at: "" as any, schema_version: 4 }, title: "t" };`,
	} {
		write(name+".ts", src)
		if b, err := check(name + ".ts"); err == nil {
			t.Errorf("%s should not type-check:\n%s", name, b)
		}
	}
}

func TestMigrationsModule(t *testing.T) {
	reg := loadVersions(t, "testdata")
	got, ok, err := Migrations(reg, "versions")
	if err != nil || !ok {
		t.Fatalf("Migrations: ok=%v err=%v", ok, err)
	}
	golden := filepath.Join("testdata", "versions.migrations.ts")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("migrations.ts differs from %s (run go test -update):\n%s", golden, got)
	}
	if _, ok, _ := Migrations(reg, "shop"); ok {
		t.Error("a realm without migrations got a migrations.ts")
	}
}

func dartVersionsOutput(t *testing.T) *Output {
	t.Helper()
	reg := loadVersions(t, "testdata")
	out, err := Generate(reg, Options{Realm: "versions", Lang: "dart"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenVersionedDart(t *testing.T) {
	out := dartVersionsOutput(t)
	golden := filepath.Join("testdata", "versions.types.dart")
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
	for _, want := range []string{"sealed class MainAssets {", "final class MainAssetsV1 extends MainAssets {", "final class MainAssetsV3 extends MainAssets {", "case 2:\n        return MainAssetsV2.fromJson(json);"} {
		if !strings.Contains(out.Types, want) {
			t.Errorf("types.dart lacks %q", want)
		}
	}
}

// The sealed class analyzes, picks the member by _meta.schema_version when decoding, and a
// switch over it must name every version.
func TestVersionedDartDecodesByTheStamp(t *testing.T) {
	dart, err := exec.LookPath("dart")
	if err != nil {
		t.Skip("dart SDK not installed")
	}
	out := dartVersionsOutput(t)
	pkg := filepath.Join("..", "..", "clients", "dart")
	generated := filepath.Join(pkg, "lib", "src", "generated_test_versions.dart")
	script := filepath.Join(pkg, "lib", "src", "generated_test_versions_main.dart")
	if err := os.WriteFile(generated, []byte(out.Types), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(generated)
	main := `import 'generated_test_versions.dart';

String describe(MainAssets a) => switch (a) {
      MainAssetsV1() => 'v1 body=${a.body}',
      MainAssetsV2() => 'v2 description=${a.description}',
      MainAssetsV3() => 'v3 visibility=${a.visibility}',
    };

void main() {
  final meta = {'created_at': '2026-01-01T00:00:00.000Z', 'updated_at': '2026-01-01T00:00:00.000Z'};
  print(describe(MainAssets.fromJson({'id': 'a', '_meta': {...meta}, 'title': 't', 'body': 'old'})));
  print(describe(MainAssets.fromJson({'id': 'b', '_meta': {...meta, 'schema_version': 2}, 'title': 't', 'description': 'd'})));
  print(describe(MainAssets.fromJson({'id': 'c', '_meta': {...meta, 'schema_version': 3}, 'title': 't', 'visibility': 'private'})));
  try {
    MainAssets.fromJson({'id': 'd', '_meta': {...meta, 'schema_version': 9}, 'title': 't'});
  } on FormatException catch (e) {
    print('unknown: ${e.message}');
  }
}
`
	if err := os.WriteFile(script, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(script)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(dart, args...)
		cmd.Dir = pkg
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	if b, err := run("analyze", "--fatal-warnings", generated, script); err != nil {
		t.Fatalf("dart analyze:\n%s", b)
	}
	b, err := run("run", script)
	if err != nil {
		t.Fatalf("dart run:\n%s", b)
	}
	for _, want := range []string{"v1 body=old", "v2 description=d", "v3 visibility=MainAssetsV3Visibility.private", "unknown: main/assets: unknown schema version 9"} {
		if !strings.Contains(b, want) {
			t.Errorf("output lacks %q:\n%s", want, b)
		}
	}
	// A switch that leaves a version out does not compile.
	bad := strings.Replace(main, "      MainAssetsV3() => 'v3 visibility=${a.visibility}',\n", "", 1)
	if err := os.WriteFile(script, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := run("analyze", "--fatal-warnings", script); err == nil {
		t.Errorf("a switch missing a version analyzed clean:\n%s", b)
	}
}

func goVersionsOutput(t *testing.T) *Output {
	t.Helper()
	reg := loadVersions(t, "testdata")
	out, err := Generate(reg, Options{Realm: "versions", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenVersionedGo(t *testing.T) {
	out := goVersionsOutput(t)
	golden := filepath.Join("testdata", "versions.types.go.txt")
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
}

// The generated Go compiles, a document decodes into the member its stamp names (absent: 1),
// and one of a version no longer kept is an error.
func TestVersionedGoDecodesByTheStamp(t *testing.T) {
	out := goVersionsOutput(t)
	dir, err := os.MkdirTemp(".", "gocheck")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(out.Types), 0o644); err != nil {
		t.Fatal(err)
	}
	use := `package backdtypes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	meta := ` + "`" + `"created_at":"2026-01-01T00:00:00.000Z","updated_at":"2026-01-01T00:00:00.000Z"` + "`" + `
	var a, b, c MainAssets
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"a","_meta":{` + "`" + `+meta+` + "`" + `},"title":"t","body":"old"}` + "`" + `), &a); err != nil || a.V1 == nil || a.Version() != 1 || a.V1.Body == nil || *a.V1.Body != "old" || a.V1.ID != "a" {
		t.Fatalf("v1: %+v %v", a, err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"b","_meta":{` + "`" + `+meta+` + "`" + `,"schema_version":2},"title":"t","description":"d"}` + "`" + `), &b); err != nil || b.V2 == nil || b.Version() != 2 || b.V2.Description == nil || *b.V2.Description != "d" || b.V2.Meta.SchemaVersion != 2 {
		t.Fatalf("v2: %+v %v", b, err)
	}
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"c","_meta":{` + "`" + `+meta+` + "`" + `,"schema_version":3},"title":"t","visibility":"private"}` + "`" + `), &c); err != nil || c.V3 == nil || c.Version() != 3 || c.V3.Visibility != MainAssetsV3VisibilityPrivate {
		t.Fatalf("v3: %+v %v", c, err)
	}
	raw, err := json.Marshal(c)
	if err != nil || !strings.Contains(string(raw), ` + "`" + `"visibility":"private"` + "`" + `) {
		t.Fatalf("marshal: %s %v", raw, err)
	}
	var bad MainAssets
	if err := json.Unmarshal([]byte(` + "`" + `{"id":"d","_meta":{` + "`" + `+meta+` + "`" + `,"schema_version":9},"title":"t"}` + "`" + `), &bad); err == nil || !strings.Contains(err.Error(), "unknown schema version 9") {
		t.Fatalf("unknown version: %v", err)
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "use_test.go"), []byte(use), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"vet", "./" + dir}, {"test", "-count=1", "./" + dir}} {
		if res, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s\n%s", strings.Join(args, " "), err, res, out.Types)
		}
	}
}

func pythonVersionsOutput(t *testing.T) *Output {
	t.Helper()
	reg := loadVersions(t, "testdata")
	out, err := Generate(reg, Options{Realm: "versions", Lang: "python"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenVersionedPython(t *testing.T) {
	out := pythonVersionsOutput(t)
	golden := filepath.Join("testdata", "versions.types.py.txt")
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
	for _, want := range []string{"MainAssets = Union[MainAssetsV1, MainAssetsV2, MainAssetsV3]", "def main_assets_version(doc: MainAssets) -> int:", "def is_main_assets_v3(doc: MainAssets) -> TypeGuard[MainAssetsV3]:"} {
		if !strings.Contains(out.Types, want) {
			t.Errorf("types.py lacks %q", want)
		}
	}
	compilesPython(t, out.Types)
	mypyChecks(t, out.Types)
}

// The guards pick the member by the stamp at run time, and the type checker follows them.
func TestVersionedPythonGuards(t *testing.T) {
	out := pythonVersionsOutput(t)
	py := python(t)
	dir := t.TempDir()
	clientSrc, err := filepath.Abs(filepath.Join("..", "..", "clients", "python", "src"))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("generated_types.py", out.Types)
	// A stand-in for the client at run time (the real one needs httpx); mypy below uses the real one.
	stub := filepath.Join(dir, "stub", "backd")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stub, "__init__.py"), []byte("from typing import Any, TypedDict\n\n\nclass Doc(TypedDict, total=False):\n    id: str\n    _meta: Any\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	use := write("use.py", `from __future__ import annotations

from generated_types import MainAssets, is_main_assets_v1, is_main_assets_v3, main_assets_version


def describe(doc: MainAssets) -> str:
    if is_main_assets_v1(doc):
        return "v1 body=" + str(doc.get("body"))
    if is_main_assets_v3(doc):
        return "v3 visibility=" + doc["visibility"]
    return "v2"


def make(version: int | None) -> MainAssets:
    meta: dict[str, object] = {"created_at": "2026-01-01T00:00:00.000Z", "updated_at": "2026-01-01T00:00:00.000Z"}
    if version is not None:
        meta["schema_version"] = version
    doc: dict[str, object] = {"id": "x", "_meta": meta, "title": "t", "visibility": "private", "body": "old"}
    return doc  # type: ignore[return-value]


if __name__ == "__main__":
    print(describe(make(None)), describe(make(2)), describe(make(3)), main_assets_version(make(None)))
`)
	cmd := exec.Command(py, use)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(dir, "stub"))
	res, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running use.py: %v\n%s", err, res)
	}
	if got := strings.TrimSpace(string(res)); got != "v1 body=old v2 v3 visibility=private 1" {
		t.Errorf("output = %q", got)
	}
	if mypy, err := exec.Command(py, "-m", "mypy", "--version").CombinedOutput(); err != nil {
		t.Skipf("mypy not installed: %s", mypy)
	}
	typecheck := exec.Command(py, "-m", "mypy", "--strict", "--python-version", "3.10", "--cache-dir", filepath.Join(dir, ".mypy"), use)
	typecheck.Dir = dir
	typecheck.Env = append(os.Environ(), "MYPYPATH="+clientSrc)
	if res, err := typecheck.CombinedOutput(); err != nil {
		t.Errorf("mypy on use.py: %v\n%s", err, res)
	}
	// A guard narrows to the version's own fields: `body` is not a field of version 3.
	bad := write("bad.py", `from generated_types import MainAssets, is_main_assets_v3


def f(doc: MainAssets) -> str:
    if is_main_assets_v3(doc):
        return str(doc["body"])
    return ""
`)
	typecheck = exec.Command(py, "-m", "mypy", "--strict", "--python-version", "3.10", "--cache-dir", filepath.Join(dir, ".mypy"), bad)
	typecheck.Dir = dir
	typecheck.Env = append(os.Environ(), "MYPYPATH="+clientSrc)
	if res, err := typecheck.CombinedOutput(); err == nil || !strings.Contains(string(res), "body") {
		t.Errorf("reading a dropped field after the v3 guard type-checked:\n%s", res)
	}
}
