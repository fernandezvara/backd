package query

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

var cursorFields = map[string]registry.Field{
	"name":    {Types: []string{"string", "null"}},
	"score":   {Types: []string{"integer"}},
	"ratio":   {Types: []string{"number", "integer"}},
	"flag":    {Types: []string{"boolean"}},
	"tags":    {Types: []string{"array"}, ItemTypes: []string{"string"}},
	"profile": {Types: []string{"object"}},
	"mixed":   {Types: []string{"string", "integer"}},
	"any":     {},
}

func sortOf(t *testing.T, order string) []storage.SortField {
	t.Helper()
	s, err := ParseOrderBy(order, cursorFields)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCursorable(t *testing.T) {
	for order, want := range map[string]string{
		"": "", "name": "", "-score,name": "", "ratio,-flag": "", "_meta.created_at": "", "_meta.owner": "",
		"tags": "tags", "score,profile": "profile", "mixed": "mixed", "-any": "any",
	} {
		ok, field := Cursorable(sortOf(t, order), cursorFields)
		if ok != (want == "") || field != want {
			t.Errorf("order_by %q: ok=%v field=%q, want field %q", order, ok, field, want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 123_000_000, time.UTC)
	doc := storage.Document{"id": "x1", "name": "ada", "score": int64(7), "_meta": map[string]any{"created_at": at}}
	sort := sortOf(t, "-name,score,-_meta.created_at")
	cursor := EncodeCursor(sort, doc)
	f, err := ParseCursor(cursor, sort, cursorFields)
	if err != nil {
		t.Fatal(err)
	}
	// (name < ada or name is null) or (name = ada and score > 7) or (… and created_at < at) or (… and id > x1)
	want := storage.Or{
		storage.Or{
			storage.Condition{Field: "name", Op: storage.OpLt, Value: "ada"},
			storage.Condition{Field: "name", Op: storage.OpIsNull, Value: true},
		},
		storage.And{storage.Condition{Field: "name", Op: storage.OpEq, Value: "ada"}, storage.Condition{Field: "score", Op: storage.OpGt, Value: int64(7)}},
		storage.And{
			storage.Condition{Field: "name", Op: storage.OpEq, Value: "ada"}, storage.Condition{Field: "score", Op: storage.OpEq, Value: int64(7)},
			storage.Or{
				storage.Condition{Field: "_meta.created_at", Op: storage.OpLt, Value: at},
				storage.Condition{Field: "_meta.created_at", Op: storage.OpIsNull, Value: true},
			},
		},
		storage.And{
			storage.Condition{Field: "name", Op: storage.OpEq, Value: "ada"}, storage.Condition{Field: "score", Op: storage.OpEq, Value: int64(7)},
			storage.Condition{Field: "_meta.created_at", Op: storage.OpEq, Value: at}, storage.Condition{Field: "id", Op: storage.OpGt, Value: "x1"},
		},
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("filter:\n got %#v\nwant %#v", f, want)
	}
}

func TestCursorNulls(t *testing.T) {
	// After a null in ascending order: everything non-null, then the ties.
	sort := sortOf(t, "name")
	f, err := ParseCursor(EncodeCursor(sort, storage.Document{"id": "x1"}), sort, cursorFields)
	if err != nil {
		t.Fatal(err)
	}
	want := storage.Or{
		storage.Condition{Field: "name", Op: storage.OpIsNull, Value: false},
		storage.And{storage.Condition{Field: "name", Op: storage.OpIsNull, Value: true}, storage.Condition{Field: "id", Op: storage.OpGt, Value: "x1"}},
	}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("ascending after null:\n got %#v\nwant %#v", f, want)
	}
	// In descending order nothing comes after a null but its ties.
	sort = sortOf(t, "-name")
	f, err = ParseCursor(EncodeCursor(sort, storage.Document{"id": "x1", "name": nil}), sort, cursorFields)
	if err != nil {
		t.Fatal(err)
	}
	want2 := storage.And{storage.Condition{Field: "name", Op: storage.OpIsNull, Value: true}, storage.Condition{Field: "id", Op: storage.OpGt, Value: "x1"}}
	if !reflect.DeepEqual(f, want2) {
		t.Errorf("descending after null:\n got %#v\nwant %#v", f, want2)
	}
}

func TestParseCursorRefusals(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	sort := sortOf(t, "score")
	valid := EncodeCursor(sort, storage.Document{"id": "x1", "score": int64(3)})
	other := EncodeCursor(sortOf(t, "-score"), storage.Document{"id": "x1", "score": int64(3)})
	for name, c := range map[string]struct{ cursor, reason string }{
		"not base64":     {"!!!", "not a cursor"},
		"not json":       {enc("hello"), "not a cursor"},
		"unknown keys":   {enc(`{"o":"score,id","v":[1,"x"],"x":1}`), "not a cursor"},
		"trailing data":  {enc(`{"o":"score,id","v":[1,"x"]} {}`), "not a cursor"},
		"another order":  {other, "another order_by"},
		"too few values": {enc(`{"o":"score,id","v":[1]}`), "not a cursor"},
		"wrong type":     {enc(`{"o":"score,id","v":["three","x1"]}`), "doesn't fit score"},
		"fractional":     {enc(`{"o":"score,id","v":[1.5,"x1"]}`), "doesn't fit score"},
		"object value":   {enc(`{"o":"score,id","v":[{"$gt":1},"x1"]}`), "doesn't fit score"},
		"non-string id":  {enc(`{"o":"score,id","v":[1,2]}`), "doesn't fit id"},
		"too long":       {strings.Repeat("a", maxCursorBytes+1), "too long"},
		"empty":          {"", "not a cursor"},
	} {
		_, err := ParseCursor(c.cursor, sort, cursorFields)
		qe, ok := err.(Errors)
		if !ok || len(qe) != 1 || qe[0].Path != "after" || !strings.Contains(qe[0].Reason, c.reason) {
			t.Errorf("%s: %v, want a reason with %q", name, err, c.reason)
		}
	}
	if _, err := ParseCursor(valid, sort, cursorFields); err != nil {
		t.Errorf("a valid cursor: %v", err)
	}
	// A sort a cursor can't follow.
	arr := sortOf(t, "tags")
	if _, err := ParseCursor(valid, arr, cursorFields); err == nil || !strings.Contains(err.Error(), "order_by tags") {
		t.Errorf("array sort: %v", err)
	}
}
