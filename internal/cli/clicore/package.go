package clicore

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/fernandezvara/backd/internal/authcore"
	"github.com/fernandezvara/backd/internal/exportpkg"
)

type ExportedPart struct {
	Checksum  string `json:"checksum"`
	Size      int64  `json:"size"`
	Documents int64  `json:"documents,omitempty"`
}

// ciphertextFields lists the encrypted fields a package still holds as ciphertext.
func CiphertextFields(m *exportpkg.Manifest) []string {
	var out []string
	for _, e := range m.Encrypted {
		if e.Form == exportpkg.FormCiphertext {
			out = append(out, e.Database+"/"+e.Collection+"."+e.Path)
		}
	}
	return out
}

// openPackage returns the directory of a package: the path itself, or an archive extracted
// to a private temporary directory that cleanup removes.
func OpenPackage(from string) (string, func(), error) {
	st, err := os.Stat(from)
	if err != nil {
		return "", nil, err
	}
	if st.IsDir() {
		return from, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "backd-import-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if err := extractArchive(from, tmp); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%s: %w", from, err)
	}
	// The archive holds one directory (what `backd export --archive` writes) or the files themselves.
	if _, err := os.Stat(filepath.Join(tmp, exportpkg.ManifestName)); err != nil {
		entries, _ := os.ReadDir(tmp)
		if len(entries) == 1 && entries[0].IsDir() {
			return filepath.Join(tmp, entries[0].Name()), cleanup, nil
		}
	}
	return tmp, cleanup, nil
}

// verifyPackage reads the manifest and checks every file it lists against its checksum.
func VerifyPackage(dir string, stderr io.Writer) (*exportpkg.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, exportpkg.ManifestName))
	if err != nil {
		return nil, fmt.Errorf("this is not a backd export package: %w", err)
	}
	m, err := exportpkg.ParseManifest(data)
	if err != nil {
		return nil, err
	}
	var bad []string
	parts := make([]string, 0, len(m.Checksums))
	for p := range m.Checksums {
		parts = append(parts, p)
	}
	sort.Strings(parts)
	for _, p := range parts {
		if !exportpkg.SafePart(p) {
			return nil, fmt.Errorf("the manifest lists an unsafe path: %q", p)
		}
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			bad = append(bad, p+": missing")
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, err
		}
		if "sha256:"+hex.EncodeToString(h.Sum(nil)) != m.Checksums[p] {
			bad = append(bad, p+": checksum differs")
		}
	}
	// Every path the import will open comes from the manifest, so each must be the one this
	// package format names, and be checksummed: a hostile manifest can't point it elsewhere.
	for _, db := range m.Databases {
		for _, col := range db.Collections {
			if !exportpkg.SafePart(db.Name) || !exportpkg.SafePart(col.Name) || col.Part != exportpkg.CollectionPart(db.Name, col.Name) {
				return nil, fmt.Errorf("the manifest names an unexpected part for %s/%s: %q", db.Name, col.Name, col.Part)
			}
			if _, ok := m.Checksums[col.Part]; !ok {
				bad = append(bad, col.Part+": not in the manifest's checksums")
			}
		}
	}
	for _, ap := range m.Auth {
		if !slices.Contains(authcore.PortableCollections, ap.Name) || ap.Part != exportpkg.AuthPart(ap.Name) {
			return nil, fmt.Errorf("the manifest names an authentication part this backd doesn't know: %q", ap.Part)
		}
		if _, ok := m.Checksums[ap.Part]; !ok {
			bad = append(bad, ap.Part+": not in the manifest's checksums")
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("the package is damaged or was changed after it was made:\n  %s", strings.Join(bad, "\n  "))
	}
	fmt.Fprintf(stderr, "package checked: %d files match their checksums\n", len(parts))
	return m, nil
}

// extractArchive unpacks a .tar.gz, refusing anything that isn't a regular file under a
// relative path without dot segments.
func extractArchive(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a .tar.gz: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || !exportpkg.SafePart(name) {
			return fmt.Errorf("the archive holds an entry that isn't a plain file in the package: %q", h.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}
