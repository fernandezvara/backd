package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// EncryptedField is a field of a collection stored encrypted (collection.yaml's
// fields.<path>.encrypt, design/field-encryption.md §3.2). Key names the realm's `key` secret
// the field's data keys are wrapped with.
type EncryptedField struct {
	Path string
	Key  string
	// Search is "" (the field can't be queried) or SearchEquality: a blind index lets `where`
	// and read rules compare the field for equality.
	Search string
	// Normalize is applied to the value before its blind index is computed (and to the value
	// searched for): "trim", "lower" or "upper", in order. Only with Search.
	Normalize []string
	// Unique makes the blind index unique: two documents can't hold the same value.
	Unique bool
}

// SearchEquality is the only kind of search an encrypted field offers.
const SearchEquality = "equality"

// Normalizations a blind index applies before hashing, by name.
var Normalizations = []string{"trim", "lower", "upper"}

// BlindField is the hidden field of a document that holds the blind index of the encrypted field
// at path: `_bidx.` followed by the path with its dots replaced (a field name has none).
func BlindField(path string) string { return BlindPrefix + strings.ReplaceAll(path, ".", "~") }

// BlindPrefix starts the name of every blind index field.
const BlindPrefix = "_bidx."

// BlindRoot is the document field that holds the blind indexes.
const BlindRoot = "_bidx"

// Searchable lists the encrypted fields that have a blind index.
func (c *Collection) Searchable() []EncryptedField {
	var out []EncryptedField
	for _, f := range c.Encrypted {
		if f.Search != "" {
			out = append(out, f)
		}
	}
	return out
}

// EncryptedField returns the encrypted field at path.
func (c *Collection) EncryptedAt(path string) (EncryptedField, bool) {
	for _, f := range c.Encrypted {
		if f.Path == path {
			return f, true
		}
	}
	return EncryptedField{}, false
}

// fieldDoc is one entry of collection.yaml's fields: here it is only allowed; loadEncryption
// reads it.
type fieldDoc struct {
	Encrypt *struct {
		Key       string   `yaml:"key"`
		Search    string   `yaml:"search"`
		Normalize []string `yaml:"normalize"`
		Unique    bool     `yaml:"unique"`
	} `yaml:"encrypt"`
}

// loadEncryption reads collection.yaml's fields: into c.Encrypted and checks each entry against
// the schema.
func loadEncryption(c *Collection, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		Fields map[string]*fieldDoc `yaml:"fields"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		// Another section's mistake is reported by the loader that reads it.
		return nil
	}
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: fields."+format, append([]any{path}, args...)...))
	}
	for _, p := range sortedKeys(doc.Fields) {
		d := doc.Fields[p]
		if d == nil || d.Encrypt == nil {
			fail("%s: declares nothing (use `encrypt`)", p)
			continue
		}
		if !ValidSecretName(d.Encrypt.Key) {
			fail("%s.encrypt.key: name the key secret (upper-case letters, digits and _, starting with a letter), created with `backd secret create --type key`", p)
			continue
		}
		if _, ok := c.Fields[p]; !ok {
			fail("%s: field %q is not declared in schema.json", p, p)
			continue
		}
		e := d.Encrypt
		switch {
		case e.Search != "" && e.Search != SearchEquality:
			fail("%s.encrypt.search: %q is not a kind of search; the only one is %q (a blind index for = and in)", p, e.Search, SearchEquality)
			continue
		case len(e.Normalize) > 0 && e.Search == "":
			fail("%s.encrypt.normalize: normalization applies to the blind index, which needs `search: equality`", p)
			continue
		case e.Unique && e.Search == "":
			fail("%s.encrypt.unique: a unique encrypted field needs `search: equality`: the blind index is what is unique", p)
			continue
		}
		if bad := slices.IndexFunc(e.Normalize, func(n string) bool { return !slices.Contains(Normalizations, n) }); bad >= 0 {
			fail("%s.encrypt.normalize: %q is not a normalization; use %s", p, e.Normalize[bad], strings.Join(Normalizations, ", "))
			continue
		}
		c.Encrypted = append(c.Encrypted, EncryptedField{Path: p, Key: e.Key, Search: e.Search, Normalize: e.Normalize, Unique: e.Unique})
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// Queries and read rules see the encrypted fields, and the ones inside them, as such.
	for _, ef := range c.Encrypted {
		for p, f := range c.Fields {
			if p == ef.Path || strings.HasPrefix(p, ef.Path+".") {
				f.Encrypted = true
				f.Searchable = p == ef.Path && ef.Search != ""
				c.Fields[p] = f
			}
		}
	}
	return nil
}

// addBlindIndexes declares the hidden, indexed field of each searchable encrypted field, unique
// when the field is. They are internal: not in indexes.json, not in the configuration view.
func (c *Collection) addBlindIndexes() {
	for _, f := range c.Searchable() {
		c.Indexes = append(c.Indexes, Index{Keys: []IndexKey{{Field: BlindField(f.Path)}}, Unique: f.Unique, Blind: true})
	}
}

// IsSearchable reports whether path is an encrypted field with a blind index.
func (c *Collection) IsSearchable(path string) bool {
	f, ok := c.Fields[path]
	return ok && f.Searchable
}

// IsEncrypted reports whether path is an encrypted field or inside one.
func (c *Collection) IsEncrypted(path string) bool {
	f, ok := c.Fields[path]
	return ok && f.Encrypted
}

// checkEncryption refuses the encrypted fields that something else in the collection's
// configuration needs in plaintext: it runs after everything is loaded, so each message can name
// the file and the feature (design/field-encryption.md §3.2).
func checkEncryption(c *Collection, path string) error {
	var errs []error
	for _, ef := range c.Encrypted {
		p := ef.Path
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("%s: fields.%s.encrypt: "+format, append([]any{path, p}, args...)...))
		}
		inside := func(other string) bool {
			return p == other || strings.HasPrefix(p, other+".") || strings.HasPrefix(other, p+".")
		}
		f := c.Fields[p]
		switch {
		case p == "id" || strings.HasPrefix(p, "_meta"):
			fail("system fields (id, _meta) can't be encrypted")
		case c.throughArray(p):
			fail("a field inside an array can't be encrypted: encrypt the array as a whole")
		case f.Date:
			fail("a date field (x-backd-store: date) can't be encrypted: its stored form is a date, not text")
		}
		for _, other := range c.Encrypted {
			if other.Path != p && strings.HasPrefix(p, other.Path+".") {
				fail("%s is already encrypted as a whole: encrypt either the object or its parts", other.Path)
			}
		}
		for _, r := range c.References {
			if inside(r.Field) {
				fail("%s is the source of the reference %q, and expanding it needs the plain value", r.Field, r.Name)
			}
		}
		for name := range c.Files {
			if inside(name) {
				fail("%s is a file field: file contents rely on bucket encryption", name)
			}
		}
		for _, ix := range c.Indexes {
			for _, k := range ix.Keys {
				if inside(k.Field) {
					fail("%s is in an index of indexes.json: an index can't see inside an encrypted value", k.Field)
				}
			}
		}
		if c.Text != nil {
			for _, tf := range c.Text.Fields {
				if inside(tf.Path) {
					fail("%s is in the text index: encrypted text can't be searched", tf.Path)
				}
			}
			if c.Text.LanguageField != "" && inside(c.Text.LanguageField) {
				fail("%s is the language field of the text index", c.Text.LanguageField)
			}
		}
		if c.Erasure != nil {
			for r := range c.Erasure.Replace {
				if inside(r) {
					fail("%s is a `replace` target of on_owner_delete: the replacement can't be encrypted for each document (use `remove`)", r)
				}
			}
			for r := range c.Erasure.Pull {
				if inside(r) {
					fail("%s is a `pull` target of on_owner_delete, which edits arrays in the database", r)
				}
			}
			for r := range c.Erasure.Unset {
				if inside(r) {
					fail("%s is an `unset` target of on_owner_delete, which matches values in the database", r)
				}
			}
		}
		if c.Aggregate != nil {
			for _, g := range append(slices.Clone(c.Aggregate.GroupBy), c.Aggregate.Metrics...) {
				if inside(g) {
					fail("%s is grouped or measured by aggregate: the database can't compute over encrypted values", g)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// EncryptionKeyUses lists where the realm's collections use the key secret name, as
// "database/collection.path", sorted.
func (r *Realm) EncryptionKeyUses(name string) []string {
	var out []string
	for dn, db := range r.Databases {
		for cn, c := range db.Collections {
			for _, f := range c.Encrypted {
				if f.Key == name {
					out = append(out, dn+"/"+cn+"."+f.Path)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// EncryptionKeyNames is the set of key secret names the realm's fields name.
func (r *Realm) EncryptionKeyNames() []string {
	seen := map[string]bool{}
	for _, db := range r.Databases {
		for _, c := range db.Collections {
			for _, f := range c.Encrypted {
				seen[f.Key] = true
			}
		}
	}
	return sortedKeys(seen)
}
