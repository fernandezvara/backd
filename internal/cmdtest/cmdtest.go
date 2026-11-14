// Package cmdtest has what the tests of the command line and the server share:
// CONFIG_DIR trees, built function manifests, and terminal input and output.
package cmdtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

// Write writes files under a new CONFIG_DIR and returns it.
func Write(t testing.TB, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// RulesSection is a collection.yaml holding the given rules: what a rules.yaml held before
// the rules became a section of collection.yaml. Append it to the rest of the file when the
// test has one.
func RulesSection(rules string) string {
	var b strings.Builder
	b.WriteString("rules:\n")
	for line := range strings.SplitSeq(strings.TrimRight(rules, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// WriteFuncManifest writes a one-function manifest as `backd functions
// build` would, so the handler's index finds a built bundle.
func WriteFuncManifest(t testing.TB, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, registry.BuildDir), 0o755); err != nil {
		t.Fatal(err)
	}
	m := registry.Manifest{Deno: registry.DenoVersion, Functions: map[string]registry.ManifestBundle{}}
	for _, name := range names {
		data := []byte("export default () => '" + name + "';\n")
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		file := name + "-" + hash[:8] + ".js"
		if err := os.WriteFile(filepath.Join(dir, registry.BuildDir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
		m.Functions[name] = registry.ManifestBundle{Bundle: file, SHA256: hash}
	}
	if src, err := registry.SourceHash(dir); err == nil {
		m.Source = src // so the build counts as fresh
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, registry.BuildDir, registry.ManifestFile), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// LockedBuffer is an output two goroutines may use.
type LockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *LockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *LockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Script is a standard input that answers each read with the next step: a fixed line, or
// one made from what the command has printed so far (the secret it showed).
type Script struct {
	Steps []func() string
	next  int
}

func (s *Script) Read(p []byte) (int, error) {
	if s.next >= len(s.Steps) {
		return 0, io.EOF
	}
	out := s.Steps[s.next]()
	s.next++
	return copy(p, out), nil
}

// WriteMigrationManifests writes a fake build of the schema migrations of every database of reg
// that has any, as `backd-server functions build` would.
func WriteMigrationManifests(t testing.TB, reg *registry.Registry) {
	t.Helper()
	for _, db := range reg.Databases() {
		colls := db.MigratingCollections()
		if len(colls) == 0 {
			continue
		}
		m := registry.MigrationManifest{Deno: registry.DenoVersion, Collections: map[string]registry.ManifestBundle{}}
		if err := os.MkdirAll(db.MigrationsBuildDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, c := range colls {
			data := []byte("export const steps = []; // " + c.Name + "\n")
			sum := sha256.Sum256(data)
			file := c.Name + ".js"
			if err := os.WriteFile(filepath.Join(db.MigrationsBuildDir(), file), data, 0o644); err != nil {
				t.Fatal(err)
			}
			m.Collections[c.Name] = registry.ManifestBundle{Bundle: file, SHA256: hex.EncodeToString(sum[:])}
		}
		m.Source, _ = registry.MigrationSourceHash(db)
		out, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(db.MigrationsBuildDir(), registry.ManifestFile), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
