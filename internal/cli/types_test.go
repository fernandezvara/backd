package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func typesConfig(t *testing.T) string {
	return writeConfig(t, map[string]string{
		"app/realm.yaml":                 "auth: disabled\n",
		"app/main/notes/schema.json":     `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}}}`,
		"app/main/notes/collection.yaml": "{}\n",
	})
}

func TestTypesWritesChecksAndDiffs(t *testing.T) {
	dir := typesConfig(t)
	out := filepath.Join(t.TempDir(), "gen")
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}

	code, stdout, stderr := c.run("", "types", "--realm", "app", "--out", out)
	if code != 0 || !strings.Contains(stdout, "wrote") {
		t.Fatalf("types: %d %s %s", code, stdout, stderr)
	}
	for _, f := range []string{"types.d.ts", "realm.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	types, _ := os.ReadFile(filepath.Join(out, "types.d.ts"))
	if !strings.Contains(string(types), "export interface Realm") || !strings.Contains(string(types), "title?: string") {
		t.Errorf("types.d.ts:\n%s", types)
	}

	if code, stdout, _ := c.run("", "types", "--realm", "app", "--out", out, "--check"); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("--check on fresh files: %d %s", code, stdout)
	}

	// A schema change makes the files stale; --diff says what changed.
	if err := os.WriteFile(filepath.Join(dir, "app/main/notes/schema.json"),
		[]byte(`{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"},"done":{"type":"boolean"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = c.run("", "types", "--realm", "app", "--out", out, "--check")
	if code == 0 || !strings.Contains(stderr, "stale") || !strings.Contains(stdout, "+ field done of main/notes: boolean") {
		t.Errorf("--check on stale files: %d\n%s\n%s", code, stdout, stderr)
	}
	code, stdout, _ = c.run("", "types", "--realm", "app", "--out", out, "--diff")
	if code != 0 || !strings.Contains(stdout, "+ field done of main/notes: boolean") {
		t.Errorf("--diff: %d %s", code, stdout)
	}
	// --diff and --check write nothing.
	after, _ := os.ReadFile(filepath.Join(out, "types.d.ts"))
	if string(after) != string(types) {
		t.Error("--check or --diff rewrote the files")
	}

	if code, _, stderr := c.run("", "types", "--realm", "nope", "--out", out); code == 0 || !strings.Contains(stderr, "unknown realm") {
		t.Errorf("unknown realm: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", filepath.Join(t.TempDir(), "none"), "--check"); code == 0 || !strings.Contains(stderr, "doesn't exist") {
		t.Errorf("--check without files: %d %s", code, stderr)
	}
}

func TestTypesGo(t *testing.T) {
	dir := typesConfig(t)
	out := filepath.Join(t.TempDir(), "gen")
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}

	code, stdout, stderr := c.run("", "types", "--realm", "app", "--lang", "go", "--package", "notesapi", "--out", out)
	if code != 0 || !strings.Contains(stdout, "types.go") {
		t.Fatalf("types --lang go: %d %s %s", code, stdout, stderr)
	}
	types, err := os.ReadFile(filepath.Join(out, "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\npackage notesapi\n", "type MainNotes struct {", "func NewAppRealm(c *client.Client) *AppRealm"} {
		if !strings.Contains(string(types), want) {
			t.Errorf("types.go lacks %q:\n%s", want, types)
		}
	}
	if code, stdout, _ := c.run("", "types", "--realm", "app", "--lang", "go", "--package", "notesapi", "--out", out, "--check"); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("--check on fresh files: %d %s", code, stdout)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--lang", "go", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "stale") {
		t.Errorf("--check with another package name: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--lang", "go", "--package", "not-a-name", "--out", out); code == 0 || !strings.Contains(stderr, "not a Go package name") {
		t.Errorf("a bad package: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--package", "x", "--out", out); code == 0 || !strings.Contains(stderr, "--package is for --lang go") {
		t.Errorf("--package without --lang go: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--lang", "rust", "--out", out); code == 0 || !strings.Contains(stderr, "ts, dart, go or python") {
		t.Errorf("an unknown language: %d %s", code, stderr)
	}
}

func TestTypesPython(t *testing.T) {
	dir := typesConfig(t)
	out := filepath.Join(t.TempDir(), "gen")
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}

	code, stdout, stderr := c.run("", "types", "--realm", "app", "--lang", "python", "--out", out)
	if code != 0 || !strings.Contains(stdout, "types.py") {
		t.Fatalf("types --lang python: %d %s %s", code, stdout, stderr)
	}
	types, err := os.ReadFile(filepath.Join(out, "types.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"from __future__ import annotations", "class MainNotes(backd.Doc,", "class AppRealm:", "class AsyncAppRealm:"} {
		if !strings.Contains(string(types), want) {
			t.Errorf("types.py lacks %q:\n%s", want, types)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "realm.json")); err != nil {
		t.Errorf("realm.json is written next to types.py: %v", err)
	}
	if code, stdout, _ := c.run("", "types", "--realm", "app", "--lang", "python", "--out", out, "--check"); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("--check on fresh files: %d %s", code, stdout)
	}
	if err := os.WriteFile(filepath.Join(out, "types.py"), append(types, "# edited\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--lang", "python", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "stale") {
		t.Errorf("--check on an edited file: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--lang", "python", "--package", "x", "--out", out); code == 0 || !strings.Contains(stderr, "--package is for --lang go") {
		t.Errorf("--package with python: %d %s", code, stderr)
	}
}

func TestTypesFunctions(t *testing.T) {
	fnDir := "app/main/_functions/"
	dir := writeConfig(t, map[string]string{
		"app/realm.yaml":                 "auth: disabled\n",
		"app/main/notes/schema.json":     `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}}}`,
		"app/main/notes/collection.yaml": "{}\n",
		fnDir + "hello/function.yaml":    "mode: sync\n",
		fnDir + "hello/index.ts":         "export default () => 1;\n",
	})
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--functions", "--check"); code == 0 || !strings.Contains(stderr, "missing or stale") {
		t.Errorf("--check before generating: %d %s", code, stderr)
	}
	code, stdout, stderr := c.run("", "types", "--realm", "app", "--functions")
	if code != 0 || !strings.Contains(stdout, "backd.d.ts") {
		t.Fatalf("--functions: %d %s %s", code, stdout, stderr)
	}
	for _, f := range []string{"backd.d.ts", ".backd/functions.js"} {
		if _, err := os.Stat(filepath.Join(dir, fnDir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if code, stdout, _ := c.run("", "types", "--realm", "app", "--functions", "--check"); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("--check after generating: %d %s", code, stdout)
	}
}

func TestOpenAPICommand(t *testing.T) {
	dir := typesConfig(t)
	out := filepath.Join(t.TempDir(), "openapi.json")
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}

	code, stdout, stderr := c.run("", "openapi", "--realm", "app")
	if code != 0 || !strings.Contains(stdout, `"openapi": "3.1.0"`) || !strings.Contains(stdout, "/v1/app/main/notes") {
		t.Fatalf("stdout: %d %.300s %s", code, stdout, stderr)
	}
	if code, _, stderr := c.run("", "openapi", "--realm", "app", "--check"); code == 0 || !strings.Contains(stderr, "--out") {
		t.Errorf("--check without --out: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "openapi", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "doesn't exist") {
		t.Errorf("--check without file: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "openapi", "--realm", "app", "--out", out); code != 0 {
		t.Fatalf("--out: %d %s", code, stderr)
	}
	if code, stdout, _ := c.run("", "openapi", "--realm", "app", "--out", out, "--check"); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("--check fresh: %d %s", code, stdout)
	}
	if err := os.WriteFile(filepath.Join(dir, "app/main/notes/schema.json"),
		[]byte(`{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"},"done":{"type":"boolean"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := c.run("", "openapi", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "stale") {
		t.Errorf("--check stale: %d %s", code, stderr)
	}
	if code, _, stderr := c.run("", "openapi", "--realm", "nope"); code == 0 || !strings.Contains(stderr, "unknown realm") {
		t.Errorf("unknown realm: %d %s", code, stderr)
	}
}

func TestOpenAPIAdminCommand(t *testing.T) {
	dir := writeConfig(t, map[string]string{
		"app/realm.yaml":                 "auth:\n  strategies:\n    password: [password]\nroles:\n  admin:\n    admin: true\n",
		"app/main/notes/schema.json":     `{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string"}}}`,
		"app/main/notes/collection.yaml": "{}\n",
	})
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}
	code, stdout, stderr := c.run("", "openapi", "--realm", "app", "--admin")
	if code != 0 || !strings.Contains(stdout, "/v1/app/_admin/users") || !strings.Contains(stdout, "/v1/app/_admin/data/main/notes") {
		t.Fatalf("--admin: %d %.300s %s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "/v1/app/_auth/login") {
		t.Error("the admin document has client routes")
	}
}

// A versioned collection gets its schema.d.ts next to its schema files; --check says when it is
// missing or stale, and the realm.json lists the current versions for the clients.
func TestTypesWritesAndChecksSchemaFiles(t *testing.T) {
	dir := writeConfig(t, map[string]string{
		"app/realm.yaml":                 "auth:\n  strategies:\n    password: [password]\n",
		"app/main/assets/schema.v1.json": `{"type":"object","properties":{"title":{"type":"string"}}}`,
		"app/main/assets/schema.v2.json": `{"type":"object","properties":{"title":{"type":"string"},"slug":{"type":"string"}}}`,
		"app/main/assets/schema.v2.ts":   "import type { V1, V2 } from \"./schema.d.ts\";\nexport default (doc: V1): V2 => ({ ...doc, slug: doc.title ?? \"\" });\n",
		"app/main/notes/schema.json":     `{"type":"object","properties":{"title":{"type":"string"}}}`,
	})
	out := filepath.Join(t.TempDir(), "gen")
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": dir}}
	schemaFile := filepath.Join(dir, "app/main/assets/schema.d.ts")

	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "doesn't exist") {
		t.Fatalf("--check before anything is written: %d %s", code, stderr)
	}
	code, stdout, stderr := c.run("", "types", "--realm", "app", "--out", out)
	if code != 0 || !strings.Contains(stdout, schemaFile) {
		t.Fatalf("types: %d %s %s", code, stdout, stderr)
	}
	body, err := os.ReadFile(schemaFile)
	if err != nil || !strings.Contains(string(body), "export type V2 = ") || !strings.Contains(string(body), "export type Current = V2;") {
		t.Fatalf("schema.d.ts: %v\n%s", err, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "app/main/notes/schema.d.ts")); err == nil {
		t.Error("an unversioned collection got a schema.d.ts")
	}
	module, err := os.ReadFile(filepath.Join(out, "migrations.ts"))
	if err != nil || !strings.Contains(string(module), "export const upgrade = {") || !strings.Contains(string(module), "mainAssets(doc: MainAssets): MainAssetsV2") {
		t.Fatalf("migrations.ts: %v\n%s", err, module)
	}
	info, _ := os.ReadFile(filepath.Join(out, "realm.json"))
	if !strings.Contains(string(info), `"schema_versions"`) || !strings.Contains(string(info), `"main/assets": 2`) {
		t.Errorf("realm.json:\n%s", info)
	}
	if code, stdout, _ := c.run("", "types", "--realm", "app", "--out", out, "--check"); code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("--check on fresh files: %d %s", code, stdout)
	}

	// A change to a schema file makes schema.d.ts stale (and --check writes nothing); a delete makes it missing.
	if err := os.WriteFile(filepath.Join(dir, "app/main/assets/schema.v2.json"), []byte(`{"type":"object","properties":{"title":{"type":"string"},"slug":{"type":"string"},"rank":{"type":"integer"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "schema.d.ts is stale") {
		t.Errorf("--check with a stale schema.d.ts: %d %s", code, stderr)
	}
	if after, _ := os.ReadFile(schemaFile); string(after) != string(body) {
		t.Error("--check rewrote schema.d.ts")
	}
	_ = os.Remove(schemaFile)
	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "schema.d.ts doesn't exist") {
		t.Errorf("--check with a missing schema.d.ts: %d %s", code, stderr)
	}
	if code, _, _ := c.run("", "types", "--realm", "app", "--out", out); code != 0 {
		t.Fatal("regenerating failed")
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", out, "--check"); code != 0 {
		t.Errorf("--check after regenerating: %d %s", code, stderr)
	}
	// migrations.ts is checked too, and a database filter leaves it out.
	if err := os.WriteFile(filepath.Join(out, "migrations.ts"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "migrations.ts is stale") {
		t.Errorf("--check with a stale migrations.ts: %d %s", code, stderr)
	}
	_ = os.Remove(filepath.Join(out, "migrations.ts"))
	if code, _, stderr := c.run("", "types", "--realm", "app", "--out", out, "--check"); code == 0 || !strings.Contains(stderr, "migrations.ts doesn't exist") {
		t.Errorf("--check with a missing migrations.ts: %d %s", code, stderr)
	}
	if code, _, _ := c.run("", "types", "--realm", "app", "--out", out); code != 0 {
		t.Fatal("regenerating failed")
	}
	// Other languages leave the schema files alone.
	_ = os.Remove(schemaFile)
	if code, _, _ := c.run("", "types", "--realm", "app", "--out", out, "--lang", "go"); code != 0 {
		t.Fatal("go types failed")
	}
	if _, err := os.Stat(schemaFile); err == nil {
		t.Error("--lang go wrote schema.d.ts")
	}
}
