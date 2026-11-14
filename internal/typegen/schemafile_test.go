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
