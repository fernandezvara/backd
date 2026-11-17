package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits of what the source of a function returns: administrators read code in a page, not
// megabytes of it.
const (
	MaxSourceFileBytes  = 256 << 10 // the content of one file is cut here
	MaxSourceTotalBytes = 2 << 20   // all the contents together; later files come without content
	maxSourceFiles      = 200       // files of one function's folder; more are left out (see Source.Omitted)
)

// SourceFile is one file of a function's source.
type SourceFile struct {
	// Path is relative to the functions project (the database's `_functions/`), with slashes:
	// `refund/index.ts`, `lib/relay.ts`, `deno.json`.
	Path string `json:"path"`
	// Role is `entry` (the module that runs), `config` (function.yaml), `schema` (input and
	// output schemas), `code` (other files of the function's folder), `shared` (code outside the
	// folder that the function imports, such as `lib/`) or `project` (deno.json).
	Role     string `json:"role"`
	Language string `json:"language"` // typescript, javascript, json, yaml, markdown or text
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	// Content is the file as text. It is null when the file is binary or the response already
	// carries MaxSourceTotalBytes; when a single file is over MaxSourceFileBytes it holds the
	// first part and Truncated is true.
	Content   *string `json:"content"`
	Truncated bool    `json:"truncated"`
	Binary    bool    `json:"binary"`
}

// SourceDependency is one module the function's code imports, found by reading its import
// statements (not by running them).
type SourceDependency struct {
	Specifier string `json:"specifier"` // as written in the code
	// Kind is `local` (a file of the project), `npm`, `jsr`, `node` (a Node built-in), `url`
	// (an https module), `backd` (the runtime's own `@backd/functions`) or `unresolved` (a bare
	// name that deno.json doesn't map).
	Kind string `json:"kind"`
	// Resolved is the project path of a local dependency; null when the file is not there or
	// is outside the project.
	Resolved *string `json:"resolved"`
	// MapsTo is what deno.json's `imports` says a bare specifier stands for; null otherwise.
	MapsTo  *string  `json:"maps_to"`
	Package *string  `json:"package"` // the npm or jsr package, without version or path
	Version *string  `json:"version"` // the version or range written, if any
	UsedBy  []string `json:"used_by"` // the files that import it
}

// SourceBundle is what `backd-server functions build` made of the project.
type SourceBundle struct {
	SHA256          string `json:"sha256"`
	BuiltWithDeno   string `json:"built_with_deno"`
	BuiltSourceHash string `json:"built_source_hash"`
	// Current says the bundle was built from the source on disk now, with the Deno this backd
	// uses: what runs is what the files say.
	Current bool `json:"current"`
}

// Source is a function's source, read-only, as the admin API shows it.
type Source struct {
	Function   string `json:"function"` // database/name
	Database   string `json:"database"`
	Name       string `json:"name"`
	Entry      string `json:"entry"` // project path of the entry module
	SourceHash string `json:"source_hash"`
	Deno       string `json:"deno"`
	// Bundle is null when the project has not been built.
	Bundle       *SourceBundle      `json:"bundle"`
	Files        []SourceFile       `json:"files"`
	Dependencies []SourceDependency `json:"dependencies"`
	// Omitted counts the files of the function's folder left out because it has more than
	// maxSourceFiles.
	Omitted int `json:"omitted"`
}

var (
	importFrom   = regexp.MustCompile(`(?m)\b(?:import|export)\b[^'"` + "`" + `;]*?\bfrom\s*["']([^"'\n]+)["']`)
	importBare   = regexp.MustCompile(`(?m)\bimport\s*["']([^"'\n]+)["']`)
	importDyn    = regexp.MustCompile(`\bimport\s*\(\s*["']([^"'\n]+)["']\s*\)`)
	npmSpecifier = regexp.MustCompile(`^(?:npm|jsr):(@[^/@]+/[^/@]+|[^/@]+)(?:@([^/]+))?`)
)

// ReadSource reads fn's source from its project on disk: the function's own files, the local
// modules they import (transitively) and deno.json. Hidden entries, `node_modules` and the
// generated types file are never read, and neither is anything that is not a regular file.
func (f *Functions) ReadSource(fn *Function) (*Source, error) {
	hash, err := SourceHash(f.Dir)
	if err != nil {
		return nil, err
	}
	out := &Source{
		Function: fn.Database + "/" + fn.Name, Database: fn.Database, Name: fn.Name,
		Entry: fn.Entry, SourceHash: hash, Deno: DenoVersion,
		Files: []SourceFile{}, Dependencies: []SourceDependency{},
	}
	if m, err := ReadManifest(f.Dir); err == nil {
		if b, ok := m.Functions[fn.Name]; ok {
			out.Bundle = &SourceBundle{SHA256: b.SHA256, BuiltWithDeno: m.Deno, BuiltSourceHash: m.Source, Current: m.Source == hash && m.Deno == DenoVersion}
		}
	}

	r := &sourceReader{root: f.Dir, seen: map[string]bool{}, out: out}
	folder := path.Dir(fn.Entry) // the function's folder, relative to the project
	// The function's own folder first: entry, config, schemas, then the rest by name.
	own, omitted := r.listFolder(folder)
	out.Omitted = omitted
	sort.SliceStable(own, func(i, j int) bool {
		a, b := roleRank(own[i], fn.Entry), roleRank(own[j], fn.Entry)
		if a != b {
			return a < b
		}
		return own[i] < own[j]
	})
	for _, p := range own {
		r.add(p, roleOf(p, fn.Entry))
	}
	// The project's deno.json, which maps the bare specifiers.
	var importMap map[string]string
	for _, name := range []string{"deno.json", "deno.jsonc"} {
		if r.add(name, "project") {
			importMap = r.importMap(name)
			break
		}
	}
	// Local modules the code imports, however far: shared code lives outside the folder.
	for i := 0; i < len(out.Files); i++ {
		file := out.Files[i]
		if file.Language != "typescript" && file.Language != "javascript" {
			continue
		}
		for _, spec := range r.specifiers(file.Path) {
			dep := r.classify(spec, file.Path, importMap)
			if dep.Kind == "local" && dep.Resolved != nil {
				r.add(*dep.Resolved, "shared")
			}
		}
	}
	// Dependencies are listed after every local file was found, so each knows who uses it.
	deps := map[string]*SourceDependency{}
	for _, file := range out.Files {
		if file.Language != "typescript" && file.Language != "javascript" {
			continue
		}
		for _, spec := range r.specifiers(file.Path) {
			d := r.classify(spec, file.Path, importMap)
			key := d.Kind + "\x00" + d.Specifier + "\x00" + deref(d.Resolved)
			if have, ok := deps[key]; ok {
				have.UsedBy = appendUnique(have.UsedBy, file.Path)
				continue
			}
			d.UsedBy = []string{file.Path}
			deps[key] = &d
		}
	}
	for _, d := range deps {
		out.Dependencies = append(out.Dependencies, *d)
	}
	sort.Slice(out.Dependencies, func(i, j int) bool {
		a, b := out.Dependencies[i], out.Dependencies[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Specifier < b.Specifier
	})
	return out, nil
}

type sourceReader struct {
	root  string
	seen  map[string]bool
	out   *Source
	spent int // bytes of content returned so far
	cache map[string][]string
}

// listFolder returns the regular files under folder (project paths), skipping hidden entries
// and node_modules, up to maxSourceFiles; the second value counts those left out.
func (r *sourceReader) listFolder(folder string) ([]string, int) {
	var files []string
	omitted := 0
	base := filepath.Join(r.root, filepath.FromSlash(folder))
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
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
		rel, err := filepath.Rel(r.root, p)
		if err != nil {
			return nil
		}
		if len(files) >= maxSourceFiles {
			omitted++
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, omitted
}

func roleOf(p, entry string) string {
	switch {
	case p == entry:
		return "entry"
	case path.Base(p) == FunctionFile:
		return "config"
	case path.Base(p) == InputSchemaFile || path.Base(p) == OutputSchemaFile:
		return "schema"
	}
	return "code"
}

func roleRank(p, entry string) int {
	switch roleOf(p, entry) {
	case "entry":
		return 0
	case "config":
		return 1
	case "schema":
		return 2
	}
	return 3
}

// add reads the project file p once, as a file of the response. It says whether the file exists.
func (r *sourceReader) add(p, role string) bool {
	if r.seen[p] {
		return true
	}
	clean := path.Clean(p)
	if clean != p || strings.HasPrefix(p, "../") || path.IsAbs(p) || p == ".." {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") || part == "node_modules" {
			return false // hidden entries are never read
		}
	}
	if p == TypesFile {
		return false
	}
	full := filepath.Join(r.root, filepath.FromSlash(p))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	r.seen[p] = true
	sum := sha256.Sum256(data)
	file := SourceFile{Path: p, Role: role, Language: languageOf(p), Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	switch {
	case !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0:
		file.Binary = true
	case r.spent >= MaxSourceTotalBytes:
		file.Truncated = true
	default:
		text := data
		if len(text) > MaxSourceFileBytes {
			text = text[:MaxSourceFileBytes]
			for len(text) > 0 && !utf8.Valid(text) {
				text = text[:len(text)-1] // never cut a character in two
			}
			file.Truncated = true
		}
		if r.spent+len(text) > MaxSourceTotalBytes {
			file.Truncated = true
			file.Content = nil
		} else {
			s := string(text)
			file.Content = &s
			r.spent += len(text)
		}
	}
	r.out.Files = append(r.out.Files, file)
	return true
}

func languageOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".ts", ".mts", ".cts", ".tsx":
		return "typescript"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".json", ".jsonc":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md":
		return "markdown"
	}
	return "text"
}

// specifiers returns the import specifiers written in the project file p (read again from the
// text already returned: a file without content has none).
func (r *sourceReader) specifiers(p string) []string {
	if r.cache == nil {
		r.cache = map[string][]string{}
	}
	if s, ok := r.cache[p]; ok {
		return s
	}
	var content string
	for _, f := range r.out.Files {
		if f.Path == p && f.Content != nil && !f.Truncated {
			content = *f.Content
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, re := range []*regexp.Regexp{importFrom, importBare, importDyn} {
		for _, m := range re.FindAllStringSubmatch(stripComments(content), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	sort.Strings(out)
	r.cache[p] = out
	return out
}

// stripComments blanks // and /* */ comments so an import in a comment is not a dependency. It
// is a scan, not a parser: strings that hold "//" (such as URLs) are left alone.
func stripComments(s string) string {
	var b strings.Builder
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == inStr || (c == '\n' && inStr != '`') {
				inStr = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'' || c == '`':
			inStr = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				if s[i] == '\n' {
					b.WriteByte('\n')
				}
				i++
			}
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// importMap reads the `imports` of a deno.json file already added.
func (r *sourceReader) importMap(p string) map[string]string {
	for _, f := range r.out.Files {
		if f.Path != p || f.Content == nil {
			continue
		}
		var doc struct {
			Imports map[string]string `json:"imports"`
		}
		// deno.jsonc may have comments.
		if err := json.Unmarshal([]byte(stripComments(*f.Content)), &doc); err != nil {
			return nil
		}
		return doc.Imports
	}
	return nil
}

// classify says what the specifier written in file `from` stands for.
func (r *sourceReader) classify(spec, from string, importMap map[string]string) SourceDependency {
	d := SourceDependency{Specifier: spec, Kind: "unresolved", UsedBy: []string{}}
	target := spec
	if spec == "@backd/functions" {
		d.Kind = "backd"
		return d
	}
	if !isPathSpecifier(spec) && !strings.Contains(spec, ":") {
		// A bare name: deno.json may map it, exactly or as a prefix ("pkg/" → "npm:pkg@1/").
		mapped, ok := importMap[spec]
		if !ok {
			best := ""
			for k, v := range importMap {
				if strings.HasSuffix(k, "/") && strings.HasPrefix(spec, k) && len(k) > len(best) {
					best, mapped, ok = k, v+spec[len(k):], true
				}
			}
		}
		if !ok {
			return d
		}
		d.MapsTo = &mapped
		target = mapped
	}
	switch {
	case strings.HasPrefix(target, "npm:") || strings.HasPrefix(target, "jsr:"):
		d.Kind = target[:3]
		if m := npmSpecifier.FindStringSubmatch(target); m != nil {
			pkg := m[1]
			d.Package = &pkg
			if m[2] != "" {
				v := m[2]
				d.Version = &v
			}
		}
	case strings.HasPrefix(target, "node:"):
		d.Kind = "node"
	case strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://"):
		d.Kind = "url"
	case isPathSpecifier(target):
		d.Kind = "local"
		var resolved string
		if strings.HasPrefix(target, "/") {
			return d
		}
		base := path.Dir(from)
		if d.MapsTo != nil {
			base = "." // deno.json paths are relative to the project
		}
		resolved = path.Join(base, target)
		if resolved == ".." || strings.HasPrefix(resolved, "../") {
			return d
		}
		if r.exists(resolved) {
			d.Resolved = &resolved
		}
	}
	return d
}

func isPathSpecifier(s string) bool {
	return strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/")
}

func (r *sourceReader) exists(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") && part != "." || part == "node_modules" {
			return false
		}
	}
	info, err := os.Lstat(filepath.Join(r.root, filepath.FromSlash(p)))
	return err == nil && info.Mode().IsRegular()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}
