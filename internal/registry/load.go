package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/push"
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
	// retiredRulesFile held a collection's rules before they became the `rules:` section of
	// collection.yaml; a collection directory that still has one does not load.
	retiredRulesFile = "rules.yaml"
)

// reservedFields are system-owned and may not be declared by a schema.
var reservedFields = []string{"id", "_id", "_meta", "_expanded", TextSearchField, BlindRoot, EncRoot}

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
	// Realms laid out the old way: everything that would follow from the missing templates,
	// texts and configuration is left out of the report, the folders to move are the news.
	oldLayout := map[string]bool{}
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
			if err := legacyFolderError(filepath.Join(realmPath, dbName)); err != nil {
				errs = append(errs, err)
				oldLayout[realmName] = true
			}
		}
		if oldLayout[realmName] {
			continue
		}
		// Before the databases: their rules may read the configuration.
		errs = append(errs, loadAppConfig(realm, realmPath)...)

		for _, dbName := range dbDirs {
			if dbName == RealmDir {
				errs = append(errs, checkRealmFolder(filepath.Join(realmPath, dbName))...)
				continue
			}
			if strings.HasPrefix(dbName, "_") {
				errs = append(errs, reservedNameError(filepath.Join(realmPath, dbName), "a realm", RealmDir))
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
		if oldLayout[realmName] {
			continue
		}
		errs = append(errs, checkReferences(reg.Realms[realmName])...)
		errs = append(errs, loadEmail(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadOnSignup(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadOrgHooks(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadTriggerFunctions(reg.Realms[realmName])...)
		errs = append(errs, loadDataHooks(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadCustomizable(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadPush(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadConsents(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, loadExportReadmes(reg.Realms[realmName], filepath.Join(root, realmName))...)
		errs = append(errs, checkConfigGrants(reg.Realms[realmName], filepath.Join(root, realmName))...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return reg, nil
}

// loadPush loads a realm's push templates and checks what functions declare against them.
func loadPush(rl *Realm, realmPath string) []error {
	file := filepath.Join(realmPath, RealmFile)
	var errs []error
	if rl.Settings.Push == nil {
		// A function can't send pushes through a realm that has none.
		for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
			if db := rl.Databases[dbName]; db.Functions != nil {
				for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
					if db.Functions.Functions[name].Push != nil {
						errs = append(errs, fmt.Errorf("%s: %s/%s declares `push`, but this realm has no push section (a function sends through the realm's providers and templates)", file, dbName, name))
					}
				}
			}
		}
		return errs
	}
	_, locales := rl.Settings.Languages()
	tpl, terrs := push.Load(RealmPath(realmPath, push.DirName), locales)
	rl.Push = tpl
	errs = append(errs, terrs...)
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		db := rl.Databases[dbName]
		if db.Functions == nil {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(db.Functions.Functions)) {
			fp := db.Functions.Functions[name].Push
			if fp == nil {
				continue
			}
			for _, kind := range fp.Kinds {
				if !tpl.HasKind(kind) {
					errs = append(errs, fmt.Errorf("%s: %s/%s declares the push kind %q, which has no templates (%s/%s/<locale>.yaml for every language of the realm)", file, dbName, name, kind, filepath.Join(RealmDir, push.DirName), kind))
				}
			}
		}
	}
	return errs
}

// hookFunction finds the function a realm setting names (<database>/<function>) and checks
// that nobody can call it over HTTP and that it has the mode the setting needs. why says,
// for the message, what that mode is for.
func hookFunction(rl *Realm, file, setting, ref, mode, why string) (*Function, error) {
	dbName, fnName, _ := strings.Cut(ref, "/")
	var fn *Function
	if db := rl.Databases[dbName]; db != nil && db.Functions != nil {
		fn = db.Functions.Functions[fnName]
	}
	switch {
	case fn == nil:
		return nil, fmt.Errorf("%s: %s: %q is not a function of this realm (it is _functions/%s in the database %q)", file, setting, ref, fnName, dbName)
	case !fn.Internal:
		return nil, fmt.Errorf("%s: %s: %s must have `internal: true` in its function.yaml (nobody should call it over HTTP)", file, setting, ref)
	case fn.Mode != mode:
		return nil, fmt.Errorf("%s: %s: %s must have `mode: %s` in its function.yaml (%s)", file, setting, ref, mode, why)
	}
	return fn, nil
}

// loadOnSignup checks that account.on_signup names a function that can run in the background
// without anyone calling it: internal and async.
func loadOnSignup(rl *Realm, realmPath string) []error {
	a := &rl.Settings.Account
	if a.OnSignup == "" {
		return nil
	}
	fn, err := hookFunction(rl, filepath.Join(realmPath, RealmFile), "account.on_signup", a.OnSignup, ModeAsync, "nothing waits for it while someone signs up")
	if err != nil {
		return []error{err}
	}
	a.OnSignupTimeout = fn.Timeout
	return nil
}

// loadOrgHooks checks that each of organizations.hooks names an internal, async function.
func loadOrgHooks(rl *Realm, realmPath string) []error {
	o := rl.Settings.Organizations
	if o == nil {
		return nil
	}
	var errs []error
	file := filepath.Join(realmPath, RealmFile)
	for _, h := range o.Hooks.All() {
		if h.Hook.Function == "" {
			continue
		}
		fn, err := hookFunction(rl, file, "organizations.hooks."+h.Key, h.Hook.Function, ModeAsync, "nothing waits for it while membership changes")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		h.Hook.Timeout = fn.Timeout
	}
	return errs
}

// loadDataHooks checks that email.data_hook and pages.data_hook name internal, sync functions:
// backd waits for their answer before it renders a message or a page.
func loadDataHooks(rl *Realm, realmPath string) []error {
	file := filepath.Join(realmPath, RealmFile)
	var errs []error
	check := func(setting, ref string) {
		if ref == "" {
			return
		}
		if _, err := hookFunction(rl, file, setting, ref, ModeSync, "backd waits for its answer to fill in the template data"); err != nil {
			errs = append(errs, err)
		}
	}
	if e := rl.Settings.Email; e != nil {
		check("email.data_hook", e.DataHook)
	}
	check("pages.data_hook", rl.Settings.PagesDataHook)
	return errs
}

// loadCustomizable checks that every kind of email.customizable has a template (the realm's own
// text is what a company's version replaces), and that the realm has organizations, which own the
// overrides.
func loadCustomizable(rl *Realm, realmPath string) []error {
	e := rl.Settings.Email
	if e == nil || len(e.Customizable) == 0 {
		return nil
	}
	file := filepath.Join(realmPath, RealmFile)
	if rl.Settings.Organizations == nil {
		return []error{fmt.Errorf("%s: email.customizable: needs the realm to enable organizations (an `organizations:` section): companies own the overrides", file)}
	}
	def, _ := rl.Settings.Languages()
	var errs []error
	for _, kind := range e.Customizable {
		if !email.ValidKind(kind) || rl.Email == nil || !rl.Email.Has(kind, def) {
			errs = append(errs, fmt.Errorf("%s: email.customizable: %q is not a kind of this realm's email folder (%s has the templates a company's version replaces)", file, kind, filepath.Join(RealmDir, email.DirName, kind)+"/"))
		}
	}
	return errs
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
	_, locales := rl.Settings.Languages()
	tpl, terrs := email.Load(RealmPath(realmPath, email.DirName), locales)
	rl.Email = tpl
	errs = append(errs, terrs...)
	pages, perrs := email.LoadPages(RealmPath(realmPath, email.PagesDirName), locales)
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
	db := &Database{Realm: realm, Name: name, MongoName: mongoName, Collections: map[string]*Collection{}, appConfig: rl.AppConfig}

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
		if strings.HasPrefix(collName, "_") {
			errs = append(errs, reservedNameError(collPath, "a database", FunctionsDir))
			continue
		}
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
		configLeaf:    db.configLeaf(),
	}
	// The id and organization policies come first: indexes and rules may name `_meta.org`.
	if err := loadCustomID(coll, settings, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := loadOrganization(coll, settings, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := loadEncryption(coll, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := loadIndexes(coll, filepath.Join(dir, indexesFile)); err != nil {
		return nil, err
	}
	if err := loadRules(coll, settings, dir); err != nil {
		return nil, err
	}
	if err := loadErasure(coll, settings, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := loadReferences(coll, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := loadAggregate(coll, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := loadRealtime(coll, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if coll.Realtime {
		if err := checkRealtimeRead(coll, filepath.Join(dir, CollectionFile)); err != nil {
			return nil, err
		}
	}
	if err := loadTriggers(coll, settings, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if err := checkEncryption(coll, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	coll.addBlindIndexes()
	if err := loadReveal(coll, settings, filepath.Join(dir, CollectionFile)); err != nil {
		return nil, err
	}
	if coll.SoftDelete == nil && coll.Rules != nil {
		for _, op := range []rules.Op{rules.Restore, rules.Purge} {
			if coll.Rules.For(op) != nil {
				return nil, fmt.Errorf("%s: a %s rule only applies to a collection that soft-deletes; add `soft_delete: true` to %s", filepath.Join(dir, CollectionFile), op, filepath.Join(dir, CollectionFile))
			}
		}
	}
	coll.addSoftDeleteIndexes()
	coll.addDefaultOrderIndex()
	coll.addOrgIndex()
	return coll, nil
}

// rulesDoc is the part of collection.yaml loadRules reads; the other sections are read
// (and checked) elsewhere.
type rulesDoc struct {
	Rules map[string]string `yaml:"rules"`
}

// loadRules compiles the optional `rules:` section of the collection's collection.yaml
// against the collection's fields and the realm's roles. A collection without it has no
// rules: nothing is allowed.
func loadRules(c *Collection, settings RealmSettings, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, retiredRulesFile)); err == nil {
		return fmt.Errorf("%s: rules.yaml is not part of the configuration any more: move its content under a `rules:` key in %s (indent it by two spaces) and delete it", filepath.Join(dir, retiredRulesFile), CollectionFile)
	}
	path := filepath.Join(dir, CollectionFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc rulesDoc
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: invalid YAML in rules: want a mapping of operation to expression string: %w", path, err)
	}
	if doc.Rules == nil {
		return nil
	}
	if !settings.AuthEnabled {
		return fmt.Errorf("%s: rules only apply when the realm has auth enabled (realm.yaml has `auth: disabled`)", path)
	}
	set, errs := rules.Parse(doc.Rules, c.ruleSchema(settings))
	if len(errs) > 0 {
		for i, e := range errs {
			errs[i] = fmt.Errorf("%s: rules: %w", path, e)
		}
		return errors.Join(errs...)
	}
	set.File = path
	c.Rules = set
	return nil
}

// ruleSchema is what the rules and trigger conditions of this collection may refer to.
func (c *Collection) ruleSchema(settings RealmSettings) rules.Schema {
	roles := make([]string, 0, len(settings.Roles))
	for r := range settings.Roles {
		roles = append(roles, r)
	}
	return rules.Schema{
		DocumentField: c.IsKnownField,
		DataField: func(p string) bool {
			// A create rule sees the id the client chose; elsewhere data has no id.
			if p == "id" {
				return c.CustomID != nil
			}
			// ...and the organization the document is created in.
			if p == MetaOrg {
				return c.InOrganizations()
			}
			if p == MetaTeam {
				return c.InTeams()
			}
			_, ok := c.Fields[p]
			return ok
		},
		ScalarField:     c.ScalarField,
		ArrayField:      c.ArrayField,
		EncryptedField:  c.IsEncrypted,
		SearchableField: c.IsSearchable,
		DateField:       c.DateField,
		Roles:           roles,
		Consents:        settings.ConsentNames(),
		ConfigLeaf:      c.configLeaf,
		OrgRoles:        orgRoles(settings),
		InOrgs:          c.InOrganizations(),
		TeamRoles:       teamRoles(settings),
		InTeams:         c.InTeams(),
	}
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
	return slices.Contains(reservedFields, name)
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

// checkRealmFolder reports the folders of _realm that are not one of RealmFolders: a misspelled
// folder would otherwise silently drop the templates in it.
func checkRealmFolder(path string) []error {
	dirs, err := subdirs(path)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, d := range dirs {
		if !slices.Contains(RealmFolders, d) {
			errs = append(errs, fmt.Errorf("%s: unknown folder %q: %s holds %s", filepath.Join(path, d), d, RealmDir, strings.Join(RealmFolders, ", ")))
		}
	}
	return errs
}

// reservedNameError is the error for a folder whose name starts with "_" where it has no meaning:
// those names are backd's, and only one is defined at each level.
func reservedNameError(path, where, allowed string) error {
	return fmt.Errorf("%s: names starting with \"_\" are reserved for backd, and %s has only %s", path, where, allowed)
}

// legacyFolderError recognizes a folder at the top of a realm that is one of the folders _realm
// holds now (email, pages, push, consents, compliance, app-config), and tells where to move it.
// A database may have the same name: it is told apart by what it holds, which is never a
// collection (a folder with a schema.json) or a schema.json of its own.
func legacyFolderError(path string) error {
	target, ok := legacyFolders[filepath.Base(path)]
	if !ok {
		return nil
	}
	legacy := false
	if _, err := os.Stat(filepath.Join(path, schemaFile)); err == nil {
		legacy = true
	}
	subs, _ := subdirs(path)
	for _, s := range subs {
		if _, err := os.Stat(filepath.Join(path, s, schemaFile)); err != nil {
			legacy = true
		}
	}
	if !legacy {
		return nil
	}
	return fmt.Errorf("%s: this folder is not a database: the realm's %s folder now lives in %s, so move it to %s", path, filepath.Base(path), RealmDir, filepath.Join(filepath.Dir(path), RealmDir, target))
}

// ValidName reports whether name is a valid realm, database or collection name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

func nameError(path, kind string) error {
	return fmt.Errorf("%s: invalid %s name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", path, kind, filepath.Base(path))
}
