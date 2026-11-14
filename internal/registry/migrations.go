package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MigrationsDir is the folder of a database that holds the built migration bundles, in
// MigrationsDir/BuildDir. It is a build output, not a folder anyone writes in: the sources are
// the schema.v<N>.ts files of each collection (design/schema-versioning.md §6.1).
const MigrationsDir = "_migrations"

// MigrationManifest describes the bundles `backd-server functions build` wrote for the
// migrations of one database: one bundle per collection that has any migration step.
type MigrationManifest struct {
	Deno        string                    `json:"deno"`
	Source      string                    `json:"source"` // MigrationSourceHash of the database when built
	Collections map[string]ManifestBundle `json:"collections"`
}

// MigratingCollections lists the collections of db with at least one migration step, in
// name order: the ones the build bundles and startup expects a bundle for.
func (db *Database) MigratingCollections() []*Collection {
	var out []*Collection
	for _, name := range sortedKeys(db.Collections) {
		if c := db.Collections[name]; len(c.Migrations) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// MigrationsBuildDir is where the database's migration bundles and manifest are written.
func (db *Database) MigrationsBuildDir() string {
	return filepath.Join(db.Dir, MigrationsDir, BuildDir)
}

// MigrationSourceHash hashes the migration sources of db: the name and content of every
// schema.v<N>.ts of its collections. The generated schema.d.ts shapes no bundle, so it is out.
func MigrationSourceHash(db *Database) (string, error) {
	h := sha256.New()
	h.Write([]byte(sourceHashSalt))
	for _, c := range db.MigratingCollections() {
		for _, n := range c.Migrations {
			data, err := os.ReadFile(filepath.Join(c.Dir, SchemaMigrationFile(n)))
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "%s/%s\x00%d\x00", c.Name, SchemaMigrationFile(n), len(data))
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReadMigrationManifest reads a database's _migrations/.build/manifest.json.
func ReadMigrationManifest(db *Database) (MigrationManifest, error) {
	var m MigrationManifest
	path := filepath.Join(db.MigrationsBuildDir(), ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// MigrationBundlePath is the file of the collection's bundle, as the manifest records it.
func (db *Database) MigrationBundlePath(m MigrationManifest, collection string) string {
	return filepath.Join(db.MigrationsBuildDir(), m.Collections[collection].Bundle)
}

// checkMigrationBundles reports the migration bundles of db that are missing, were built by
// another Deno version, don't match the sources or were changed since the build.
func checkMigrationBundles(db *Database) []error {
	colls := db.MigratingCollections()
	if len(colls) == 0 {
		return nil
	}
	dir := filepath.Join(db.Dir, MigrationsDir)
	fix := fmt.Sprintf("run `backd-server functions build` (for %s/%s)", db.Realm, db.Name)
	m, err := ReadMigrationManifest(db)
	if errors.Is(err, os.ErrNotExist) {
		return []error{fmt.Errorf("%s: migrations are not built: %s", dir, fix)}
	}
	if err != nil {
		return []error{err}
	}
	if m.Deno != DenoVersion {
		return []error{fmt.Errorf("%s: migration bundles were built with Deno %s, this backd uses %s: %s", dir, m.Deno, DenoVersion, fix)}
	}
	src, err := MigrationSourceHash(db)
	if err != nil {
		return []error{err}
	}
	if src != m.Source {
		return []error{fmt.Errorf("%s: migration bundles are stale (a schema.v<N>.ts changed since the last build): %s", dir, fix)}
	}
	var errs []error
	for _, c := range colls {
		b, ok := m.Collections[c.Name]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: collection %q has no migration bundle: %s", dir, c.Name, fix))
			continue
		}
		data, err := os.ReadFile(db.MigrationBundlePath(m, c.Name))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: migration bundle of %q: %v: %s", dir, c.Name, err, fix))
			continue
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != b.SHA256 {
			errs = append(errs, fmt.Errorf("%s: migration bundle of %q was modified after the build: %s", dir, c.Name, fix))
		}
	}
	return errs
}

// migrationImport matches the module imports a migration file could hold: `import ... from`,
// `export ... from`, a bare `import "x"`, and a dynamic `import(`.
var migrationImport = regexp.MustCompile(`(?s)\b(?:(import|export)\b(?:[^;"'` + "`" + `]*?)\bfrom\s*(["'])([^"']*)["']|import\s*(["'])([^"']*)["']|import\s*\()`)

// CheckMigrationImports refuses a migration file with any import other than
// `import type { ... } from "./schema.d.ts"` (design §5.1): a value import would need a
// runtime and could pull code from anywhere. The error names the file and the line.
func CheckMigrationImports(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := blankCommentsAndStrings(string(data))
	for _, loc := range migrationImport.FindAllStringSubmatchIndex(text, -1) {
		stmt := text[loc[0]:loc[1]]
		line := 1 + strings.Count(text[:loc[0]], "\n")
		orig := string(data)[loc[0]:loc[1]]
		if strings.HasPrefix(stmt, "import") && !strings.HasPrefix(stmt, "import(") && !strings.HasPrefix(stmt, "import (") {
			rest := strings.TrimLeft(stmt[len("import"):], " \t\r\n")
			if strings.HasPrefix(rest, "type") && (len(rest) == 4 || strings.ContainsRune(" \t\r\n{*", rune(rest[4]))) && strings.HasSuffix(strings.TrimSpace(importSpecifier(orig)), "./schema.d.ts") {
				continue
			}
		}
		return fmt.Errorf("%s:%d: a migration may only `import type { ... } from \"./schema.d.ts\"`, not %s (a migration is pure, and runs with no modules but its own)", path, line, firstLine(orig))
	}
	return nil
}

// importSpecifier is the module name of an import statement's text, or "".
func importSpecifier(stmt string) string {
	m := regexp.MustCompile(`from\s*["']([^"']*)["']\s*$`).FindStringSubmatch(strings.TrimSpace(stmt))
	if m == nil {
		return ""
	}
	return m[1]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return "`" + strings.TrimSpace(s) + "`"
}

// blankCommentsAndStrings returns src with the text of comments replaced by spaces, and of
// template literals, keeping newlines and the quotes of plain strings, so offsets and line
// numbers stay those of src. An import in a comment is not an import.
func blankCommentsAndStrings(src string) string {
	b := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to && i < len(b); i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	for i := 0; i < len(b); {
		switch {
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			j := i + 2
			for j+1 < len(b) && (b[j] != '*' || b[j+1] != '/') {
				j++
			}
			j = min(j+2, len(b))
			blank(i, j)
			i = j
		case b[i] == '"' || b[i] == '\'':
			q := b[i]
			j := i + 1
			for j < len(b) && b[j] != q && b[j] != '\n' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			i = min(j+1, len(b))
		case b[i] == '`':
			j := i + 1
			for j < len(b) && b[j] != '`' {
				if b[j] == '\\' {
					j++
				}
				j++
			}
			blank(i+1, j)
			i = min(j+1, len(b))
		default:
			i++
		}
	}
	return string(b)
}
