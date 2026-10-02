// Package registry loads the realm/database/collection tree from CONFIG_DIR
// into an immutable, validated registry used by routing, validation and
// provisioning.
package registry

import (
	"cmp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/rules"
)

// Registry is the loaded configuration. It is read-only after Load.
type Registry struct {
	Realms map[string]*Realm
	Root   string // the CONFIG_DIR it was loaded from
}

// Realm groups databases and owns a user pool.
type Realm struct {
	Name      string
	Settings  RealmSettings // from realm.yaml
	Databases map[string]*Database
	// Email holds the parsed email templates of <realm>/email/; nil when the
	// realm doesn't configure email.
	Email *email.Templates
	// Pages are the hosted pages the links in emails open (<realm>/pages/);
	// nil when the realm doesn't configure email.
	Pages *email.Pages
}

// ReservedDirs are folders of a realm that hold something other than a
// database: they are never read as databases.
var ReservedDirs = []string{email.DirName, email.PagesDirName}

// Database is one application's data, stored in one MongoDB database.
type Database struct {
	Realm       string
	Name        string
	MongoName   string // <realm>__<database>
	Collections map[string]*Collection
	Functions   *Functions // from _functions/; nil when the database has none
}

// Collection is a set of documents validated by one JSON Schema.
type Collection struct {
	Realm         string
	Database      string
	MongoDatabase string // <realm>__<database>
	Name          string
	SchemaPath    string             // path of schema.json, for error messages
	RawSchema     map[string]any     // decoded schema.json (numbers as json.Number)
	Schema        *jsonschema.Schema // compiled draft 2020-12 schema
	Fields        map[string]Field   // dot path → field info, for query checks
	Indexes       []Index            // declared in indexes.json
	IndexPath     string             // path of indexes.json, when present
	Rules         *rules.Set         // from rules.yaml; nil allows nothing
	Erasure       *ErasurePolicy     // from collection.yaml; nil: an erase leaves the collection alone
	ErasurePath   string             // path of collection.yaml, for error messages
}

// Index is one index declared in indexes.json.
type Index struct {
	Keys   []IndexKey
	Unique bool
}

// IndexKey is one field of an index, in API terms (e.g. "id", "address.city").
type IndexKey struct {
	Field string
	Desc  bool
}

// String renders the key list in order_by notation, e.g. "status,-_meta.created_at".
func (ix Index) String() string {
	parts := make([]string, len(ix.Keys))
	for i, k := range ix.Keys {
		parts[i] = k.Field
		if k.Desc {
			parts[i] = "-" + k.Field
		}
	}
	return strings.Join(parts, ",")
}

// SystemFields are the server-owned fields that can be queried, sorted
// and indexed in addition to the schema's fields.
var SystemFields = []string{"id", "_meta.created_at", "_meta.updated_at", "_meta.version", "_meta.owner", "_meta.created_by", "_meta.updated_by"}

// IsKnownField reports whether path is a system field or declared in the schema.
func (c *Collection) IsKnownField(path string) bool {
	if slices.Contains(SystemFields, path) {
		return true
	}
	_, ok := c.Fields[path]
	return ok
}

// ScalarField reports whether path holds a single value: a system field,
// or a declared field whose types exclude "array", with no array on the
// way to it.
func (c *Collection) ScalarField(path string) bool {
	if slices.Contains(SystemFields, path) {
		return true
	}
	f, ok := c.Fields[path]
	return ok && len(f.Types) > 0 && !slices.Contains(f.Types, "array") && !c.throughArray(path)
}

// ArrayField reports whether path is declared as an array (only), with no
// array on the way to it.
func (c *Collection) ArrayField(path string) bool {
	f, ok := c.Fields[path]
	return ok && slices.Equal(f.Types, []string{"array"}) && !c.throughArray(path)
}

// throughArray reports whether a parent of path may be an array.
func (c *Collection) throughArray(path string) bool {
	for i := range len(path) {
		if path[i] == '.' {
			if p, ok := c.Fields[path[:i]]; ok && (len(p.Types) == 0 || slices.Contains(p.Types, "array")) {
				return true
			}
		}
	}
	return false
}

// Field describes one property path declared in a collection schema.
type Field struct {
	Types     []string // JSON Schema types; empty when the schema doesn't constrain it
	ItemTypes []string // for arrays: the types of the items, when declared
}

// Has reports whether the field's declared types include t. An
// unconstrained field (no declared types) accepts every type.
func (f Field) Has(t string) bool {
	return len(f.Types) == 0 || slices.Contains(f.Types, t)
}

// Collection returns the collection at realm/database/collection, if configured.
func (r *Registry) Collection(realm, database, collection string) (*Collection, bool) {
	rl, ok := r.Realms[realm]
	if !ok {
		return nil, false
	}
	db, ok := rl.Databases[database]
	if !ok {
		return nil, false
	}
	c, ok := db.Collections[collection]
	return c, ok
}

// SystemDatabaseSuffix is appended to a realm name to form the MongoDB
// database holding the realm's users, sessions and keys.
const SystemDatabaseSuffix = "___system"

// SortedRealms returns every realm, sorted by name.
func (r *Registry) SortedRealms() []*Realm {
	out := make([]*Realm, 0, len(r.Realms))
	for _, rl := range r.Realms {
		out = append(out, rl)
	}
	slices.SortFunc(out, func(a, b *Realm) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Databases returns every database, sorted by MongoDB name.
func (r *Registry) Databases() []*Database {
	var out []*Database
	for _, rl := range r.Realms {
		for _, db := range rl.Databases {
			out = append(out, db)
		}
	}
	slices.SortFunc(out, func(a, b *Database) int { return cmp.Compare(a.MongoName, b.MongoName) })
	return out
}

// SortedCollections returns the database's collections sorted by name.
func (d *Database) SortedCollections() []*Collection {
	out := make([]*Collection, 0, len(d.Collections))
	for _, c := range d.Collections {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *Collection) int { return cmp.Compare(a.Name, b.Name) })
	return out
}
