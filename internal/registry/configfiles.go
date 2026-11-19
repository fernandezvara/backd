package registry

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// maxConfigFiles bounds how many files of one realm the admin API lists.
const maxConfigFiles = 2000

// ConfigFile is one file of a realm's configuration, as the admin API shows it.
type ConfigFile struct {
	SourceFile
	// Database and Collection say where the file belongs: both empty for the realm's own files,
	// the database alone for a functions project or migrations, both for a collection's files.
	Database   string `json:"database"`
	Collection string `json:"collection"`
	// Restricted says the file holds what the caller may not read (the realm's seeded user
	// emails are personal data): it is listed without its content.
	Restricted bool `json:"restricted"`
}

// ConfigFiles lists the files backd reads for one realm, relative to the config directory, with
// their text within the usual limits: realm.yaml, everything in `_realm/`, each collection's schema
// files, indexes and policy, and each database's functions project and migrations. Hidden entries,
// `node_modules`, the generated type files and anything that is not a regular file are left out,
// and so is whatever backd doesn't read (seed data, notes). Realm.yaml comes without its content
// when hideRealm is set. The second value counts the files left out for the limit.
func (r *Registry) ConfigFiles(realm string, hideRealm bool) ([]ConfigFile, int) {
	rl, ok := r.Realms[realm]
	if !ok {
		return nil, 0
	}
	base := filepath.Join(r.Root, realm)
	var rels []string
	omitted := 0
	add := func(rel string) {
		if len(rels) >= maxConfigFiles {
			omitted++
			return
		}
		rels = append(rels, rel)
	}
	walk := func(dir string, keep func(rel string) bool) {
		_ = filepath.WalkDir(filepath.Join(base, filepath.FromSlash(dir)), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if p != base && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if keep == nil || keep(rel) {
				add(rel)
			}
			return nil
		})
	}
	if fi, err := os.Stat(filepath.Join(base, RealmFile)); err == nil && fi.Mode().IsRegular() {
		add(RealmFile)
	}
	walk(RealmDir, nil)
	for _, db := range rl.Databases {
		for _, c := range db.Collections {
			dir := filepath.Join(db.Name, c.Name)
			entries, _ := os.ReadDir(filepath.Join(base, dir))
			for _, e := range entries {
				n := e.Name()
				if !e.Type().IsRegular() || strings.HasPrefix(n, ".") || n == generatedTypesFile {
					continue
				}
				if n == indexesFile || n == CollectionFile || strings.HasPrefix(n, "schema.") {
					add(path.Join(db.Name, c.Name, n))
				}
			}
		}
		walk(path.Join(db.Name, FunctionsDir), func(rel string) bool { return path.Base(rel) != TypesFile })
		walk(path.Join(db.Name, "_migrations"), nil)
	}
	sort.Strings(rels)

	out := make([]ConfigFile, 0, len(rels))
	spent := 0
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		cf := ConfigFile{}
		role, db, col := configRole(rel)
		cf.Database, cf.Collection = db, col
		cf.Path = path.Join(realm, rel)
		if hideRealm && rel == RealmFile {
			sum := newSourceFile(cf.Path, role, data, new(int))
			sum.Content, sum.Truncated = nil, false
			cf.SourceFile, cf.Restricted = sum, true
		} else {
			cf.SourceFile = newSourceFile(cf.Path, role, data, &spent)
		}
		out = append(out, cf)
	}
	return out, omitted
}

// configRole says what a file of the realm directory is for, and where it belongs.
func configRole(rel string) (role, database, collection string) {
	parts := strings.Split(rel, "/")
	switch {
	case rel == RealmFile:
		return "realm", "", ""
	case parts[0] == RealmDir:
		return "realm_config", "", ""
	}
	database = parts[0]
	if len(parts) >= 2 && parts[1] == FunctionsDir {
		return "function", database, ""
	}
	if len(parts) >= 2 && parts[1] == "_migrations" {
		return "migration", database, ""
	}
	if len(parts) >= 3 {
		collection = parts[1]
		switch n := parts[2]; {
		case n == CollectionFile:
			return "collection", database, collection
		case n == indexesFile:
			return "indexes", database, collection
		case strings.HasSuffix(n, ".ts"):
			return "migration", database, collection
		}
		return "schema", database, collection
	}
	return "other", database, ""
}
