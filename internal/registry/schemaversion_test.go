package registry

import (
	"slices"
	"strings"
	"testing"
)

const versionSchema = `{"type": "object", "properties": {"name": {"type": "string"}}}`

func versionTree(t *testing.T, collection string, files map[string]string) (*Registry, error) {
	t.Helper()
	tree := map[string]string{}
	for name, content := range files {
		tree["shop/main/items/"+name] = content
	}
	if collection != "" {
		tree["shop/main/items/collection.yaml"] = collection
	}
	return Load(writeTree(t, tree))
}

func TestSchemaVersionFiles(t *testing.T) {
	two := map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": versionSchema, "schema.v2.ts": "export default (d: any) => d;"}
	reg, err := versionTree(t, "", two)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "items")
	if !c.Versioned || c.SchemaVersion != 2 || !slices.Equal(c.SchemaVersions, []int{1, 2}) || !strings.HasSuffix(c.SchemaPath, "schema.v2.json") {
		t.Errorf("versioned=%v version=%d versions=%v path=%s", c.Versioned, c.SchemaVersion, c.SchemaVersions, c.SchemaPath)
	}

	// The lowest kept version needs no migration, and old versions may be deleted.
	reg, err = versionTree(t, "", map[string]string{"schema.v3.json": versionSchema, "schema.v4.json": versionSchema, "schema.v4.ts": "x"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = reg.Collection("shop", "main", "items")
	if c.SchemaVersion != 4 || !slices.Equal(c.SchemaVersions, []int{3, 4}) {
		t.Errorf("version=%d versions=%v", c.SchemaVersion, c.SchemaVersions)
	}

	// A plain schema.json keeps working, unversioned; schema.d.ts is never read.
	reg, err = versionTree(t, "", map[string]string{"schema.json": versionSchema, "schema.d.ts": "export {}"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = reg.Collection("shop", "main", "items")
	if c.Versioned || c.SchemaVersion != 1 {
		t.Errorf("plain schema: versioned=%v version=%d", c.Versioned, c.SchemaVersion)
	}
}

func TestSchemaVersionFileErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"a bad name":               {map[string]string{"schema.v1.json": versionSchema, "schema.V2.json": versionSchema}, "schema.V2.json"},
		"a leading zero":           {map[string]string{"schema.v1.json": versionSchema, "schema.v02.json": versionSchema}, "schema.v02.json"},
		"version 0":                {map[string]string{"schema.v0.json": versionSchema}, "schema.v0.json"},
		"a backup":                 {map[string]string{"schema.v1.json": versionSchema, "schema.v1.json.bak": "x"}, "schema.v1.json.bak"},
		"schema.json and a vN":     {map[string]string{"schema.json": versionSchema, "schema.v1.json": versionSchema}, "rename schema.json to schema.v1.json"},
		"a .ts without its JSON":   {map[string]string{"schema.v1.json": versionSchema, "schema.v3.ts": "x"}, "schema.v3.ts: a migration needs its schema: add schema.v3.json"},
		"only a .ts":               {map[string]string{"schema.v2.ts": "x"}, "schema.v2.ts: a migration needs its schema"},
		"schema.v1.ts":             {map[string]string{"schema.v1.json": versionSchema, "schema.v1.ts": "x"}, "schema.v1.ts: there is no version 0"},
		"a gap in the .ts chain":   {map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": versionSchema, "schema.v3.json": versionSchema, "schema.v3.ts": "x"}, "schema.v2.json: version 2 has no migration"},
		"no schema":                {map[string]string{"collection.yaml": "realtime: true\n"}, "has no schema.json"},
		"an invalid current JSON":  {map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": "{"}, "schema.v2.json"},
		"an invalid current shape": {map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": `{"type": "object", "$ref": "http://x"}`}, "schema.v2.json"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := versionTree(t, "", tc.files)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestOldVersionsAreNotChecked(t *testing.T) {
	// Only the current schema goes through the checks: an old file may be anything that's JSON.
	_, err := versionTree(t, "", map[string]string{"schema.v1.json": `{"$ref": "http://elsewhere/x.json"}`, "schema.v2.json": versionSchema, "schema.v2.ts": "x"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMinSchemaVersion(t *testing.T) {
	files := map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": versionSchema, "schema.v2.ts": "x"}
	for name, tc := range map[string]struct {
		yaml  string
		files map[string]string
		min   int
		err   string
	}{
		"unset":         {"", files, 0, ""},
		"one":           {"min_schema_version: 1\n", files, 1, ""},
		"the current":   {"min_schema_version: 2\n", files, 2, ""},
		"above current": {"min_schema_version: 3\n", files, 0, "out of range"},
		"zero":          {"min_schema_version: 0\n", files, 0, "out of range"},
		"text":          {"min_schema_version: two\n", files, 0, "whole number"},
		"unversioned":   {"min_schema_version: 1\n", map[string]string{"schema.json": versionSchema}, 0, "needs a versioned schema"},
	} {
		t.Run(name, func(t *testing.T) {
			reg, err := versionTree(t, tc.yaml, tc.files)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c, _ := reg.Collection("shop", "main", "items")
			if c.MinSchemaVersion != tc.min {
				t.Errorf("MinSchemaVersion = %d, want %d", c.MinSchemaVersion, tc.min)
			}
		})
	}
}

func TestSchemaVersionIndexAndStamp(t *testing.T) {
	has := func(c *Collection) bool {
		return slices.ContainsFunc(c.Indexes, func(ix Index) bool { return ix.String() == MetaSchemaVersion })
	}
	reg, err := versionTree(t, "", map[string]string{"schema.v1.json": versionSchema, "schema.v2.json": versionSchema, "schema.v2.ts": "x"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "items")
	if !has(c) {
		t.Errorf("indexes = %v, want one on %s", c.Indexes, MetaSchemaVersion)
	}
	meta := map[string]any{}
	c.StampSchemaVersion(meta)
	if meta["schema_version"] != int64(2) {
		t.Errorf("stamp = %v", meta["schema_version"])
	}

	// One version file has nothing to migrate: no index, but still stamped.
	reg, _ = versionTree(t, "", map[string]string{"schema.v1.json": versionSchema})
	c, _ = reg.Collection("shop", "main", "items")
	meta = map[string]any{}
	c.StampSchemaVersion(meta)
	if has(c) || meta["schema_version"] != int64(1) {
		t.Errorf("one version: index=%v stamp=%v", has(c), meta["schema_version"])
	}

	// An unversioned collection stamps nothing.
	reg, _ = versionTree(t, "", map[string]string{"schema.json": versionSchema})
	c, _ = reg.Collection("shop", "main", "items")
	meta = map[string]any{}
	c.StampSchemaVersion(meta)
	if has(c) || len(meta) != 0 {
		t.Errorf("unversioned: index=%v meta=%v", has(c), meta)
	}
}

// Migrations run as jobs, and jobs exist only in realms with auth.
func TestMigrationsNeedAuth(t *testing.T) {
	tree := map[string]string{
		"shop/realm.yaml":                "auth: disabled\n",
		"shop/main/items/schema.v1.json": versionSchema,
		"shop/main/items/schema.v2.json": versionSchema,
		"shop/main/items/schema.v2.ts":   "export default (d: unknown) => d;",
	}
	_, err := Load(writeTree(t, tree))
	if err == nil || !strings.Contains(err.Error(), "schema.v2.ts") || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v, want the .ts file named and auth blamed", err)
	}
	// Versions without migrations still load in such a realm.
	delete(tree, "shop/main/items/schema.v2.ts")
	delete(tree, "shop/main/items/schema.v2.json")
	if _, err := Load(writeTree(t, tree)); err != nil {
		t.Errorf("one version in a realm without auth: %v", err)
	}
}
