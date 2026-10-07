package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/registry"
)

// Limits of a configuration check: it copies the realm's configuration, so it is bounded.
const (
	maxCheckFiles     = 50      // files in one draft
	maxCheckFileBytes = 1 << 20 // one draft file
	maxRealmFiles     = 5000    // files of the realm it copies
	maxRealmBytes     = 64 << 20
)

// checkSlots lets a couple of checks run at once: each copies a realm's files.
var checkSlots = make(chan struct{}, 2)

type configCheckBody struct {
	// Files are the draft files by path relative to the realm's directory
	// (`main/notes/rules.yaml`, `realm.yaml`); a null value deletes the file.
	Files map[string]*string `json:"files"`
}

type configCheckProblem struct {
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}

// checkConfig answers POST /v1/{realm}/_admin/config/check: it loads the realm's whole
// configuration as startup does, with the draft files in place of (or beside) the ones on
// disk, and reports what it found. Every file of the realm is read and validated, not just
// the drafts, since a change can break another file (a rule naming a field a schema no
// longer has). It runs on a copy: nothing on the server is changed, and nothing is applied.
// Function bundles are built by `backd functions build`, outside this check.
func (a *adminAPI) checkConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "realm")
	if _, ok := a.reg.Realms[name]; !ok {
		notFound(w, r)
		return
	}
	var body configCheckBody
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(maxCheckFiles)*maxCheckFileBytes+(1<<16)))
	if err != nil {
		tooLarge(w, r, int64(maxCheckFiles)*maxCheckFileBytes)
		return
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Files == nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, `the body must be {"files": {"<path in the realm>": "<content>" | null}}`)
		return
	}
	if len(body.Files) > maxCheckFiles {
		writeError(w, r, http.StatusBadRequest, codeValidation, "too many files in one check")
		return
	}
	for p, content := range body.Files {
		if !validDraftPath(p) {
			writeError(w, r, http.StatusBadRequest, codeValidation, "a draft file's path is relative to the realm and inside it", Detail{Path: p, Reason: "invalid path"})
			return
		}
		if content != nil && len(*content) > maxCheckFileBytes {
			writeError(w, r, http.StatusBadRequest, codeValidation, "a draft file is too large", Detail{Path: p, Reason: "over 1 MiB"})
			return
		}
	}
	select {
	case checkSlots <- struct{}{}:
		defer func() { <-checkSlots }()
	default:
		w.Header().Set("Retry-After", "2")
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "other configuration checks are running; retry in a moment")
		return
	}

	tmp, err := os.MkdirTemp("", "backd-config-check-*")
	if err != nil {
		adminError(w, r, err)
		return
	}
	defer os.RemoveAll(tmp)
	dst := filepath.Join(tmp, name)
	if err := copyRealm(filepath.Join(a.reg.Root, name), dst); err != nil {
		if errors.Is(err, errRealmTooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, codePayloadTooLarge, "this realm's configuration is too large to check here")
			return
		}
		adminError(w, r, err)
		return
	}
	for p, content := range body.Files {
		target := filepath.Join(dst, filepath.FromSlash(p))
		if content == nil {
			_ = os.Remove(target)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			adminError(w, r, err)
			return
		}
		if err := os.WriteFile(target, []byte(*content), 0o644); err != nil {
			adminError(w, r, err)
			return
		}
	}

	checked := countFiles(dst)
	problems := []configCheckProblem{}
	if _, err := registry.Load(tmp); err != nil {
		for _, e := range splitErrors(err) {
			problems = append(problems, problem(e.Error(), tmp+string(filepath.Separator)+name+string(filepath.Separator)))
		}
	}
	changed := make([]string, 0, len(body.Files))
	for p := range body.Files {
		changed = append(changed, p)
	}
	sort.Strings(changed)
	writeJSON(w, http.StatusOK, map[string]any{"ok": len(problems) == 0, "realm": name, "checked": checked, "changed": changed, "problems": problems})
}

// validDraftPath: relative, forward slashes, no `..`, no hidden or odd segments.
func validDraftPath(p string) bool {
	if p == "" || len(p) > 200 || path.IsAbs(p) || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) || path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

var errRealmTooLarge = errors.New("realm too large")

// copyRealm copies a realm's configuration files, leaving out hidden entries (such as the
// functions' .build) and anything that isn't a regular file.
func copyRealm(src, dst string) error {
	var files, size int64
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if files++; files > maxRealmFiles {
			return errRealmTooLarge
		}
		if size += info.Size(); size > maxRealmBytes {
			return errRealmTooLarge
		}
		in, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, in, 0o644)
	})
}

func countFiles(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// splitErrors lists the errors a Load joined.
func splitErrors(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, splitErrors(e)...)
		}
		return out
	}
	return []error{err}
}

// problem turns an error message into a problem: the temporary directory's prefix is
// removed from the paths, and the file the message starts with (when it starts with one)
// is named.
func problem(msg, prefix string) configCheckProblem {
	msg = strings.ReplaceAll(msg, prefix, "")
	p := configCheckProblem{Message: msg}
	if i := strings.Index(msg, ": "); i > 0 && !strings.ContainsAny(msg[:i], " ") {
		p.File = msg[:i]
	}
	return p
}
