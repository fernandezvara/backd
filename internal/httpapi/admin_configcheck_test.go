package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// treeHash fingerprints a directory, to prove a check leaves the configuration alone.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			h.Write([]byte(p))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func TestConfigCheckLoadsTheWholeRealmWithTheDrafts(t *testing.T) {
	f := newFilesFixture(t)
	key := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}
	post := func(body string) (int, map[string]any) {
		t.Helper()
		rec, out := f.doRaw(t, "POST", "/v1/acme/_admin/config/check", []byte(body), key)
		return rec.Code, out
	}
	before := treeHash(t, f.reg.Root)

	// Nothing changed: the realm as it is loads, and every file was read.
	code, out := post(`{"files": {}}`)
	if code != 200 || out["ok"] != true || out["checked"].(float64) < 10 {
		t.Fatalf("unchanged: %d %v", code, out)
	}
	all := out["checked"]

	// A rule that names a field the schema doesn't have is found, in the collection.yaml that holds it.
	code, out = post(`{"files": {"app/notes/collection.yaml": "rules:\n  read: document.nothing == 1\n"}}`)
	problems, _ := out["problems"].([]any)
	if code != 200 || out["ok"] != false || len(problems) == 0 || !strings.Contains(problems[0].(map[string]any)["file"].(string), "app/notes/collection.yaml") {
		t.Fatalf("a bad rule: %d %v", code, out)
	}
	if strings.Contains(problems[0].(map[string]any)["message"].(string), os.TempDir()) {
		t.Errorf("the temporary directory leaks into the message: %v", problems[0])
	}

	// A schema change that breaks a rule in another file: the whole realm is what is checked.
	schema := `{"type":"object","properties":{"title":{"type":"string"},"avatar":{"type":"string"}}}` // avatar is a file field of collection.yaml
	code, out = post(`{"files": {"app/library/schema.json": ` + quoted(schema) + `}}`)
	problems, _ = out["problems"].([]any)
	if code != 200 || out["ok"] != false || len(problems) == 0 {
		t.Fatalf("a schema that breaks the rules: %d %v", code, out)
	}
	if file := problems[0].(map[string]any)["file"].(string); !strings.Contains(file, "collection.yaml") {
		t.Errorf("the problem is in another file than the draft: %v", problems[0])
	}

	// A schema that isn't JSON Schema is refused, naming the file.
	code, out = post(`{"files": {"app/library/schema.json": "{\"type\": \"not-a-type\"}"}}`)
	problems, _ = out["problems"].([]any)
	if code != 200 || out["ok"] != false || len(problems) == 0 || !strings.Contains(problems[0].(map[string]any)["message"].(string), "library/schema.json") {
		t.Errorf("an invalid schema: %d %v", code, out)
	}

	// A valid change passes, a new collection counts as one more file, and a null deletes.
	code, out = post(`{"files": {"app/notes/collection.yaml": "rules:\n  read: user != nil\n", "app/fresh/schema.json": "{\"type\":\"object\"}"}}`)
	if code != 200 || out["ok"] != true || out["checked"].(float64) != all.(float64)+1 {
		t.Errorf("a valid change: %d %v (was %v files)", code, out, all)
	}
	if code, out = post(`{"files": {"app/notes/collection.yaml": null}}`); code != 200 || out["checked"].(float64) != all.(float64)-1 {
		t.Errorf("a deleted file: %d %v", code, out)
	}

	// Refused input.
	for _, body := range []string{`{}`, `nope`, `{"files": {"../x": "a"}}`, `{"files": {"/etc/passwd": "a"}}`, `{"files": {"a//b": "a"}}`, `{"files": {".hidden": "a"}}`} {
		if code, _ := post(body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	// Not for users, and nothing on disk changed.
	if rec, _ := f.doRaw(t, "POST", "/v1/acme/_admin/config/check", []byte(`{"files": {}}`), map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}); rec.Code != 403 {
		t.Errorf("a user: %d", rec.Code)
	}
	if treeHash(t, f.reg.Root) != before {
		t.Error("a check changed the configuration on disk")
	}
	tmps, _ := filepath.Glob(filepath.Join(os.TempDir(), "backd-config-check-*"))
	if len(tmps) != 0 {
		t.Errorf("temporary copies left: %v", tmps)
	}
}

func quoted(s string) string {
	b := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
	return `"` + b + `"`
}
