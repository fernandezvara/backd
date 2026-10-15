package registry

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// TextSearchField is the stored field that holds a document's text-search language: the
// language_field's value mapped to a language the database knows. It is server-owned and
// never part of a document's API shape.
const TextSearchField = "_search_lang"

// IndexTextName is the name of the collection's text index in MongoDB.
const IndexTextName = "backd_text"

// TextIndex is a collection's full-text index, from indexes.json's `{"text": {...}}` entry.
type TextIndex struct {
	// Fields are the indexed fields (strings or arrays of strings) with their weights, in
	// field order.
	Fields []TextField
	// DefaultLanguage is the language of documents with none: a database language name, or "none".
	DefaultLanguage string
	// LanguageField is the declared string field that holds each document's locale code
	// (`es`, `en-GB`), or "".
	LanguageField string
}

// TextField is one indexed field and its weight.
type TextField struct {
	Path   string
	Weight int
}

type textDecl struct {
	Fields          map[string]int `json:"fields"`
	DefaultLanguage string         `json:"default_language"`
	LanguageField   string         `json:"language_field"`
}

// searchLanguages are the languages the database stems and stops words for, by name.
var searchLanguages = []string{
	"danish", "dutch", "english", "finnish", "french", "german", "hungarian", "italian",
	"norwegian", "portuguese", "romanian", "russian", "spanish", "swedish", "turkish",
}

// searchLanguageCodes map a primary locale subtag to a language of the database.
var searchLanguageCodes = map[string]string{
	"da": "danish", "nl": "dutch", "en": "english", "fi": "finnish", "fr": "french", "de": "german",
	"hu": "hungarian", "it": "italian", "nb": "norwegian", "nn": "norwegian", "no": "norwegian",
	"pt": "portuguese", "ro": "romanian", "ru": "russian", "es": "spanish", "sv": "swedish", "tr": "turkish",
}

// SearchLanguage maps a locale code (`es`, `es-MX`, `pt_BR`) to the language the database
// uses to stem it, or "none" when it has none (the text is then indexed without stemming).
func SearchLanguage(locale string) string {
	primary := strings.ToLower(locale)
	if i := strings.IndexAny(primary, "-_"); i >= 0 {
		primary = primary[:i]
	}
	if l, ok := searchLanguageCodes[primary]; ok {
		return l
	}
	return "none"
}

// searchLanguageName normalizes a default_language: a language name, a two-letter code or none.
func searchLanguageName(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "none":
		return "none", true
	case slices.Contains(searchLanguages, s):
		return s, true
	}
	if l, ok := searchLanguageCodes[s]; ok && len(s) == 2 {
		return l, true
	}
	return "", false
}

// parseTextIndex reads and checks a text entry of indexes.json into c.Text.
func parseTextIndex(c *Collection, d indexDecl) error {
	if c.Text != nil {
		return errors.New("a collection can have only one text index (a limit of the database): put every field in the same entry")
	}
	if len(d.Fields) > 0 || d.Unique {
		return errors.New(`a "text" entry has no "fields" list or "unique": its fields are the keys of text.fields`)
	}
	t := d.Text
	var errs []error
	if len(t.Fields) == 0 {
		errs = append(errs, errors.New(`text.fields must name at least one field, with its weight: {"title": 10, "body": 1}`))
	}
	if len(t.Fields) > 30 {
		errs = append(errs, fmt.Errorf("text.fields: %d fields, at most 30", len(t.Fields)))
	}
	idx := &TextIndex{}
	for _, path := range slices.Sorted(maps.Keys(t.Fields)) {
		w := t.Fields[path]
		switch {
		case w < 1 || w > 1000:
			errs = append(errs, fmt.Errorf("text.fields.%s: weight %d: must be between 1 and 1000", path, w))
			continue
		}
		if why := c.textFieldProblem(path); why != "" {
			errs = append(errs, fmt.Errorf("text.fields.%s: %s", path, why))
			continue
		}
		idx.Fields = append(idx.Fields, TextField{Path: path, Weight: w})
	}
	idx.DefaultLanguage = "english"
	if t.DefaultLanguage != "" {
		l, ok := searchLanguageName(t.DefaultLanguage)
		if !ok {
			errs = append(errs, fmt.Errorf("text.default_language: %q is not a language the database supports: use none or one of %s", t.DefaultLanguage, strings.Join(searchLanguages, ", ")))
		}
		idx.DefaultLanguage = l
	}
	if t.LanguageField != "" {
		f, ok := c.Fields[t.LanguageField]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("text.language_field: %q is not declared in schema.json", t.LanguageField))
		case !stringOnly(f.Types) || c.throughArray(t.LanguageField):
			errs = append(errs, fmt.Errorf("text.language_field: %q must be a string field (it holds a locale code such as es or en-GB)", t.LanguageField))
		default:
			idx.LanguageField = t.LanguageField
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	c.Text = idx
	return nil
}

// textFieldProblem says why a field can't be text-indexed, or "" when it can: strings and
// arrays of strings, held directly or in objects.
func (c *Collection) textFieldProblem(path string) string {
	f, ok := c.Fields[path]
	if !ok {
		return "field is not declared in schema.json"
	}
	if c.throughArray(path) {
		return "field is inside an array of objects: index a field that holds one string or a list of strings"
	}
	if stringOnly(f.Types) {
		return ""
	}
	if slices.Equal(f.Types, []string{"array"}) && slices.Equal(f.ItemTypes, []string{"string"}) {
		return ""
	}
	return "only strings and arrays of strings can be text-indexed"
}
