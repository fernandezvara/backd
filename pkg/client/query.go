package client

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Where is a filter in backd's query language: field names to a value (equality) or to the
// operators built by [Eq], [Gt], [In] and the others; "$or" and "$and" take lists of filters. The
// client only serializes it; the server checks it against the collection.
//
//	client.Where{"status": "active", "price": client.Between(10, 20)}
type Where map[string]any

// Eq, Ne, Gt, Gte, Lt, Lte, In, Nin, Between, IsNull, NotNull, Like, ILike, StartsWith,
// IStartsWith, EndsWith, IEndsWith, Contains and IContains build the operators of a field's condition.
func Eq(v any) map[string]any  { return map[string]any{"$eq": v} }
func Ne(v any) map[string]any  { return map[string]any{"$ne": v} }
func Gt(v any) map[string]any  { return map[string]any{"$gt": v} }
func Gte(v any) map[string]any { return map[string]any{"$gte": v} }
func Lt(v any) map[string]any  { return map[string]any{"$lt": v} }
func Lte(v any) map[string]any { return map[string]any{"$lte": v} }

func In(vs ...any) map[string]any  { return map[string]any{"$in": vs} }
func Nin(vs ...any) map[string]any { return map[string]any{"$nin": vs} }

// Between matches values from lo to hi, both included.
func Between(lo, hi any) map[string]any { return map[string]any{"$between": []any{lo, hi}} }

func IsNull() map[string]any  { return map[string]any{"$isNull": true} }
func NotNull() map[string]any { return map[string]any{"$notNull": true} }

func Like(s string) map[string]any        { return map[string]any{"$like": s} }
func ILike(s string) map[string]any       { return map[string]any{"$ilike": s} }
func StartsWith(s string) map[string]any  { return map[string]any{"$startsWith": s} }
func IStartsWith(s string) map[string]any { return map[string]any{"$istartsWith": s} }
func EndsWith(s string) map[string]any    { return map[string]any{"$endsWith": s} }
func IEndsWith(s string) map[string]any   { return map[string]any{"$iendsWith": s} }
func Contains(s string) map[string]any    { return map[string]any{"$contains": s} }
func IContains(s string) map[string]any   { return map[string]any{"$icontains": s} }

// Or matches documents that match any of the filters.
func Or(w ...Where) Where { return Where{"$or": w} }

// And matches documents that match all of the filters.
func And(w ...Where) Where { return Where{"$and": w} }

// Query is what a list reads: the filter, the order and the page. The zero value lists the first page.
type Query struct {
	// Where is a [Where], any value that encodes to a JSON object, or the JSON text of one.
	Where any
	// OrderBy lists fields, with a "-" prefix for descending: "-_meta.created_at".
	OrderBy []string
	// Limit is the page size, 1 to 100; the server's default (20) when 0.
	Limit int
	Skip  int
	// After is the NextCursor of the previous page: it continues the list after it.
	After string
	// Count also returns the Total of the whole list.
	Count bool
	// Deleted is "only" or "include" in a collection with soft_delete.
	Deleted string
	// FileLinks adds url and expires_at to every file of every document.
	FileLinks bool
	// Expand names the references to return in _expanded; build it with [Expand].
	Expand string
	// Language is the language a $search is stemmed in (a locale code such as "es").
	Language string
}

// whereJSON is the where parameter: the text as it is, or the JSON encoding of the filter.
func whereJSON(w any) (string, error) {
	switch v := w.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case json.RawMessage:
		return string(v), nil
	}
	b, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("client: the where can't be encoded: %w", err)
	}
	return string(b), nil
}

func (q Query) values() (url.Values, error) {
	v := url.Values{}
	w, err := whereJSON(q.Where)
	if err != nil {
		return nil, err
	}
	if w != "" {
		v.Set("where", w)
	}
	if len(q.OrderBy) > 0 {
		v.Set("order_by", strings.Join(q.OrderBy, ","))
	}
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Skip != 0 {
		v.Set("skip", strconv.Itoa(q.Skip))
	}
	if q.After != "" {
		v.Set("after", q.After)
	}
	if q.Count {
		v.Set("count", "true")
	}
	if q.Deleted != "" {
		v.Set("deleted", q.Deleted)
	}
	if q.FileLinks {
		v.Set("file_links", "true")
	}
	if q.Expand != "" {
		v.Set("expand", q.Expand)
	}
	if q.Language != "" {
		v.Set("language", q.Language)
	}
	return v, nil
}

var expandName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// ExpandPart is one reference of an expand, with the fields to return of it.
type ExpandPart struct {
	Name   string
	Fields []string
}

// Ref names a reference to expand (or "name.nested" for a second level) and, optionally, the
// fields to return of it: Ref("author", "name", "avatar").
func Ref(name string, fields ...string) ExpandPart { return ExpandPart{Name: name, Fields: fields} }

// Expand builds the Expand of a [Query] and of a read: the references to return next to the
// result, in _expanded, with the fields of each: "author(name,avatar),tags". Nothing is expanded
// unless you pass this.
func Expand(parts ...ExpandPart) (string, error) {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if !expandName.MatchString(p.Name) {
			return "", fmt.Errorf("client: %q is not a reference name to expand", p.Name)
		}
		if p.Fields == nil {
			out = append(out, p.Name)
			continue
		}
		if len(p.Fields) == 0 {
			return "", fmt.Errorf("client: the fields of %s must be a non-empty list of field names", p.Name)
		}
		for _, f := range p.Fields {
			if !expandName.MatchString(f) {
				return "", fmt.Errorf("client: %q is not a field name (in the fields of %s)", f, p.Name)
			}
		}
		out = append(out, p.Name+"("+strings.Join(p.Fields, ",")+")")
	}
	return strings.Join(out, ","), nil
}

// MustExpand is [Expand] for names written in the program: it panics on a bad one.
func MustExpand(parts ...ExpandPart) string {
	s, err := Expand(parts...)
	if err != nil {
		panic(err)
	}
	return s
}
