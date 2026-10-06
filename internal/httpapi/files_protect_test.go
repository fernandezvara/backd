package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

func fileDetails(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name, "size": int64(3), "type": "image/png", "sha256": strings.Repeat("0", 64), "uploaded_at": "2026-10-06T10:00:00Z"}
}

// seedFile gives a stored document a file, the way an upload does.
func (f *rulesFixture) seedFile(t *testing.T, collection, id, field string, value any) {
	t.Helper()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	doc := f.store.docs["acme__app."+collection][id]
	if doc == nil {
		t.Fatalf("no document %s/%s", collection, id)
	}
	doc[field] = value
	meta := doc["_meta"].(map[string]any)
	meta["updated_at"] = time.Now().UTC()
}

func (f *rulesFixture) storedDoc(collection, id string) storage.Document {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return clone(f.store.docs["acme__app."+collection][id]).(map[string]any)
}

// A file field holds what backd writes: a client's create, PUT, PATCH and batch
// can't set it, and a PUT keeps the files the document has.
func TestFileFieldsAreServerOwned(t *testing.T) {
	f := newRulesFixture(t)
	forged := `"avatar": {"id": "fl_aaaaaaaaaaaaaaaaaaaa", "name": "x.png", "size": 1, "type": "image/png", "sha256": "` + strings.Repeat("0", 64) + `", "uploaded_at": "2026-10-06T10:00:00Z"}`

	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/library", `{"title": "a", `+forged+`, "receipts": []}`)
	if code != 201 || doc["avatar"] != nil || doc["receipts"] != nil {
		t.Fatalf("create: %d %v", code, doc)
	}
	id := doc["id"].(string)
	f.seedFile(t, "library", id, "avatar", fileDetails("fl_bbbbbbbbbbbbbbbbbbbb", "mine.png"))
	f.seedFile(t, "library", id, "receipts", []any{fileDetails("fl_cccccccccccccccccccc", "r1.png")})

	// PATCH can't change them, and leaves them be.
	code, got := f.as(t, f.ada, "PATCH", "/v1/acme/app/library/"+id, `{"title": "b", `+forged+`}`)
	if code != 200 || got["title"] != "b" || got["avatar"].(map[string]any)["id"] != "fl_bbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("patch: %d %v", code, got)
	}
	// PUT replaces the rest and keeps the files.
	code, got = f.as(t, f.ada, "PUT", "/v1/acme/app/library/"+id, `{"title": "c", `+forged+`}`)
	if code != 200 || got["title"] != "c" || got["avatar"].(map[string]any)["name"] != "mine.png" || len(got["receipts"].([]any)) != 1 {
		t.Fatalf("put: %d %v", code, got)
	}
	// A client can't null a file field either.
	code, got = f.as(t, f.ada, "PATCH", "/v1/acme/app/library/"+id, `{"avatar": null, "receipts": null}`)
	if code != 200 || got["avatar"] == nil || got["receipts"] == nil {
		t.Fatalf("patch null: %d %v", code, got)
	}

	// The same in a batch.
	ops := `{"operations": [
	  {"op": "create", "collection": "library", "document": {"title": "n", ` + forged + `}},
	  {"op": "replace", "collection": "library", "id": "` + id + `", "document": {"title": "d", ` + forged + `}},
	  {"op": "patch", "collection": "library", "id": "` + id + `", "patch": {"title": "e", "avatar": null}}
	]}`
	code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_batch", ops)
	if code != 200 {
		t.Fatalf("batch: %d %v", code, out)
	}
	results := out["results"].([]any)
	if results[0].(map[string]any)["avatar"] != nil {
		t.Errorf("a batch create set a file: %v", results[0])
	}
	stored := f.storedDoc("library", id)
	if stored["title"] != "e" || stored["avatar"].(map[string]any)["name"] != "mine.png" {
		t.Errorf("after the batch: %v", stored)
	}
	// Not even through the API key (it skips rules, not the ownership of these fields).
	code, got = f.as(t, f.key, "PATCH", "/v1/acme/app/library/"+id, `{"avatar": {"id": "x"}}`)
	if code != 200 || got["avatar"].(map[string]any)["name"] != "mine.png" {
		t.Errorf("api key patch: %d %v", code, got)
	}
}
