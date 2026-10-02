// Package templates writes commented starter files for realms and
// databases into CONFIG_DIR (`backd template`).
package templates

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// all: — without it, embed silently drops files/dirs starting with "_"
// (sample/_functions and its contents, notably).
//
//go:embed all:files
var files embed.FS

// sampleDatabase is the database `template realm --sample` creates.
const sampleDatabase = "main"

// File reports what happened to one file.
type File struct {
	Path    string // relative to CONFIG_DIR, slash-separated
	Created bool   // false: it already existed and was left unchanged
}

// Realm creates <realm>/realm.yaml, and the default email templates under
// <realm>/email/, under configDir. With sample, it also
// creates the database "main" with the blog sample collection.
func Realm(configDir, realm string, sample bool) ([]File, error) {
	if !registry.ValidName(realm) {
		return nil, fmt.Errorf("invalid realm name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", realm)
	}
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	out, err := write(configDir, realm+"/"+registry.RealmFile, "files/realm.yaml")
	if err != nil {
		return out, err
	}
	// The English email templates, one folder per kind (used once the realm
	// configures `email:`).
	for _, kind := range email.SystemKinds {
		defaults, err := email.DefaultFiles(kind)
		if err != nil {
			return out, err
		}
		for _, name := range slices.Sorted(maps.Keys(defaults)) {
			more, err := writeBytes(configDir, realm+"/"+email.DirName+"/"+kind+"/"+name, defaults[name])
			if err != nil {
				return out, err
			}
			out = append(out, more...)
		}
	}
	// The hosted pages the links in emails open, in English.
	for _, kind := range email.PageKinds {
		data, err := email.DefaultPage(kind)
		if err != nil {
			return out, err
		}
		more, err := writeBytes(configDir, realm+"/"+email.PagesDirName+"/"+kind+"/en.html", data)
		if err != nil {
			return out, err
		}
		out = append(out, more...)
	}
	if !sample {
		return out, nil
	}
	more, err := writeSample(configDir, realm+"/"+sampleDatabase)
	return append(out, more...), err
}

// Database creates <realm>/<database>/ under configDir; the realm must
// already exist. With sample, it adds the blog sample collection.
func Database(configDir, realm, database string, sample bool) ([]File, error) {
	if !registry.ValidName(database) {
		return nil, fmt.Errorf("invalid database name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", database)
	}
	if slices.Contains(registry.ReservedDirs, database) {
		return nil, fmt.Errorf("%q can't be a database name: a realm's %s folder holds something else (email templates)", database, database)
	}
	if name := realm + "__" + database; len(name) >= 64 {
		return nil, fmt.Errorf("MongoDB database name %q must be shorter than 64 characters", name)
	}
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(filepath.Join(configDir, realm)); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("realm %q doesn't exist in %s (create it with `backd template realm --realm %s`)", realm, configDir, realm)
	}
	dir := realm + "/" + database
	if err := os.MkdirAll(filepath.Join(configDir, dir), 0o755); err != nil {
		return nil, err
	}
	if !sample {
		return nil, nil
	}
	return writeSample(configDir, dir)
}

// projectDatabase is the database `template project` creates the sample
// function in (alongside the sample "posts" collection, in the same
// database — a function can read any database in its realm, but keeping
// them together keeps the template's layout simple to read).
const projectDatabase = sampleDatabase

// Project creates a new, self-contained config repository at dir (which
// must not already exist): a realm with the sample database and
// collection, a sample function with tests, the Dockerfiles and compose
// stack (MongoDB, backd, the executor and egress) for local development,
// and the CI workflow from the docs' "Deploying config" guide — so
// "develop locally → CI → promote a tag" works from the first commit.
func Project(dir, realm string) ([]File, error) {
	if !registry.ValidName(realm) {
		return nil, fmt.Errorf("invalid realm name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", realm)
	}
	if fi, err := os.Stat(dir); err == nil {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			return nil, rerr
		}
		if !fi.IsDir() || len(entries) > 0 {
			return nil, fmt.Errorf("%s already exists and isn't an empty directory", dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}

	out, err := Realm(configDir, realm, true)
	for i := range out {
		out[i].Path = path.Join("config", filepath.ToSlash(out[i].Path))
	}
	if err != nil {
		return out, err
	}

	// deno.json and the stats function itself came from writeSample above
	// (Realm's sample database now includes a sample function, roadmap
	// F16); only the test-only extras are project-specific.
	fnDir := path.Join(realm, projectDatabase, registry.FunctionsDir)
	for _, f := range [][2]string{
		{fnDir + "/lib/testing.js", "files/project/testing.js"},
		{fnDir + "/stats/index.test.ts", "files/project/stats/index.test.ts"},
	} {
		written, err := write(configDir, f[0], f[1])
		for i := range written {
			written[i].Path = path.Join("config", filepath.ToSlash(written[i].Path))
		}
		out = append(out, written...)
		if err != nil {
			return out, err
		}
	}

	for _, f := range [][2]string{
		{"Dockerfile", "files/project/Dockerfile"},
		{"Dockerfile.executor", "files/project/Dockerfile.executor"},
		{"compose.yaml", "files/project/compose.yaml"},
		{".gitignore", "files/project/gitignore"},
		{".github/workflows/ci.yml", "files/project/ci.yml"},
	} {
		written, err := write(dir, f[0], f[1])
		out = append(out, written...)
		if err != nil {
			return out, err
		}
	}

	readme, err := files.ReadFile("files/project/README.md")
	if err != nil {
		return out, err
	}
	r := strings.NewReplacer("__REALM__", realm, "__DATABASE__", projectDatabase)
	written, err := writeBytes(dir, "README.md", []byte(r.Replace(string(readme))))
	out = append(out, written...)
	return out, err
}

// Function creates the function <name> in <realm>/<database>/_functions,
// with the project's deno.json if it has none; the database must exist.
func Function(configDir, realm, database, name string) ([]File, error) {
	if !registry.ValidName(name) || name == "lib" {
		return nil, fmt.Errorf("invalid function name %q: must be lowercase letters and digits, optionally separated by single '-' or '_' (and not lib, which is reserved for shared code)", name)
	}
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	dbDir := realm + "/" + database
	if fi, err := os.Stat(filepath.Join(configDir, dbDir)); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("database %s doesn't exist in %s (create it with `backd template database --realm %s --database %s`)", dbDir, configDir, realm, database)
	}
	project := dbDir + "/" + registry.FunctionsDir
	var out []File
	for _, f := range [][2]string{
		{project + "/deno.json", "files/function/deno.json"},
		{project + "/" + name + "/" + registry.FunctionFile, "files/function/function.yaml"},
		{project + "/" + name + "/index.ts", "files/function/index.ts"},
	} {
		written, err := write(configDir, f[0], f[1])
		out = append(out, written...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// EmailCapture adds the development delivery function email-capture to
// <realm>/<database>/_functions, and the collection outbox it writes to.
// The database must exist.
func EmailCapture(configDir, realm, database string) ([]File, error) {
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	dbDir := realm + "/" + database
	if fi, err := os.Stat(filepath.Join(configDir, dbDir)); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("database %s doesn't exist in %s (create it with `backd template database --realm %s --database %s`)", dbDir, configDir, realm, database)
	}
	project := dbDir + "/" + registry.FunctionsDir
	var out []File
	add := func(more []File, err error) error {
		out = append(out, more...)
		return err
	}
	if err := add(write(configDir, project+"/deno.json", "files/function/deno.json")); err != nil {
		return out, err
	}
	replacer := strings.NewReplacer("__DATABASE__", database)
	for _, f := range [][2]string{
		{project + "/email-capture/" + registry.FunctionFile, "files/email-capture/function.yaml"},
		{project + "/email-capture/index.ts", "files/email-capture/index.ts"},
		{dbDir + "/outbox/schema.json", "files/email-capture/outbox/schema.json"},
		{dbDir + "/outbox/rules.yaml", "files/email-capture/outbox/rules.yaml"},
		{dbDir + "/outbox/indexes.json", "files/email-capture/outbox/indexes.json"},
	} {
		data, err := files.ReadFile(f[1])
		if err != nil {
			return out, err
		}
		if err := add(writeBytes(configDir, f[0], []byte(replacer.Replace(string(data))))); err != nil {
			return out, err
		}
	}
	return out, nil
}

// CollectionPolicy creates <realm>/<database>/<collection>/collection.yaml, a
// commented example of what an erase can do to the collection (everything is
// commented out: without a policy an erase leaves the collection alone). The
// collection must exist.
func CollectionPolicy(configDir, realm, database, collection string) ([]File, error) {
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	dir := realm + "/" + database + "/" + collection
	if _, err := os.Stat(filepath.Join(configDir, dir, registry.SchemaFile)); err != nil {
		return nil, fmt.Errorf("collection %s doesn't exist in %s (it needs a %s)", dir, configDir, registry.SchemaFile)
	}
	return write(configDir, dir+"/"+registry.CollectionFile, "files/collection/collection.yaml")
}

// writeSample copies the embedded sample collections into dir.
func writeSample(configDir, dir string) ([]File, error) {
	var out []File
	err := fs.WalkDir(files, "files/sample", func(src string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel("files/sample", src)
		f, err := write(configDir, path.Join(dir, filepath.ToSlash(rel)), src)
		out = append(out, f...)
		return err
	})
	return out, err
}

// checkDir fails unless CONFIG_DIR exists, so a typo doesn't silently
// create a new tree.
func checkDir(configDir string) error {
	if fi, err := os.Stat(configDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("CONFIG_DIR %q is not an existing directory", configDir)
	}
	return nil
}

// write copies an embedded file to configDir/dst unless dst exists.
func write(configDir, dst, src string) ([]File, error) {
	data, err := files.ReadFile(src)
	if err != nil {
		return nil, err
	}
	return writeBytes(configDir, dst, data)
}

// writeBytes writes data to configDir/dst unless dst already exists.
func writeBytes(configDir, dst string, data []byte) ([]File, error) {
	full := filepath.Join(configDir, filepath.FromSlash(dst))
	if _, err := os.Stat(full); err == nil {
		return []File{{Path: dst}}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	return []File{{Path: dst, Created: true}}, f.Close()
}
