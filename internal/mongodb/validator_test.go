package mongodb

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(s)))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

var systemProps = map[string]any{
	"_id": map[string]any{"bsonType": "string"},
	"_meta": map[string]any{
		"bsonType": "object",
		"required": []any{"created_at", "updated_at", "version"},
		"properties": map[string]any{
			"created_at": map[string]any{"bsonType": "date"},
			"updated_at": map[string]any{"bsonType": "date"},
			"version":    map[string]any{"bsonType": []any{"int", "long"}, "minimum": int64(1)},
			"owner":      map[string]any{"bsonType": []any{"string", "null"}},
			"created_by": map[string]any{"bsonType": "string"},
			"updated_by": map[string]any{"bsonType": "string"},
		},
	},
}

func withSystem(props map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range props {
		out[k] = v
	}
	for k, v := range systemProps {
		out[k] = v
	}
	return out
}

func TestValidatorInjectsSystemFields(t *testing.T) {
	got, om := Validator(decode(t, `{}`))
	want := map[string]any{"$jsonSchema": map[string]any{
		"properties": withSystem(nil),
		"required":   []any{"_id", "_meta"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
	if len(om) != 0 {
		t.Errorf("omissions = %v, want none", om)
	}
}

func TestValidatorTranslation(t *testing.T) {
	schema := decode(t, `{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "title": "Item",
	  "type": "object",
	  "properties": {
	    "name":   {"type": "string", "minLength": 1, "maxLength": 80, "pattern": "^[a-z]+$", "format": "email"},
	    "count":  {"type": "integer", "minimum": 0, "exclusiveMaximum": 10},
	    "price":  {"type": ["number", "null"], "exclusiveMinimum": 0, "multipleOf": 0.5},
	    "flag":   {"type": "boolean", "const": true},
	    "status": {"enum": ["a", "b", 3]},
	    "tags":   {"type": "array", "items": {"type": "string"}, "minItems": 1, "uniqueItems": true, "prefixItems": [true]},
	    "meta":   {"type": "object", "additionalProperties": {"type": "string"}, "minProperties": 1},
	    "any":    {"anyOf": [{"type": "string"}, {"type": "integer"}], "not": {"type": "null"}},
	    "never":  false
	  },
	  "required": ["name"],
	  "additionalProperties": false,
	  "$defs": {"x": {}}
	}`)
	got, om := Validator(schema)

	want := map[string]any{"$jsonSchema": map[string]any{
		"bsonType": []any{"object"},
		"properties": withSystem(map[string]any{
			"name":   map[string]any{"bsonType": []any{"string"}, "minLength": int64(1), "maxLength": int64(80), "pattern": "^[a-z]+$"},
			"count":  map[string]any{"bsonType": []any{"int", "long", "double"}, "minimum": int64(0), "maximum": int64(10), "exclusiveMaximum": true, "multipleOf": int64(1)},
			"price":  map[string]any{"bsonType": []any{"int", "long", "double", "decimal", "null"}, "minimum": int64(0), "exclusiveMinimum": true, "multipleOf": 0.5},
			"flag":   map[string]any{"bsonType": []any{"bool"}, "enum": []any{true}},
			"status": map[string]any{"enum": []any{"a", "b", int64(3)}},
			"tags":   map[string]any{"bsonType": []any{"array"}, "items": map[string]any{"bsonType": []any{"string"}}, "minItems": int64(1), "uniqueItems": true},
			"meta":   map[string]any{"bsonType": []any{"object"}, "additionalProperties": map[string]any{"bsonType": []any{"string"}}, "minProperties": int64(1)},
			"any": map[string]any{
				"anyOf": []any{map[string]any{"bsonType": []any{"string"}}, map[string]any{"bsonType": []any{"int", "long", "double"}, "multipleOf": int64(1)}},
				"not":   map[string]any{"bsonType": []any{"null"}},
			},
			"never": map[string]any{"not": map[string]any{}},
		}),
		"required":             []any{"name", "_id", "_meta"},
		"additionalProperties": false,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}

	wantOm := map[Omission]bool{
		{Path: "/properties/name", Keyword: "format"}:      true,
		{Path: "/properties/tags", Keyword: "prefixItems"}: true,
		{Path: "/", Keyword: "$defs"}:                      true,
	}
	if len(om) != len(wantOm) {
		t.Errorf("omissions = %v, want %v", om, wantOm)
	}
	for _, o := range om {
		if !wantOm[o] {
			t.Errorf("unexpected omission %v", o)
		}
	}
}

func TestValidatorExclusiveBoundKeepsStricter(t *testing.T) {
	got, _ := Validator(decode(t, `{"properties": {
	  "a": {"minimum": 5, "exclusiveMinimum": 3},
	  "b": {"minimum": 3, "exclusiveMinimum": 3}
	}}`))
	props := got["$jsonSchema"].(map[string]any)["properties"].(map[string]any)
	if a := props["a"]; !reflect.DeepEqual(a, map[string]any{"minimum": int64(5)}) {
		t.Errorf("a = %v, want minimum 5 only", a)
	}
	if b := props["b"]; !reflect.DeepEqual(b, map[string]any{"minimum": int64(3), "exclusiveMinimum": true}) {
		t.Errorf("b = %v, want exclusive minimum 3", b)
	}
}

func TestValidatorDoesNotModifyInput(t *testing.T) {
	schema := decode(t, `{"properties": {"a": {"type": "integer", "enum": [1]}}, "required": ["a"]}`)
	before := decode(t, `{"properties": {"a": {"type": "integer", "enum": [1]}}, "required": ["a"]}`)
	Validator(schema)
	if !reflect.DeepEqual(schema, before) {
		t.Errorf("input modified: %v", schema)
	}
}
