package registry

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// What a collection contributes to a user's personal data export.
const (
	ExportInclude = "include"
	ExportExclude = "exclude"
)

// PersonalData is collection.yaml's personal_data: it overrides what the
// export of a user's data takes from the collection. Without it the export
// follows the erasure model: the documents the user owns, whole.
type PersonalData struct {
	Export string   // ExportInclude (default) or ExportExclude
	Fields []string // only these fields (plus id and _meta); nil: whole documents
	Reason string   // why: required to exclude, and to leave fields out
}

// personalDataDoc is personal_data in collection.yaml.
type personalDataDoc struct {
	Export string   `yaml:"export"`
	Fields []string `yaml:"fields"`
	Reason string   `yaml:"reason"`
}

// policy checks the declaration against the collection's schema.
func (d *personalDataDoc) policy(c *Collection, path string) (*PersonalData, error) {
	if d == nil {
		return nil, nil
	}
	p := &PersonalData{Export: d.Export, Fields: d.Fields, Reason: strings.TrimSpace(d.Reason)}
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: personal_data."+format, append([]any{path}, args...)...))
	}
	switch p.Export {
	case "":
		p.Export = ExportInclude
	case ExportInclude, ExportExclude:
	default:
		add("export: must be include or exclude, got %q", p.Export)
	}
	if p.Export == ExportExclude {
		if len(p.Fields) > 0 {
			add("fields: only apply with `export: include`")
		}
		if p.Reason == "" {
			add("reason: is required to exclude a collection from a user's export (say why the data is exempt)")
		}
	}
	for _, f := range p.Fields {
		if strings.HasPrefix(f, "_meta") || f == "id" {
			add("fields: %s is a system field; id and _meta are always exported", f)
		} else if _, ok := c.Fields[f]; !ok {
			add("fields: %s is not a field of schema.json", f)
		}
	}
	if len(p.Fields) > 0 && p.Reason == "" {
		add("reason: is required when `fields` leaves data out of the export")
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return p, nil
}

// ScopeEntry says what the export of a user's data takes from one collection.
type ScopeEntry struct {
	Database   string
	Collection string
	// Export is ExportInclude or ExportExclude.
	Export string
	// Owned: documents whose _meta.owner is the user. Always true when included.
	Owned bool
	// Fields lists the exported fields when the export is partial; nil means whole documents.
	Fields []string
	// Pull lists the arrays that hold the user, whose documents are included whoever owns them.
	Pull []string
	// Files lists the file fields whose files are part of the export.
	Files  []string
	Reason string
	// Declared: personal_data is in the collection's collection.yaml.
	Declared bool
}

// PersonalScope lists, collection by collection, what the export of a user's
// data takes: the documents they own, those where they appear in a `pull`
// field, and the files those hold, minus what personal_data excludes.
func (r *Realm) PersonalScope() []ScopeEntry {
	var out []ScopeEntry
	for _, dbName := range slices.Sorted(mapKeys(r.Databases)) {
		for _, c := range r.Databases[dbName].SortedCollections() {
			e := ScopeEntry{Database: dbName, Collection: c.Name, Export: ExportInclude, Owned: true}
			if p := c.PersonalData; p != nil {
				e.Declared, e.Export, e.Reason = true, p.Export, p.Reason
				e.Fields = slices.Clone(p.Fields)
			}
			if e.Export == ExportExclude {
				e.Owned = false
				out = append(out, e)
				continue
			}
			if c.Erasure != nil {
				e.Pull = slices.Sorted(mapKeys(c.Erasure.Pull))
			}
			e.Files = slices.Sorted(mapKeys(c.Files))
			if e.Fields != nil {
				slices.Sort(e.Fields)
			}
			out = append(out, e)
		}
	}
	return out
}
