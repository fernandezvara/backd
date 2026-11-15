package query

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

var projectionFields = map[string]registry.Field{
	"title": {}, "body": {}, "address": {}, "address.city": {}, "address.zip": {}, "lines": {}, "lines.sku": {}, "lines.qty": {},
}

func TestParseFields(t *testing.T) {
	good := []string{"", " ", "title", "title,address.city", "id", "_meta", "_meta.version,title", "lines.sku", "title,title"}
	for _, s := range good {
		if _, err := ParseFields(s, projectionFields); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := map[string]string{
		"nope":           "fields.nope: unknown field",
		"title,,body":    "empty field name",
		"_meta.nope":     "fields._meta.nope: unknown field",
		"address.street": "fields.address.street: unknown field",
	}
	for s, want := range bad {
		_, err := ParseFields(s, projectionFields)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", s, err, want)
		}
	}
	var many []string
	for i := range MaxSelectedFields + 1 {
		many = append(many, "title"+string(rune('a'+i%26)))
	}
	if _, err := ParseFields(strings.Join(many, ","), projectionFields); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many: %v", err)
	}
	if sel, err := ParseFields("", projectionFields); sel != nil || err != nil {
		t.Errorf("empty = %v, %v", sel, err)
	}
}

func TestSelectionApply(t *testing.T) {
	doc := map[string]any{
		"id": "a1", "title": "T", "body": "long text",
		"address":   map[string]any{"city": "Madrid", "zip": "28001"},
		"lines":     []any{map[string]any{"sku": "x", "qty": 1}, map[string]any{"sku": "y", "qty": 2}},
		"_meta":     map[string]any{"version": 3, "created_at": "2026-01-01T00:00:00Z"},
		"_expanded": map[string]any{"author": map[string]any{"id": "u1"}},
	}
	tests := []struct {
		fields string
		want   map[string]any
	}{
		{"title", map[string]any{"id": "a1", "title": "T", "_expanded": doc["_expanded"]}},
		{"address.city", map[string]any{"id": "a1", "address": map[string]any{"city": "Madrid"}, "_expanded": doc["_expanded"]}},
		{"address,address.city", map[string]any{"id": "a1", "address": doc["address"], "_expanded": doc["_expanded"]}},
		{"lines.sku", map[string]any{"id": "a1", "lines": []any{map[string]any{"sku": "x"}, map[string]any{"sku": "y"}}, "_expanded": doc["_expanded"]}},
		{"_meta.version", map[string]any{"id": "a1", "_meta": map[string]any{"version": 3}, "_expanded": doc["_expanded"]}},
		{"_meta", map[string]any{"id": "a1", "_meta": doc["_meta"], "_expanded": doc["_expanded"]}},
		{"id", map[string]any{"id": "a1", "_expanded": doc["_expanded"]}},
	}
	for _, tt := range tests {
		sel, err := ParseFields(tt.fields, projectionFields)
		if err != nil {
			t.Fatal(err)
		}
		if got := sel.Apply(doc); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("fields=%s\n got  %v\n want %v", tt.fields, got, tt.want)
		}
	}
	if len(doc) != 7 || doc["body"] != "long text" {
		t.Error("Apply changed the document")
	}
	var none *Selection
	if got := none.Apply(doc); !reflect.DeepEqual(got, doc) || none.Key() != "" {
		t.Error("no selection must leave the document as it is")
	}
}

func TestSelectionKeyIsOrderIndependent(t *testing.T) {
	a, _ := ParseFields("title,body", projectionFields)
	b, _ := ParseFields("body, title,body", projectionFields)
	if a.Key() != b.Key() || a.Key() == "" {
		t.Errorf("%q vs %q", a.Key(), b.Key())
	}
}
