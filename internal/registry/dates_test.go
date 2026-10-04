package registry

import (
	"reflect"
	"testing"
	"time"
)

func TestDatesToStorage(t *testing.T) {
	c := &Collection{Fields: map[string]Field{
		"starts_at":    {Date: true},
		"event.at":     {Date: true},
		"box.inner.at": {Date: true},
		"note":         {}, // declared, not a date
	}}

	doc := map[string]any{
		"starts_at": "2026-03-01T10:00:00.123456789+02:00", // → UTC, ms truncated
		"ends_at":   "2026-03-02T10:00:00Z",                // not declared: stays a string
		"note":      "2026-03-01T10:00:00Z",                // not a date field: stays a string
		"event": map[string]any{
			"at":   "2026-03-01T00:00:00Z",
			"name": "launch",
		},
		"box": map[string]any{
			"inner": map[string]any{"at": "2026-03-01T01:02:03Z"},
		},
	}

	out := c.DatesToStorage(doc)

	want := map[string]any{
		"starts_at": time.Date(2026, 3, 1, 8, 0, 0, 123_000_000, time.UTC),
		"ends_at":   "2026-03-02T10:00:00Z",
		"note":      "2026-03-01T10:00:00Z",
		"event": map[string]any{
			"at":   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			"name": "launch",
		},
		"box": map[string]any{
			"inner": map[string]any{"at": time.Date(2026, 3, 1, 1, 2, 3, 0, time.UTC)},
		},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("DatesToStorage = %#v, want %#v", out, want)
	}

	// The input doc, nested maps included, is left alone: a request may be
	// retried with the same body.
	wantDoc := map[string]any{
		"starts_at": "2026-03-01T10:00:00.123456789+02:00",
		"ends_at":   "2026-03-02T10:00:00Z",
		"note":      "2026-03-01T10:00:00Z",
		"event":     map[string]any{"at": "2026-03-01T00:00:00Z", "name": "launch"},
		"box":       map[string]any{"inner": map[string]any{"at": "2026-03-01T01:02:03Z"}},
	}
	if !reflect.DeepEqual(doc, wantDoc) {
		t.Errorf("input doc was mutated: %#v", doc)
	}
}

func TestDatesToStorageLeavesValuesAlone(t *testing.T) {
	c := &Collection{Fields: map[string]Field{
		"at":      {Date: true},
		"deep.at": {Date: true},
	}}
	tests := []struct {
		name string
		doc  map[string]any
	}{
		{"field absent", map[string]any{"x": 1}},
		{"field null", map[string]any{"at": nil}},
		{"field a number", map[string]any{"at": 42}},
		{"field a bool", map[string]any{"at": true}},
		{"field already a time", map[string]any{"at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"field not RFC 3339", map[string]any{"at": "yesterday"}},
		{"parent absent", map[string]any{"other": map[string]any{"at": "2026-01-01T00:00:00Z"}}},
		{"parent null", map[string]any{"deep": nil}},
		{"parent a scalar", map[string]any{"deep": "oops"}},
		{"leaf absent", map[string]any{"deep": map[string]any{"x": 1}}},
		{"leaf null", map[string]any{"deep": map[string]any{"at": nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if out := c.DatesToStorage(tt.doc); !reflect.DeepEqual(out, tt.doc) {
				t.Errorf("changed %#v to %#v", tt.doc, out)
			}
		})
	}
}

func TestDatesToStorageNoDateFields(t *testing.T) {
	c := &Collection{} // nil Fields
	doc := map[string]any{"name": "x"}
	out := c.DatesToStorage(doc)
	if !reflect.DeepEqual(out, doc) {
		t.Errorf("content changed: %#v", out)
	}
	// With nothing to convert the same map is returned, not a copy.
	out["marker"] = true
	if _, ok := doc["marker"]; !ok {
		t.Error("returned a copy, want the same map")
	}
}
