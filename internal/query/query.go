// Package query parses and checks the list query language (where,
// order_by) against a collection's schema, producing backend-neutral
// storage conditions. Client input never reaches the database as-is:
// only the operators below exist, and every field and value is checked.
package query

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Limits on query complexity.
const (
	MaxWhereBytes    = 4096 // size of the raw where parameter
	MaxConditions    = 32   // operator clauses in where
	MaxTextLen       = 256  // value length for text operators
	MaxLikeWildcards = 4    // % and _ in a $like / $ilike pattern
	MaxSortFields    = 8    // fields in order_by
	MaxGroupDepth    = 4    // nesting of $or / $and groups
)

// Error names one problem in a query.
type Error struct {
	Path   string
	Reason string
}

// Errors is returned when a query is invalid; it lists every problem found.
type Errors []Error

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, x := range e {
		parts[i] = x.Path + ": " + x.Reason
	}
	return "invalid query: " + strings.Join(parts, "; ")
}

type fieldKind int

const (
	kindSchema fieldKind = iota
	kindID
	kindDate
)

type field struct {
	registry.Field
	kind fieldKind
}

// systemFields are queryable fields that are not in user schemas.
var systemFields = map[string]field{
	"id":               {Field: registry.Field{Types: []string{"string"}}, kind: kindID},
	"_meta.created_at": {kind: kindDate},
	"_meta.updated_at": {kind: kindDate},
	"_meta.version":    {Field: registry.Field{Types: []string{"integer"}}},
	"_meta.owner":      {Field: registry.Field{Types: []string{"string", "null"}}},
	"_meta.created_by": {Field: registry.Field{Types: []string{"string"}}},
	"_meta.updated_by": {Field: registry.Field{Types: []string{"string"}}},
}

func lookup(fields map[string]registry.Field, path string) (field, bool) {
	if f, ok := systemFields[path]; ok {
		return f, true
	}
	f, ok := fields[path]
	return field{Field: f}, ok
}

var textOps = map[string]storage.Operator{
	"$like": storage.OpLike, "$ilike": storage.OpILike,
	"$startsWith": storage.OpStartsWith, "$istartsWith": storage.OpIStartsWith,
	"$endsWith": storage.OpEndsWith, "$iendsWith": storage.OpIEndsWith,
	"$contains": storage.OpContains, "$icontains": storage.OpIContains,
}

var rangeOps = map[string]storage.Operator{
	"$gt": storage.OpGt, "$gte": storage.OpGte, "$lt": storage.OpLt, "$lte": storage.OpLte,
}

// ParseWhere parses the where parameter (a JSON object) into a filter
// tree: the object's keys combine with AND, and "$or" / "$and" take an
// array of where objects. An empty string or object means no filter (nil).
// Keys are processed in sorted order, so the result is deterministic.
func ParseWhere(where string, fields map[string]registry.Field) (storage.Filter, error) {
	if where == "" {
		return nil, nil
	}
	if len(where) > MaxWhereBytes {
		return nil, Errors{{Path: "where", Reason: fmt.Sprintf("must be at most %d bytes", MaxWhereBytes)}}
	}
	dec := json.NewDecoder(strings.NewReader(where))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil || dec.More() {
		return nil, Errors{{Path: "where", Reason: "must be a valid JSON object"}}
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, Errors{{Path: "where", Reason: "must be a valid JSON object"}}
	}

	p := &parser{fields: fields}
	f := p.object(obj, 0)
	if p.count > MaxConditions {
		p.errs = append(p.errs, Error{Path: "where", Reason: fmt.Sprintf("must have at most %d conditions", MaxConditions)})
	}
	if len(p.errs) > 0 {
		return nil, p.errs
	}
	if len(f) == 0 {
		return nil, nil
	}
	return f, nil
}

type parser struct {
	fields map[string]registry.Field
	cur    *[]storage.Filter // the group conditions are added to
	loc    string            // error path prefix inside groups, e.g. "$or.1."
	count  int               // conditions so far, across all groups
	errs   Errors
}

func (p *parser) fail(path, format string, args ...any) {
	at := strings.TrimSuffix("where."+p.loc+path, ".")
	p.errs = append(p.errs, Error{Path: at, Reason: fmt.Sprintf(format, args...)})
}

// object parses one where object: its keys combine with AND.
func (p *parser) object(obj map[string]any, depth int) storage.And {
	parts := storage.And{}
	saved := p.cur
	p.cur = (*[]storage.Filter)(&parts)
	defer func() { p.cur = saved }()
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if k == "$or" || k == "$and" {
			p.group(k, obj[k], depth)
			continue
		}
		p.field(k, obj[k])
	}
	return parts
}

// group parses "$or" or "$and": a non-empty array of where objects.
func (p *parser) group(op string, v any, depth int) {
	if depth >= MaxGroupDepth {
		p.fail(op, "$or and $and can be nested at most %d levels deep", MaxGroupDepth)
		return
	}
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		p.fail(op, "must be a non-empty array of where objects")
		return
	}
	loc := p.loc
	defer func() { p.loc = loc }()
	items := make([]storage.Filter, 0, len(arr))
	for i, e := range arr {
		p.loc = loc + op + "." + strconv.Itoa(i) + "."
		obj, ok := e.(map[string]any)
		if !ok || len(obj) == 0 {
			p.fail("", "must be a non-empty where object")
			continue
		}
		items = append(items, p.object(obj, depth+1))
	}
	p.loc = loc
	if op == "$or" {
		*p.cur = append(*p.cur, storage.Or(items))
	} else {
		*p.cur = append(*p.cur, storage.And(items))
	}
}

func (p *parser) field(path string, v any) {
	if strings.HasPrefix(path, "$") {
		p.fail(path, "is not supported: the only logical operators are $or and $and")
		return
	}
	f, ok := lookup(p.fields, path)
	if !ok {
		p.fail(path, "unknown field")
		return
	}

	ops, isObj := v.(map[string]any)
	if !isObj || !hasOperatorKeys(ops) {
		p.operator(path, f, "$eq", v) // bare value: $eq shorthand
		return
	}
	names := make([]string, 0, len(ops))
	for k := range ops {
		if !strings.HasPrefix(k, "$") {
			p.fail(path, "cannot mix operators and field names")
			return
		}
		names = append(names, k)
	}
	slices.Sort(names)
	for _, op := range names {
		p.operator(path, f, op, ops[op])
	}
}

func hasOperatorKeys(m map[string]any) bool {
	for k := range m {
		if strings.HasPrefix(k, "$") {
			return true
		}
	}
	return false
}

func (p *parser) add(path string, op storage.Operator, v any) {
	p.count++
	*p.cur = append(*p.cur, storage.Condition{Field: path, Op: op, Value: v})
}

func (p *parser) operator(path string, f field, op string, v any) {
	at := path + "." + op
	switch {
	case op == "$eq" || op == "$ne":
		val, err := equalityValue(f, v)
		if err != "" {
			p.fail(at, "%s", err)
			return
		}
		p.add(path, storage.Operator(op), val)

	case rangeOps[op] != "":
		val, err := rangeValue(f, v)
		if err != "" {
			p.fail(at, "%s", err)
			return
		}
		p.add(path, rangeOps[op], val)

	case op == "$between":
		arr, ok := v.([]any)
		if !ok || len(arr) != 2 {
			p.fail(at, "must be an array of two values [min, max]")
			return
		}
		lo, err1 := rangeValue(f, arr[0])
		hi, err2 := rangeValue(f, arr[1])
		if err := cmpOr(err1, err2); err != "" {
			p.fail(at, "%s", err)
			return
		}
		p.add(path, storage.OpBetween, []any{lo, hi})

	case op == "$in" || op == "$nin":
		arr, ok := v.([]any)
		if !ok {
			p.fail(at, "must be an array")
			return
		}
		vals := make([]any, len(arr))
		for i, e := range arr {
			val, err := equalityValue(f, e)
			if err != "" {
				p.fail(at+"."+strconv.Itoa(i), "%s", err)
				return
			}
			vals[i] = val
		}
		p.add(path, storage.Operator(op), vals)

	case op == "$isNull" || op == "$notNull":
		b, ok := v.(bool)
		if !ok {
			p.fail(at, "must be true or false")
			return
		}
		if op == "$notNull" {
			b = !b
		}
		p.add(path, storage.OpIsNull, b)

	case textOps[op] != "":
		s, ok := v.(string)
		switch {
		case f.kind == kindDate || !f.Has("string"):
			p.fail(at, "text operators apply only to string fields")
		case !ok:
			p.fail(at, "must be a string")
		case len(s) > MaxTextLen:
			p.fail(at, "must be at most %d characters", MaxTextLen)
		case (op == "$like" || op == "$ilike") && strings.Count(s, "%")+strings.Count(s, "_") > MaxLikeWildcards:
			p.fail(at, "must have at most %d wildcards", MaxLikeWildcards)
		default:
			p.add(path, textOps[op], s)
		}

	default:
		p.fail(at, "unknown operator")
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// equalityValue checks a value for $eq/$ne/$in/$nin and normalizes it.
func equalityValue(f field, v any) (any, string) {
	switch f.kind {
	case kindDate:
		return dateValue(v)
	case kindID:
		if _, ok := v.(string); !ok {
			return nil, "must be a string"
		}
		return v, ""
	}
	if v == nil {
		if f.Has("null") {
			return nil, ""
		}
		return nil, "null is not a valid value for this field; use $isNull"
	}
	if hasDollarKey(v) {
		return nil, "object keys must not start with '$'"
	}
	if !accepts(f.Field, v) {
		return nil, "must be " + describe(f.Field)
	}
	return jsonnum.Normalize(v), ""
}

// rangeValue checks a value for $gt/$gte/$lt/$lte/$between.
func rangeValue(f field, v any) (any, string) {
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
	case json.Number, string:
	default:
		return nil, "must be a number or a string"
	}
	if !accepts(f.Field, v) {
		return nil, "must be " + describe(f.Field)
	}
	return jsonnum.Normalize(v), ""
}

func dateValue(v any) (any, string) {
	s, ok := v.(string)
	if !ok {
		return nil, "must be an RFC3339 timestamp string"
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, "must be an RFC3339 timestamp"
	}
	return t.UTC(), ""
}

// accepts reports whether v matches the field's declared types. For array
// fields a scalar is accepted when it matches the item type, following
// MongoDB's element-matching semantics.
func accepts(f registry.Field, v any) bool {
	if len(f.Types) == 0 {
		return true
	}
	if matchesTypes(f.Types, v) {
		return true
	}
	if slices.Contains(f.Types, "array") {
		if _, isArr := v.([]any); !isArr {
			return len(f.ItemTypes) == 0 || matchesTypes(f.ItemTypes, v)
		}
	}
	return false
}

func matchesTypes(types []string, v any) bool {
	for _, t := range types {
		if jsonType(v, t) {
			return true
		}
	}
	return false
}

func jsonType(v any, t string) bool {
	switch x := v.(type) {
	case string:
		return t == "string"
	case bool:
		return t == "boolean"
	case nil:
		return t == "null"
	case map[string]any:
		return t == "object"
	case []any:
		return t == "array"
	case json.Number:
		if t == "number" {
			return true
		}
		if t == "integer" {
			if _, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
				return true
			}
			fl, err := x.Float64()
			return err == nil && fl == math.Trunc(fl) && !math.IsInf(fl, 0)
		}
	}
	return false
}

func describe(f registry.Field) string {
	types := slices.Clone(f.Types)
	if slices.Contains(types, "array") && len(f.ItemTypes) > 0 {
		types = append(types, "an item of type "+strings.Join(f.ItemTypes, " or "))
	}
	return "of type " + strings.Join(types, " or ")
}

func hasDollarKey(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if strings.HasPrefix(k, "$") || hasDollarKey(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasDollarKey(e) {
				return true
			}
		}
	}
	return false
}

// ParseOrderBy parses a comma-separated field list with an optional "-"
// prefix for descending order. An "id" tiebreaker is appended unless id is
// already present, so pagination is stable. Empty means id ascending.
func ParseOrderBy(s string, fields map[string]registry.Field) ([]storage.SortField, error) {
	var out []storage.SortField
	var errs Errors
	seen := map[string]bool{}
	if s != "" {
		parts := strings.Split(s, ",")
		if len(parts) > MaxSortFields {
			return nil, Errors{{Path: "order_by", Reason: fmt.Sprintf("must have at most %d fields", MaxSortFields)}}
		}
		for _, part := range parts {
			sf := storage.SortField{Field: strings.TrimSpace(part)}
			if strings.HasPrefix(sf.Field, "-") {
				sf.Desc = true
				sf.Field = sf.Field[1:]
			}
			switch _, ok := lookup(fields, sf.Field); {
			case sf.Field == "":
				errs = append(errs, Error{Path: "order_by", Reason: "empty field name"})
			case !ok:
				errs = append(errs, Error{Path: "order_by." + sf.Field, Reason: "unknown field"})
			case seen[sf.Field]:
				errs = append(errs, Error{Path: "order_by." + sf.Field, Reason: "duplicate field"})
			default:
				seen[sf.Field] = true
				out = append(out, sf)
			}
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if !seen["id"] {
		out = append(out, storage.SortField{Field: "id"})
	}
	return out, nil
}

// ParseCount parses the count parameter ("", "true" or "false").
func ParseCount(s string) (bool, error) {
	switch s {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, Errors{{Path: "count", Reason: "must be true or false"}}
}
