// Package exportreadme renders the README.html of a personal data export from a logic-less
// template: the realm's own (compliance/readme/<locale>.html) or the built-in one of its language.
package exportreadme

import (
	"embed"
	"fmt"
	"html"
	"sort"
	"time"

	"github.com/fernandezvara/backd/internal/email"
)

//go:embed readme/*.html
var readmeFiles embed.FS

// ReadmeName is the human summary at the root of a personal data export.
const ReadmeName = "README.html"

// ReadmeDirName is where, inside a realm's folder, its own README templates live:
// compliance/readme/<locale>.html (design/compliance.md §4.2).
const ReadmeDirName = "compliance/readme"

// DefaultReadmeLocale is the language of the template used when a realm has none for a user's.
const DefaultReadmeLocale = "en"

// DefaultReadme returns the built-in README template for a language, and whether there is one.
func DefaultReadme(locale string) ([]byte, bool) {
	data, err := readmeFiles.ReadFile("readme/" + locale + ".html")
	return data, err == nil
}

// DefaultReadmeLocales lists the languages that have a built-in template.
func DefaultReadmeLocales() []string {
	entries, _ := readmeFiles.ReadDir("readme")
	var out []string
	for _, e := range entries {
		out = append(out, e.Name()[:len(e.Name())-len(".html")])
	}
	sort.Strings(out)
	return out
}

// ReadmePart is one file of the package the README lists.
type ReadmePart struct {
	Part      string
	Documents int64
}

// ReadmeExclusion is something left out of the package, and why.
type ReadmeExclusion struct {
	What   string
	Reason string
}

// ReadmeData is what a README template can print (logic-less: values, sections and inverted
// sections; the same syntax as the owner's email templates).
//
//	{{Realm}} {{Email}} {{Generated}} {{Days}}
//	{{Count.sessions}}                          records of each _account part
//	{{#Collections}}{{#Items}}{{Part}} {{Documents}}{{/Items}} {{Files}}{{/Collections}}
//	{{#Exclusions}}{{#List}}{{What}}: {{Reason}}{{/List}}{{/Exclusions}}
//	{{#Truncated}}{{#List}}{{Name}}{{/List}}{{/Truncated}}
type ReadmeData struct {
	Realm     string
	Email     string
	Generated time.Time
	// Days is how long the downloadable copy is kept; 0 for an export handed to an administrator.
	Days int
	// Count is the number of records of each _account part, by name.
	Count       map[string]int
	Collections []ReadmePart
	Files       int64
	Exclusions  []ReadmeExclusion
	// Truncated names the parts that stopped at a limit.
	Truncated []string
}

func (d ReadmeData) values() map[string]any {
	count := map[string]any{}
	for k, v := range d.Count {
		count[k] = v
	}
	out := map[string]any{
		"Realm": d.Realm, "Email": d.Email, "Generated": d.Generated.UTC().Format("2006-01-02 15:04 UTC"),
		"Count": count,
	}
	if d.Days > 0 {
		out["Days"] = d.Days
	}
	if len(d.Collections) > 0 {
		items := make([]any, len(d.Collections))
		for i, c := range d.Collections {
			items[i] = map[string]any{"Part": c.Part, "Documents": c.Documents}
		}
		coll := map[string]any{"Items": items}
		if d.Files > 0 {
			coll["Files"] = d.Files
		}
		out["Collections"] = coll
	}
	if len(d.Exclusions) > 0 {
		list := make([]any, len(d.Exclusions))
		for i, e := range d.Exclusions {
			list[i] = map[string]any{"What": e.What, "Reason": e.Reason}
		}
		out["Exclusions"] = map[string]any{"List": list}
	}
	if len(d.Truncated) > 0 {
		list := make([]any, len(d.Truncated))
		for i, n := range d.Truncated {
			list[i] = map[string]any{"Name": n}
		}
		out["Truncated"] = map[string]any{"List": list}
	}
	return out
}

// CheckReadme parses a README template, with where in it a problem is.
func CheckReadme(src string) error {
	if len(src) > email.MaxTemplateBytes {
		return fmt.Errorf("is larger than %d KiB", email.MaxTemplateBytes>>10)
	}
	if _, err := email.ParseMustache(src); err != nil {
		return fmt.Errorf("line %d, column %d: %s", err.Line, err.Column, err.Message)
	}
	return nil
}

// RenderReadme prints a README template with data; every value is HTML-escaped.
func RenderReadme(src string, data ReadmeData) (string, error) {
	m, serr := email.ParseMustache(src)
	if serr != nil {
		return "", fmt.Errorf("README template: %w", serr)
	}
	return m.Render(data.values(), html.EscapeString)
}
