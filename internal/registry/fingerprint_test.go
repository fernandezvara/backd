package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprint(t *testing.T) {
	files := map[string]string{
		"shop/realm.yaml":                        "roles:\n  staff: {}\n",
		"shop/app/notes/schema.json":             `{}`,
		"shop/app/notes/rules.yaml":              "read: \"true\"\n",
		"shop/app/notes/indexes.json":            `[]`,
		fnPrefix + "hello/function.yaml":         "",
		fnPrefix + "hello/index.ts":              "export default () => 1;\n",
		fnPrefix + BuildDir + "/" + ManifestFile: `{}`,
		fnPrefix + BuildDir + "/hello-0123.js":   "export default () => 1;\n",
		"open/realm.yaml":                        "auth: disabled\n",
		"open/data/items/schema.json":            `{}`,
	}
	root := writeTree(t, files)
	fp := func(root string) string {
		t.Helper()
		reg, err := Load(root)
		if err != nil {
			t.Fatal(err)
		}
		f, err := reg.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	base := fp(root)
	if len(base) != 64 || fp(root) != base {
		t.Fatalf("fingerprint %q not stable", base)
	}
	// The same files anywhere give the same fingerprint.
	if other := fp(writeTree(t, files)); other != base {
		t.Error("fingerprint depends on the directory")
	}

	change := func(rel, content string) string {
		t.Helper()
		full := filepath.Join(root, rel)
		old, _ := os.ReadFile(full)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got := fp(root)
		if old == nil {
			os.Remove(full)
		} else {
			os.WriteFile(full, old, 0o644)
		}
		return got
	}
	// What backd reads changes it.
	for rel, content := range map[string]string{
		"shop/realm.yaml":                      "roles:\n  staff: {}\n  editor: {}\n",
		"shop/app/notes/schema.json":           `{"type": "object"}`,
		"shop/app/notes/rules.yaml":            "read: \"false\"\n",
		"shop/app/notes/indexes.json":          `[{"fields": ["id"]}]`,
		fnPrefix + "hello/index.ts":            "export default () => 2;\n",
		fnPrefix + "lib/money.ts":              "export const x = 1;\n",
		fnPrefix + BuildDir + "/hello-0123.js": "export default () => 2;\n",
		"open/data/items/indexes.json":         `[]`, // a new file backd reads
	} {
		if change(rel, content) == base {
			t.Errorf("changing %s didn't change the fingerprint", rel)
		}
	}
	// What backd ignores doesn't.
	for rel, content := range map[string]string{
		".git/HEAD":                    "ref: refs/heads/main\n",
		"README.md":                    "# config\n",
		"shop/NOTES.md":                "notes\n",
		"shop/app/notes/.DS_Store":     "x",
		"shop/app/notes/README.md":     "about notes\n",
		fnPrefix + ".vscode/s.json":    "{}",
		fnPrefix + "hello/.env.sample": "X=1\n",
	} {
		if change(rel, content) != base {
			t.Errorf("adding %s changed the fingerprint", rel)
		}
	}
	if _, err := (&Registry{}).Fingerprint(); err == nil {
		t.Error("a registry without root has a fingerprint")
	}
}
