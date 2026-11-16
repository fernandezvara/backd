// Package templates writes commented starter files for realms and
// databases into CONFIG_DIR (`backd template`).
package templates

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
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
	"github.com/fernandezvara/backd/internal/exportreadme"
	"github.com/fernandezvara/backd/internal/push"
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
// <realm>/_realm/email/, under configDir. With sample, it also
// creates the database "main" with the blog sample collection.
func Realm(configDir, realm string, sample bool) ([]File, error) {
	return realmWith(configDir, realm, sample, false)
}

// realmWith is Realm, and with noMFA the realm.yaml of a development realm (see devRealmYAML).
func realmWith(configDir, realm string, sample, noMFA bool) ([]File, error) {
	if !registry.ValidName(realm) {
		return nil, fmt.Errorf("invalid realm name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", realm)
	}
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	realmYAML, err := files.ReadFile("files/realm.yaml")
	if err != nil {
		return nil, err
	}
	if noMFA {
		if realmYAML, err = devRealmYAML(realmYAML); err != nil {
			return nil, err
		}
	}
	out, err := writeBytes(configDir, realm+"/"+registry.RealmFile, withAuditEntries(realmYAML))
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
			more, err := writeBytes(configDir, realm+"/"+registry.RealmDir+"/"+email.DirName+"/"+kind+"/"+name, defaults[name])
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
		more, err := writeBytes(configDir, realm+"/"+registry.RealmDir+"/"+email.PagesDirName+"/"+kind+"/en.html", data)
		if err != nil {
			return out, err
		}
		out = append(out, more...)
	}
	// The page shown when a provider sends the user back with a sign-in backd can't
	// trace to an app; the built-in one is used without this file.
	for _, kind := range email.OptionalPageKinds {
		data, err := email.DefaultPage(kind)
		if err != nil {
			return out, err
		}
		more, err := writeBytes(configDir, realm+"/"+registry.RealmDir+"/"+email.PagesDirName+"/"+kind+"/en.html", data)
		if err != nil {
			return out, err
		}
		out = append(out, more...)
	}
	// The README of a personal data export, in English: used once the realm sets
	// `compliance.export`. Edit it to say what your users should know, or add one per language.
	readme, ok := exportreadme.DefaultReadme(exportreadme.DefaultReadmeLocale)
	if !ok {
		return out, errors.New("the built-in README template is missing")
	}
	more, err := writeBytes(configDir, realm+"/"+registry.RealmDir+"/"+exportreadme.ReadmeDirName+"/"+exportreadme.DefaultReadmeLocale+".html", readme)
	if err != nil {
		return out, err
	}
	out = append(out, more...)
	// An example push template, used once the realm configures `push:`.
	more, err = write(configDir, realm+"/"+registry.RealmDir+"/"+push.DirName+"/example/en.yaml", "files/push/example.yaml")
	if err != nil {
		return out, err
	}
	out = append(out, more...)
	if !sample {
		return out, nil
	}
	more, err = writeSample(configDir, realm+"/"+sampleDatabase)
	return append(out, more...), err
}

// Database creates <realm>/<database>/ under configDir; the realm must
// already exist. With sample, it adds the blog sample collection.
func Database(configDir, realm, database string, sample bool) ([]File, error) {
	if !registry.ValidName(database) {
		return nil, fmt.Errorf("invalid database name %q: must be lowercase letters and digits, optionally separated by single '-' or '_'", database)
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

// ProjectOptions are the choices of Project.
type ProjectOptions struct {
	// NoMFA makes a development realm: its administrator signs in with a password alone, so the
	// first request needs no authenticator app. For local development only; the default keeps the
	// second factor.
	NoMFA bool
}

// devBanner opens the realm.yaml of a development realm.
const devBanner = `# LOCAL DEVELOPMENT ONLY (backd template project --no-mfa): the administrator of this realm
# signs in with a password alone, with no second factor, so that a first request needs no
# authenticator app. Never deploy it as it is: a stolen password would open everything. Before
# anyone else uses this realm, put the second step back (see "admin_password" below).

`

// devRealmYAML turns the template's realm.yaml into the development realm's: no strategy with a
// second step and an admin role that takes the password alone. It fails when the template no
// longer has the lines it changes, so a change of the template can't silently leave the second
// factor in a "development" realm (or take it out of the default).
func devRealmYAML(data []byte) ([]byte, error) {
	s := string(data)
	const strategy = "    admin_password: [password, totp]\n"
	const role = "    strategies: [admin_password]   # the operators hold the keys to everything: a second step\n"
	if !strings.Contains(s, strategy) || !strings.Contains(s, role) {
		return nil, errors.New("the realm.yaml template changed: update devRealmYAML")
	}
	s = strings.Replace(s, strategy, "    # admin_password: [password, totp]   # the second step, for a realm that serves anyone else\n", 1)
	s = strings.Replace(s, role, "    # strategies: [admin_password]       # LOCAL DEVELOPMENT ONLY: no second step for the administrator; add this line back\n", 1)
	return []byte(devBanner + s), nil
}

// projectEnv is the .env of a project: the three secrets its compose stack needs, random and
// different for every project.
func projectEnv() ([]byte, error) {
	var b strings.Builder
	b.WriteString("# The secrets of this project's local stack, generated by `backd template project`.\n")
	b.WriteString("# compose reads this file on every run. It is not committed (see .gitignore).\n")
	for _, name := range []string{"EXECUTOR_TOKEN", "CALLBACK_KEY", "EGRESS_KEY"} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s=%s\n", name, base64.StdEncoding.EncodeToString(raw))
	}
	return []byte(b.String()), nil
}

// projectLogin is what the README says about signing in as the first administrator: the command,
// and a note on the second factor that matches the realm.
func projectLogin(realm string, noMFA bool) (command, note string) {
	if noMFA {
		return "backd login --realm " + realm + " --url http://127.0.0.1:8080 --email ops@example.com",
			"   This project was created with `--no-mfa`: **the administrator has no second factor**, so a password is enough. That is for working on your own machine only. Before this realm serves anyone else, put the second step back in `config/" + realm + "/realm.yaml` (the commented `admin_password` strategy and the role's `strategies:` line), and read the [hardening checklist](https://fernandezvara.github.io/backd/docs/operate/checklist/)."
	}
	return "backd login --realm " + realm + " --url http://127.0.0.1:8080 --email ops@example.com --strategy admin_password",
		"   The `admin` role requires a second factor, so this first login makes you enroll an authenticator app (any TOTP app) before it opens. To work without one on your own machine, create the project with `backd template project --no-mfa`."
}

// Project creates a new, self-contained config repository at dir (which
// must not already exist): a realm with the sample database and
// collection, a sample function with tests, a compose stack on the
// released images (MongoDB, backd in dev mode, the executor, egress and
// MinIO) for local development, the check-only backd.yml workflow, an
// AGENTS.md for IDE assistants, and a README.
func Project(dir, realm string, opts ProjectOptions) ([]File, error) {
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

	out, err := realmWith(configDir, realm, true, opts.NoMFA)
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
		{"compose.yaml", "files/project/compose.yaml"},
		{".gitignore", "files/project/gitignore"},
		{".github/workflows/backd.yml", "files/project/backd.yml"},
	} {
		written, err := write(dir, f[0], f[1])
		out = append(out, written...)
		if err != nil {
			return out, err
		}
	}

	env, err := projectEnv()
	if err != nil {
		return out, err
	}
	written, err := writeBytesMode(dir, ".env", env, 0o600)
	out = append(out, written...)
	if err != nil {
		return out, err
	}

	login, note := projectLogin(realm, opts.NoMFA)
	r := strings.NewReplacer("__REALM__", realm, "__DATABASE__", projectDatabase, "__LOGIN__", login, "__LOGIN_NOTE__", note)
	for _, name := range []string{"README.md", "AGENTS.md"} {
		data, err := files.ReadFile("files/project/" + name)
		if err != nil {
			return out, err
		}
		written, err := writeBytes(dir, name, []byte(r.Replace(string(data))))
		out = append(out, written...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
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
		{dbDir + "/outbox/" + registry.CollectionFile, "files/email-capture/outbox/collection.yaml"},
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
// commented example of the collection's rules, soft delete, and what an erase can do to it (everything is
// commented out: without a policy an erase leaves the collection alone). The
// collection must exist.
func CollectionPolicy(configDir, realm, database, collection string) ([]File, error) {
	if err := checkDir(configDir); err != nil {
		return nil, err
	}
	dir := realm + "/" + database + "/" + collection
	if !registry.HasSchema(filepath.Join(configDir, dir)) {
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
	return writeBytesMode(configDir, dst, data, 0o644)
}

// writeBytesMode is writeBytes with the file's permissions: the secrets of a project are its
// owner's alone.
func writeBytesMode(configDir, dst string, data []byte, perm os.FileMode) ([]File, error) {
	full := filepath.Join(configDir, filepath.FromSlash(dst))
	if _, err := os.Stat(full); err == nil {
		return []File{{Path: dst}}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	return []File{{Path: dst, Created: true}}, f.Close()
}

// withAuditEntries fills the #AUDIT_ENTRIES# line of realm.yaml with one line per audit action, in
// groups and with their defaults, generated from the registry so the template can't go stale.
func withAuditEntries(realmYAML []byte) []byte {
	var b strings.Builder
	for i, group := range registry.AuditGroups {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "    # %s\n", group)
		for _, a := range registry.AuditActions {
			if a.Group == group {
				fmt.Fprintf(&b, "    %s: %t\n", a.Name, !a.Off)
			}
		}
	}
	return []byte(strings.Replace(string(realmYAML), "#AUDIT_ENTRIES#\n", b.String(), 1))
}
