package query

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Cursors (the `after` parameter) page by position instead of by offset: the
// cursor of a page holds the sort values of its last document, and the next
// page is "the documents after that position in this order", as one more
// condition on the query. It doesn't read the document it was made from, so it
// survives that document being changed or deleted; it is only a position, and
// the where and the read rules still apply to everything after it, so a forged
// cursor can't show a document the caller couldn't list anyway.
//
// A cursor can follow sort fields that hold one scalar kind (strings, numbers,
// booleans, dates, ids), each possibly missing or null. Fields that hold
// arrays, objects or several kinds can't (MongoDB orders them by element or
// across types, which a range condition doesn't follow): Cursorable says which.

// maxCursorBytes bounds the text of a cursor.
const maxCursorBytes = 2048

// Cursorable reports whether a cursor can follow the sort. When it can't, field
// names the sort field that stops it.
func Cursorable(sort []storage.SortField, fields map[string]registry.Field) (ok bool, field string) {
	for _, s := range sort {
		f, found := lookup(fields, s.Field)
		if !found || !scalarField(f) {
			return false, s.Field
		}
	}
	return true, ""
}

// scalarField says whether the field holds one kind of scalar (plus null).
func scalarField(f field) bool {
	if f.kind != kindSchema {
		return true
	}
	kinds := map[string]bool{}
	for _, t := range f.Types {
		switch t {
		case "string":
			kinds["string"] = true
		case "integer", "number":
			kinds["number"] = true
		case "boolean":
			kinds["boolean"] = true
		case "null":
		default: // array, object, or an unconstrained field (no types)
			return false
		}
	}
	return len(kinds) == 1
}

type cursorPayload struct {
	Order  string `json:"o"` // the sort, as order_by writes it: the cursor belongs to it
	Values []any  `json:"v"` // the sort values of the last document, in order
}

// sortSpec writes a sort the way order_by does.
func sortSpec(sort []storage.SortField) string {
	parts := make([]string, len(sort))
	for i, s := range sort {
		parts[i] = s.Field
		if s.Desc {
			parts[i] = "-" + s.Field
		}
	}
	return strings.Join(parts, ",")
}

// EncodeCursor makes the cursor that continues after doc in this sort. The sort
// must be Cursorable.
func EncodeCursor(sort []storage.SortField, doc storage.Document) string {
	p := cursorPayload{Order: sortSpec(sort), Values: make([]any, len(sort))}
	for i, s := range sort {
		v := valueAt(doc, s.Field)
		if t, ok := v.(time.Time); ok {
			v = t.UTC().Format(time.RFC3339Nano)
		}
		p.Values[i] = v
	}
	data, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(data)
}

// valueAt reads a dot path ("address.city", "_meta.created_at") from a
// document; a missing field is nil.
func valueAt(doc storage.Document, path string) any {
	var cur any = map[string]any(doc)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return cur
}

// ParseCursor checks a cursor against the sort it is used with, and returns the
// condition that selects the documents after its position. Anything else is
// reported as an error on the `after` parameter.
func ParseCursor(cursor string, sort []storage.SortField, fields map[string]registry.Field) (storage.Filter, error) {
	bad := func(format string, args ...any) (storage.Filter, error) {
		return nil, Errors{{Path: "after", Reason: fmt.Sprintf(format, args...)}}
	}
	if ok, name := Cursorable(sort, fields); !ok {
		return bad("can't be used with order_by %s: a cursor follows fields that hold one kind of scalar value, not arrays, objects or mixed types; use skip", name)
	}
	if len(cursor) > maxCursorBytes {
		return bad("is not a cursor of this API (too long)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return bad("is not a cursor: use the next_cursor of a previous page")
	}
	var p cursorPayload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || dec.More() {
		return bad("is not a cursor: use the next_cursor of a previous page")
	}
	if p.Order != sortSpec(sort) {
		return bad("belongs to another order_by (%q); a cursor only continues the list it came from", p.Order)
	}
	if len(p.Values) != len(sort) {
		return bad("is not a cursor: use the next_cursor of a previous page")
	}
	values := make([]any, len(sort))
	for i, s := range sort {
		f, _ := lookup(fields, s.Field)
		v, reason := cursorValue(f, p.Values[i])
		if reason != "" {
			return bad("holds a value that doesn't fit %s: %s", s.Field, reason)
		}
		values[i] = v
	}
	return after(sort, values), nil
}

// cursorValue checks one value of a cursor like a where value of the field and
// normalizes it the same way. null (or a missing field) fits any field.
func cursorValue(f field, v any) (any, string) {
	if v == nil {
		return nil, ""
	}
	switch f.kind {
	case kindDate:
		return dateValue(v)
	case kindID:
		if _, ok := v.(string); !ok {
			return nil, "must be a string"
		}
		return v, ""
	}
	switch v.(type) {
	case json.Number, string, bool:
	default:
		return nil, "must be a string, a number or a boolean"
	}
	if !accepts(f.Field, v) {
		return nil, "must be " + describe(f.Field)
	}
	return jsonnum.Normalize(v), ""
}

// after builds "positioned after values in this sort":
//
//	(s1 after v1) OR (s1 = v1 AND s2 after v2) OR …
//
// MongoDB orders a missing or null value before every other, so in ascending
// order what comes after a null is every non-null value, in descending order
// nothing does, and what comes after a value includes the nulls.
func after(sort []storage.SortField, values []any) storage.Filter {
	var terms storage.Or
	for i, s := range sort {
		var term storage.And
		for j := 0; j < i; j++ {
			term = append(term, equal(sort[j].Field, values[j]))
		}
		next := follows(s, values[i])
		if c, ok := next.(storage.Const); ok && !bool(c) {
			continue
		}
		terms = append(terms, append(term, next))
	}
	return storage.Simplify(terms)
}

func equal(field string, v any) storage.Filter {
	if v == nil {
		return storage.Condition{Field: field, Op: storage.OpIsNull, Value: true}
	}
	return storage.Condition{Field: field, Op: storage.OpEq, Value: v}
}

// follows selects the values that come after v in the field's direction.
func follows(s storage.SortField, v any) storage.Filter {
	switch {
	case v == nil && s.Desc:
		return storage.Const(false)
	case v == nil:
		return storage.Condition{Field: s.Field, Op: storage.OpIsNull, Value: false}
	case s.Desc:
		return storage.Or{
			storage.Condition{Field: s.Field, Op: storage.OpLt, Value: v},
			storage.Condition{Field: s.Field, Op: storage.OpIsNull, Value: true},
		}
	}
	return storage.Condition{Field: s.Field, Op: storage.OpGt, Value: v}
}
