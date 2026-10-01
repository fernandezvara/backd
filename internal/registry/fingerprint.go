package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/email"
)

const fingerprintSalt = "backd-config-v1\x00"

// Fingerprint identifies the config: a SHA-256 over every file backd
// reads from CONFIG_DIR — realm.yaml, each collection's schema.json,
// indexes.json and rules.yaml, and each functions project with its
// bundles — by relative path, length and content. Files backd ignores
// (hidden entries such as .git, stray files) don't change it, so the same
// checkout always has the same fingerprint, on any machine.
func (r *Registry) Fingerprint() (string, error) {
	if r.Root == "" {
		return "", errors.New("registry has no root directory")
	}
	var files []string
	add := func(rel string) {
		if fi, err := os.Stat(filepath.Join(r.Root, rel)); err == nil && fi.Mode().IsRegular() {
			files = append(files, filepath.ToSlash(rel))
		}
	}
	for _, rl := range r.SortedRealms() {
		add(filepath.Join(rl.Name, RealmFile))
		if rl.Settings.Email != nil {
			// The templates backd renders its emails from.
			dir := filepath.Join(r.Root, rl.Name, email.DirName)
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".") {
					if rel, err := filepath.Rel(r.Root, path); err == nil {
						files = append(files, filepath.ToSlash(rel))
					}
				}
				return nil
			})
		}
		for _, db := range rl.Databases {
			for _, c := range db.Collections {
				for _, f := range []string{schemaFile, indexesFile, rulesFile} {
					add(filepath.Join(rl.Name, db.Name, c.Name, f))
				}
			}
			if db.Functions == nil {
				continue
			}
			proj := filepath.Join(rl.Name, db.Name, FunctionsDir)
			err := filepath.WalkDir(filepath.Join(r.Root, proj), func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(r.Root, path)
				if err != nil {
					return err
				}
				// Hidden entries don't count, except the build output.
				if strings.HasPrefix(d.Name(), ".") && rel != filepath.Join(proj, BuildDir) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if d.Type().IsRegular() {
					files = append(files, filepath.ToSlash(rel))
				}
				return nil
			})
			if err != nil {
				return "", err
			}
		}
	}
	slices.Sort(files)
	h := sha256.New()
	h.Write([]byte(fingerprintSalt))
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("fingerprint: %w", err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RealmNames returns every configured realm's name, sorted.
func (r *Registry) RealmNames() []string {
	out := make([]string, 0, len(r.Realms))
	for _, rl := range r.SortedRealms() {
		out = append(out, rl.Name)
	}
	return out
}
