package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// CollectionFile is the optional file next to a collection's schema.json
// holding its data policy for an erased user.
const CollectionFile = "collection.yaml"

// Erasure actions on the documents a user owns.
const (
	ErasureDelete    = "delete"
	ErasureAnonymize = "anonymize"
)

// What a `pull` or `unset` field holds: the user's email or their id.
const (
	MatchEmail = "email"
	MatchID    = "id"
)

// ErasurePolicy is what an erase does to a collection (collection.yaml's
// on_owner_delete). A collection without the file is left alone.
type ErasurePolicy struct {
	// Action applies to the documents whose _meta.owner is the user: "" leaves
	// them, ErasureDelete removes them, ErasureAnonymize applies Remove and
	// Replace and clears the owner.
	Action  string
	Remove  []string       // fields removed (anonymize)
	Replace map[string]any // fields set to a fixed value (anonymize)
	// Pull and Unset act on every document of the collection, whoever owns it:
	// the user is removed from the array field / cleared from the field. The
	// value says what the field holds: MatchEmail or MatchID.
	Pull  map[string]string
	Unset map[string]string
}

// Fields lists every field the policy touches, for the indexes it needs.
func (p *ErasurePolicy) Fields() []string {
	var out []string
	out = append(out, p.Remove...)
	for f := range p.Replace {
		out = append(out, f)
	}
	for f := range p.Pull {
		out = append(out, f)
	}
	for f := range p.Unset {
		out = append(out, f)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// IndexedFields are the fields an erase searches by: the arrays and fields
// that hold the user (pull and unset), which need an index to be found
// without scanning the collection.
func (p *ErasurePolicy) IndexedFields() []string {
	var out []string
	for f := range p.Pull {
		out = append(out, f)
	}
	for f := range p.Unset {
		out = append(out, f)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

type erasureDoc struct {
	SoftDelete *softDeleteDoc `yaml:"soft_delete"`
	// Files is read by parseFiles, which checks it; here it is only allowed.
	Files         map[string]*fileFieldDoc `yaml:"files"`
	OnOwnerDelete *struct {
		Action  string            `yaml:"action"`
		Remove  []string          `yaml:"remove"`
		Replace map[string]any    `yaml:"replace"`
		Pull    map[string]string `yaml:"pull"`
		Unset   map[string]string `yaml:"unset"`
	} `yaml:"on_owner_delete"`
}

// loadErasure reads the optional collection.yaml of c and checks it against
// the collection's schema, so that an erase leaves every document valid.
func loadErasure(c *Collection, settings RealmSettings, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc erasureDoc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: invalid YAML: %w", path, err)
	}
	sd, err := doc.SoftDelete.settings(path)
	if err != nil {
		return err
	}
	c.SoftDelete = sd
	d := doc.OnOwnerDelete
	if d == nil {
		return nil
	}
	if !settings.AuthEnabled {
		return fmt.Errorf("%s: a data policy for an erased user only applies when the realm has auth enabled (realm.yaml has `auth: disabled`: it has no users)", path)
	}
	p := &ErasurePolicy{Action: d.Action, Remove: d.Remove, Replace: d.Replace, Pull: d.Pull, Unset: d.Unset}
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: on_owner_delete."+format, append([]any{path}, args...)...))
	}

	switch p.Action {
	case "", ErasureDelete, ErasureAnonymize:
	default:
		add("action: must be delete or anonymize, got %q (leave it out to keep the user's documents)", p.Action)
	}
	if p.Action != ErasureAnonymize && (len(p.Remove) > 0 || len(p.Replace) > 0) {
		add("remove, replace: only apply with `action: anonymize`")
	}

	required := requiredPaths(c.RawSchema)
	known := func(field, key string) bool {
		if strings.HasPrefix(field, "_meta") || field == "id" {
			add("%s: %s is a system field", key, field)
			return false
		}
		if _, ok := c.Fields[field]; !ok {
			add("%s: %s is not a field of schema.json", key, field)
			return false
		}
		if c.throughArray(field) {
			add("%s: %s is inside an array, which a policy can't reach (name a field outside the array)", key, field)
			return false
		}
		return true
	}

	for _, f := range p.Remove {
		if !known(f, "remove") {
			continue
		}
		if _, replaced := p.Replace[f]; replaced {
			add("remove: %s is also in replace", f)
		} else if required[f] {
			add("remove: %s is required by schema.json, so removing it would make the document invalid (replace it instead)", f)
		}
	}
	for _, f := range slices.Sorted(mapKeys(p.Replace)) {
		if !known(f, "replace") {
			continue
		}
		if reason := invalidFieldValue(c.Schema, f, p.Replace[f]); reason != "" {
			add("replace: the value for %s doesn't satisfy schema.json: %s", f, reason)
		}
		for _, ix := range c.Indexes {
			if ix.Unique && slices.ContainsFunc(ix.Keys, func(k IndexKey) bool { return k.Field == f }) {
				add("replace: %s is in a unique index, so every erased document would get the same value and conflict", f)
			}
		}
	}
	for _, f := range slices.Sorted(mapKeys(p.Pull)) {
		if !known(f, "pull") {
			continue
		}
		if m := p.Pull[f]; m != MatchEmail && m != MatchID {
			add("pull: %s must say what the array holds: email or id, got %q", f, m)
		}
		field := c.Fields[f]
		if !slices.Equal(field.Types, []string{"array"}) || !slices.Equal(field.ItemTypes, []string{"string"}) {
			add("pull: %s must be an array of strings in schema.json", f)
		}
		if schemaAt(c.RawSchema, f)["minItems"] != nil {
			add("pull: %s has minItems in schema.json, so removing the user could make the array invalid", f)
		}
	}
	for _, f := range slices.Sorted(mapKeys(p.Unset)) {
		if !known(f, "unset") {
			continue
		}
		if m := p.Unset[f]; m != MatchEmail && m != MatchID {
			add("unset: %s must say what the field holds: email or id, got %q", f, m)
		}
		if !slices.Equal(c.Fields[f].Types, []string{"string"}) {
			add("unset: %s must be a string in schema.json", f)
		}
		if required[f] {
			add("unset: %s is required by schema.json, so clearing it would make documents invalid (make it optional)", f)
		}
		if _, pulled := p.Pull[f]; pulled {
			add("unset: %s is also in pull", f)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for f, v := range p.Replace {
		p.Replace[f] = normalizeYAML(v)
	}
	c.Erasure = p
	c.ErasurePath = path
	c.addErasureIndexes()
	return nil
}

// addErasureIndexes declares the indexes an erase searches by, next to the ones
// in indexes.json: the owner when the policy has an action, and each field that
// holds the user (pull, unset). Without them an erase would scan the whole
// collection. One already declared (same keys) isn't added twice.
func (c *Collection) addErasureIndexes() {
	fields := c.Erasure.IndexedFields()
	if c.Erasure.Action != "" {
		fields = append([]string{"_meta.owner"}, fields...)
	}
	for _, f := range fields {
		declared := slices.ContainsFunc(c.Indexes, func(ix Index) bool {
			return len(ix.Keys) == 1 && ix.Keys[0].Field == f && !ix.Keys[0].Desc
		})
		if !declared {
			c.Indexes = append(c.Indexes, Index{Keys: []IndexKey{{Field: f}}})
		}
	}
}

// normalizeYAML gives a value read from YAML the types documents have: whole
// numbers are int64.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case int:
		return int64(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalizeYAML(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeYAML(e)
		}
		return out
	}
	return v
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// requiredPaths lists the dotted paths schema.json requires (through objects,
// not through arrays).
func requiredPaths(schema map[string]any) map[string]bool {
	out := map[string]bool{}
	var walk func(prefix string, s map[string]any)
	walk = func(prefix string, s map[string]any) {
		names, _ := s["required"].([]any)
		for _, n := range names {
			if name, ok := n.(string); ok {
				out[join(prefix, name)] = true
			}
		}
		props, _ := s["properties"].(map[string]any)
		for name, v := range props {
			if sub, ok := v.(map[string]any); ok {
				walk(join(prefix, name), sub)
			}
		}
	}
	walk("", schema)
	return out
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// schemaAt returns the sub-schema of a dotted field path, or nil.
func schemaAt(schema map[string]any, path string) map[string]any {
	cur := schema
	for _, part := range strings.Split(path, ".") {
		props, _ := cur["properties"].(map[string]any)
		next, ok := props[part].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

var english = message.NewPrinter(language.English)

// invalidFieldValue checks one value against schema.json at a dotted path
// (the rest of the document is ignored) and says why it doesn't fit, or "".
func invalidFieldValue(schema *jsonschema.Schema, path string, value any) string {
	doc := map[string]any{}
	cur := doc
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next := map[string]any{}
		cur[p] = next
		cur = next
	}
	cur[parts[len(parts)-1]] = value
	err := schema.Validate(doc)
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	var reasons []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			if slices.Equal(e.InstanceLocation, parts) || (len(e.InstanceLocation) >= len(parts) && slices.Equal(e.InstanceLocation[:len(parts)], parts)) {
				reasons = append(reasons, e.ErrorKind.LocalizedString(english))
			}
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return strings.Join(reasons, "; ")
}
