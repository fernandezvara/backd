package main

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
