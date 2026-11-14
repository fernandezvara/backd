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
