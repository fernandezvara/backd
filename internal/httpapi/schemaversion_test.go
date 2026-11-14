package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

// versionedFixture has shop/orders/items with schema.v1.json and schema.v2.json (and v2's migration).
func versionedFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "shop", "orders", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"schema.v1.json": itemSchema,
		"schema.v2.json": itemSchema,
		"schema.v2.ts":   "export default (doc: unknown) => doc;",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "shop", registry.RealmFile), []byte("auth: disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return newFixtureWith(t, reg, &memStore{})
}

func schemaVersionOf(body map[string]any) any {
	meta, _ := body["_meta"].(map[string]any)
	return meta["schema_version"]
}

func TestSchemaVersionStamp(t *testing.T) {
	f := versionedFixture(t)

	// A create is stamped with the current version, whatever the client says.
	rec, body := f.do(t, "POST", items, `{"name": "Widget", "_meta": {"schema_version": 1}}`)
	if rec.Code != http.StatusCreated || schemaVersionOf(body) != float64(2) {
		t.Fatalf("create = %d %s, want schema_version 2", rec.Code, rec.Body)
	}
	id := body["id"].(string)

	// A document stored without a stamp reads as version 1...
	delete(f.store.docs["shop__orders.items"][id]["_meta"].(map[string]any), "schema_version")
	rec, body = f.do(t, "GET", items+"/"+id, "")
	if rec.Code != http.StatusOK || schemaVersionOf(body) != float64(1) {
		t.Fatalf("get = %d %s, want schema_version 1", rec.Code, rec.Body)
	}

	// ...and each kind of write moves it to the current one.
	for name, do := range map[string]func() int{
		"PUT": func() int {
			rec, _ := f.do(t, "PUT", items+"/"+id, `{"name": "Put", "_meta": {"schema_version": 1}}`)
			return rec.Code
		},
		"PATCH": func() int { rec, _ := f.do(t, "PATCH", items+"/"+id, `{"name": "Patched"}`); return rec.Code },
		"batch replace": func() int {
			rec, _ := f.do(t, "POST", "/v1/shop/orders/_batch", `{"operations": [{"op": "replace", "collection": "items", "id": "`+id+`", "document": {"name": "Batch put"}}]}`)
			return rec.Code
		},
		"batch patch": func() int {
			rec, _ := f.do(t, "POST", "/v1/shop/orders/_batch", `{"operations": [{"op": "patch", "collection": "items", "id": "`+id+`", "patch": {"name": "Batch patch"}}]}`)
			return rec.Code
		},
	} {
		delete(f.store.docs["shop__orders.items"][id]["_meta"].(map[string]any), "schema_version")
		if code := do(); code != http.StatusOK {
			t.Fatalf("%s = %d", name, code)
		}
		if got := f.store.docs["shop__orders.items"][id]["_meta"].(map[string]any)["schema_version"]; got != int64(2) {
			t.Errorf("after %s the stored stamp = %v, want 2", name, got)
		}
	}

	// A batch create is stamped too.
	rec, out := f.do(t, "POST", "/v1/shop/orders/_batch", `{"operations": [{"op": "create", "collection": "items", "document": {"name": "B"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch create = %d %s", rec.Code, rec.Body)
	}
	if got := schemaVersionOf(out["results"].([]any)[0].(map[string]any)); got != float64(2) {
		t.Errorf("batch create stamp = %v, want 2", got)
	}
}

func TestUnversionedCollectionIsNotStamped(t *testing.T) {
	f := newFixture(t)
	rec, body := f.do(t, "POST", items, `{"name": "Widget"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if v := schemaVersionOf(body); v != nil {
		t.Errorf("schema_version = %v, want none in a collection with a plain schema.json", v)
	}
}
