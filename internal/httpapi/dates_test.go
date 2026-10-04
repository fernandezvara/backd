package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

const eventSchema = `{
  "type": "object",
  "properties": {
    "title":     {"type": "string"},
    "starts_at": {"type": "string", "format": "date-time", "x-backd-store": "date"},
    "ends_at":   {"type": ["string", "null"], "format": "date-time", "x-backd-store": "date"},
    "note":      {"type": "string", "format": "date-time"},
    "event":     {"type": "object", "properties": {"at": {"type": "string", "format": "date-time", "x-backd-store": "date"}}}
  },
  "required": ["title"]
}`

func eventsRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "shop", "orders", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, c := range map[string]string{filepath.Join(dir, "schema.json"): eventSchema, filepath.Join(root, "shop", registry.RealmFile): "auth: disabled\n"} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestDatesAreStoredAsDatesAndSpokenAsText(t *testing.T) {
	f := newFixtureWith(t, eventsRegistry(t), &memStore{})
	stored := func(id string) map[string]any {
		for _, docs := range f.store.docs {
			if d, ok := docs[id]; ok {
				return d
			}
		}
		t.Fatalf("no stored document %s", id)
		return nil
	}

	// In: any offset and precision. Stored: a time, UTC, to the millisecond. Out: text.
	rec, doc := f.do(t, "POST", items, `{"title": "a", "starts_at": "2026-09-01T14:00:00.123456+02:00", "ends_at": null, "note": "2026-09-01T14:00:00+02:00", "event": {"at": "2026-12-31T23:59:59Z"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if doc["starts_at"] != "2026-09-01T12:00:00.123Z" || doc["ends_at"] != nil || doc["event"].(map[string]any)["at"] != "2026-12-31T23:59:59.000Z" {
		t.Errorf("answer: %v", doc)
	}
	if doc["note"] != "2026-09-01T14:00:00+02:00" {
		t.Errorf("a date-time left as text must stay as it was: %v", doc["note"])
	}
	id := doc["id"].(string)
	row := stored(id)
	want := time.Date(2026, 9, 1, 12, 0, 0, 123_000_000, time.UTC)
	if at, ok := row["starts_at"].(time.Time); !ok || !at.Equal(want) {
		t.Errorf("stored starts_at = %#v, want the time %v", row["starts_at"], want)
	}
	if _, ok := row["event"].(map[string]any)["at"].(time.Time); !ok {
		t.Errorf("stored event.at = %#v", row["event"])
	}
	if _, ok := row["note"].(string); !ok {
		t.Errorf("stored note = %#v, want text", row["note"])
	}

	// Not a date: refused by the schema, with the field's path.
	for _, bad := range []string{`{"title": "x", "starts_at": "yesterday"}`, `{"title": "x", "starts_at": 5}`, `{"title": "x", "event": {"at": "soon"}}`} {
		if rec, _ := f.do(t, "POST", items, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}

	// Reading gives text again; a patch of something else keeps the dates; a patch of a date converts.
	if _, got := f.do(t, "GET", items+"/"+id, ""); got["starts_at"] != "2026-09-01T12:00:00.123Z" {
		t.Errorf("get: %v", got)
	}
	rec, patched := f.do(t, "PATCH", items+"/"+id, `{"title": "b"}`)
	if rec.Code != http.StatusOK || patched["starts_at"] != "2026-09-01T12:00:00.123Z" || patched["title"] != "b" {
		t.Errorf("patch of another field: %d %v", rec.Code, patched)
	}
	rec, patched = f.do(t, "PATCH", items+"/"+id, `{"starts_at": "2027-01-01T00:00:00+01:00", "ends_at": "2027-01-02T00:00:00Z"}`)
	if rec.Code != http.StatusOK || patched["starts_at"] != "2026-12-31T23:00:00.000Z" || patched["ends_at"] != "2027-01-02T00:00:00.000Z" {
		t.Errorf("patch of a date: %d %v", rec.Code, patched)
	}
	if _, ok := stored(id)["ends_at"].(time.Time); !ok {
		t.Errorf("stored ends_at = %#v", stored(id)["ends_at"])
	}
	rec, replaced := f.do(t, "PUT", items+"/"+id, `{"title": "c", "starts_at": "2030-05-05T05:05:05Z"}`)
	if rec.Code != http.StatusOK || replaced["starts_at"] != "2030-05-05T05:05:05.000Z" || replaced["ends_at"] != nil {
		t.Errorf("put: %d %v", rec.Code, replaced)
	}

	// Batches too.
	batch := "/v1/shop/orders/_batch"
	rec, out := f.do(t, "POST", batch, `{"operations": [
	  {"op": "create", "collection": "items", "document": {"title": "n", "starts_at": "2026-01-01T01:00:00+01:00"}},
	  {"op": "patch", "collection": "items", "id": "`+id+`", "patch": {"starts_at": "2031-01-01T00:00:00Z"}}]}`)
	results, _ := out["results"].([]any)
	if rec.Code != http.StatusOK || len(results) != 2 || results[0].(map[string]any)["starts_at"] != "2026-01-01T00:00:00.000Z" || results[1].(map[string]any)["starts_at"] != "2031-01-01T00:00:00.000Z" {
		t.Errorf("batch: %d %s", rec.Code, rec.Body)
	}
}

func TestQueriesOnDates(t *testing.T) {
	f := newFixtureWith(t, eventsRegistry(t), &memStore{})
	for _, at := range []string{"2026-01-01T00:00:00Z", "2026-06-01T00:00:00Z", "2026-12-01T00:00:00Z"} {
		if rec, _ := f.do(t, "POST", items, `{"title": "`+at[:7]+`", "starts_at": "`+at+`"}`); rec.Code != http.StatusCreated {
			t.Fatal(rec.Body)
		}
	}
	list := func(where string) []string {
		t.Helper()
		rec, out := f.do(t, "GET", items+"?where="+url.QueryEscape(where), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", where, rec.Code, rec.Body)
		}
		var titles []string
		for _, it := range out["items"].([]any) {
			titles = append(titles, it.(map[string]any)["title"].(string))
		}
		return titles
	}
	// Compared as instants, whatever the offset.
	if got := list(`{"starts_at": {"$gte": "2026-06-01T02:00:00+02:00"}}`); len(got) != 2 {
		t.Errorf("$gte: %v", got)
	}
	if got := list(`{"starts_at": {"$lt": "2026-06-01T00:00:00Z"}}`); len(got) != 1 || got[0] != "2026-01" {
		t.Errorf("$lt: %v", got)
	}
	if got := list(`{"starts_at": {"$between": ["2026-02-01T00:00:00Z", "2026-07-01T00:00:00Z"]}}`); len(got) != 1 || got[0] != "2026-06" {
		t.Errorf("$between: %v", got)
	}
	if got := list(`{"starts_at": "2026-12-01T00:00:00.000Z"}`); len(got) != 1 {
		t.Errorf("$eq: %v", got)
	}
	// Refusals name the operator.
	for _, bad := range []string{`{"starts_at": {"$gt": "yesterday"}}`, `{"starts_at": {"$startsWith": "2026"}}`} {
		if rec, _ := f.do(t, "GET", items+"?where="+url.QueryEscape(bad), ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// The cursor of a list ordered by a date goes through the same conversion.
	rec, out := f.do(t, "GET", items+"?order_by=starts_at&limit=1", "")
	if rec.Code != http.StatusOK || out["next_cursor"] == nil {
		t.Fatalf("first page: %d %v", rec.Code, out)
	}
	if rec, _ := f.do(t, "GET", items+"?order_by=starts_at&limit=1&after="+url.QueryEscape(out["next_cursor"].(string)), ""); rec.Code != http.StatusOK {
		t.Errorf("after a date cursor: %d %s", rec.Code, rec.Body)
	}
}
