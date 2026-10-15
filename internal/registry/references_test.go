package registry

import (
	"strings"
	"testing"
)

const postsSchema = `{
  "type": "object",
  "properties": {
    "title":    {"type": "string"},
    "category": {"type": "string"},
    "tags":     {"type": "array", "items": {"type": "string"}},
    "views":    {"type": "integer"},
    "editor":   {"type": ["string", "null"]},
    "meta":     {"type": "object", "properties": {"ref": {"type": "string"}}},
    "lines":    {"type": "array", "items": {"type": "object", "properties": {"sku": {"type": "string"}}}}
  },
  "additionalProperties": false
}`

// refTree is a realm with posts (which declares references), categories and profiles
// (unique on owner), and a second database crm.
func refTree(t *testing.T, posts string, mutate func(files map[string]string)) (*Registry, error) {
	t.Helper()
	files := map[string]string{
		"shop/realm.yaml":                    "signup: open\n",
		"shop/main/posts/schema.json":        postsSchema,
		"shop/main/posts/collection.yaml":    posts,
		"shop/main/categories/schema.json":   `{"type":"object","properties":{"name":{"type":"string"},"slug":{"type":"string"}},"additionalProperties":false}`,
		"shop/main/categories/indexes.json":  `[{"fields": ["slug"], "unique": true}]`,
		"shop/main/profiles/schema.json":     `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
		"shop/main/profiles/indexes.json":    `[{"fields": ["_meta.owner"], "unique": true}]`,
		"shop/crm/customers/schema.json":     `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
		"shop/crm/customers/collection.yaml": "soft_delete: true\n",
		"shop/crm/customers/indexes.json":    `[{"fields": ["name"], "unique": true}]`,
	}
	if mutate != nil {
		mutate(files)
	}
	return Load(writeTree(t, files))
}

func TestReferencesDeclared(t *testing.T) {
	reg, err := refTree(t, `references:
  category:
    collection: categories
  tags:
    collection: categories
    many: true
    max_expand: 10
  author:
    field: _meta.owner
    collection: profiles
    key: _meta.owner
  by_slug:
    field: meta.ref
    collection: categories
    key: slug
  customer:
    field: editor
    collection: customers
    database: crm
    key: name
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "posts")
	want := map[string]Reference{
		"category": {Name: "category", Field: "category", Database: "main", Collection: "categories", Key: "id"},
		"tags":     {Name: "tags", Field: "tags", Database: "main", Collection: "categories", Key: "id", Many: true, MaxExpand: 10},
		"author":   {Name: "author", Field: "_meta.owner", Database: "main", Collection: "profiles", Key: "_meta.owner"},
		"by_slug":  {Name: "by_slug", Field: "meta.ref", Database: "main", Collection: "categories", Key: "slug"},
		"customer": {Name: "customer", Field: "editor", Database: "crm", Collection: "customers", Key: "name"},
	}
	if len(c.References) != len(want) {
		t.Fatalf("References = %+v", c.References)
	}
	for name, w := range want {
		if got := c.References[name]; got == nil || *got != w {
			t.Errorf("%s = %+v, want %+v", name, got, w)
		}
	}
	if other, _ := reg.Collection("shop", "main", "categories"); other.References != nil {
		t.Errorf("a collection without references has %+v", other.References)
	}
}

func TestReferencesErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml   string
		mutate func(map[string]string)
		want   string
	}{
		"no collection":        {"references:\n  category: {}\n", nil, "references.category: `collection` is required"},
		"unknown collection":   {"references:\n  category:\n    collection: nope\n", nil, `collection "nope" does not exist in database "main"; collections: categories, posts, profiles`},
		"unknown database":     {"references:\n  category:\n    collection: customers\n    database: nope\n", nil, `database "nope" is not a database of realm "shop"`},
		"another realm":        {"references:\n  category:\n    collection: categories\n    database: other/main\n", nil, `is not a database of realm "shop"`},
		"undeclared source":    {"references:\n  writer:\n    collection: profiles\n", nil, `field "writer" is not declared in schema.json`},
		"not a string":         {"references:\n  views:\n    collection: categories\n", nil, `field "views" must be declared as a string`},
		"many on a string":     {"references:\n  category:\n    collection: categories\n    many: true\n", nil, "must be an array whose items are strings"},
		"array without many":   {"references:\n  tags:\n    collection: categories\n", nil, `field "tags" must be declared as a string`},
		"in an array":          {"references:\n  sku:\n    field: lines.sku\n    collection: categories\n", nil, "is inside an array"},
		"owner with many":      {"references:\n  author:\n    field: _meta.owner\n    collection: profiles\n    many: true\n", nil, "field _meta.owner holds one id"},
		"another meta field":   {"references:\n  when:\n    field: _meta.created_at\n    collection: profiles\n", nil, "field _meta.created_at can't be a source"},
		"key without unique":   {"references:\n  category:\n    collection: profiles\n    key: name\n", nil, `key "name" of main/profiles needs a unique index`},
		"key unique but joint": {"references:\n  category:\n    collection: customers\n    database: crm\n    key: _meta.owner\n", nil, `key "_meta.owner" of crm/customers needs a unique index`},
		"name is a field":      {"references:\n  title:\n    field: category\n    collection: categories\n", nil, "the name is also a field of the schema"},
		"bad name":             {"references:\n  my.author:\n    field: category\n    collection: categories\n", nil, "the name may use letters"},
		"max_expand zero":      {"references:\n  tags:\n    collection: categories\n    many: true\n    max_expand: 0\n", nil, "max_expand: must be at least 1"},
		"max_expand one value": {"references:\n  category:\n    collection: categories\n    max_expand: 5\n", nil, "max_expand only applies with `many: true`"},
		"unknown key":          {"references:\n  category:\n    collection: categories\n    on: x\n", nil, "field on not found"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := refTree(t, tc.yaml, tc.mutate)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "posts/collection.yaml") {
				t.Errorf("the error doesn't name the file: %v", err)
			}
		})
	}
}

func TestReferencesKeyOnSoftDeleteTarget(t *testing.T) {
	// soft_delete appends _meta.deleted_at to a unique index; the key still counts as unique.
	_, err := refTree(t, "references:\n  customer:\n    field: editor\n    collection: customers\n    database: crm\n    key: name\n", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckExpandLimits(t *testing.T) {
	reg, err := refTree(t, "references:\n  tags:\n    collection: categories\n    many: true\n    max_expand: 60\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.CheckExpandLimits(100); err != nil {
		t.Fatal(err)
	}
	err = reg.CheckExpandLimits(50)
	if err == nil || !strings.Contains(err.Error(), "references.tags: max_expand: 60 is above this instance's limit of 50 (BACKD_EXPAND_MAX_ARRAY)") {
		t.Fatalf("err = %v", err)
	}
}
