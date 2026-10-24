package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/exportreadme"
)

// ComplianceDir is the folder of a realm that holds its compliance files: today the README
// templates of personal data exports (compliance/readme/<locale>.html).
const ComplianceDir = "compliance"

// ExportReadme returns the realm's own README template for a language, and whether it has one.
// Without it the export uses the built-in template of that language, or the English one.
func (r *Realm) ExportReadme(locale string) (string, bool) {
	src, ok := r.exportReadmes[locale]
	return src, ok
}

// loadExportReadmes reads <realm>/compliance/readme/<locale>.html of a realm that exports its
// users' data, and checks each: it parses, and it is for a language the realm lists.
func loadExportReadmes(rl *Realm, realmPath string) []error {
	dir := filepath.Join(realmPath, ComplianceDir, "readme")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // no templates: the built-in ones are used
	}
	if !rl.Settings.Compliance.Export {
		return nil
	}
	_, locales := rl.Settings.Languages()
	var errs []error
	rl.exportReadmes = map[string]string{}
	for _, e := range entries {
		name := e.Name()
		file := filepath.Join(dir, name)
		loc, ok := strings.CutSuffix(name, ".html")
		switch {
		case e.IsDir() || strings.HasPrefix(name, "."):
			continue
		case !ok:
			errs = append(errs, fmt.Errorf("%s: README templates are <language>.html files", file))
			continue
		case !slices.Contains(locales, loc):
			errs = append(errs, fmt.Errorf("%s: %q is not a language of this realm (%s)", file, loc, strings.Join(locales, ", ")))
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
			continue
		}
		if err := exportreadme.CheckReadme(string(data)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
			continue
		}
		rl.exportReadmes[loc] = string(data)
	}
	return errs
}
