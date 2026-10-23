package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExpandLimits are what a reference expansion may cost on this instance (BACKD_EXPAND_MAX_*);
// a reference may only lower MaxArray with its max_expand.
type ExpandLimits struct {
	MaxDepth int // levels of references followed, at most 2
	MaxPaths int // entries of one `expand`
	MaxArray int // ids of one `many` reference followed
	MaxDocs  int // expanded documents in one response
}

// DefaultExpandLimits are the limits when the instance sets none.
var DefaultExpandLimits = ExpandLimits{MaxDepth: 2, MaxPaths: 5, MaxArray: 100, MaxDocs: 2000}

// DefaultExpandMaxArray is the most ids of one `many` reference an expansion follows when the
// instance sets no other value (BACKD_EXPAND_MAX_ARRAY).
const DefaultExpandMaxArray = 100

// OrDefault fills the limits that are zero with the defaults.
func (l ExpandLimits) OrDefault() ExpandLimits {
	d := DefaultExpandLimits
	if l.MaxDepth > 0 {
		d.MaxDepth = l.MaxDepth
	}
	if l.MaxPaths > 0 {
		d.MaxPaths = l.MaxPaths
	}
	if l.MaxArray > 0 {
		d.MaxArray = l.MaxArray
	}
	if l.MaxDocs > 0 {
		d.MaxDocs = l.MaxDocs
	}
	return d
}

// referenceName is what a reference may be called: it appears in `expand=` paths, which use
// `.`, `(`, `)` and `,` as syntax, so none of those can be part of it.
var referenceName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// ReferenceSources are the system fields a reference may read its value from.
var ReferenceSources = []string{"_meta.owner", "_meta.created_by"}

// Reference is one declared relation of collection.yaml's `references:`: the value of Field
// in this collection is the Key of a document of Database/Collection of the same realm.
type Reference struct {
	Name       string
	Field      string // source field in this collection
	Database   string // target database
	Collection string // target collection
	Key        string // target field that equals the source value: "id" or a uniquely indexed field
	Many       bool   // the source is an array of values
	// MaxExpand lowers the instance's per-array cap for this reference; 0 uses the instance's.
	MaxExpand int
}

type referenceDoc struct {
	Field      string `yaml:"field"`
	Database   string `yaml:"database"`
	Collection string `yaml:"collection"`
	Key        string `yaml:"key"`
	Many       bool   `yaml:"many"`
	MaxExpand  *int   `yaml:"max_expand"`
}

// loadReferences reads collection.yaml's references: into c and checks what only the
// collection itself can tell: the source of each reference and its name. The targets are
// checked by checkReferences once the whole realm is loaded.
func loadReferences(c *Collection, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		References map[string]*referenceDoc `yaml:"references"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		// Another section's mistake is reported by the loader that reads it.
		if strings.Contains(err.Error(), "references") || strings.Contains(err.Error(), "max_expand") {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(doc.References)) {
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: references.%s: "+format, append([]any{path, name}, args...)...))
		}
		d := doc.References[name]
		if d == nil {
			d = &referenceDoc{}
		}
		if !referenceName.MatchString(name) {
			fail("the name may use letters, digits and _, starting with a letter: it appears in expand= paths")
			continue
		}
		if d.Collection == "" {
			fail("`collection` is required: the collection the values point to")
			continue
		}
		ref := &Reference{Name: name, Field: d.Field, Database: d.Database, Collection: d.Collection, Key: d.Key, Many: d.Many}
		if ref.Field == "" {
			ref.Field = name
		}
		if ref.Database == "" {
			ref.Database = c.Database
		}
		if ref.Key == "" {
			ref.Key = "id"
		}
		if d.MaxExpand != nil {
			if *d.MaxExpand < 1 {
				fail("max_expand: must be at least 1, got %d", *d.MaxExpand)
				continue
			}
			if !ref.Many {
				fail("max_expand only applies with `many: true`")
				continue
			}
			ref.MaxExpand = *d.MaxExpand
		}
		if why := c.referenceSourceProblem(ref); why != "" {
			fail("%s", why)
			continue
		}
		if _, declared := c.Fields[name]; declared && name != ref.Field {
			fail("the name is also a field of the schema; rename the reference (it may share its name only with its own `field`)")
			continue
		}
		if c.References == nil {
			c.References = map[string]*Reference{}
		}
		c.References[name] = ref
	}
	return errors.Join(errs...)
}

// referenceSourceProblem says why ref's source field can't hold references, or "" when it can.
func (c *Collection) referenceSourceProblem(ref *Reference) string {
	if slices.Contains(ReferenceSources, ref.Field) {
		if ref.Many {
			return fmt.Sprintf("field %s holds one id: remove `many: true`", ref.Field)
		}
		return ""
	}
	if strings.HasPrefix(ref.Field, "_meta.") || ref.Field == "id" {
		return fmt.Sprintf("field %s can't be a source: use a declared string field, %s", ref.Field, strings.Join(ReferenceSources, " or "))
	}
	f, ok := c.Fields[ref.Field]
	if !ok {
		return fmt.Sprintf("field %q is not declared in schema.json: declare it as a string%s, or set `field` to one that is", ref.Field, map[bool]string{true: " array", false: ""}[ref.Many])
	}
	if c.throughArray(ref.Field) {
		return fmt.Sprintf("field %q is inside an array: a reference reads one value or an array of values of its own", ref.Field)
	}
	if ref.Many {
		if !slices.Equal(f.Types, []string{"array"}) || !slices.Equal(f.ItemTypes, []string{"string"}) {
			return fmt.Sprintf("with `many: true`, field %q must be an array whose items are strings in schema.json", ref.Field)
		}
		return ""
	}
	if !stringOnly(f.Types) {
		return fmt.Sprintf("field %q must be declared as a string in schema.json (use `many: true` for an array of strings)", ref.Field)
	}
	return ""
}

// stringOnly reports whether types is a string, possibly also null.
func stringOnly(types []string) bool {
	return slices.Contains(types, "string") && !slices.ContainsFunc(types, func(t string) bool { return t != "string" && t != "null" })
}

// checkReferences checks the references of every collection of a realm against the
// realm's databases: the target must exist, and a `key` other than id must be unique.
func checkReferences(rl *Realm) []error {
	var errs []error
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		db := rl.Databases[dbName]
		for _, c := range db.SortedCollections() {
			for _, name := range slices.Sorted(maps.Keys(c.References)) {
				ref := c.References[name]
				where := fmt.Sprintf("%s: references.%s", c.ConfigPath(), name)
				tdb := rl.Databases[ref.Database]
				if tdb == nil {
					errs = append(errs, fmt.Errorf("%s: database %q is not a database of realm %q (the target must be in the same realm); databases: %s", where, ref.Database, rl.Name, strings.Join(slices.Sorted(maps.Keys(rl.Databases)), ", ")))
					continue
				}
				target := tdb.Collections[ref.Collection]
				if target == nil {
					errs = append(errs, fmt.Errorf("%s: collection %q does not exist in database %q; collections: %s", where, ref.Collection, ref.Database, strings.Join(slices.Sorted(maps.Keys(tdb.Collections)), ", ")))
					continue
				}
				if target.IsEncrypted(ref.Key) {
					errs = append(errs, fmt.Errorf("%s: key %q of %s/%s is an encrypted field, and expanding a reference needs the plain value", where, ref.Key, ref.Database, ref.Collection))
					continue
				}
				if ref.Key != "id" && !target.hasUniqueIndexOn(ref.Key) {
					errs = append(errs, fmt.Errorf("%s: key %q of %s/%s needs a unique index of its own in its indexes.json, so each value matches at most one document: add {\"fields\": [%q], \"unique\": true}", where, ref.Key, ref.Database, ref.Collection, ref.Key))
				}
			}
		}
	}
	return errs
}

// hasUniqueIndexOn reports whether a unique index of the collection has path as its only key.
func (c *Collection) hasUniqueIndexOn(path string) bool {
	return slices.ContainsFunc(c.Indexes, func(ix Index) bool {
		keys := ix.Keys
		// soft_delete adds _meta.deleted_at as the last key of a unique index.
		if n := len(keys); n > 1 && keys[n-1].Field == MetaDeletedAt {
			keys = keys[:n-1]
		}
		return ix.Unique && !ix.TTL && len(keys) == 1 && keys[0].Field == path
	})
}

// ConfigPath is the collection.yaml of c, for messages.
func (c *Collection) ConfigPath() string {
	if c.ErasurePath != "" {
		return c.ErasurePath
	}
	return c.Realm + "/" + c.Database + "/" + c.Name + "/" + CollectionFile
}

// CheckExpandLimits fails for a reference whose max_expand is above what the instance
// follows per array (BACKD_EXPAND_MAX_ARRAY): a reference may only lower it.
func (r *Registry) CheckExpandLimits(instanceMaxArray int) error {
	var errs []error
	for _, db := range r.Databases() {
		for _, c := range db.SortedCollections() {
			for _, name := range slices.Sorted(maps.Keys(c.References)) {
				if m := c.References[name].MaxExpand; m > instanceMaxArray {
					errs = append(errs, fmt.Errorf("%s: references.%s: max_expand: %d is above this instance's limit of %d (BACKD_EXPAND_MAX_ARRAY); a reference can only lower it", c.ConfigPath(), name, m, instanceMaxArray))
				}
			}
		}
	}
	return errors.Join(errs...)
}
