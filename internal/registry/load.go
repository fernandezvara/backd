package registry

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/rules"
)

// namePattern applies to realm, database, collection and role names. It rules out
// a leading "_" (reserved for the system) and "__" (the realm/database separator).
var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// maxMongoDBNameLen is MongoDB's limit on database name length (exclusive).
const maxMongoDBNameLen = 64

const (
	schemaFile = "schema.json"
	// SchemaFile is the name of a collection's schema file.
	SchemaFile  = schemaFile
	indexesFile = "indexes.json"
	rulesFile   = "rules.yaml"
)

// reservedFields are system-owned and may not be declared by a schema.
var reservedFields = []string{"id", "_id", "_meta"}

var draft2020URIs = map[string]bool{
	"https://json-schema.org/draft/2020-12/schema":  true,
	"https://json-schema.org/draft/2020-12/schema#": true,
}

// Load walks root and builds the registry. Every problem found is reported,
// each naming the offending path.
func Load(root string) (*Registry, error) {
	realmDirs, err := subdirs(root)
	if err != nil {
		return nil, fmt.Errorf("CONFIG_DIR: %w", err)
	}

	reg := &Registry{Realms: map[string]*Realm{}, Root: root}
	var errs []error
	for _, realmName := range realmDirs {
		realmPath := filepath.Join(root, realmName)
		if !namePattern.MatchString(realmName) {
			errs = append(errs, nameError(realmPath, "realm"))
			continue
		}
		if sys := realmName + SystemDatabaseSuffix; len(sys) >= maxMongoDBNameLen {
			errs = append(errs, fmt.Errorf("%s: realm name is too long: its system database name %q must be shorter than %d characters", realmPath, sys, maxMongoDBNameLen))
			continue
		}
		settings, err := loadRealmSettings(realmPath)
		if err != nil {
			errs = append(errs, err)
		}
		realm := &Realm{Name: realmName, Settings: settings, Databases: map[string]*Database{}}
		reg.Realms[realmName] = realm

		dbDirs, err := subdirs(realmPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, dbName := range dbDirs {
			if slices.Contains(ReservedDirs, dbName) {
				continue
			}
			db, dbErrs := loadDatabase(realm, dbName, filepath.Join(realmPath, dbName))
			errs = append(errs, dbErrs...)
			if db != nil {
				realm.Databases[dbName] = db
			}
		}
	}
	for _, realmName := range reg.RealmNames() {
		errs = append(errs, loadEmail(reg.Realms[realmName], filepath.Join(root, realmName))...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return reg, nil
}

// loadEmail checks a realm's email settings against its functions and loads
// its templates.
func loadEmail(rl *Realm, realmPath string) []error {
	es := rl.Settings.Email
	file := filepath.Join(realmPath, RealmFile)
	var errs []error
	if es == nil {
		// A function can't send email through a realm that has none.
		for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
			if db := rl.Databases[dbName]; db.Functions != nil {
				for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
					if db.Functions.Functions[name].Email {
						errs = append(errs, fmt.Errorf("%s: %s/%s has `email: true`, but this realm has no email section (a function sends through the realm's delivery function and templates)", file, dbName, name))
					}
				}
			}
		}
		return errs
	}
	dbName, fnName := es.DatabaseAndName()
	var fn *Function
	if db := rl.Databases[dbName]; db != nil && db.Functions != nil {
		fn = db.Functions.Functions[fnName]
	}
	switch {
	case fn == nil:
		errs = append(errs, fmt.Errorf("%s: email.function: %q is not a function of this realm (the delivery function is _functions/%s in the database %q)", file, es.Function, fnName, dbName))
	case !fn.Internal:
		errs = append(errs, fmt.Errorf("%s: email.function: %s must have `internal: true` in its function.yaml (nobody should call it over HTTP)", file, es.Function))
	case fn.Mode != ModeAsync:
		errs = append(errs, fmt.Errorf("%s: email.function: %s must have `mode: async` in its function.yaml (nothing waits for an email in a request)", file, es.Function))
	}
	if fn != nil {
		rl.Settings.Email.Timeout = fn.Timeout
	}
	tpl, terrs := email.Load(filepath.Join(realmPath, email.DirName), es.Locales)
	rl.Email = tpl
	errs = append(errs, terrs...)
	pages, perrs := email.LoadPages(filepath.Join(realmPath, email.PagesDirName), es.Locales)
	rl.Pages = pages
	return append(errs, perrs...)
}

func loadDatabase(rl *Realm, name, path string) (*Database, []error) {
	realm := rl.Name
	if !namePattern.MatchString(name) {
		return nil, []error{nameError(path, "database")}
	}
	mongoName := realm + "__" + name
	if len(mongoName) >= maxMongoDBNameLen {
		return nil, []error{fmt.Errorf("%s: MongoDB database name %q must be shorter than %d characters", path, mongoName, maxMongoDBNameLen)}
	}
	db := &Database{Realm: realm, Name: name, MongoName: mongoName, Collections: map[string]*Collection{}}

	collDirs, err := subdirs(path)
	if err != nil {
		return nil, []error{err}
	}
	var errs []error
	for _, collName := range collDirs {
		if collName == FunctionsDir {
			continue
		}
		collPath := filepath.Join(path, collName)
		if !namePattern.MatchString(collName) {
			errs = append(errs, nameError(collPath, "collection"))
			continue
		}
		c, err := loadCollection(db, rl.Settings, collName, collPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		db.Collections[collName] = c
	}
	fns, fnErrs := loadFunctions(db, rl.Settings, path)
	errs = append(errs, fnErrs...)
	db.Functions = fns
	return db, errs
}

func loadCollection(db *Database, settings RealmSettings, name, dir string) (*Collection, error) {
	path := filepath.Join(dir, schemaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: collection directory has no %s", dir, schemaFile)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", path, err)
	}
	raw, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: schema must be a JSON object", path)
	}
	if err := checkSchema(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if errs := checkStore(raw); len(errs) > 0 {
		return nil, fmt.Errorf("%s: %w", path, errors.Join(errs...))
	}

	// File fields are declared in collection.yaml, and backd adds their schema: from
	// here on the collection's schema is the author's plus theirs.
	files, err := parseFiles(filepath.Join(dir, CollectionFile), raw, settings)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		raw = withFileSchemas(raw, files)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "file://" + filepath.ToSlash(abs)
	if err := c.AddResource(url, raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid schema: %w", path, err)
	}

	coll := &Collection{
		Realm:         db.Realm,
		Database:      db.Name,
		MongoDatabase: db.MongoName,
		Name:          name,
		SchemaPath:    path,
		RawSchema:     raw,
		Schema:        compiled,
		Fields:        fieldIndex(raw),
		Files:         files,
	}
	if err := loadIndexes(coll, filepath.Join(dir, indexesFile)); err != nil {
		return nil, err
	}
	if err := loadRules(coll, settings, filepath.Join(dir, rulesFile)); err != nil {
		return nil, err
	}
	if err := loadErasure(coll, settings, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if coll.SoftDelete == nil && coll.Rules != nil {
		for _, op := range []rules.Op{rules.Restore, rules.Purge} {
			if coll.Rules.For(op) != nil {
				return nil, fmt.Errorf("%s: a %s rule only applies to a collection that soft-deletes; add `soft_delete: true` to %s", filepath.Join(dir, rulesFile), op, filepath.Join(dir, CollectionFile))
			}
		}
	}
	coll.addSoftDeleteIndexes()
	return coll, nil
}

// loadRules compiles the optional rules.yaml against the collection's
// fields and the realm's roles.
func loadRules(c *Collection, settings RealmSettings, path string) error {
	if !settings.AuthEnabled {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s: rules only apply when the realm has auth enabled (realm.yaml has `auth: disabled`)", path)
		}
		return nil
	}
	roles := make([]string, 0, len(settings.Roles))
	for r := range settings.Roles {
		roles = append(roles, r)
	}
	set, err := rules.Load(path, rules.Schema{
		DocumentField: c.IsKnownField,
		DataField:     func(p string) bool { _, ok := c.Fields[p]; return ok },
		ScalarField:   c.ScalarField,
		ArrayField:    c.ArrayField,
		DateField:     c.DateField,
		Roles:         roles,
	})
	if err != nil {
		return err
	}
	c.Rules = set
	return nil
}

// checkSchema enforces backd's rules on top of JSON Schema validity.
func checkSchema(raw map[string]any) error {
	if v, ok := raw["$schema"]; ok {
		s, _ := v.(string)
		if !draft2020URIs[s] {
			return fmt.Errorf("$schema must be JSON Schema draft 2020-12 (or omitted), got %v", v)
		}
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		for _, f := range reservedFields {
			if _, ok := props[f]; ok {
				return fmt.Errorf("property %q is system-owned and must not be declared", f)
			}
		}
	}
	if req, ok := raw["required"].([]any); ok {
		for _, r := range req {
			if s, _ := r.(string); isReserved(s) {
				return fmt.Errorf("property %q is system-owned and must not be required", s)
			}
		}
	}
	return nil
}

func isReserved(name string) bool {
	for _, f := range reservedFields {
		if name == f {
			return true
		}
	}
	return false
}

// StoreKeyword is the schema.json keyword, on a property, that picks how its
// value is stored. The only choice is StoreDate: a `format: date-time` string
// is stored as a BSON Date, so queries and sorting compare dates.
const (
	StoreKeyword = "x-backd-store"
	StoreDate    = "date"
)

// checkStore enforces where StoreKeyword may appear and what it needs: a
// declared property (not inside an array, and not in a $defs, allOf...) whose
// type is a string, perhaps null, with format date-time, and no string keywords
// that mean nothing for a date.
func checkStore(raw map[string]any) []error {
	var errs []error
	var walk func(node any, pointer string)
	supported := regexp.MustCompile(`^(/properties/[^/]+)+$`)
	walk = func(node any, pointer string) {
		switch n := node.(type) {
		case map[string]any:
			if v, has := n[StoreKeyword]; has {
				switch {
				case v != StoreDate:
					errs = append(errs, fmt.Errorf("%s: %s must be %q, got %v", pointer, StoreKeyword, StoreDate, v))
				case !supported.MatchString(pointer):
					errs = append(errs, fmt.Errorf("%s: %s is for a declared property (properties, nested in objects); not inside arrays, $defs or allOf/anyOf/oneOf/not", pointer, StoreKeyword))
				default:
					errs = append(errs, checkDateProperty(pointer, n)...)
				}
			}
			for k, v := range n {
				walk(v, pointer+"/"+escapePointer(k))
			}
		case []any:
			for i, v := range n {
				walk(v, pointer+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(raw, "")
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errs
}

func checkDateProperty(pointer string, n map[string]any) []error {
	var errs []error
	types := schemaTypes(n)
	if len(types) == 0 || !slices.Contains(types, "string") || slices.ContainsFunc(types, func(t string) bool { return t != "string" && t != "null" }) {
		errs = append(errs, fmt.Errorf("%s: %s: %s needs type string (or [string, null])", pointer, StoreDate, StoreKeyword))
	}
	if n["format"] != "date-time" {
		errs = append(errs, fmt.Errorf(`%s: %s: %s needs "format": "date-time"`, pointer, StoreDate, StoreKeyword))
	}
	for _, k := range []string{"pattern", "minLength", "maxLength", "enum", "const"} {
		if _, has := n[k]; has {
			errs = append(errs, fmt.Errorf("%s: %s can't be combined with %q: the value is stored as a date, not as text", pointer, StoreKeyword, k))
		}
	}
	return errs
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// fieldIndex maps each declared property (nested via dot paths) to its
// types. Properties of objects inside arrays are included under the
// array's path, matching MongoDB's dot notation (e.g. "lines.sku").
func fieldIndex(schema map[string]any) map[string]Field {
	out := map[string]Field{}
	var walk func(prefix string, s map[string]any, inItems bool)
	walk = func(prefix string, s map[string]any, inItems bool) {
		props, _ := s["properties"].(map[string]any)
		for name, v := range props {
			ps, _ := v.(map[string]any)
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			f := Field{Types: schemaTypes(ps), Date: !inItems && ps[StoreKeyword] == StoreDate}
			items, _ := ps["items"].(map[string]any)
			if items != nil {
				f.ItemTypes = schemaTypes(items)
			}
			if _, exists := out[path]; !exists {
				out[path] = f
			}
			walk(path, ps, inItems)
			if items != nil {
				walk(path, items, true)
			}
		}
	}
	walk("", schema, false)
	return out
}

func schemaTypes(s map[string]any) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// subdirs lists the directories directly under path, skipping hidden entries.
// Plain files are ignored.
func subdirs(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ValidName reports whether name is a valid realm, database or collection name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

func nameError(path, kind string) error {
	return fmt.Errorf("%s: invalid %s name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", path, kind, filepath.Base(path))
}
