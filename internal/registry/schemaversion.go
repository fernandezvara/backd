package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// schemaVersionPattern names the version files of a collection: schema.v<N>.json (the schema)
// and schema.v<N>.ts (the migration from N-1). N is a positive integer without leading zeros.
var schemaVersionPattern = regexp.MustCompile(`^schema\.v([1-9][0-9]*)\.(json|ts)$`)

// generatedTypesFile is written by `backd types` and never read by the server.
const generatedTypesFile = "schema.d.ts"

// SchemaFiles is what a collection directory holds of its schema (design/schema-versioning.md §3).
type SchemaFiles struct {
	// Current is the file the server compiles: schema.json, or the highest schema.vN.json.
	Current string
	// Version is the current version: N of the highest schema.vN.json, 1 for a plain schema.json.
	Version int
	// Versioned is true when the collection uses schema.vN.json files.
	Versioned bool
	// Versions lists the N of every kept schema.vN.json, ascending (empty when unversioned).
	Versions []int
	// Migrations lists the N of every schema.vN.ts, ascending: the step from N-1 to N.
	Migrations []int
}

// SchemaVersionFile is the name of version n's schema file.
func SchemaVersionFile(n int) string { return "schema.v" + strconv.Itoa(n) + ".json" }

// SchemaMigrationFile is the name of the migration to version n.
func SchemaMigrationFile(n int) string { return "schema.v" + strconv.Itoa(n) + ".ts" }

// findSchemaFiles reads a collection directory's schema files and applies the rules of the
// design's §3. Each error names the file.
func findSchemaFiles(dir string) (*SchemaFiles, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	var plain bool
	jsons := map[int]bool{}
	tss := map[int]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "schema.") {
			continue
		}
		switch name {
		case schemaFile:
			plain = true
		case generatedTypesFile:
		default:
			m := schemaVersionPattern.FindStringSubmatch(name)
			if m == nil {
				return nil, fmt.Errorf("%s: %q is not a schema file name: use schema.json, or schema.v<N>.json and schema.v<N>.ts with N a positive number without leading zeros (schema.d.ts is generated)", filepath.Join(dir, name), name)
			}
			n, _ := strconv.Atoi(m[1])
			if m[2] == "json" {
				jsons[n] = true
			} else {
				tss[n] = true
			}
		}
	}

	out := &SchemaFiles{}
	for n := range jsons {
		out.Versions = append(out.Versions, n)
	}
	for n := range tss {
		out.Migrations = append(out.Migrations, n)
	}
	slices.Sort(out.Versions)
	slices.Sort(out.Migrations)

	if len(out.Versions) == 0 {
		if len(out.Migrations) > 0 {
			n := out.Migrations[0]
			return nil, fmt.Errorf("%s: a migration needs its schema: add %s", filepath.Join(dir, SchemaMigrationFile(n)), SchemaVersionFile(n))
		}
		if !plain {
			return nil, fmt.Errorf("%s: collection directory has no %s", dir, schemaFile)
		}
		out.Current, out.Version = filepath.Join(dir, schemaFile), 1
		return out, nil
	}

	if plain {
		return nil, fmt.Errorf("%s: schema.json can't be next to %s: rename schema.json to %s", filepath.Join(dir, schemaFile), SchemaVersionFile(out.Versions[0]), SchemaVersionFile(1))
	}
	for _, n := range out.Migrations {
		if n == 1 {
			return nil, fmt.Errorf("%s: there is no version 0 to migrate from: delete it", filepath.Join(dir, SchemaMigrationFile(1)))
		}
		if !jsons[n] {
			return nil, fmt.Errorf("%s: a migration needs its schema: add %s", filepath.Join(dir, SchemaMigrationFile(n)), SchemaVersionFile(n))
		}
	}
	// The chain of migrations is continuous from the lowest kept version up to the current one.
	lowest := out.Versions[0]
	for _, n := range out.Versions {
		if n > lowest && !tss[n] {
			return nil, fmt.Errorf("%s: version %d has no migration from the one before it: add %s (the lowest kept version, %d, needs none)", filepath.Join(dir, SchemaVersionFile(n)), n, SchemaMigrationFile(n), lowest)
		}
	}
	out.Versioned = true
	out.Version = out.Versions[len(out.Versions)-1]
	out.Current = filepath.Join(dir, SchemaVersionFile(out.Version))
	return out, nil
}

// loadSchemaVersion reads collection.yaml's `min_schema_version:` into c: from 1 to the
// current version. It is enforced by the request path (design §10.3).
func loadSchemaVersion(c *Collection, path string, data []byte) error {
	if data == nil {
		return nil
	}
	var doc struct {
		Min any `yaml:"min_schema_version"`
	}
	if ok, err := decodeSection(data, path, &doc); !ok || err != nil {
		return err
	}
	if doc.Min == nil {
		return nil
	}
	n, ok := doc.Min.(int)
	if !ok {
		return fmt.Errorf("%s: min_schema_version must be a whole number from 1 to %d, got %v", path, c.SchemaVersion, doc.Min)
	}
	if !c.Versioned {
		return fmt.Errorf("%s: min_schema_version needs a versioned schema: rename schema.json to %s", path, SchemaVersionFile(1))
	}
	if n < 1 || n > c.SchemaVersion {
		return fmt.Errorf("%s: min_schema_version %d is out of range: it must be from 1 to the current version, %d", path, n, c.SchemaVersion)
	}
	c.MinSchemaVersion = n
	return nil
}

// HasSchema reports whether dir holds a collection's schema: a schema.json or a schema.vN.json.
func HasSchema(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && (n == schemaFile || (schemaVersionPattern.MatchString(n) && strings.HasSuffix(n, ".json"))) {
			return true
		}
	}
	return false
}

// StampSchemaVersion sets meta's schema_version to the collection's current version when it
// has versioned schema files (an unversioned collection stamps nothing). Writes call it for the
// content they store: create, PUT, PATCH and the batch operations.
func (c *Collection) StampSchemaVersion(meta map[string]any) {
	if c.Versioned {
		meta["schema_version"] = int64(c.SchemaVersion)
	}
}

// MetaSchemaVersion is the stamp of the schema version a document was written under.
const MetaSchemaVersion = "_meta.schema_version"

// addSchemaVersionIndex adds the index the migration job and its count query (design §7): only
// on a versioned collection with more than one version file, as the one that has a single
// version has nothing to migrate.
func (c *Collection) addSchemaVersionIndex() {
	if !c.Versioned || len(c.SchemaVersions) < 2 {
		return
	}
	ix := Index{Keys: []IndexKey{{Field: MetaSchemaVersion}}}
	if !slices.ContainsFunc(c.Indexes, func(have Index) bool { return have.String() == ix.String() && !have.Unique }) {
		c.Indexes = append(c.Indexes, ix)
	}
}
