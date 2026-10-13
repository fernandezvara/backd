package typegen

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// converter turns JSON Schema documents (decoded with json.Number) into TypeScript types.
type converter struct {
	// refs names the type a local $ref points to, by pointer; filled lazily by ref.
	refs map[string]string
	// root is the document the pointers resolve in.
	root map[string]any
	// prefix names the types generated for $defs, e.g. "MainNotes".
	prefix string
	// defs collects the named types generated for $refs, by name.
	defs map[string]string
	// resolving guards recursive references.
	warn func(format string, args ...any)
	// where is what a warning is about, e.g. "main/notes schema.json".
	where string
}

func newConverter(root map[string]any, prefix, where string, warn func(string, ...any)) *converter {
	return &converter{refs: map[string]string{}, root: root, prefix: prefix, defs: map[string]string{}, warn: warn, where: where}
}

// unsupported keywords make a schema `unknown`: they shape what is valid in ways TypeScript
// can't express.
var unsupported = []string{"not", "if", "then", "else", "patternProperties", "propertyNames", "dependentSchemas", "unevaluatedProperties", "unevaluatedItems", "contains"}

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

// literal renders a JSON value as a TypeScript literal type.
func literal(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case nil:
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "unknown"
	}
	return string(b)
}

// typeOf renders the TypeScript type of the schema. path is only for warnings.
func (c *converter) typeOf(s any, path string) string {
	switch t := s.(type) {
	case bool:
		if t {
			return "unknown"
		}
		return "never"
	case map[string]any:
		return c.object(t, path)
	}
	return "unknown"
}

func (c *converter) object(s map[string]any, path string) string {
	for _, k := range unsupported {
		if _, ok := s[k]; ok {
			c.warn("%s%s: keyword %q has no TypeScript equivalent; typed unknown", c.where, path, k)
			return "unknown"
		}
	}
	if t, ok := str(s["x-backd-ts"]); ok {
		return t
	}
	if ref, ok := str(s["$ref"]); ok {
		return c.ref(ref, path)
	}
	if v, ok := s["const"]; ok {
		return literal(v)
	}
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		parts := make([]string, len(e))
		for i, v := range e {
			parts[i] = literal(v)
		}
		return strings.Join(unique(parts), " | ")
	}
	var parts []string
	for _, kw := range []string{"oneOf", "anyOf"} {
		if arr, ok := s[kw].([]any); ok && len(arr) > 0 {
			var u []string
			for i, e := range arr {
				u = append(u, c.typeOf(e, fmt.Sprintf("%s/%s/%d", path, kw, i)))
			}
			parts = append(parts, strings.Join(unique(u), " | "))
		}
	}
	if arr, ok := s["allOf"].([]any); ok && len(arr) > 0 {
		var u []string
		for i, e := range arr {
			u = append(u, c.typeOf(e, fmt.Sprintf("%s/allOf/%d", path, i)))
		}
		parts = append(parts, "("+strings.Join(u, " & ")+")")
	}
	if len(parts) > 0 && s["type"] == nil && s["properties"] == nil {
		if len(parts) == 1 {
			return parts[0]
		}
		return "(" + strings.Join(parts, ") & (") + ")"
	}

	var types []string
	switch t := s["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, e := range t {
			if n, ok := e.(string); ok {
				types = append(types, n)
			}
		}
	}
	if len(types) == 0 {
		switch {
		case s["properties"] != nil, s["additionalProperties"] != nil, s["required"] != nil:
			types = []string{"object"}
		case s["items"] != nil, s["prefixItems"] != nil:
			types = []string{"array"}
		case s["format"] != nil && len(parts) == 0:
			// a bare format says nothing about the type
		}
	}
	if len(types) == 0 {
		if len(parts) > 0 {
			return strings.Join(parts, " & ")
		}
		return "unknown"
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, c.single(s, t, path))
	}
	res := strings.Join(unique(out), " | ")
	if len(parts) > 0 {
		res = "(" + res + ") & " + strings.Join(parts, " & ")
	}
	return res
}

func (c *converter) single(s map[string]any, t, path string) string {
	switch t {
	case "string":
		if f, _ := str(s["format"]); f == "date-time" {
			return "DateTimeString"
		}
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "null":
		return "null"
	case "array":
		if pre, ok := s["prefixItems"].([]any); ok {
			elems := make([]string, len(pre))
			for i, e := range pre {
				elems[i] = c.typeOf(e, fmt.Sprintf("%s/prefixItems/%d", path, i))
			}
			rest := ""
			if it, ok := s["items"]; ok && it != false {
				rest = ", ..." + wrap(c.typeOf(it, path+"/items")) + "[]"
			}
			return "[" + strings.Join(elems, ", ") + rest + "]"
		}
		if it, ok := s["items"]; ok {
			return wrap(c.typeOf(it, path+"/items")) + "[]"
		}
		return "unknown[]"
	case "object":
		return c.objectType(s, path)
	}
	return "unknown"
}

// wrap parenthesizes unions and intersections so a suffix like [] applies to all of it.
func wrap(t string) string {
	if strings.ContainsAny(t, " |&") && !strings.HasPrefix(t, "{") {
		return "(" + t + ")"
	}
	return t
}

func (c *converter) objectType(s map[string]any, path string) string {
	props, _ := s["properties"].(map[string]any)
	required := map[string]bool{}
	if r, ok := s["required"].([]any); ok {
		for _, n := range r {
			if name, ok := n.(string); ok {
				required[name] = true
			}
		}
	}
	if len(props) == 0 {
		switch ap := s["additionalProperties"].(type) {
		case map[string]any:
			return "Record<string, " + c.typeOf(ap, path+"/additionalProperties") + ">"
		}
		if len(required) == 0 {
			if ap, ok := s["additionalProperties"].(bool); ok && !ap {
				return "Record<string, never>"
			}
			return "Record<string, unknown>"
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("{ ")
	for _, n := range names {
		opt := "?"
		if required[n] {
			opt = ""
		}
		fmt.Fprintf(&b, "%s%s: %s; ", member(n), opt, c.typeOf(props[n], path+"/properties/"+n))
	}
	for n := range required {
		if _, ok := props[n]; !ok {
			fmt.Fprintf(&b, "%s: unknown; ", member(n))
		}
	}
	closed := false
	if ap, ok := s["additionalProperties"].(bool); ok && !ap {
		closed = true
	}
	if !closed {
		b.WriteString("[key: string]: unknown; ")
	}
	b.WriteString("}")
	return b.String()
}

var identStart = func(r rune) bool { return r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// member renders a property name, quoted when it isn't an identifier.
func member(n string) string {
	ok := n != ""
	for i, r := range n {
		if !(identStart(r) || (i > 0 && r >= '0' && r <= '9')) {
			ok = false
		}
	}
	if ok {
		return n
	}
	b, _ := json.Marshal(n)
	return string(b)
}

// ref names a local $ref's type, generating it on first use.
func (c *converter) ref(ref, path string) string {
	if !strings.HasPrefix(ref, "#") {
		c.warn("%s%s: $ref %q is not local; typed unknown", c.where, path, ref)
		return "unknown"
	}
	if name, ok := c.refs[ref]; ok {
		return name
	}
	target := resolvePointer(c.root, strings.TrimPrefix(ref, "#"))
	if target == nil {
		c.warn("%s%s: $ref %q doesn't resolve; typed unknown", c.where, path, ref)
		return "unknown"
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	name := c.prefix + "_" + pascal(last)
	if ref == "#" {
		name = c.prefix + "_Self"
	}
	for i := 2; c.defs[name] != "" || slices.Contains(valuesOf(c.refs), name); i++ {
		name = fmt.Sprintf("%s_%s%d", c.prefix, pascal(last), i)
	}
	c.refs[ref] = name
	c.defs[name] = "unknown" // placeholder while a recursive reference resolves to the name
	c.defs[name] = c.typeOf(target, path)
	return name
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func resolvePointer(root map[string]any, ptr string) any {
	var cur any = root
	if ptr == "" {
		return cur
	}
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
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

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// pascal turns "order_items" or "main-db" into "OrderItems" and "MainDb".
func pascal(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			if up {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			up = false
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			up = false
		default:
			up = true
		}
	}
	return b.String()
}

// schemaAt finds the schema of a dot path of `properties`, or nil.
func schemaAt(root map[string]any, path string) any {
	var cur any = root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		props, _ := m["properties"].(map[string]any)
		if cur, ok = props[part]; !ok {
			return nil
		}
	}
	return cur
}
