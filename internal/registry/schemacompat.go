package registry

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// Kinds of compatibility finding (design/schema-versioning.md §6.2).
const (
	// CompatRefuses: version N refuses a write that version N-1 allowed (apps on N-1 get 400).
	CompatRefuses = "refuses"
	// CompatDrops: version N no longer declares a field N-1 declared (apps on N-1 read documents without it).
	CompatDrops = "drops"
	// CompatType: version N changes a field's type (both of the above).
	CompatType = "type"
	// CompatUnsure: a keyword the check can't compare soundly.
	CompatUnsure = "unsure"
)

// CompatFinding is one way a schema version breaks the one below it.
type CompatFinding struct {
	Collection string // <database>/<collection>
	From, To   int    // the versions compared: To is From+1 in the kept chain
	Kind       string
	Path       string // the field, dotted; "" for the document itself
	Rule       string // the keyword
	Message    string
	// Note is true when min_schema_version already turns the affected apps away: the finding is
	// what the developer chose, not a surprise.
	Note bool
}

func (f CompatFinding) String() string {
	level := "warning"
	if f.Note {
		level = "note"
	}
	where := f.Path
	if where == "" {
		where = "(document)"
	}
	return fmt.Sprintf("%s: %s %s → %s: %s: %s", level, f.Collection, SchemaVersionFile(f.From), SchemaVersionFile(f.To), where, f.Message)
}

// CompatibilityFindings compares each kept schema.vN.json of a versioned collection with the one
// below it, statically, and lists what a change breaks for the apps still on the lower version. It
// is a sound subset of JSON Schema comparison, not a full one: keywords it can't compare are
// reported as "can't tell, check by hand" rather than guessed. It never fails the build.
func (c *Collection) CompatibilityFindings() ([]CompatFinding, error) {
	if !c.Versioned || len(c.SchemaVersions) < 2 {
		return nil, nil
	}
	var out []CompatFinding
	for i := 1; i < len(c.SchemaVersions); i++ {
		from, to := c.SchemaVersions[i-1], c.SchemaVersions[i]
		lower, err := c.VersionSchema(from)
		if err != nil {
			return nil, err
		}
		upper, err := c.VersionSchema(to)
		if err != nil {
			return nil, err
		}
		cmp := &schemaComparer{lower: lower, upper: upper, active: map[string]bool{}}
		cmp.compare(lower, upper, "")
		slices.SortStableFunc(cmp.found, func(a, b compatRaw) int {
			if a.path != b.path {
				return strings.Compare(a.path, b.path)
			}
			return strings.Compare(a.rule, b.rule)
		})
		for _, r := range cmp.found {
			f := CompatFinding{Collection: c.Database + "/" + c.Name, From: from, To: to, Kind: r.kind, Path: r.path, Rule: r.rule, Message: r.msg}
			// The apps affected are those on version `from` (a refusal reaches them when they write; a
			// dropped or retyped field when they read), so min_schema_version >= to turns them away.
			// A dropped or retyped field is read only by apps that use it, which min_schema_version >=
			// from vouches for: the contract step of design §10.1.
			switch r.kind {
			case CompatDrops, CompatType:
				f.Note = c.MinSchemaVersion >= from
			default:
				f.Note = c.MinSchemaVersion >= to
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// CompatibilityFindings lists the findings of every versioned collection, in name order.
func (r *Registry) CompatibilityFindings() ([]CompatFinding, error) {
	var out []CompatFinding
	for _, db := range r.Databases() {
		for _, name := range sortedKeys(db.Collections) {
			f, err := db.Collections[name].CompatibilityFindings()
			if err != nil {
				return nil, err
			}
			out = append(out, f...)
		}
	}
	return out, nil
}

type compatRaw struct{ kind, path, rule, msg string }

type schemaComparer struct {
	lower, upper map[string]any // the documents $ref pointers resolve in
	found        []compatRaw
	depth        int             // how deep the walk is, against recursive $refs
	active       map[string]bool // the $ref pairs being compared
}

func (c *schemaComparer) add(kind, path, rule, format string, args ...any) {
	c.found = append(c.found, compatRaw{kind: kind, path: path, rule: rule, msg: fmt.Sprintf(format, args...)})
}

// unsureKeywords can't be compared soundly: when they differ the check says so.
var unsureKeywords = []string{"oneOf", "anyOf", "allOf", "not", "if", "then", "else", "patternProperties", "propertyNames", "dependentSchemas", "dependentRequired", "unevaluatedProperties", "unevaluatedItems", "contains", "prefixItems"}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// resolve follows a local $ref ("#/$defs/x") in doc; nil when it can't.
func resolveRef(doc map[string]any, ref string) map[string]any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return asMap(cur)
}

// compare walks two schemas of the same place.
func (c *schemaComparer) compare(lo, up map[string]any, path string) {
	// A $ref is followed on each side; a reference that can't be followed is "can't tell".
	var refs []string
	for _, side := range []struct {
		s   *map[string]any
		doc map[string]any
	}{{&lo, c.lower}, {&up, c.upper}} {
		if ref, ok := (*side.s)["$ref"].(string); ok {
			target := resolveRef(side.doc, ref)
			if target == nil {
				c.add(CompatUnsure, path, "$ref", "can't tell: %s can't be followed, check by hand", ref)
				return
			}
			*side.s = target
			refs = append(refs, ref)
		} else {
			refs = append(refs, "")
		}
	}
	if refs[0] != "" || refs[1] != "" { // a recursive schema is compared once, where it first appears
		key := refs[0] + "|" + refs[1]
		if c.active[key] {
			return
		}
		c.active[key] = true
		defer delete(c.active, key)
	}
	if jsonEqual(lo, up) {
		return
	}
	if c.depth++; c.depth > 24 { // a recursive $ref
		c.depth--
		return
	}
	defer func() { c.depth-- }()
	// Keywords this check can't compare.
	for _, kw := range unsureKeywords {
		if !jsonEqual(lo[kw], up[kw]) {
			c.add(CompatUnsure, path, kw, "can't tell: %q differs, check by hand", kw)
		}
	}

	lt, ut := typeSet(lo), typeSet(up)
	if !sameSet(lt, ut) {
		// Everything below a field of another type is another thing: say so once.
		c.add(CompatType, path, "type", "type changed from %s to %s", showTypes(lt), showTypes(ut))
		return
	}

	// enum / const
	if ue, ok := up["enum"].([]any); ok {
		if le, ok := lo["enum"].([]any); !ok {
			c.add(CompatRefuses, path, "enum", "enum added: only %s is accepted now", showValues(ue))
		} else if gone := missingValues(le, ue); len(gone) > 0 {
			c.add(CompatRefuses, path, "enum", "enum values removed: %s", showValues(gone))
		}
	}
	if uc, ok := up["const"]; ok && !jsonEqual(lo["const"], uc) {
		c.add(CompatRefuses, path, "const", "const is %s now", showValues([]any{uc}))
	}

	// numbers and sizes: narrower when the upper bound falls or the lower one rises
	c.bound(lo, up, path, "maxLength", false)
	c.bound(lo, up, path, "minLength", true)
	c.bound(lo, up, path, "maximum", false)
	c.bound(lo, up, path, "exclusiveMaximum", false)
	c.bound(lo, up, path, "minimum", true)
	c.bound(lo, up, path, "exclusiveMinimum", true)
	c.bound(lo, up, path, "maxItems", false)
	c.bound(lo, up, path, "minItems", true)
	c.bound(lo, up, path, "maxProperties", false)
	c.bound(lo, up, path, "minProperties", true)
	if up["uniqueItems"] == true && lo["uniqueItems"] != true {
		c.add(CompatRefuses, path, "uniqueItems", "uniqueItems is required now")
	}
	for _, kw := range []string{"pattern", "format", "multipleOf"} {
		if uv, ok := up[kw]; ok && !jsonEqual(lo[kw], uv) {
			if _, had := lo[kw]; had {
				c.add(CompatUnsure, path, kw, "can't tell: %q changed from %s to %s, check by hand", kw, show(lo[kw]), show(uv))
			} else {
				c.add(CompatRefuses, path, kw, "%s %s is required now", kw, show(uv))
			}
		}
	}

	// objects
	if ur := strList(up["required"]); len(ur) > 0 {
		lr := strList(lo["required"])
		for _, name := range ur {
			if !slices.Contains(lr, name) {
				c.add(CompatRefuses, compatJoin(path, name), "required", "%q is required now: a write without it is refused", name)
			}
		}
	}
	if ua, ok := up["additionalProperties"]; ok {
		la, had := lo["additionalProperties"]
		switch {
		case ua == false && la != false:
			c.add(CompatRefuses, path, "additionalProperties", "additionalProperties: false is set now: a write with a field that is not declared is refused")
		case asMap(ua) != nil && (!had || la == true):
			c.add(CompatRefuses, path, "additionalProperties", "additionalProperties is restricted to a schema now")
		case asMap(ua) != nil && asMap(la) != nil:
			c.compare(asMap(la), asMap(ua), compatJoin(path, "*"))
		}
	}
	lp, up2 := asMap(lo["properties"]), asMap(up["properties"])
	for _, name := range sortedKeys(lp) {
		child, ok := up2[name]
		if !ok {
			c.add(CompatDrops, compatJoin(path, name), "properties", "%q is no longer declared: apps on the lower version read documents without it", name)
			continue
		}
		c.compare(asMap(lp[name]), asMap(child), compatJoin(path, name))
	}
	// items
	if li, ui := asMap(lo["items"]), asMap(up["items"]); li != nil && ui != nil {
		c.compare(li, ui, path+"[]")
	} else if li == nil && ui != nil {
		c.add(CompatRefuses, path+"[]", "items", "the elements are constrained now")
	}
}

// bound compares a numeric bound: a smaller maximum, or a larger minimum, refuses writes the lower
// version accepted. A bound that appears is a restriction; one that goes away is not.
func (c *schemaComparer) bound(lo, up map[string]any, path, kw string, isMin bool) {
	uv, ok := up[kw]
	if !ok {
		return
	}
	un, uok := number(uv)
	if !uok {
		return
	}
	lv, had := lo[kw]
	if !had {
		c.add(CompatRefuses, path, kw, "%s %s is required now", kw, show(uv))
		return
	}
	ln, lok := number(lv)
	if !lok {
		return
	}
	if (isMin && un > ln) || (!isMin && un < ln) {
		c.add(CompatRefuses, path, kw, "%s went from %s to %s", kw, show(lv), show(uv))
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func compatJoin(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func jsonEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return reflect.DeepEqual(ja, jb)
}

func strList(v any) []string {
	var out []string
	for _, e := range asSlice(v) {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// typeSet is the set of JSON types a schema accepts; nil means any. "integer" counts as part of
// "number" when the other side says number.
func typeSet(s map[string]any) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		out := strList(t)
		slices.Sort(out)
		return out
	}
	return nil
}

func sameSet(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func showTypes(t []string) string {
	if t == nil {
		return "any"
	}
	return strings.Join(t, " | ")
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func showValues(vs []any) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = show(v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// missingValues are the values of from that to doesn't hold.
func missingValues(from, to []any) []any {
	var gone []any
	for _, v := range from {
		found := false
		for _, w := range to {
			if jsonEqual(v, w) {
				found = true
				break
			}
		}
		if !found {
			gone = append(gone, v)
		}
	}
	return gone
}
