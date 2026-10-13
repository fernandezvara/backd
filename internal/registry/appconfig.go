package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/rules"
)

// AppConfigDir is the folder of a realm that declares its app configuration:
// <realm>/app-config/schema.json and rules.yaml. It is never read as a database.
const AppConfigDir = "app-config"

const (
	appConfigSchemaFile = "schema.json"
	appConfigRulesFile  = "rules.yaml"
)

// AppConfigMaxBytes is the most the whole configuration, defaults included, may
// take once encoded.
const AppConfigMaxBytes = 64 << 10

var appConfigKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// appConfigRefused are the schema keywords an app configuration doesn't take:
// the configuration is a tree of objects whose leaves hold plain values.
var appConfigRefused = []string{"$ref", "$defs", "definitions", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "patternProperties", "dependentSchemas"}

// AppConfig is a realm's declared app configuration (<realm>/app-config/): the
// keys, their types and defaults, and who may read which path.
type AppConfig struct {
	SchemaPath string
	RulesPath  string // rules.yaml, whether or not the file exists
	RawSchema  map[string]any
	Schema     *jsonschema.Schema
	// Paths holds every declared path (dot notation, `features.reports.enabled`);
	// a path is a branch (an object) or a leaf (a value).
	Paths map[string]ConfigPath
	// Defaults is the whole configuration with every leaf at its default.
	Defaults map[string]any
	// Rules are the read rules by path, as written; use Effective for what applies.
	Rules map[string]*rules.Rule
}

// ConfigPath is one declared path of the configuration.
type ConfigPath struct {
	Leaf        bool
	Description string
}

// HasPath reports whether the path is declared, as a branch or as a leaf.
func (a *AppConfig) HasPath(path string) bool {
	_, ok := a.Paths[path]
	return ok
}

// SortedPaths lists the declared paths in order.
func (a *AppConfig) SortedPaths() []string { return slices.Sorted(maps.Keys(a.Paths)) }

// Effective returns the rules that apply to a path: its own rule and those of
// every ancestor, all of which must allow the read. It reports false when
// nothing in the chain has a rule, which means clients can't read the path.
func (a *AppConfig) Effective(path string) ([]*rules.Rule, bool) {
	var out []*rules.Rule
	parts := strings.Split(path, ".")
	for i := 1; i <= len(parts); i++ {
		if r := a.Rules[strings.Join(parts[:i], ".")]; r != nil {
			out = append(out, r)
		}
	}
	return out, len(out) > 0
}

// Validate checks a whole configuration document against the schema.
func (a *AppConfig) Validate(doc map[string]any) error {
	return a.Schema.Validate(doc)
}

type appConfigRulesDoc struct {
	Read map[string]string `yaml:"read"`
}

// loadAppConfig reads <realm>/app-config/ when it exists.
func loadAppConfig(rl *Realm, realmPath string) []error {
	dir := filepath.Join(realmPath, AppConfigDir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	schemaPath := filepath.Join(dir, appConfigSchemaFile)
	if !rl.Settings.AuthEnabled {
		return []error{fmt.Errorf("%s: app configuration is stored with the realm's system data, which only realms with auth enabled have (realm.yaml has `auth: disabled`)", dir)}
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []error{fmt.Errorf("%s: the app-config directory has no %s", dir, appConfigSchemaFile)}
		}
		return []error{fmt.Errorf("%s: %w", schemaPath, err)}
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return []error{fmt.Errorf("%s: invalid JSON: %w", schemaPath, err)}
	}
	raw, ok := doc.(map[string]any)
	if !ok {
		return []error{fmt.Errorf("%s: schema must be a JSON object", schemaPath)}
	}
	if v, ok := raw["$schema"]; ok {
		if s, _ := v.(string); !draft2020URIs[s] {
			return []error{fmt.Errorf("%s: $schema must be JSON Schema draft 2020-12 (or omitted), got %v", schemaPath, v)}
		}
	}

	a := &AppConfig{
		SchemaPath: schemaPath,
		RulesPath:  filepath.Join(dir, appConfigRulesFile),
		RawSchema:  raw,
		Paths:      map[string]ConfigPath{},
		Rules:      map[string]*rules.Rule{},
	}
	var errs []error
	defaults, es := walkAppConfig(a, raw, "", true)
	for _, e := range es {
		errs = append(errs, fmt.Errorf("%s: %w", schemaPath, e))
	}
	if len(errs) > 0 {
		return errs
	}
	a.Defaults, _ = defaults.(map[string]any)
	if !jsonHasNoSecret(defaults) {
		errs = append(errs, fmt.Errorf("%s: a default holds a `secret:` reference: app configuration is readable by whoever its rules allow, so it never holds secrets", schemaPath))
	}
	if b, err := json.Marshal(a.Defaults); err == nil && len(b) > AppConfigMaxBytes {
		errs = append(errs, fmt.Errorf("%s: the defaults take %d bytes, more than the %d the configuration may hold", schemaPath, len(b), AppConfigMaxBytes))
	}

	abs, err := filepath.Abs(schemaPath)
	if err != nil {
		return append(errs, fmt.Errorf("%s: %w", schemaPath, err))
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "file://" + filepath.ToSlash(abs)
	if err := c.AddResource(url, raw); err != nil {
		return append(errs, fmt.Errorf("%s: %w", schemaPath, err))
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return append(errs, fmt.Errorf("%s: invalid schema: %w", schemaPath, err))
	}
	a.Schema = compiled
	if len(errs) == 0 {
		// Defaults are normalized through JSON so numbers compare as the schema expects.
		if err := compiled.Validate(a.Defaults); err != nil {
			errs = append(errs, fmt.Errorf("%s: the defaults don't satisfy the schema: %w", schemaPath, err))
		}
	}
	errs = append(errs, loadAppConfigRules(a, rl.Settings)...)
	if len(errs) == 0 {
		rl.AppConfig = a
	}
	return errs
}

// walkAppConfig checks a schema node and returns the default document of its
// subtree. Objects must close their properties; leaves need a default or null.
func walkAppConfig(a *AppConfig, node map[string]any, path string, root bool) (any, []error) {
	where := path
	if root {
		where = "the root"
	}
	var errs []error
	for _, k := range appConfigRefused {
		if _, ok := node[k]; ok {
			errs = append(errs, fmt.Errorf("%s: `%s` isn't supported in app configuration: declare the keys as nested objects with `properties`", where, k))
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	desc, _ := node["description"].(string)
	typ, _ := node["type"].(string)
	props, hasProps := node["properties"].(map[string]any)
	if !hasProps && typ != "object" {
		if root {
			return nil, []error{errors.New("the root of the schema must be an object with `properties`")}
		}
		// A leaf.
		a.Paths[path] = ConfigPath{Leaf: true, Description: desc}
		if v, ok := node["default"]; ok {
			return v, nil
		}
		if allowsNull(node) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("%s: a value needs a `default` (or `\"null\"` among its types), so clients always receive a complete configuration", path)}
	}
	if typ != "object" {
		errs = append(errs, fmt.Errorf("%s: an object with `properties` must have `\"type\": \"object\"`", where))
	}
	if ap, ok := node["additionalProperties"].(bool); !ok || ap {
		errs = append(errs, fmt.Errorf("%s: an object must set `\"additionalProperties\": false`, so keys that aren't declared can't be set", where))
	}
	if _, ok := node["default"]; ok {
		errs = append(errs, fmt.Errorf("%s: defaults belong on the values, not on an object", where))
	}
	if len(props) == 0 {
		errs = append(errs, fmt.Errorf("%s: an object must declare at least one property", where))
	}
	if !root {
		a.Paths[path] = ConfigPath{Description: desc}
	}
	out := map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(props)) {
		child := path
		if child != "" {
			child += "."
		}
		child += name
		if !appConfigKeyPattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: %q isn't a valid key: letters, digits, underscores and dashes, starting with a letter or underscore (a dot would make it a path)", where, name))
			continue
		}
		sub, ok := props[name].(map[string]any)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: the schema of the key must be an object", child))
			continue
		}
		v, es := walkAppConfig(a, sub, child, false)
		errs = append(errs, es...)
		out[name] = v
	}
	return out, errs
}

// allowsNull reports whether a leaf's `type` includes "null".
func allowsNull(node map[string]any) bool {
	switch t := node["type"].(type) {
	case string:
		return t == "null"
	case []any:
		for _, v := range t {
			if s, _ := v.(string); s == "null" {
				return true
			}
		}
	}
	return false
}

// jsonHasNoSecret reports whether no string in v is a secret reference.
func jsonHasNoSecret(v any) bool {
	switch v := v.(type) {
	case string:
		return !strings.HasPrefix(v, secretPrefix)
	case []any:
		for _, e := range v {
			if !jsonHasNoSecret(e) {
				return false
			}
		}
	case map[string]any:
		for _, e := range v {
			if !jsonHasNoSecret(e) {
				return false
			}
		}
	}
	return true
}

// loadAppConfigRules reads rules.yaml, which is optional: without it no path is
// readable by clients.
func loadAppConfigRules(a *AppConfig, settings RealmSettings) []error {
	data, err := os.ReadFile(a.RulesPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []error{fmt.Errorf("%s: %w", a.RulesPath, err)}
	}
	var doc appConfigRulesDoc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return []error{fmt.Errorf("%s: invalid YAML: want `read:` with a mapping of path to expression: %w", a.RulesPath, err)}
	}
	var errs []error
	roles, consents := roleNames(settings), settings.ConsentNames()
	for _, path := range slices.Sorted(maps.Keys(doc.Read)) {
		if !a.HasPath(path) {
			errs = append(errs, fmt.Errorf("%s: read.%s: the path isn't declared in %s", a.RulesPath, path, appConfigSchemaFile))
			continue
		}
		r, err := rules.CompileConfig(doc.Read[path], roles, consents)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: read.%s: %w", a.RulesPath, path, err))
			continue
		}
		a.Rules[path] = r
	}
	return errs
}
