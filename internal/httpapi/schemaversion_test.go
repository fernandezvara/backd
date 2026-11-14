package httpapi

import (
	"net/http"
	"testing"
)

func schemaVersionOf(body map[string]any) any {
	meta, _ := body["_meta"].(map[string]any)
	return meta["schema_version"]
}

// The schema version of the collection is recorded by every kind of write that stores content.
func TestSchemaVersionStamp(t *testing.T) {
	f := newRulesFixture(t) // acme/app/revisions has schema.v1.json, schema.v2.json and schema.v2.ts
	const revisions = "/v1/acme/app/revisions"
	stored := func(id string) map[string]any {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		return f.store.docs[revisionsKey][id]["_meta"].(map[string]any)
	}
	as := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}
	patch := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/merge-patch+json"}

	// A create is stamped with the current version, whatever the client says.
	rec, body := f.doH(t, "POST", revisions, `{"title": "Widget", "_meta": {"schema_version": 1}}`, as)
	if rec.Code != http.StatusCreated || schemaVersionOf(body) != float64(2) {
		t.Fatalf("create = %d %s, want schema_version 2", rec.Code, rec.Body)
	}
	id := body["id"].(string)

	// A document stored without a stamp reads as version 1...
	delete(stored(id), "schema_version")
	rec, body = f.doH(t, "GET", revisions+"/"+id, "", as)
	if rec.Code != http.StatusOK || schemaVersionOf(body) != float64(1) {
		t.Fatalf("get = %d %s, want schema_version 1", rec.Code, rec.Body)
	}

	// ...and each kind of write moves it to the current one.
	batch := func(op string) int {
		rec, _ := f.doH(t, "POST", "/v1/acme/app/_batch", `{"operations": [`+op+`]}`, as)
		return rec.Code
	}
	for name, do := range map[string]func() int{
		"PUT": func() int {
			rec, _ := f.doH(t, "PUT", revisions+"/"+id, `{"title": "Put", "_meta": {"schema_version": 1}}`, as)
			return rec.Code
		},
		"PATCH": func() int {
			rec, _ := f.doH(t, "PATCH", revisions+"/"+id, `{"title": "Patched"}`, patch)
			return rec.Code
		},
		"batch replace": func() int {
			return batch(`{"op": "replace", "collection": "revisions", "id": "` + id + `", "document": {"title": "Batch replace"}}`)
		},
		"batch patch": func() int {
			return batch(`{"op": "patch", "collection": "revisions", "id": "` + id + `", "patch": {"title": "Batch patch"}}`)
		},
	} {
		delete(stored(id), "schema_version")
		if code := do(); code != http.StatusOK {
			t.Fatalf("%s = %d", name, code)
		}
		if got := stored(id)["schema_version"]; got != int64(2) {
			t.Errorf("after %s the stored stamp = %v, want 2", name, got)
		}
	}

	// A batch create is stamped too.
	rec, out := f.doH(t, "POST", "/v1/acme/app/_batch", `{"operations": [{"op": "create", "collection": "revisions", "document": {"title": "B"}}]}`, as)
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
