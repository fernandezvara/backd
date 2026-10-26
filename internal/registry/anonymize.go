package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// AnonRule is what an anonymized export does to every document of one collection: the
// fields it removes, the fields it sets to a fixed value and the fields that hold a user's
// email, whose addresses are rewritten like the users'.
type AnonRule struct {
	Remove  []string
	Replace map[string]any
	// Emails are fields (strings, or arrays of strings) that hold the email of a user: the
	// `pull` and `unset` fields of the collection's erase policy that say `email`.
	Emails []string
}

// AnonOverride is one collection's entry of an --anonymize-config file.
type AnonOverride struct {
	Remove  []string       `yaml:"remove"`
	Replace map[string]any `yaml:"replace"`
}

// ParseAnonymizeConfig reads an --anonymize-config file:
//
//	collections:
//	  <database>/<collection>:
//	    remove: [field, …]
//	    replace: {field: value}
func ParseAnonymizeConfig(data []byte) (map[string]AnonOverride, error) {
	var doc struct {
		Collections map[string]AnonOverride `yaml:"collections"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	for _, o := range doc.Collections {
		for f, v := range o.Replace {
			o.Replace[f] = normalizeYAML(v)
		}
	}
	return doc.Collections, nil
}

// AnonymizeRules works out, for every collection of the realm that has one, what an anonymized
// export does: the collection's erase policy (its `remove` and `replace`, whatever its action)
// with the overrides on top. An override is checked against the collection's schema like an
// erase policy is; the problems of all of them are reported together.
func AnonymizeRules(rl *Realm, overrides map[string]AnonOverride) (map[string]*AnonRule, error) {
	var errs []error
	rules := map[string]*AnonRule{}
	for key := range overrides {
		db, name, _ := strings.Cut(key, "/")
		if rl.Databases[db] == nil || rl.Databases[db].Collections[name] == nil {
			errs = append(errs, fmt.Errorf("collections: %s is not a collection of realm %s", key, rl.Name))
		}
	}
	for dbName, db := range rl.Databases {
		for name, c := range db.Collections {
			key := dbName + "/" + name
			rule := &AnonRule{Replace: map[string]any{}}
			if p := c.Erasure; p != nil {
				rule.Remove = slices.Clone(p.Remove)
				maps.Copy(rule.Replace, p.Replace)
				for f, m := range p.Pull {
					if m == MatchEmail {
						rule.Emails = append(rule.Emails, f)
					}
				}
				for f, m := range p.Unset {
					if m == MatchEmail {
						rule.Emails = append(rule.Emails, f)
					}
				}
			}
			if o, ok := overrides[key]; ok {
				add := func(format string, args ...any) {
					errs = append(errs, fmt.Errorf("collections.%s."+format, append([]any{key}, args...)...))
				}
				c.checkRemoveReplace(o.Remove, o.Replace, add)
				// An override wins over the policy: a field it sets is no longer removed, and
				// a field it removes is no longer set.
				for f, v := range o.Replace {
					rule.Replace[f] = v
					rule.Remove = slices.DeleteFunc(rule.Remove, func(x string) bool { return x == f })
				}
				for _, f := range o.Remove {
					delete(rule.Replace, f)
					if !slices.Contains(rule.Remove, f) {
						rule.Remove = append(rule.Remove, f)
					}
				}
			}
			slices.Sort(rule.Remove)
			slices.Sort(rule.Emails)
			if len(rule.Remove) > 0 || len(rule.Replace) > 0 || len(rule.Emails) > 0 {
				rules[key] = rule
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return rules, nil
}
