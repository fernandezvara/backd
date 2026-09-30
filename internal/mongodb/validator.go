// Package mongodb holds everything MongoDB-specific: the $jsonSchema
// validator translation, the repository implementation and the provisioner.
package mongodb

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/fernandezvara/backd/internal/jsonnum"
)

// Omission records a JSON Schema keyword that has no MongoDB $jsonSchema
// equivalent and was left out of the database validator.
type Omission struct {
	Path    string // JSON pointer of the schema containing the keyword
	Keyword string
}

var bsonTypes = map[string][]any{
	"string":  {"string"},
	"boolean": {"bool"},
	"object":  {"object"},
	"array":   {"array"},
	"null":    {"null"},
	// Integers beyond 64 bits arrive as doubles (see jsonnum); the API
	// has already checked that they are integers.
	"integer": {"int", "long", "double"},
	"number":  {"int", "long", "double", "decimal"},
}

// copied are keywords with identical meaning in both dialects. Numeric
// values are normalized to int64/float64.
var copied = map[string]bool{
	"required": true, "enum": true, "minimum": true, "maximum": true, "multipleOf": true,
	"minLength": true, "maxLength": true, "pattern": true, "minItems": true, "maxItems": true,
	"uniqueItems": true, "minProperties": true, "maxProperties": true,
}

// annotations carry no validation meaning and are dropped silently.
var annotations = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "$anchor": true, "title": true,
	"description": true, "examples": true, "default": true, "deprecated": true,
	"readOnly": true, "writeOnly": true,
}

// Validator translates a collection's JSON Schema (as decoded by the
// registry) into a MongoDB $jsonSchema document, injecting the system
// fields _id and _meta at the top level. It never modifies schema.
func Validator(schema map[string]any) (map[string]any, []Omission) {
	var om []Omission
	out := translate(schema, "", &om)

	props, _ := out["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["_id"] = map[string]any{"bsonType": "string"}
	props["_meta"] = map[string]any{
		"bsonType": "object",
		"required": []any{"created_at", "updated_at", "version"},
		"properties": map[string]any{
			"created_at": map[string]any{"bsonType": "date"},
			"updated_at": map[string]any{"bsonType": "date"},
			"version":    map[string]any{"bsonType": []any{"int", "long"}, "minimum": int64(1)},
			// Ownership, set in realms with auth enabled; absent elsewhere
			// and in documents stored before it existed.
			"owner":      map[string]any{"bsonType": []any{"string", "null"}},
			"created_by": map[string]any{"bsonType": "string"},
			"updated_by": map[string]any{"bsonType": "string"},
		},
	}
	out["properties"] = props
	req, _ := out["required"].([]any)
	out["required"] = append(append([]any{}, req...), "_id", "_meta")

	return map[string]any{"$jsonSchema": out}, om
}

func translate(s any, path string, om *[]Omission) map[string]any {
	switch v := s.(type) {
	case bool:
		if v {
			return map[string]any{}
		}
		return map[string]any{"not": map[string]any{}}
	case map[string]any:
		return translateObject(v, path, om)
	}
	return map[string]any{}
}

func translateObject(s map[string]any, path string, om *[]Omission) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		sub := path + "/" + escapePointer(k)
		switch {
		case copied[k]:
			out[k] = jsonnum.Normalize(deepCopy(v))
		case k == "type":
			if t := bsonType(v); t != nil {
				out["bsonType"] = t
			}
		case k == "const":
			out["enum"] = []any{jsonnum.Normalize(deepCopy(v))}
		case k == "properties":
			props := map[string]any{}
			m, _ := v.(map[string]any)
			for name, ps := range m {
				props[name] = translate(ps, sub+"/"+escapePointer(name), om)
			}
			out[k] = props
		case k == "items", k == "not":
			out[k] = translate(v, sub, om)
		case k == "additionalProperties":
			if b, ok := v.(bool); ok {
				out[k] = b
			} else {
				out[k] = translate(v, sub, om)
			}
		case k == "allOf", k == "anyOf", k == "oneOf":
			arr, _ := v.([]any)
			list := make([]any, len(arr))
			for i, e := range arr {
				list[i] = translate(e, sub+"/"+strconv.Itoa(i), om)
			}
			out[k] = list
		case k == "exclusiveMinimum", k == "exclusiveMaximum":
			// handled below, after the plain bounds are known
		case annotations[k]:
		default:
			*om = append(*om, Omission{Path: pointerOrRoot(path), Keyword: k})
		}
	}
	exclusiveBound(s, out, "exclusiveMinimum", "minimum", func(a, b float64) bool { return a >= b })
	exclusiveBound(s, out, "exclusiveMaximum", "maximum", func(a, b float64) bool { return a <= b })
	// "integer" admits doubles (for integers beyond 64 bits); multipleOf 1
	// keeps them integral. MongoDB applies it to numbers only.
	if _, has := s["multipleOf"]; !has && integerOnly(s["type"]) {
		out["multipleOf"] = int64(1)
	}
	return out
}

// integerOnly reports whether a JSON Schema type allows integers but not
// other numbers.
func integerOnly(t any) bool {
	var types []string
	switch v := t.(type) {
	case string:
		types = []string{v}
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				types = append(types, s)
			}
		}
	}
	return slices.Contains(types, "integer") && !slices.Contains(types, "number")
}

// exclusiveBound converts draft 2020-12's numeric exclusive bound into
// MongoDB's draft-4 form (bound + boolean flag), keeping the stricter bound
// when both are present.
func exclusiveBound(s, out map[string]any, exclKey, plainKey string, stricter func(a, b float64) bool) {
	ev, ok := s[exclKey]
	if !ok {
		return
	}
	excl := jsonnum.Normalize(ev)
	if plain, ok := out[plainKey]; ok && !stricter(toFloat(excl), toFloat(plain)) {
		return
	}
	out[plainKey] = excl
	out[exclKey] = true
}

func bsonType(v any) []any {
	var names []any
	switch t := v.(type) {
	case string:
		names = []any{t}
	case []any:
		names = t
	}
	var out []any
	seen := map[any]bool{}
	for _, n := range names {
		s, _ := n.(string)
		for _, b := range bsonTypes[s] {
			if !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
	}
	return out
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = deepCopy(e)
		}
		return s
	}
	return v
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func pointerOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
