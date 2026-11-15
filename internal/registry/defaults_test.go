package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const defaultsSchema = `{"type":"object","required":["status"],"properties":{
 "status":{"enum":["draft","sent"],"default":"draft"},
 "qty":{"type":"integer","default":1},
 "note":{"type":["string","null"],"default":"n/a"},
 "address":{"type":"object","default":{},"properties":{"country":{"type":"string","default":"ES"}}},
 "billing":{"type":"object","properties":{"vat":{"type":"string","default":"none"}}},
 "tags":{"type":"array","default":["new"],"items":{"type":"string"}}}}`

func TestApplyDefaults(t *testing.T) {
	reg, err := versionTree(t, "", map[string]string{"schema.json": defaultsSchema})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "items")
	doc := map[string]any{"qty": json.Number("5"), "note": nil}
	c.ApplyDefaults(doc)
	want := map[string]any{
		"status":  "draft",
		"qty":     json.Number("5"), // sent: kept
		"note":    nil,              // an explicit null is a value
		"address": map[string]any{"country": "ES"},
		"tags":    []any{"new"},
		// billing is absent and has no default of its own: nothing is invented inside it
	}
	if !reflect.DeepEqual(doc, want) {
		t.Errorf("got %v\nwant %v", doc, want)
	}

	// A present object gets its inner defaults; the shared default value is never aliased.
	doc = map[string]any{"billing": map[string]any{}}
	c.ApplyDefaults(doc)
	doc["tags"].([]any)[0] = "changed"
	if doc["billing"].(map[string]any)["vat"] != "none" || c.Defaults[len(c.Defaults)-1].Value == nil {
		t.Errorf("billing: %v", doc)
	}
	again := c.ApplyDefaults(map[string]any{})
	if again["tags"].([]any)[0] != "new" {
		t.Errorf("the default was changed through a document: %v", again)
	}
}

func TestDefaultMustSatisfyItsProperty(t *testing.T) {
	_, err := versionTree(t, "", map[string]string{"schema.json": `{"type":"object","properties":{"n":{"type":"integer","minimum":1,"default":0}}}`})
	if err == nil || !strings.Contains(err.Error(), "default") || !strings.Contains(err.Error(), "n:") {
		t.Errorf("err = %v", err)
	}
}
