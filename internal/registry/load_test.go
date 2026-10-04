package registry

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const personSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "age": {"type": ["integer", "null"]},
    "address": {"type": "object", "properties": {"city": {"type": "string"}}},
    "tags": {"type": "array", "items": {"type": "string"}},
    "lines": {"type": "array", "items": {"type": "object", "properties": {"sku": {"type": "string"}}}}
  },
  "required": ["name"],
  "additionalProperties": false
}`

// omit, as the content of a "<realm>/realm.yaml" entry, stops writeTree
// from adding the file.
const omit = "\x00omit"

// writeTree creates files under a temp dir from a path → content map.
// A path ending in "/" creates an empty directory. Every realm gets an
// empty realm.yaml unless the map sets one.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	withRealms := maps.Clone(files)
	for p := range files {
		realm, rest, nested := strings.Cut(p, "/")
		if _, set := files[realm+"/"+RealmFile]; nested && rest != "" && !set && !strings.HasPrefix(realm, ".") {
			withRealms[realm+"/"+RealmFile] = ""
		}
	}
	for p, content := range withRealms {
		if content == omit {
			continue
		}
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadValidTree(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shop/orders/items/schema.json":     personSchema,
		"shop/orders/clients/schema.json":   `{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"}`,
		"shop/billing/invoices/schema.json": `{}`,
		"blog/main/posts/schema.json":       `{"type": "object"}`,
		"blog/empty-db/":                    "",
		"README.md":                         "ignored",
		".git/":                             "",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(reg.Realms) != 2 {
		t.Errorf("realms = %d, want 2", len(reg.Realms))
	}
	var names []string
	for _, db := range reg.Databases() {
		names = append(names, db.MongoName)
	}
	want := []string{"blog__empty-db", "blog__main", "shop__billing", "shop__orders"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("databases = %v, want %v", names, want)
	}

	c, ok := reg.Collection("shop", "orders", "items")
	if !ok {
		t.Fatal("shop/orders/items not found")
	}
	if c.Schema == nil || c.RawSchema == nil {
		t.Error("schema not compiled")
	}
	wantFields := map[string]Field{
		"name":         {Types: []string{"string"}},
		"age":          {Types: []string{"integer", "null"}},
		"address":      {Types: []string{"object"}},
		"address.city": {Types: []string{"string"}},
		"tags":         {Types: []string{"array"}, ItemTypes: []string{"string"}},
		"lines":        {Types: []string{"array"}, ItemTypes: []string{"object"}},
		"lines.sku":    {Types: []string{"string"}},
	}
	if !reflect.DeepEqual(c.Fields, wantFields) {
		t.Errorf("fields = %v, want %v", c.Fields, wantFields)
	}

	if _, ok := reg.Collection("shop", "orders", "nope"); ok {
		t.Error("unknown collection found")
	}
	if _, ok := reg.Collection("nope", "orders", "items"); ok {
		t.Error("unknown realm found")
	}
}

func TestLoadErrors(t *testing.T) {
	long := strings.Repeat("a", 32)
	tests := []struct {
		name  string
		files map[string]string
		want  []string // substrings expected in the error
	}{
		{"invalid realm name", map[string]string{"Shop/db/c/schema.json": `{}`}, []string{"Shop", "invalid realm name"}},
		{"leading underscore", map[string]string{"_sys/db/c/schema.json": `{}`}, []string{"invalid realm name"}},
		{"double underscore", map[string]string{"shop/a__b/c/schema.json": `{}`}, []string{"invalid database name"}},
		{"invalid collection name", map[string]string{"shop/db/my.items/schema.json": `{}`}, []string{"invalid collection name"}},
		{"realm too long for system db", map[string]string{strings.Repeat("r", 55) + "/db/c/schema.json": `{}`}, []string{"realm name is too long", "___system"}},
		{"mongo name too long", map[string]string{long + "/" + long + "/c/schema.json": `{}`}, []string{"shorter than 64"}},
		{"missing schema", map[string]string{"shop/db/items/indexes.json": `[]`}, []string{"items: collection directory has no schema.json"}},
		{"invalid json", map[string]string{"shop/db/items/schema.json": `{`}, []string{"schema.json: invalid JSON"}},
		{"not an object", map[string]string{"shop/db/items/schema.json": `[]`}, []string{"must be a JSON object"}},
		{"invalid schema", map[string]string{"shop/db/items/schema.json": `{"type": "nope"}`}, []string{"schema.json: invalid schema"}},
		{"other draft", map[string]string{"shop/db/items/schema.json": `{"$schema": "http://json-schema.org/draft-07/schema#"}`}, []string{"draft 2020-12"}},
		{"declares _meta", map[string]string{"shop/db/items/schema.json": `{"properties": {"_meta": {}}}`}, []string{`"_meta" is system-owned`}},
		{"declares id", map[string]string{"shop/db/items/schema.json": `{"properties": {"id": {}}}`}, []string{`"id" is system-owned`}},
		{"requires _id", map[string]string{"shop/db/items/schema.json": `{"required": ["_id"]}`}, []string{`"_id" is system-owned`}},
		{"store date, wrong value", map[string]string{"shop/db/items/schema.json": `{"properties": {"at": {"type": "string", "format": "date-time", "x-backd-store": "datetime"}}}`}, []string{`x-backd-store must be "date"`}},
		{"store date on a number", map[string]string{"shop/db/items/schema.json": `{"properties": {"at": {"type": "integer", "format": "date-time", "x-backd-store": "date"}}}`}, []string{"/properties/at", "needs type string"}},
		{"store date without a type", map[string]string{"shop/db/items/schema.json": `{"properties": {"at": {"format": "date-time", "x-backd-store": "date"}}}`}, []string{"needs type string"}},
		{"store date without format", map[string]string{"shop/db/items/schema.json": `{"properties": {"at": {"type": "string", "x-backd-store": "date"}}}`}, []string{`needs "format": "date-time"`}},
		{"store date with text keywords", map[string]string{"shop/db/items/schema.json": `{"properties": {"at": {"type": "string", "format": "date-time", "x-backd-store": "date", "pattern": "^2", "maxLength": 30}}}`}, []string{`can't be combined with "maxLength"`, `can't be combined with "pattern"`}},
		{"store date in an array", map[string]string{"shop/db/items/schema.json": `{"properties": {"ats": {"type": "array", "items": {"type": "string", "format": "date-time", "x-backd-store": "date"}}}}`}, []string{"/properties/ats/items", "not inside arrays"}},
		{"store date in an object in an array", map[string]string{"shop/db/items/schema.json": `{"properties": {"lines": {"type": "array", "items": {"type": "object", "properties": {"at": {"type": "string", "format": "date-time", "x-backd-store": "date"}}}}}}`}, []string{"not inside arrays"}},
		{"store date under anyOf", map[string]string{"shop/db/items/schema.json": `{"properties": {"at": {"anyOf": [{"type": "string", "format": "date-time", "x-backd-store": "date"}]}}}`}, []string{"not inside arrays"}},
		{"reports all errors", map[string]string{
			"shop/db/a/schema.json": `{`,
			"shop/db/b/schema.json": `{"properties": {"id": {}}}`,
		}, []string{"a/schema.json", "b/schema.json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTree(t, tt.files))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestLoadMissingRoot(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "CONFIG_DIR") {
		t.Errorf("error = %v, want CONFIG_DIR error", err)
	}
}

func TestLoadIndexes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shop/db/items/schema.json": personSchema,
		"shop/db/items/indexes.json": `[
		  {"fields": ["name"], "unique": true},
		  {"fields": ["address.city", "-_meta.created_at"]},
		  {"fields": ["id", "age"]},
		  {"fields": ["-_meta.version"]}
		]`,
		"shop/db/plain/schema.json": `{}`,
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c, _ := reg.Collection("shop", "db", "items")
	want := []Index{
		{Keys: []IndexKey{{Field: "name"}}, Unique: true},
		{Keys: []IndexKey{{Field: "address.city"}, {Field: "_meta.created_at", Desc: true}}},
		{Keys: []IndexKey{{Field: "id"}, {Field: "age"}}},
		{Keys: []IndexKey{{Field: "_meta.version", Desc: true}}},
	}
	if !reflect.DeepEqual(c.Indexes, want) {
		t.Errorf("indexes = %+v\nwant %+v", c.Indexes, want)
	}
	if got := c.Indexes[1].String(); got != "address.city,-_meta.created_at" {
		t.Errorf("String() = %q", got)
	}
	if plain, _ := reg.Collection("shop", "db", "plain"); plain.Indexes != nil {
		t.Errorf("collection without indexes.json has indexes %v", plain.Indexes)
	}
}

func TestLoadIndexesErrors(t *testing.T) {
	tests := []struct{ indexes, want string }{
		{`{`, "invalid index declarations"},
		{`{"fields": ["name"]}`, "invalid index declarations"},
		{`[{"fields": ["name"], "sparse": true}]`, "invalid index declarations"},
		{`[{"fields": []}]`, "at least one field"},
		{`[{"unique": true}]`, "at least one field"},
		{`[{"fields": ["nope"]}]`, `"nope" is not declared`},
		{`[{"fields": ["_meta.other"]}]`, `"_meta.other" is not declared`},
		{`[{"fields": ["name", "-name"]}]`, "appears twice"},
		{`[{"fields": ["name"]}, {"fields": ["name"], "unique": true}]`, "same fields as index 0"},
	}
	for _, tt := range tests {
		t.Run(tt.indexes, func(t *testing.T) {
			_, err := Load(writeTree(t, map[string]string{
				"shop/db/items/schema.json":  personSchema,
				"shop/db/items/indexes.json": tt.indexes,
			}))
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "indexes.json") {
				t.Errorf("error = %v, want it to mention indexes.json and %q", err, tt.want)
			}
		})
	}
}

func TestDateFields(t *testing.T) {
	reg, err := Load(writeTree(t, map[string]string{"shop/db/items/schema.json": `{
	  "type": "object",
	  "properties": {
	    "starts_at": {"type": "string", "format": "date-time", "x-backd-store": "date"},
	    "ends_at":   {"type": ["string", "null"], "format": "date-time", "x-backd-store": "date"},
	    "note":      {"type": "string", "format": "date-time"},
	    "event":     {"type": "object", "properties": {"at": {"type": "string", "format": "date-time", "x-backd-store": "date"}}},
	    "lines":     {"type": "array", "items": {"type": "object", "properties": {"at": {"type": "string", "format": "date-time"}}}}
	  }
	}`}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "db", "items")
	for path, want := range map[string]bool{"starts_at": true, "ends_at": true, "event.at": true, "note": false, "lines.at": false, "event": false, "nope": false} {
		if got := c.DateField(path); got != want {
			t.Errorf("DateField(%q) = %v, want %v", path, got, want)
		}
	}
	// And the value is still validated as an RFC 3339 string.
	if err := c.Schema.Validate(map[string]any{"starts_at": "yesterday"}); err == nil {
		t.Error("a date that isn't RFC 3339 passed the schema")
	}
}
