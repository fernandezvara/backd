// Package push holds what backd knows about push notification messages that doesn't depend
// on the realm's settings or its store: the templates they are written from, the portable
// message and its validation, and the payloads each provider receives (design:
// design/push-notifications.md §6).
package push

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
)

// DirName is the folder of a realm that holds its push templates: <realm>/_realm/push/<kind>/<locale>.yaml.
const DirName = "push"

// RawKind is the kind of a message whose text the function wrote itself.
const RawKind = "raw"

// Data is what a template can use: the realm, the recipient's language and the call's data.
// Templates get no address or profile field: lock screens are seen by others.
type Data struct {
	Realm string
	User  struct{ Locale string }
	Data  map[string]any
}

// Rendered is a template's output.
type Rendered struct{ Title, Body, Link string }

type compiled struct {
	title, body, link *template.Template
}

// Templates are the parsed push templates of a realm, by kind and locale.
type Templates struct {
	kinds map[string]map[string]*compiled
}

// fileDoc mirrors a template file.
type fileDoc struct {
	Title string `yaml:"title"`
	Body  string `yaml:"body"`
	Link  string `yaml:"link"`
}

// Load reads <dir>/<kind>/<locale>.yaml for every kind in the folder and checks that each kind
// has a file for every locale and that every file parses. A missing folder is no templates.
func Load(dir string, locales []string) (*Templates, []error) {
	t := &Templates{kinds: map[string]map[string]*compiled{}}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, []error{err}
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		kind := e.Name()
		if !validKind(kind) {
			errs = append(errs, fmt.Errorf("%s: %q isn't a valid kind name (lower case letters, digits and dashes)", filepath.Join(dir, kind), kind))
			continue
		}
		if kind == RawKind {
			errs = append(errs, fmt.Errorf("%s: %q is the kind of messages a function writes itself, not a template", filepath.Join(dir, kind), RawKind))
			continue
		}
		byLocale := map[string]*compiled{}
		for _, locale := range locales {
			path := filepath.Join(dir, kind, locale+".yaml")
			c, err := loadFile(path, kind, locale)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			byLocale[locale] = c
		}
		t.kinds[kind] = byLocale
	}
	return t, errs
}

func validKind(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

func loadFile(path, kind, locale string) (*compiled, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: missing (every language the realm lists needs a template of every push kind)", path)
	}
	if err != nil {
		return nil, err
	}
	var doc fileDoc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(doc.Title) == "" || strings.TrimSpace(doc.Body) == "" {
		return nil, fmt.Errorf("%s: title and body are required", path)
	}
	c := &compiled{}
	for _, f := range []struct {
		name, text string
		dst        **template.Template
	}{{"title", doc.Title, &c.title}, {"body", doc.Body, &c.body}, {"link", doc.Link, &c.link}} {
		if f.text == "" {
			continue
		}
		tpl, err := template.New(kind + "/" + locale + "." + f.name).Option("missingkey=zero").Parse(f.text)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, f.name, err)
		}
		*f.dst = tpl
	}
	// Rendered once with empty data: a template that refers to something that isn't there fails
	// here, not on a user's phone.
	if _, err := c.render(Data{Realm: "realm", Data: map[string]any{}}); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *compiled) render(d Data) (Rendered, error) {
	var out Rendered
	for _, f := range []struct {
		tpl *template.Template
		dst *string
	}{{c.title, &out.Title}, {c.body, &out.Body}, {c.link, &out.Link}} {
		if f.tpl == nil {
			continue
		}
		var b bytes.Buffer
		if err := f.tpl.Execute(&b, d); err != nil {
			return Rendered{}, err
		}
		*f.dst = strings.ReplaceAll(b.String(), "<no value>", "")
	}
	return out, nil
}

// Has says whether the kind has a template for the locale.
func (t *Templates) Has(kind, locale string) bool {
	if t == nil {
		return false
	}
	_, ok := t.kinds[kind][locale]
	return ok
}

// HasKind says whether any template of the kind exists.
func (t *Templates) HasKind(kind string) bool {
	if t == nil {
		return false
	}
	_, ok := t.kinds[kind]
	return ok
}

// Kinds lists the kinds that have templates, sorted.
func (t *Templates) Kinds() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.kinds))
	for k := range t.kinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Render writes a kind's message in a locale.
func (t *Templates) Render(kind, locale string, d Data) (Rendered, error) {
	c, ok := t.kinds[kind][locale]
	if !ok {
		return Rendered{}, fmt.Errorf("no push template %s/%s", kind, locale)
	}
	d.User.Locale = locale
	return c.render(d)
}
