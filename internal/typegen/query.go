package typegen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

type fieldKind int

const (
	kindAny fieldKind = iota
	kindString
	kindNumber
	kindBool
	kindDate
	kindOpaque // an object: not queryable as a whole
)

// queryField is one field a where can name.
type queryField struct {
	path string
	kind fieldKind
	// value is the TypeScript type of a value to compare with, null excluded.
	value    string
	nullable bool
}

// systemQueryFields mirror internal/query's systemFields.
var systemQueryFields = []queryField{
	{path: "id", kind: kindString, value: "string"},
	{path: "_meta.created_at", kind: kindDate, value: "DateTimeString"},
	{path: "_meta.updated_at", kind: kindDate, value: "DateTimeString"},
	{path: "_meta.version", kind: kindNumber, value: "number"},
	{path: "_meta.owner", kind: kindString, value: "string", nullable: true},
	{path: "_meta.created_by", kind: kindString, value: "string"},
	{path: "_meta.updated_by", kind: kindString, value: "string"},
}

var softDeleteQueryFields = []queryField{
	{path: registry.MetaDeletedAt, kind: kindDate, value: "DateTimeString"},
	{path: registry.MetaDeletedBy, kind: kindString, value: "string"},
	{path: registry.MetaPurgeAt, kind: kindDate, value: "DateTimeString"},
}

func fieldOf(path string, f registry.Field, schema any) queryField {
	q := queryField{path: path, nullable: f.Has("null") && len(f.Types) > 0}
	if len(f.Types) == 0 {
		if lits, kind := enumOf(schema); lits != "" {
			q.kind, q.value = kind, lits
			return q
		}
		if m, ok := schema.(map[string]any); ok && (m["type"] == "object" || m["properties"] != nil) {
			q.kind = kindOpaque
			return q
		}
	}
	if f.Date {
		q.kind, q.value = kindDate, "DateTimeString"
		return q
	}
	types := f.Types
	if slices.Contains(types, "array") {
		types = f.ItemTypes // a scalar compares with the elements
	}
	var vals []string
	kind := kindAny
	set := func(k fieldKind, v string) {
		if kind == kindAny || kind == k {
			kind = k
		} else {
			kind = kindAny
		}
		vals = append(vals, v)
	}
	for _, t := range types {
		switch t {
		case "string":
			set(kindString, "string")
		case "integer", "number":
			set(kindNumber, "number")
		case "boolean":
			set(kindBool, "boolean")
		case "null":
		default:
			set(kindOpaque, "unknown")
		}
	}
	if len(f.Types) == 0 || len(vals) == 0 {
		kind, vals = kindAny, []string{"unknown"}
	}
	if slices.Contains(vals, "unknown") {
		vals = []string{"unknown"}
	}
	q.kind = kind
	q.value = strings.Join(unique(vals), " | ")
	return q
}

// query renders the where and order_by types of a collection.
func (g *gen) query(c *registry.Collection) (where, order string) {
	fields := append([]queryField{}, systemQueryFields...)
	if c.SoftDelete != nil {
		fields = append(fields, softDeleteQueryFields...)
	}
	for _, p := range slices.Sorted(keys(c.Fields)) {
		fields = append(fields, fieldOf(p, c.Fields[p], schemaFor(c.RawSchema, p)))
	}
	var b strings.Builder
	b.WriteString("{\n")
	var orderNames []string
	for _, f := range fields {
		if f.kind == kindOpaque {
			continue
		}
		orderNames = append(orderNames, f.path)
		v := f.value
		eq := v
		if f.nullable {
			eq = v + " | null"
		}
		ops := fmt.Sprintf("Ops<%s> & NullOps", wrap(eq))
		switch f.kind {
		case kindString:
			ops += fmt.Sprintf(" & RangeOps<%s> & TextOps", v)
		case kindNumber, kindDate:
			ops += fmt.Sprintf(" & RangeOps<%s>", v)
		}
		fmt.Fprintf(&b, "  %s?: %s | (%s);\n", member(f.path), wrap(eq), ops)
	}
	b.WriteString("  $or?: Array<this>;\n  $and?: Array<this>;\n}")
	where = b.String()
	// A recursive alias is named by the caller; `this` isn't valid in a type alias, so it is
	// replaced there with the name.
	var ob []string
	for _, n := range orderNames {
		ob = append(ob, literal(n), literal("-"+n))
	}
	order = strings.Join(ob, " | ")
	return
}

// enumOf renders an enum or const schema as literal types, with the kind of its values.
func enumOf(s any) (string, fieldKind) {
	m, ok := s.(map[string]any)
	if !ok {
		return "", kindAny
	}
	var vals []any
	if e, ok := m["enum"].([]any); ok {
		vals = e
	} else if c, ok := m["const"]; ok {
		vals = []any{c}
	}
	if len(vals) == 0 {
		return "", kindAny
	}
	kind := fieldKind(-1)
	var lits []string
	for _, v := range vals {
		var k fieldKind
		switch v.(type) {
		case string:
			k = kindString
		case bool:
			k = kindBool
		case nil:
			continue
		default:
			k = kindNumber
		}
		if kind != -1 && kind != k {
			kind = kindAny
		} else if kind == -1 {
			kind = k
		}
		lits = append(lits, literal(v))
	}
	if len(lits) == 0 {
		return "", kindAny
	}
	return strings.Join(unique(lits), " | "), kind
}

// schemaFor finds the schema of a dot path of a collection, through properties, array
// items and local $ref.
func schemaFor(root map[string]any, path string) any {
	var cur any = root
	for _, part := range strings.Split(path, ".") {
		cur = deref(root, cur)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if it, ok := m["items"]; ok && m["properties"] == nil {
			cur = deref(root, it)
			m, ok = cur.(map[string]any)
			if !ok {
				return nil
			}
		}
		props, _ := m["properties"].(map[string]any)
		if cur, ok = props[part]; !ok {
			return nil
		}
	}
	return deref(root, cur)
}

func deref(root map[string]any, s any) any {
	for range 8 {
		m, ok := s.(map[string]any)
		if !ok {
			return s
		}
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#") {
			return s
		}
		t := resolvePointer(root, strings.TrimPrefix(ref, "#"))
		if t == nil {
			return s
		}
		s = t
	}
	return s
}
