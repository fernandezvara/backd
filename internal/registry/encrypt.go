package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// EncryptedField is a field of a collection stored encrypted (collection.yaml's
// fields.<path>.encrypt, design/field-encryption.md §3.2). Key names the realm's `key` secret
// the field's data keys are wrapped with.
type EncryptedField struct {
	Path string
	Key  string
}

// fieldDoc is one entry of collection.yaml's fields: here it is only allowed; loadEncryption
// reads it.
type fieldDoc struct {
	Encrypt *struct {
		Key string `yaml:"key"`
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
		c.Encrypted = append(c.Encrypted, EncryptedField{Path: p, Key: d.Encrypt.Key})
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
