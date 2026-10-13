package openapigen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

func load(t *testing.T, files map[string]string) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func generate(t *testing.T, reg *registry.Registry, realm string) map[string]any {
	t.Helper()
	d, err := Public(reg, realm)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(d.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func pathsOf(doc map[string]any) []string {
	var out []string
	for p := range doc["paths"].(map[string]any) {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

const noteSchema = `{"type":"object","additionalProperties":false,"required":["title"],"properties":{"title":{"type":"string"},"done":{"type":"boolean"}}}`

func TestPublicDocument(t *testing.T) {
	reg := load(t, map[string]string{
		"app/realm.yaml":                 "auth: enabled\nsignup: open\nroles:\n  admin:\n    admin: true\n",
		"app/main/notes/schema.json":     noteSchema,
		"app/main/notes/collection.yaml": "rules:\n  read: user != nil\n  create: user != nil\n  update: false\n  delete: false\n",
		// Reachable by nobody: left out.
		"app/main/secret/schema.json":            noteSchema,
		"app/main/secret/collection.yaml":        "rules:\n  read: false\n  write: false\n",
		"app/main/hidden/schema.json":            noteSchema,
		"app/main/_functions/pub/function.yaml":  "invoke: user != nil\n",
		"app/main/_functions/pub/index.js":       "export default () => 1;\n",
		"app/main/_functions/priv/function.yaml": "internal: true\n",
		"app/main/_functions/priv/index.js":      "export default () => 1;\n",
	})
	// The loader needs bundles for functions only at startup, not to describe the API.
	doc := generate(t, reg, "app")
	paths := pathsOf(doc)
	has := func(p string) bool { return slices.Contains(paths, p) }

	for _, want := range []string{
		"/v1/app/main/notes", "/v1/app/main/notes/{id}", "/v1/app/main/_batch", "/v1/app/main/_func/pub",
		"/v1/app/_auth/login", "/v1/app/_auth/signup", "/v1/app/_auth/me",
	} {
		if !has(want) {
			t.Errorf("missing %s in %v", want, paths)
		}
	}
	for _, p := range paths {
		for _, banned := range []string{"/_admin", "/_dev", "/secret", "/hidden", "/_func/priv", "/healthz"} {
			if strings.Contains(p, banned) {
				t.Errorf("%s must not be in the public document", p)
			}
		}
	}
	// No email, no providers, no push: their routes don't exist.
	for _, absent := range []string{"/v1/app/_auth/verify-email", "/v1/app/_auth/oauth/{provider}/start", "/v1/app/_auth/devices", "/v1/app/_config", "/v1/app/_auth/magic-link"} {
		if has(absent) {
			t.Errorf("%s is in a realm that doesn't enable it", absent)
		}
	}

	// Operations whose rule is literally false aren't offered.
	item := doc["paths"].(map[string]any)["/v1/app/main/notes/{id}"].(map[string]any)
	if _, ok := item["get"]; !ok {
		t.Error("read is allowed but GET is missing")
	}
	for _, m := range []string{"put", "patch", "delete"} {
		if _, ok := item[m]; ok {
			t.Errorf("%s is offered although its rule is false", m)
		}
	}

	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	note := schemas["MainNotes"].(map[string]any)
	props := note["properties"].(map[string]any)
	for _, f := range []string{"title", "done", "id", "_meta"} {
		if _, ok := props[f]; !ok {
			t.Errorf("MainNotes lacks %s", f)
		}
	}
	input := schemas["MainNotesInput"].(map[string]any)["properties"].(map[string]any)
	if _, ok := input["id"]; ok {
		t.Error("the input schema has id")
	}
	// Every $ref resolves inside the document.
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/") {
				parts := strings.Split(strings.TrimPrefix(ref, "#/components/"), "/")
				cur := doc["components"]
				for _, p := range parts {
					m, ok := cur.(map[string]any)
					if !ok {
						t.Errorf("dangling %s", ref)
						return
					}
					if cur, ok = m[p]; !ok {
						t.Errorf("dangling %s", ref)
						return
					}
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
}

func TestFunctionsAndFilesAndConfigAreTyped(t *testing.T) {
	reg := load(t, map[string]string{
		"app/realm.yaml":                               "auth: enabled\nroles:\n  admin:\n    admin: true\nstorage:\n  provider: minio\n  endpoint: https://minio.example.com\n  bucket: files-bucket\n  prefix: p\n  access_key: secret:A\n  secret_key: secret:B\n",
		"app/main/docs/schema.json":                    `{"type":"object","properties":{"title":{"type":"string"},"address":{"$ref":"#/$defs/addr"}},"$defs":{"addr":{"type":"object","properties":{"city":{"type":"string"}}}}}`,
		"app/main/docs/collection.yaml":                "rules:\n  read: true\nfiles:\n  scan:\n    max_size: 1MiB\n",
		"app/main/_functions/total/function.yaml":      "invoke: user != nil\n",
		"app/main/_functions/total/index.js":           "export default () => 1;\n",
		"app/main/_functions/total/input.schema.json":  `{"type":"object","properties":{"n":{"type":"integer"}}}`,
		"app/main/_functions/total/output.schema.json": `{"type":"object","properties":{"sum":{"type":"number"}}}`,
		"app/main/_functions/later/function.yaml":      "mode: async\ninvoke: user != nil\n",
		"app/main/_functions/later/index.js":           "export default () => 1;\n",
		"app/app-config/schema.json":                   `{"type":"object","additionalProperties":false,"properties":{"banner":{"type":"string","default":""}}}`,
	})
	doc := generate(t, reg, "app")
	paths := pathsOf(doc)
	for _, want := range []string{"/v1/app/_config", "/v1/app/main/_jobs/{id}", "/v1/app/main/docs/{id}/_files/scan"} {
		if !slices.Contains(paths, want) {
			t.Errorf("missing %s in %v", want, paths)
		}
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, want := range []string{"MainTotalInput", "MainTotalOutput", "MainDocs_Addr", "FileDetails", "AppConfigValues"} {
		if _, ok := schemas[want]; !ok {
			t.Errorf("schema %s missing: %v", want, keys(schemas))
		}
	}
	docProps := schemas["MainDocs"].(map[string]any)["properties"].(map[string]any)
	if ref := docProps["address"].(map[string]any)["$ref"]; ref != "#/components/schemas/MainDocs_Addr" {
		t.Errorf("address ref = %v", ref)
	}
	if ref := docProps["scan"].(map[string]any)["$ref"]; ref != "#/components/schemas/FileDetails" {
		t.Errorf("file field ref = %v", ref)
	}
	call := doc["paths"].(map[string]any)["/v1/app/main/_func/total"].(map[string]any)["post"].(map[string]any)
	body := call["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["$ref"] != "#/components/schemas/MainTotalInput" {
		t.Errorf("function input schema = %v", body)
	}
	// Operation ids are unique.
	seen := map[string]bool{}
	for p, item := range doc["paths"].(map[string]any) {
		for m, op := range item.(map[string]any) {
			o, ok := op.(map[string]any)
			if !ok {
				continue
			}
			id, _ := o["operationId"].(string)
			if id == "" {
				continue
			}
			if seen[id] {
				t.Errorf("operationId %s repeated (%s %s)", id, m, p)
			}
			seen[id] = true
		}
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestAuthDisabledRealmIsOpen(t *testing.T) {
	reg := load(t, map[string]string{
		"open/realm.yaml":                 "auth: disabled\n",
		"open/main/notes/schema.json":     noteSchema,
		"open/main/notes/collection.yaml": "{}\n",
	})
	paths := pathsOf(generate(t, reg, "open"))
	if !slices.Contains(paths, "/v1/open/main/notes") {
		t.Errorf("paths = %v", paths)
	}
	for _, p := range paths {
		if strings.Contains(p, "_auth") {
			t.Errorf("%s in a realm without authentication", p)
		}
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	reg, err := registry.Load("../typegen/testdata")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Public(reg, "shop")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Public(reg, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.JSON) != string(b.JSON) || a.Fingerprint != b.Fingerprint {
		t.Error("two runs differ")
	}
	if _, err := Public(reg, "nope"); err == nil {
		t.Error("unknown realm accepted")
	}
}
