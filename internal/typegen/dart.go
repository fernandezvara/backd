package typegen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// DartTypesFile is the file `backd types --lang dart` writes.
const DartTypesFile = "types.dart"

// dartGen renders a realm's types as Dart: classes with fromJson/toJson for the
// schemas, TypedCollection wrappers over the client's collections, and a
// <Realm>Realm class an extension on BackdClient<Realm> hands out.
type dartGen struct {
	warnings []string
	used     map[string]string // declared name → what declared it (collisions)
	decls    strings.Builder
	// classes records the emitted names that are classes (against typedefs of
	// Map<String, Object?>, which need no decode).
	classes map[string]bool
	// enums records the emitted names that are Dart enums.
	enums map[string]bool
	// emitted guards class emission by name so a shared hint emits once.
	emitted map[string]bool
	// collPrefix is each collection's claimed name prefix, by "db/collection".
	collPrefix map[string]string
	// collTypes remembers each collection's emitted doc/input/create names.
	collTypes map[string][3]string
	// fnTypes remembers each function's emitted input/output names.
	fnTypes map[string][2]string
	// partials are the all-nullable doc classes a reference expansion needs,
	// "db/collection" → its name ("" until emitted).
	partials map[string]string
	// dbNames holds each database wrapper's emitted class name.
	dbNames map[string]string
	// configName holds the app-config wrapper's emitted class name.
	configName string
	rl         *registry.Realm
}

func newDartGen() *dartGen {
	return &dartGen{
		used:       map[string]string{},
		classes:    map[string]bool{},
		enums:      map[string]bool{},
		emitted:    map[string]bool{},
		collPrefix: map[string]string{},
		collTypes:  map[string][3]string{},
		fnTypes:    map[string][2]string{},
		partials:   map[string]string{},
		dbNames:    map[string]string{},
	}
}

func (g *dartGen) warn(format string, args ...any) {
	m := fmt.Sprintf(format, args...)
	if !slices.Contains(g.warnings, m) {
		g.warnings = append(g.warnings, m)
	}
}

// claim takes a name for what it names; a collision with a different thing gets
// a numeric suffix and a warning, the Dart analogue of the alias fallback.
func (g *dartGen) claim(name, what string) string {
	if prev, ok := g.used[name]; ok {
		if prev == what {
			return name
		}
		for i := 2; ; i++ {
			cand := fmt.Sprintf("%s%d", name, i)
			if _, ok := g.used[cand]; !ok {
				g.warn("%s: the name %s is already taken by %s; using %s", what, name, prev, cand)
				name = cand
				break
			}
		}
	}
	g.used[name] = what
	return name
}

func (g *dartGen) emit(src string) { g.decls.WriteString(src + "\n") }

// dartKeywords can't be identifiers, or confuse enough that they shouldn't.
var dartKeywords = map[string]bool{
	"abstract": true, "as": true, "assert": true, "async": true, "await": true,
	"base": true, "break": true, "case": true, "catch": true, "class": true,
	"const": true, "continue": true, "covariant": true, "default": true,
	"deferred": true, "do": true, "dynamic": true, "else": true, "enum": true,
	"export": true, "extends": true, "extension": true, "external": true,
	"factory": true, "false": true, "final": true, "for": true, "get": true,
	"hide": true, "if": true, "implements": true, "import": true, "in": true,
	"interface": true, "is": true, "late": true, "library": true, "mixin": true,
	"new": true, "null": true, "of": true, "on": true, "operator": true,
	"part": true, "required": true, "rethrow": true, "return": true,
	"sealed": true, "set": true, "show": true, "static": true, "super": true,
	"switch": true, "sync": true, "this": true, "throw": true, "true": true,
	"try": true, "typedef": true, "var": true, "void": true, "while": true,
	"with": true, "yield": true, "int": true, "double": true, "num": true,
	"bool": true, "String": true, "Object": true, "Map": true, "List": true,
	"Set": true, "Function": true, "Never": true, "Iterable": true,
	"Future": true, "Stream": true, "duration": true,
}

// camel renders a name as a lowerCamel Dart identifier, safe against keywords.
func camel(s string) string {
	p := pascal(s)
	if p == "" {
		return "x"
	}
	id := strings.ToLower(p[:1]) + p[1:]
	if dartKeywords[id] {
		id += "_"
	}
	return id
}

// dartLit renders a JSON value as a Dart literal.
func dartLit(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, `$`, `\$`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(x) + "'"
	case bool:
		return fmt.Sprint(x)
	case nil:
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// propsOf splits a schema's properties from its required set.
func propsOf(s map[string]any) (props map[string]any, required map[string]bool) {
	props, _ = s["properties"].(map[string]any)
	required = map[string]bool{}
	if r, ok := s["required"].([]any); ok {
		for _, n := range r {
			if name, ok := n.(string); ok {
				required[name] = true
			}
		}
	}
	return
}

// isObject reports whether the schema renders as a class (an object with
// declared properties) rather than a Map typedef.
func isObject(s map[string]any) bool {
	props, _ := s["properties"].(map[string]any)
	return len(props) > 0
}

// dartKind identifies what a schema becomes, shared by the name and the
// encode/decode renderers.
type dartKind int

const (
	dkAny dartKind = iota
	dkNull
	dkString
	dkInt
	dkNum
	dkBool
	dkDateTime
	dkList
	dkMap   // Map<String, T>
	dkClass // a generated class (object with properties, $ref or enum)
	dkUnion // a sealed class over object variants
)

func (c *dartConverter) kindOf(s map[string]any) dartKind {
	if _, ok := str(s["x-backd-ts"]); ok {
		if t, _ := str(s["x-backd-ts"]); t == "FileDetails[]" {
			return dkList
		}
		return dkClass
	}
	if _, ok := str(s["$ref"]); ok {
		return dkClass
	}
	if c.isStringEnum(s) {
		return dkClass
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if arr, ok := s[kw].([]any); ok && len(arr) > 0 {
			if c.allObjectVariants(arr) {
				return dkUnion
			}
			return dkAny
		}
	}
	types := typeList(s["type"])
	if len(types) == 0 {
		switch {
		case s["properties"] != nil, s["additionalProperties"] != nil, s["required"] != nil:
			types = []string{"object"}
		case s["items"] != nil:
			types = []string{"array"}
		default:
			return dkAny
		}
	}
	nonNull := 0
	var single string
	for _, t := range types {
		if t != "null" {
			nonNull++
			single = t
		}
	}
	if nonNull != 1 {
		if nonNull == 0 {
			return dkNull
		}
		return dkAny // a union of several JSON types
	}
	switch single {
	case "string":
		if f, _ := str(s["format"]); f == "date-time" {
			return dkDateTime
		}
		return dkString
	case "integer":
		return dkInt
	case "number":
		return dkNum
	case "boolean":
		return dkBool
	case "null":
		return dkNull
	case "array":
		return dkList
	case "object":
		props, _ := s["properties"].(map[string]any)
		if len(props) > 0 {
			return dkClass
		}
		return dkMap
	}
	return dkAny
}

func typeList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if n, ok := e.(string); ok {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

// isStringEnum reports whether the schema is an enum (or const) whose values
// are all strings — the only kind a Dart enum carries.
func (c *dartConverter) isStringEnum(s map[string]any) bool {
	var vals []any
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		vals = e
	} else if v, ok := s["const"]; ok {
		vals = []any{v}
	} else {
		return false
	}
	for _, v := range vals {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return len(vals) > 0
}

func (c *dartConverter) allObjectVariants(arr []any) bool {
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			return false
		}
		if ref, ok := str(m["$ref"]); ok && strings.HasPrefix(ref, "#") {
			if t := resolvePointer(c.root, strings.TrimPrefix(ref, "#")); t != nil {
				if tm, ok := t.(map[string]any); ok && c.kindOf(tm) == dkClass {
					continue
				}
			}
			return false
		}
		if !isObject(m) {
			return false
		}
	}
	return len(arr) > 0
}

// dartConverter turns JSON Schema documents into Dart type names, emitting the
// named classes, enums and sealed unions the schema needs into the dartGen.
type dartConverter struct {
	g      *dartGen
	root   map[string]any
	prefix string
	where  string
	warn   func(string, ...any)
	refs   map[string]string
}

func newDartConverter(root map[string]any, prefix, where string, warn func(string, ...any), g *dartGen) *dartConverter {
	return &dartConverter{g: g, root: root, prefix: prefix, where: where, warn: warn, refs: map[string]string{}}
}

// dartUnsupported keywords have no Dart equivalent: the schema becomes Object?.
var dartUnsupported = unsupported

// typeOf renders the Dart type of the schema. hint names the class the schema
// gets when it becomes one.
func (c *dartConverter) typeOf(s any, path, hint string) string {
	switch t := s.(type) {
	case bool:
		if t {
			return "Object?"
		}
		return "Null"
	case map[string]any:
		return c.typeOfMap(t, path, hint)
	}
	return "Object?"
}

func (c *dartConverter) typeOfMap(s map[string]any, path, hint string) string {
	for _, k := range dartUnsupported {
		if _, ok := s[k]; ok {
			c.warn("%s%s: keyword %q has no Dart equivalent; typed Object?", c.where, path, k)
			return "Object?"
		}
	}
	if t, ok := str(s["x-backd-ts"]); ok {
		switch t {
		case "FileDetails":
			return "FileDetails"
		case "FileDetails[]":
			return "List<FileDetails>"
		}
		c.warn("%s%s: x-backd-ts %q unknown to the Dart emitter; typed Object?", c.where, path, t)
		return "Object?"
	}
	if ref, ok := str(s["$ref"]); ok {
		return c.ref(ref, path)
	}
	if c.isStringEnum(s) {
		return c.enumClass(s, path, hint)
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if arr, ok := s[kw].([]any); ok && len(arr) > 0 {
			if c.allObjectVariants(arr) {
				return c.unionClass(s, arr, path, hint)
			}
			c.warn("%s%s: %s over non-object variants can't be a sealed class; typed Object?", c.where, path, kw)
			return "Object?"
		}
	}
	types := typeList(s["type"])
	if len(types) == 0 {
		switch {
		case s["properties"] != nil, s["additionalProperties"] != nil, s["required"] != nil:
			types = []string{"object"}
		case s["items"] != nil, s["prefixItems"] != nil:
			types = []string{"array"}
		default:
			return "Object?"
		}
	}
	nonNull := []string{}
	for _, t := range types {
		if t != "null" {
			nonNull = append(nonNull, t)
		}
	}
	var base string
	if len(nonNull) == 1 {
		base = c.single(s, nonNull[0], path, hint)
	} else if len(nonNull) == 0 {
		return "Null"
	} else {
		c.warn("%s%s: a union of JSON types %v has no Dart equivalent; typed Object?", c.where, path, nonNull)
		return "Object?"
	}
	if len(types) > len(nonNull) && !strings.HasSuffix(base, "?") {
		return base + "?"
	}
	return base
}

func (c *dartConverter) single(s map[string]any, t, path, hint string) string {
	switch t {
	case "string":
		if f, _ := str(s["format"]); f == "date-time" {
			return "DateTimeString"
		}
		return "String"
	case "integer":
		return "int"
	case "number":
		return "num"
	case "boolean":
		return "bool"
	case "null":
		return "Null"
	case "array":
		if pre, ok := s["prefixItems"].([]any); ok && len(pre) > 0 {
			c.warn("%s%s: tuple types have no Dart equivalent here; typed List<Object?>", c.where, path)
			return "List<Object?>"
		}
		if it, ok := s["items"]; ok {
			return "List<" + c.typeOf(it, path+"/items", hint+"Item") + ">"
		}
		return "List<Object?>"
	case "object":
		props, _ := s["properties"].(map[string]any)
		if len(props) > 0 {
			return c.class(s, path, hint)
		}
		if ap, ok := s["additionalProperties"].(map[string]any); ok {
			return "Map<String, " + c.typeOf(ap, path+"/additionalProperties", hint+"Value") + ">"
		}
		return "Map<String, Object?>"
	}
	return "Object?"
}

// ref names the class a local $ref points to, emitting it on first use.
func (c *dartConverter) ref(ref, path string) string {
	if !strings.HasPrefix(ref, "#") {
		c.warn("%s%s: $ref %q is not local; typed Object?", c.where, path, ref)
		return "Object?"
	}
	if name, ok := c.refs[ref]; ok {
		return name
	}
	target := resolvePointer(c.root, strings.TrimPrefix(ref, "#"))
	if target == nil {
		c.warn("%s%s: $ref %q doesn't resolve; typed Object?", c.where, path, ref)
		return "Object?"
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	name := c.g.claim(c.prefix+pascal(last), c.where+"$ref "+ref)
	if ref == "#" {
		name = c.g.claim(c.prefix+"Self", c.where+"$ref #")
	}
	c.refs[ref] = name
	// Guard recursive references and the shared definitions the doc and input
	// passes both resolve: the class is emitted once under its claimed name.
	if c.g.emitted[name] {
		return name
	}
	c.g.emitted[name] = true
	if m, ok := target.(map[string]any); ok && c.kindOf(m) == dkClass {
		c.emitClass(name, m, path, false)
	} else {
		c.emitAlias(name, target, path)
	}
	return name
}

func (c *dartConverter) emitAlias(name string, s any, path string) {
	c.g.classes[name] = false
	c.g.emit(fmt.Sprintf("typedef %s = %s;\n", name, c.typeOf(s, path, name)))
}

// class emits a class for an object schema and answers its name; the same
// hint converting the same schema twice emits it once.
func (c *dartConverter) class(s map[string]any, path, hint string) string {
	name := c.g.claim(hint, c.where+path)
	if c.g.emitted[name] {
		return name
	}
	c.g.emitted[name] = true
	c.emitClass(name, s, path, false)
	return name
}

// emitClass writes the class: final fields, a constructor, fromJson and toJson.
// With partial every field is nullable (a reference expanded with a field
// selection).
func (c *dartConverter) emitClass(name string, s map[string]any, path string, partial bool) {
	c.g.classes[name] = true
	props, required := propsOf(s)
	var b strings.Builder
	fmt.Fprintf(&b, "final class %s {\n", name)
	var params []string
	fields := slices.Sorted(keys(props))
	for _, n := range fields {
		if required[n] && !partial {
			params = append(params, "required this."+camel(n))
		} else {
			params = append(params, "this."+camel(n))
		}
	}
	fmt.Fprintf(&b, "  const %s({%s});\n\n", name, strings.Join(params, ", "))
	fmt.Fprintf(&b, "  factory %s.fromJson(Map<String, Object?> json) => %s(\n", name, name)
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", camel(n), c.decode(props[n], path+"/properties/"+n, "json["+dartLit(n)+"]", required[n] && !partial, name+pascal(n)))
	}
	b.WriteString("      );\n\n")
	for _, n := range fields {
		t := c.typeOf(props[n], path+"/properties/"+n, name+pascal(n))
		if !required[n] || partial {
			if !strings.HasSuffix(t, "?") {
				t += "?"
			}
		}
		fmt.Fprintf(&b, "  final %s %s;\n", t, camel(n))
	}
	b.WriteString("\n  Map<String, Object?> toJson() => {\n")
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", dartLit(n), c.encode(props[n], path+"/properties/"+n, camel(n), required[n] && !partial, name+pascal(n)))
	}
	b.WriteString("      };\n}\n")
	c.g.emit(b.String())
}

// enumClass emits a Dart enum for a string-valued enum/const and answers its
// name.
func (c *dartConverter) enumClass(s map[string]any, path, hint string) string {
	name := c.g.claim(hint, c.where+path)
	if c.g.emitted[name] {
		return name
	}
	c.g.emitted[name] = true
	c.g.classes[name] = true
	c.g.enums[name] = true
	var vals []any
	if e, ok := s["enum"].([]any); ok {
		vals = e
	} else if v, ok := s["const"]; ok {
		vals = []any{v}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "enum %s {\n", name)
	var members []string
	used := map[string]bool{}
	for _, v := range vals {
		strV, _ := v.(string)
		id := camel(strV)
		for used[id] {
			id += "Value"
		}
		used[id] = true
		members = append(members, fmt.Sprintf("%s(%s)", id, dartLit(strV)))
	}
	fmt.Fprintf(&b, "  %s;\n\n  const %s(this.value);\n\n  /// The JSON value.\n  final String value;\n\n", strings.Join(members, ",\n  "), name)
	fmt.Fprintf(&b, "  /// The member whose value is [v], or null.\n  static %s? maybeValue(Object? v) {\n    for (final e in values) {\n      if (e.value == v) return e;\n    }\n    return null;\n  }\n}\n", name)
	c.g.emit(b.String())
	return name
}

// unionClass emits a sealed class over object variants, decoded by trying each
// in order: the first whose required fields all decode wins.
func (c *dartConverter) unionClass(s map[string]any, variants []any, path, hint string) string {
	name := c.g.claim(hint, c.where+path)
	if c.g.emitted[name] {
		return name
	}
	c.g.emitted[name] = true
	c.g.classes[name] = true
	var b strings.Builder
	fmt.Fprintf(&b, "sealed class %s {\n  const %s._();\n\n", name, name)
	fmt.Fprintf(&b, "  factory %s.fromJson(Object? json) {\n    Object? last;\n", name)
	for i, v := range variants {
		m, _ := v.(map[string]any)
		var sub string
		if ref, ok := str(m["$ref"]); ok && strings.HasPrefix(ref, "#") {
			// A reference inside a union becomes a wrapper variant carrying the
			// referenced type, so it still belongs to the sealed family.
			sub = c.emitRefVariant(c.g.claim(fmt.Sprintf("%s%d", name, i+1), c.where+fmt.Sprintf("%s/%d", path, i)), name, c.ref(ref, fmt.Sprintf("%s/%d", path, i)))
		} else {
			sub = c.emitVariant(c.g.claim(fmt.Sprintf("%s%d", name, i+1), c.where+fmt.Sprintf("%s/%d", path, i)), name, m, fmt.Sprintf("%s/%d", path, i))
		}
		fmt.Fprintf(&b, "    try {\n      return %s.fromJson(Map<String, Object?>.from(json as Map));\n    } catch (e) {\n      last = e;\n    }\n", sub)
	}
	fmt.Fprintf(&b, "    throw ArgumentError.value(json, 'json', 'matches no %s variant: $last');\n  }\n}\n", name)
	c.g.emit(b.String())
	return name
}

// emitVariant writes one variant of a sealed class: it extends the base, which
// the emitted class source gets by parameter here.
func (c *dartConverter) emitVariant(name, base string, s map[string]any, path string) string {
	c.g.classes[name] = true
	props, required := propsOf(s)
	var b strings.Builder
	fmt.Fprintf(&b, "final class %s extends %s {\n", name, base)
	var params []string
	fields := slices.Sorted(keys(props))
	for _, n := range fields {
		if required[n] {
			params = append(params, "required this."+camel(n))
		} else {
			params = append(params, "this."+camel(n))
		}
	}
	fmt.Fprintf(&b, "  const %s({%s}) : super._();\n\n", name, strings.Join(params, ", "))
	fmt.Fprintf(&b, "  factory %s.fromJson(Map<String, Object?> json) => %s(\n", name, name)
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", camel(n), c.decode(props[n], path+"/properties/"+n, "json["+dartLit(n)+"]", required[n], name+pascal(n)))
	}
	b.WriteString("      );\n\n")
	for _, n := range fields {
		t := c.typeOf(props[n], path+"/properties/"+n, name+pascal(n))
		if !required[n] && !strings.HasSuffix(t, "?") {
			t += "?"
		}
		fmt.Fprintf(&b, "  final %s %s;\n", t, camel(n))
	}
	b.WriteString("\n  Map<String, Object?> toJson() => {\n")
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", dartLit(n), c.encode(props[n], path+"/properties/"+n, camel(n), required[n], name+pascal(n)))
	}
	b.WriteString("      };\n}\n")
	c.g.emit(b.String())
	return name
}

// emitRefVariant writes a union member that wraps a referenced type.
func (c *dartConverter) emitRefVariant(name, base, refType string) string {
	c.g.classes[name] = true
	c.g.emit(fmt.Sprintf(`final class %s extends %s {
  const %s(this.value) : super._();

  factory %s.fromJson(Map<String, Object?> json) =>
      %s(%s.fromJson(json));

  final %s value;

  Map<String, Object?> toJson() => value.toJson();
}
`, name, base, name, name, name, refType, refType))
	return name
}

// decode renders the expression turning the JSON [expr] (an Object?) into the
// schema's type. required means the JSON may not hold null; hint is the same
// name the caller typed the field with, so shared classes emit once.
func (c *dartConverter) decode(s any, path, expr string, required bool, hint string) string {
	m, ok := s.(map[string]any)
	if !ok {
		return expr
	}
	k := c.kindOf(m)
	t := c.typeOf(m, path, hint)
	nullable := !required || strings.HasSuffix(t, "?")
	var nonNull string
	switch k {
	case dkString:
		nonNull = expr + " as String"
	case dkInt:
		nonNull = "(" + expr + " as num).toInt()"
	case dkNum:
		nonNull = expr + " as num"
	case dkBool:
		nonNull = expr + " as bool"
	case dkDateTime:
		nonNull = "DateTimeString(" + expr + " as String)"
	case dkNull:
		return "null"
	case dkMap:
		nonNull = "Map<String, Object?>.from(" + expr + " as Map)"
	case dkList:
		var inner string
		if t == "List<FileDetails>" {
			inner = "FileDetails.fromJson(Map<String, Object?>.from(e as Map))"
		} else {
			inner = c.decodeListItem(m, path, "e", hint)
		}
		nonNull = "[for (final e in " + expr + " as List) " + inner + "]"
	default:
		// Classes (objects, $refs, enums) and unions decode through fromJson /
		// maybeValue; typedef'd scalars only cast, and Object? not at all.
		if t == "Object?" {
			return expr
		}
		if !c.g.classes[t] {
			nonNull = expr + " as " + t
			break
		}
		if c.isStringEnum(m) {
			call := t + ".maybeValue(" + expr + ")"
			if nullable {
				return call
			}
			return call + "!"
		}
		nonNull = t + ".fromJson(Map<String, Object?>.from(" + expr + " as Map))"
	}
	if nullable {
		return expr + " == null ? null : " + nonNull
	}
	return nonNull
}

// decodeListItem renders the decode of a list's element expression.
func (c *dartConverter) decodeListItem(m map[string]any, path, e, hint string) string {
	it, ok := m["items"]
	if !ok {
		return e
	}
	im, ok := it.(map[string]any)
	if !ok {
		return e
	}
	return c.decode(im, path+"/items", e, true, hint+"Item")
}

// encode renders the expression turning a typed [expr] back to JSON.
func (c *dartConverter) encode(s any, path, expr string, required bool, hint string) string {
	m, ok := s.(map[string]any)
	if !ok {
		return expr
	}
	k := c.kindOf(m)
	t := c.typeOf(m, path, hint)
	var suffix string
	switch k {
	case dkClass, dkUnion:
		if !c.g.classes[t] {
			return expr // a typedef'd scalar: the value is the JSON
		}
		if c.isStringEnum(m) {
			suffix = "value"
		} else {
			suffix = "toJson()"
		}
	case dkList:
		inner := "e"
		if it, ok := m["items"].(map[string]any); ok {
			inner = c.encode(it, path+"/items", "e", true, hint+"Item")
		}
		if inner == "e" {
			return expr
		}
		// map rather than a collection if: a field doesn't promote past null
		// checks, so `x == null ? null : [for ...]` wouldn't compile.
		if required {
			return expr + ".map((e) => " + inner + ").toList()"
		}
		return expr + "?.map((e) => " + inner + ").toList()"
	case dkDateTime:
		suffix = "value"
	default:
		return expr
	}
	if required {
		return expr + "." + suffix
	}
	return expr + "?." + suffix
}

// readFunctionSchema loads a function's input/output schema like the
// TypeScript side does.
func readFunctionSchema(dir, file string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

const dartPreamble = `/// An RFC 3339 timestamp in UTC.
extension type const DateTimeString(String value) implements String {}

/// The fields backd owns on every document.
final class Meta {
  const Meta({
    required this.createdAt,
    required this.updatedAt,
    this.version,
    this.owner,
    this.createdBy,
    this.updatedBy,
    this.org,
    this.team,
    this.deletedAt,
    this.deletedBy,
    this.purgeAt,
    this.chain,
    this.score,
  });

  factory Meta.fromJson(Map<String, Object?> json) => Meta(
        createdAt: DateTimeString(json['created_at'] as String),
        updatedAt: DateTimeString(json['updated_at'] as String),
        version: (json['version'] as num?)?.toInt(),
        owner: json['owner'] as String?,
        createdBy: json['created_by'] as String?,
        updatedBy: json['updated_by'] as String?,
        org: json['org'] as String?,
        team: json['team'] as String?,
        deletedAt: json['deleted_at'] == null
            ? null
            : DateTimeString(json['deleted_at'] as String),
        deletedBy: json['deleted_by'] as String?,
        purgeAt: json['purge_at'] == null
            ? null
            : DateTimeString(json['purge_at'] as String),
        chain: json['chain'] == null
            ? null
            : Map<String, Object?>.from(json['chain'] as Map),
        score: json['score'] as num?,
      );

  final DateTimeString createdAt;
  final DateTimeString updatedAt;
  final int? version;
  final String? owner;
  final String? createdBy;
  final String? updatedBy;

  /// The organization the document belongs to, in collections that have them.
  final String? org;

  /// The team of the organization the document belongs to, in collections
  /// that have them.
  final String? team;
  final DateTimeString? deletedAt;
  final String? deletedBy;
  final DateTimeString? purgeAt;

  /// Set on a write made by a trigger run: where it sits in a chain of such
  /// writes ({root, depth}).
  final Map<String, Object?>? chain;

  /// The relevance score a search puts on the document.
  final num? score;
}

/// What backd stores about an uploaded file.
final class FileDetails {
  const FileDetails({
    required this.id,
    required this.name,
    required this.size,
    required this.type,
    required this.sha256,
    required this.uploadedAt,
    this.width,
    this.height,
    this.url,
    this.expiresAt,
    this.versions,
  });

  factory FileDetails.fromJson(Map<String, Object?> json) => FileDetails(
        id: json['id'] as String,
        name: json['name'] as String,
        size: (json['size'] as num).toInt(),
        type: json['type'] as String,
        sha256: json['sha256'] as String,
        uploadedAt: DateTimeString(json['uploaded_at'] as String),
        width: (json['width'] as num?)?.toInt(),
        height: (json['height'] as num?)?.toInt(),
        url: json['url'] as String?,
        expiresAt: json['expires_at'] == null
            ? null
            : DateTimeString(json['expires_at'] as String),
        versions: json['versions'] == null
            ? null
            : {
                for (final e in (json['versions'] as Map).entries)
                  e.key as String: FileVersionDetails.fromJson(
                      Map<String, Object?>.from(e.value as Map)),
              },
      );

  final String id;
  final String name;
  final int size;
  final String type;
  final String sha256;
  final DateTimeString uploadedAt;
  final int? width;
  final int? height;
  final String? url;
  final DateTimeString? expiresAt;
  final Map<String, FileVersionDetails>? versions;

  Map<String, Object?> toJson() => {
        'id': id,
        'name': name,
        'size': size,
        'type': type,
        'sha256': sha256,
        'uploaded_at': uploadedAt.value,
        if (width != null) 'width': width,
        if (height != null) 'height': height,
        if (url != null) 'url': url,
        if (expiresAt != null) 'expires_at': expiresAt!.value,
      };
}

/// The state of a version made of a file.
sealed class FileVersionDetails {
  const FileVersionDetails._();

  factory FileVersionDetails.fromJson(Map<String, Object?> json) {
    return switch (json['status']) {
      'pending' => const FileVersionPending(),
      'ready' => FileVersionReady.fromJson(json),
      'empty' => const FileVersionEmpty(),
      'skipped' => FileVersionSkipped.fromJson(json),
      'failed' => FileVersionFailed.fromJson(json),
      _ => throw ArgumentError.value(json['status'], 'status',
          'unknown file version'),
    };
  }
}

final class FileVersionPending extends FileVersionDetails {
  const FileVersionPending() : super._();
}

final class FileVersionReady extends FileVersionDetails {
  const FileVersionReady({
    required this.id,
    required this.size,
    required this.type,
    required this.fingerprint,
    required this.generatedAt,
    this.width,
    this.height,
    this.params,
  }) : super._();

  factory FileVersionReady.fromJson(Map<String, Object?> json) =>
      FileVersionReady(
        id: json['id'] as String,
        size: (json['size'] as num).toInt(),
        type: json['type'] as String,
        fingerprint: json['fingerprint'] as String,
        generatedAt: DateTimeString(json['generated_at'] as String),
        width: (json['width'] as num?)?.toInt(),
        height: (json['height'] as num?)?.toInt(),
        params: json['params'] == null
            ? null
            : Map<String, Object?>.from(json['params'] as Map),
      );

  final String id;
  final int size;
  final String type;
  final String fingerprint;
  final DateTimeString generatedAt;
  final int? width;
  final int? height;
  final Map<String, Object?>? params;
}

final class FileVersionEmpty extends FileVersionDetails {
  const FileVersionEmpty() : super._();
}

final class FileVersionSkipped extends FileVersionDetails {
  const FileVersionSkipped({required this.reason}) : super._();

  factory FileVersionSkipped.fromJson(Map<String, Object?> json) =>
      FileVersionSkipped(reason: json['reason'] as String);

  /// 'not_an_image' or 'unsupported_format'.
  final String reason;
}

final class FileVersionFailed extends FileVersionDetails {
  const FileVersionFailed({required this.reason}) : super._();

  factory FileVersionFailed.fromJson(Map<String, Object?> json) =>
      FileVersionFailed(reason: json['reason'] as String);

  /// 'too_large' or 'decode_error'.
  final String reason;
}

/// A 'many' reference expanded: the documents, or truncated when the list ran
/// past the expansion limit.
final class ExpandedMany<T> {
  const ExpandedMany({required this.items, this.truncated = false});

  factory ExpandedMany.from(
      Object? raw, T? Function(Map<String, Object?>) decode) {
    Object? list = raw;
    var truncated = false;
    if (raw is Map) {
      truncated = raw['truncated'] == true;
      list = raw['items'];
    }
    final items = <T?>[
      for (final e in (list as List? ?? const <Object?>[]))
        e is Map ? decode(Map<String, Object?>.from(e)) : null,
    ];
    return ExpandedMany(items: items, truncated: truncated);
  }

  final List<T?> items;
  final bool truncated;
}
`

// realm renders types.dart for the realm.
func (g *dartGen) realm(rl *registry.Realm, o Options) string {
	g.rl = rl
	dbs := make([]string, 0, len(rl.Databases))
	for n := range rl.Databases {
		if o.Database == "" || n == o.Database {
			dbs = append(dbs, n)
		}
	}
	sort.Strings(dbs)
	prefix := pascal(rl.Name)

	// Claim every collection's prefix up front, in the order the emission
	// pass runs, so references between collections name it consistently.
	for _, dbName := range dbs {
		for _, c := range rl.Databases[dbName].SortedCollections() {
			g.collPrefix[dbName+"/"+c.Name] = g.claim(pascal(dbName)+pascal(c.Name), "collection "+dbName+"/"+c.Name)
		}
	}

	// Referenced collections get an all-nullable partial class for _expanded.
	referenced := map[string]bool{}
	for _, dbName := range dbs {
		for _, c := range rl.Databases[dbName].Collections {
			for _, r := range c.References {
				target := r.Database
				if target == "" {
					target = dbName
				}
				referenced[target+"/"+r.Collection] = true
			}
		}
	}

	for _, dbName := range dbs {
		db := rl.Databases[dbName]
		for _, c := range db.SortedCollections() {
			g.collection(dbName, c, referenced[dbName+"/"+c.Name])
		}
		if db.Functions != nil {
			for _, name := range slices.Sorted(keys(db.Functions.Functions)) {
				g.function(dbName, db.Functions.Functions[name])
			}
		}
	}

	if ac := rl.AppConfig; ac != nil {
		g.config(prefix, ac)
	}
	g.roleConsts(prefix, rl)

	out := "import 'package:backd/backd.dart';\n\n" + dartPreamble + "\n" + g.decls.String()

	var tail strings.Builder
	// One wrapper per database, then the realm class and its accessor.
	for _, dbName := range dbs {
		db := rl.Databases[dbName]
		name := g.claim(prefix+pascal(dbName), "database "+dbName)
		g.dbNames[dbName] = name
		var w strings.Builder
		fmt.Fprintf(&w, "/// The %q database of %q, typed.\nfinal class %s {\n  %s(this._db);\n\n  final Database _db;\n", dbName, rl.Name, name, name)
		for _, c := range db.SortedCollections() {
			ts := g.collTypes[dbName+"/"+c.Name]
			docT, inT, createT := ts[0], ts[1], ts[2]
			decode := "(Map<String, Object?> m) => m"
			if g.classes[docT] {
				decode = docT + ".fromJson"
			}
			encIn := "(Map<String, Object?> i) => i"
			if g.classes[inT] {
				encIn = "(i) => i.toJson()"
			}
			encCreate := encIn
			if createT != inT {
				encCreate = "(Map<String, Object?> i) => i"
				if g.classes[createT] {
					encCreate = "(i) => i.toJson()"
				}
			}
			fmt.Fprintf(&w, "\n  /// The %q collection.\n  TypedCollection<%s, %s, %s> get %s =>\n      TypedCollection(_db.collection(%s), %s, %s, %s);\n",
				c.Name, docT, inT, createT, camel(c.Name), dartLit(c.Name), decode, encIn, encCreate)
		}
		if db.Functions != nil {
			for _, fn := range slices.Sorted(keys(db.Functions.Functions)) {
				f := db.Functions.Functions[fn]
				if f.Internal {
					continue
				}
				g.functionMethod(&w, dbName, f)
			}
		}
		w.WriteString("}\n")
		fmt.Fprintf(&tail, "%s", w.String())
	}

	realmName := g.claim(prefix+"Realm", "realm "+rl.Name)
	fmt.Fprintf(&tail, `
/// The %q realm bound to a client: `+"`client.typed`"+` gives it.
final class %s {
  %s(this.client);

  final BackdClient<%s> client;
`, rl.Name, realmName, realmName, realmName)
	for _, dbName := range dbs {
		w := g.dbNames[dbName]
		fmt.Fprintf(&tail, "\n  /// The %q database.\n  %s get %s => %s(client.db(%s));\n",
			dbName, w, camel(dbName), w, dartLit(dbName))
	}
	if rl.AppConfig != nil {
		fmt.Fprintf(&tail, "\n  /// The realm's app configuration, typed per path.\n  %s get config => %s(client.config);\n",
			g.configName, g.configName)
	}
	fmt.Fprintf(&tail, "}\n\n/// `BackdClient<%s>` binds the realm's types: `client.typed`.\nextension %sAccess on BackdClient<%s> {\n  %s get typed => %s(this);\n}\n",
		realmName, realmName, realmName, realmName, realmName)

	return out + tail.String()
}

// collection emits one collection's types.
func (g *dartGen) collection(db string, c *registry.Collection, referenced bool) {
	prefix := g.collPrefix[db+"/"+c.Name]
	where0 := db + "/" + c.Name + " schema.json "
	docSchema, inputSchema := fileAware(c)
	conv := newDartConverter(docSchema, prefix, where0, g.warn, g)
	// The input converts against its own root (refs resolve within it).
	inConv := newDartConverter(inputSchema, prefix, where0, g.warn, g)

	docName := prefix
	inName := g.claim(prefix+"Input", "input "+db+"/"+c.Name)
	createName := inName
	if c.CustomID != nil {
		createName = g.claim(prefix+"Create", "create "+db+"/"+c.Name)
	}
	g.collTypes[db+"/"+c.Name] = [3]string{docName, inName, createName}

	var expandedName string
	if len(c.References) > 0 {
		expandedName = g.claim(docName+"Expanded", "expanded "+db+"/"+c.Name)
	}

	g.inputClass(inConv, inName, inputSchema, db, c)
	g.docClass(conv, docName, expandedName, docSchema, db, c)
	if c.CustomID != nil {
		g.createClass(inConv, createName, inputSchema, db, c)
	}
	if referenced {
		g.partialFor(db, c)
	}
	if len(c.References) > 0 {
		g.expandedClass(db, c, expandedName)
	}
	g.queryTypes(db, c, prefix)
	if c.Aggregate != nil {
		g.aggregateConsts(c, prefix)
	}
}

// inputClass emits the write-side type: the class of the schema without the
// file fields (uploads write them), or a Map typedef when the schema declares
// no properties.
func (g *dartGen) inputClass(conv *dartConverter, name string, schema map[string]any, db string, c *registry.Collection) {
	if isObject(schema) {
		g.emit(fmt.Sprintf("/// What a %s/%s write accepts.\n", db, c.Name))
		conv.emitClass(name, schema, "", false)
		return
	}
	g.emit(fmt.Sprintf("/// What a %s/%s write accepts.\ntypedef %s = Map<String, Object?>;\n", db, c.Name, name))
}

// docClass emits the read-side type: id, _meta, the document's fields (files
// as their details) and _expanded when the collection declares references.
func (g *dartGen) docClass(conv *dartConverter, name, expandedName string, schema map[string]any, db string, c *registry.Collection) {
	if !isObject(schema) {
		g.emit(fmt.Sprintf("/// A %s/%s document.\ntypedef %s = Map<String, Object?>;\n", db, c.Name, name))
		return
	}
	g.classes[name] = true
	props, required := propsOf(schema)
	fields := slices.Sorted(keys(props))
	var b strings.Builder
	fmt.Fprintf(&b, "/// A %s/%s document.\nfinal class %s {\n", db, c.Name, name)
	params := []string{"required this.id", "required this.meta"}
	if expandedName != "" {
		params = append(params, "this.expanded")
	}
	for _, n := range fields {
		if required[n] {
			params = append(params, "required this."+camel(n))
		} else {
			params = append(params, "this."+camel(n))
		}
	}
	fmt.Fprintf(&b, "  const %s({%s});\n\n", name, strings.Join(params, ", "))
	fmt.Fprintf(&b, "  factory %s.fromJson(Map<String, Object?> json) => %s(\n        id: json['id'] as String,\n        meta: json['_meta'] is Map\n            ? Meta.fromJson(Map<String, Object?>.from(json['_meta'] as Map))\n            : const Meta(createdAt: DateTimeString(''), updatedAt: DateTimeString('')),\n", name, name)
	if expandedName != "" {
		fmt.Fprintf(&b, "        expanded: json['_expanded'] is Map\n            ? %s._(Map<String, Object?>.from(json['_expanded'] as Map))\n            : null,\n", expandedName)
	}
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", camel(n), conv.decode(props[n], "/properties/"+n, "json["+dartLit(n)+"]", required[n], name+pascal(n)))
	}
	b.WriteString("      );\n\n  final String id;\n  final Meta meta;\n")
	if expandedName != "" {
		fmt.Fprintf(&b, "\n  /// The references `expand` asked for; null when the request didn't.\n  final %s? expanded;\n", expandedName)
	}
	b.WriteString("\n")
	for _, n := range fields {
		t := conv.typeOf(props[n], "/properties/"+n, name+pascal(n))
		if !required[n] && !strings.HasSuffix(t, "?") {
			t += "?"
		}
		fmt.Fprintf(&b, "  final %s %s;\n", t, camel(n))
	}
	b.WriteString("\n  Map<String, Object?> toJson() => {\n")
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", dartLit(n), conv.encode(props[n], "/properties/"+n, camel(n), required[n], name+pascal(n)))
	}
	b.WriteString("      };\n}\n")
	g.emit(b.String())
}

// createClass emits the input a custom_id collection's create takes: the
// input plus the client-chosen id.
func (g *dartGen) createClass(conv *dartConverter, name string, schema map[string]any, db string, c *registry.Collection) {
	cid := c.CustomID
	note := "the id is chosen by the client and never changes; it must match " + cid.Anchored()
	if cid.User {
		note = "the id is chosen by the client and never changes; it must be the id of the signed-in user"
	}
	if !isObject(schema) {
		g.emit(fmt.Sprintf("/// %s/%s: %s — the document must carry 'id'.\ntypedef %s = Map<String, Object?>;\n", db, c.Name, note, name))
		return
	}
	g.classes[name] = true
	props, required := propsOf(schema)
	fields := slices.Sorted(keys(props))
	var b strings.Builder
	fmt.Fprintf(&b, "/// %s/%s: %s.\nfinal class %s {\n", db, c.Name, note, name)
	params := []string{"required this.id"}
	for _, n := range fields {
		if required[n] {
			params = append(params, "required this."+camel(n))
		} else {
			params = append(params, "this."+camel(n))
		}
	}
	fmt.Fprintf(&b, "  const %s({%s});\n\n", name, strings.Join(params, ", "))
	fmt.Fprintf(&b, "  factory %s.fromJson(Map<String, Object?> json) => %s(\n        id: json['id'] as String,\n", name, name)
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", camel(n), conv.decode(props[n], "/properties/"+n, "json["+dartLit(n)+"]", required[n], name+pascal(n)))
	}
	b.WriteString("      );\n\n")
	b.WriteString("  /// " + strings.ReplaceAll(note, "*/", "* /") + ".\n  final String id;\n")
	for _, n := range fields {
		t := conv.typeOf(props[n], "/properties/"+n, name+pascal(n))
		if !required[n] && !strings.HasSuffix(t, "?") {
			t += "?"
		}
		fmt.Fprintf(&b, "  final %s %s;\n", t, camel(n))
	}
	b.WriteString("\n  Map<String, Object?> toJson() => {\n        'id': id,\n")
	for _, n := range fields {
		fmt.Fprintf(&b, "        %s: %s,\n", dartLit(n), conv.encode(props[n], "/properties/"+n, camel(n), required[n], name+pascal(n)))
	}
	b.WriteString("      };\n}\n")
	g.emit(b.String())
}

// partialName claims the name of a collection's partial class.
func (g *dartGen) partialName(db, coll string) string {
	key := db + "/" + coll
	if name, ok := g.partials[key]; ok {
		return name
	}
	prefix := g.collPrefix[key]
	if prefix == "" {
		prefix = pascal(db) + pascal(coll)
	}
	name := g.claim(prefix+"Partial", "partial "+key)
	g.partials[key] = name
	return name
}

// partialFor emits the all-nullable class an expansion of the collection
// returns: a field selection may leave every field but the id out.
func (g *dartGen) partialFor(db string, c *registry.Collection) {
	key := db + "/" + c.Name
	name := g.partialName(db, c.Name)
	if g.emitted[name] {
		return
	}
	g.emitted[name] = true
	docSchema, _ := fileAware(c)
	if !isObject(docSchema) {
		g.emit(fmt.Sprintf("/// A %s document as an expansion returns it.\ntypedef %s = Map<String, Object?>;\n", key, name))
		return
	}
	// The expansion always carries the id; the other fields may be selected out.
	partial := map[string]any{}
	for k, v := range docSchema {
		partial[k] = v
	}
	props := map[string]any{"id": map[string]any{"type": "string"}}
	if p, ok := docSchema["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	partial["properties"] = props
	partial["required"] = []any{"id"}
	conv := newDartConverter(docSchema, g.collPrefix[key], db+"/"+c.Name+" schema.json ", g.warn, g)
	g.emit(fmt.Sprintf("/// A %s document as `expand` returns it: the id and only the fields\n/// the selection asked for.\n", key))
	conv.emitClass(name, partial, "", false)
}

// expandedClass emits the `_expanded` field's class: one getter per declared
// reference, decoding lazily so missing selections stay absent.
func (g *dartGen) expandedClass(db string, c *registry.Collection, name string) {
	var b strings.Builder
	fmt.Fprintf(&b, "/// The `_expanded` of a %s/%s document, decoded on access.\nfinal class %s {\n  const %s._(this._raw);\n\n  final Map<String, Object?> _raw;\n", db, c.Name, name, name)
	for _, refName := range slices.Sorted(keys(c.References)) {
		r := c.References[refName]
		target := r.Database
		if target == "" {
			target = db
		}
		partial := g.partialName(target, r.Collection)
		// Emit the target's partial lazily: the reference may point into a
		// database the collection pass hasn't emitted or isn't emitting.
		if !g.emitted[partial] {
			if tc := g.rl.Databases[target].Collections[r.Collection]; tc != nil {
				g.partialFor(target, tc)
			}
		}
		field := camel(refName)
		decode := "(Map<String, Object?> m) => m"
		if g.classes[partial] {
			decode = partial + ".fromJson"
		}
		if r.Many {
			fmt.Fprintf(&b, "\n  /// %q → %s/%s (many).\n  ExpandedMany<%s>? get %s {\n    final v = _raw[%s];\n    return v == null ? null : ExpandedMany<%s>.from(v, %s);\n  }\n",
				refName, target, r.Collection, partial, field, dartLit(refName), partial, decode)
		} else {
			fmt.Fprintf(&b, "\n  /// %q → %s/%s.\n  %s? get %s {\n    final v = _raw[%s];\n    return v is Map ? %s(Map<String, Object?>.from(v)) : null;\n  }\n",
				refName, target, r.Collection, partial, field, dartLit(refName), decode)
		}
	}
	b.WriteString("}\n")
	g.emit(b.String())
}

// queryTypes emits the per-collection where/orderBy names: a Map typedef for
// the filter plus const holders of the queryable field names and the sorts.
func (g *dartGen) queryTypes(db string, c *registry.Collection, prefix string) {
	fields := append([]queryField{}, systemQueryFields...)
	if c.InOrganizations() {
		fields = append(fields, orgQueryFields...)
	}
	if c.InTeams() {
		fields = append(fields, teamQueryFields...)
	}
	if c.SoftDelete != nil {
		fields = append(fields, softDeleteQueryFields...)
	}
	for _, p := range slices.Sorted(keys(c.Fields)) {
		fields = append(fields, fieldOf(p, c.Fields[p], schemaFor(c.RawSchema, p)))
	}
	var names []string
	for _, f := range fields {
		if f.kind == kindOpaque {
			continue
		}
		names = append(names, f.path)
	}
	fieldsName := g.claim(prefix+"Field", "where "+db+"/"+c.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "/// The fields `where` of %s/%s may name.\nabstract final class %s {\n", db, c.Name, fieldsName)
	used := map[string]bool{}
	for _, p := range names {
		id := camel(p)
		for used[id] {
			id += "X"
		}
		used[id] = true
		fmt.Fprintf(&b, "  static const String %s = %s;\n", id, dartLit(p))
	}
	fmt.Fprintf(&b, "}\n\n/// A `where` of %s/%s: `{<field>: {$eq: v}}` and `$and`/`$or`, keyed by %s.\ntypedef %s = Map<String, Object?>;\n", db, c.Name, fieldsName, g.claim(prefix+"Where", "where "+db+"/"+c.Name))
	orderName := g.claim(prefix+"OrderBy", "order "+db+"/"+c.Name)
	fmt.Fprintf(&b, "\n/// The sorts `orderBy` of %s/%s accepts (a name, a list, or '-name').\nabstract final class %s {\n", db, c.Name, orderName)
	used = map[string]bool{}
	for _, p := range names {
		id := camel(p)
		for used[id] {
			id += "X"
		}
		used[id] = true
		fmt.Fprintf(&b, "  static const String %s = %s;\n  static const String %sDesc = %s;\n", id, dartLit(p), id, dartLit("-"+p))
	}
	b.WriteString("}\n")
	g.emit(b.String())
}

// aggregateConsts emits the fields a collection's aggregate may group by and
// measure, as const sets.
func (g *dartGen) aggregateConsts(c *registry.Collection, prefix string) {
	a := c.Aggregate
	name := g.claim(prefix+"Aggregate", "aggregate "+c.Name)
	var dates []string
	for _, f := range a.GroupBy {
		if f == "_meta.created_at" || f == "_meta.updated_at" || c.Fields[f].Date {
			dates = append(dates, f)
		}
	}
	set := func(list []string) string {
		var parts []string
		for _, f := range list {
			parts = append(parts, dartLit(f))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	g.emit(fmt.Sprintf(`/// What an aggregate of %s may use: the group-by fields, those of them
/// that are dates (they can be bucketed `+"`field:day`"+`), and the metrics.
abstract final class %s {
  static const Set<String> groupBy = %s;
  static const Set<String> dates = %s;
  static const Set<String> metrics = %s;
}
`, c.Name, name, set(a.GroupBy), set(dates), set(a.Metrics)))
}

// function emits a function's input and output types.
func (g *dartGen) function(db string, fn *registry.Function) {
	in, _ := readFunctionSchema(fn.Dir, registry.InputSchemaFile)
	out, _ := readFunctionSchema(fn.Dir, registry.OutputSchemaFile)
	prefix := "Fn" + pascal(db) + pascal(fn.Name)
	key := db + "/" + fn.Name
	inName, outName := "", ""
	if in != nil {
		conv := newDartConverter(in, prefix, key+" "+registry.InputSchemaFile+" ", g.warn, g)
		inName = g.claim(prefix+"Input", "function "+key+" input")
		if isObject(in) {
			g.emit(fmt.Sprintf("/// The input of %s.\n", key))
			conv.emitClass(inName, in, "", false)
		} else {
			g.emit(fmt.Sprintf("/// The input of %s.\ntypedef %s = %s;\n", key, inName, conv.typeOf(in, "", inName)))
		}
	}
	if out != nil {
		conv := newDartConverter(out, prefix, key+" "+registry.OutputSchemaFile+" ", g.warn, g)
		outName = g.claim(prefix+"Output", "function "+key+" output")
		if isObject(out) {
			g.emit(fmt.Sprintf("/// The output of %s.\n", key))
			conv.emitClass(outName, out, "", false)
		} else {
			g.emit(fmt.Sprintf("/// The output of %s.\ntypedef %s = %s;\n", key, outName, conv.typeOf(out, "", outName)))
		}
	}
	g.fnTypes[key] = [2]string{inName, outName}
}

// functionMethod writes a typed call of a non-internal function on the
// database wrapper.
func (g *dartGen) functionMethod(w *strings.Builder, db string, fn *registry.Function) {
	ts := g.fnTypes[db+"/"+fn.Name]
	inT, outT := ts[0], ts[1]
	if inT == "" {
		inT = "Map<String, Object?>"
	}
	if outT == "" {
		outT = "Object?"
	}
	inExpr := "input"
	if g.classes[inT] {
		inExpr = "input.toJson()"
	}
	outExpr := "result"
	if g.classes[outT] {
		outExpr = outT + ".fromJson(Map<String, Object?>.from(result as Map))"
	} else if outT != "Object?" {
		outExpr = "result as " + outT
	}
	if fn.Mode == registry.ModeAsync {
		fmt.Fprintf(w, "\n  /// Calls %s/%s (async: answers a job).\n  Future<Job> %s(%s input, {RequestOptions? options}) async =>\n      (await _db.fn(%s, %s, respondAsync: true, options: options)) as Job;\n",
			db, fn.Name, camel(fn.Name), inT, dartLit(fn.Name), inExpr)
		return
	}
	fmt.Fprintf(w, "\n  /// Calls %s/%s.\n  Future<%s> %s(%s input, {String? idempotencyKey, RequestOptions? options}) async {\n    final result = await _db.fn(%s, %s, idempotencyKey: idempotencyKey, options: options);\n    return %s;\n  }\n",
		db, fn.Name, outT, camel(fn.Name), inT, dartLit(fn.Name), inExpr, outExpr)
}

// config emits the configuration's tree class and the typed path getters.
func (g *dartGen) config(prefix string, ac *registry.AppConfig) {
	conv := newDartConverter(ac.RawSchema, prefix+"Config", "app-config/schema.json ", g.warn, g)
	if isObject(ac.RawSchema) {
		g.emit("/// The realm's app configuration.\n")
		conv.emitClass(g.claim(prefix+"ConfigTree", "config tree"), ac.RawSchema, "", false)
	} else {
		g.emit(fmt.Sprintf("/// The realm's app configuration.\ntypedef %s = Map<String, Object?>;\n", g.claim(prefix+"ConfigTree", "config tree")))
	}
	name := g.claim(prefix+"Config", "config")
	g.configName = name
	var b strings.Builder
	fmt.Fprintf(&b, "/// The app configuration's leaf paths, typed.\nfinal class %s {\n  %s(this._config);\n\n  final Config _config;\n", name, name)
	used := map[string]bool{}
	for _, p := range slices.Sorted(keys(ac.Paths)) {
		if !ac.Paths[p].Leaf {
			continue
		}
		t := "Object?"
		conv2 := conv
		if s := schemaAt(ac.RawSchema, p); s != nil {
			t = conv2.typeOf(s, "/"+p, prefix+"Config"+pascal(p))
		}
		method := camel(p)
		for used[method] {
			method += "Path"
		}
		used[method] = true
		get := "return v"
		switch {
		case t == "Object?":
		case g.enums[t]:
			get = "return " + t + ".maybeValue(v)"
		case g.classes[t]:
			get = "return v == null ? null : " + t + ".fromJson(Map<String, Object?>.from(v as Map))"
		default:
			get = "return v as " + nullable(t)
		}
		fmt.Fprintf(&b, "\n  /// The %q path.\n  Future<%s> %s({bool refresh = false, RequestOptions? options}) async {\n    final v = await _config.get(%s, refresh: refresh, options: options);\n    %s;\n  }\n", p, nullable(t), method, dartLit(p), get)
	}
	b.WriteString("}\n")
	g.emit(b.String())
}

func nullable(t string) string {
	if strings.HasSuffix(t, "?") || t == "Object?" {
		return t
	}
	return t + "?"
}

// roleConsts emits the realm's role, organization role, team role and
// template kind names as const holders.
func (g *dartGen) roleConsts(prefix string, rl *registry.Realm) {
	consts := func(name, what string, values []string) {
		if len(values) == 0 {
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "/// The %s %q declares.\nabstract final class %s {\n", what, rl.Name, g.claim(name, what))
		used := map[string]bool{}
		for _, v := range values {
			id := camel(v)
			for used[id] {
				id += "Value"
			}
			used[id] = true
			fmt.Fprintf(&b, "  static const String %s = %s;\n", id, dartLit(v))
		}
		b.WriteString("}\n")
		g.emit(b.String())
	}
	consts(prefix+"Role", "roles", slices.Sorted(keys(rl.Settings.Roles)))
	if o := rl.Settings.Organizations; o != nil {
		consts(prefix+"OrgRole", "organization roles", o.RoleNames())
		consts(prefix+"TeamRole", "team roles", o.TeamRoleNames())
	}
	if rl.Email != nil {
		consts(prefix+"EmailKind", "email kinds", rl.Email.Kinds())
	}
	if rl.Push != nil {
		consts(prefix+"PushKind", "push kinds", rl.Push.Kinds())
	}
}
