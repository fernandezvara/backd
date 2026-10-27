package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/kli"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
)

// Encrypted fields in packages (design/field-encryption.md §9). The server always exports them as
// ciphertext with the wrapped keys; whoever holds a field's key opens it here, in the command,
// with the format published in "Encrypted fields format". Keys are given as `--field`:
// `<path>=<source>` entries separated by `;`, where the path is `health_notes` (any collection
// with such a field) or `main/patients.health_notes`, and the source is key:, env: or file:.

// fieldKey is one key given with --field.
type fieldKey struct {
	database, collection, path string
	spec                       string // as given, for messages
	key                        []byte
	fingerprint                string
	used                       bool
}

func (k *fieldKey) matches(e exportpkg.EncryptedField) bool {
	if k.path != e.Path {
		return false
	}
	return (k.database == "" || k.database == e.Database) && (k.collection == "" || k.collection == e.Collection)
}

func (k *fieldKey) name() string {
	if k.database != "" {
		return k.database + "/" + k.collection + "." + k.path
	}
	return k.path
}

// parseFieldKeys reads the value of --field.
func parseFieldKeys(spec string, getenv func(string) string, warn io.Writer) ([]*fieldKey, error) {
	var out []*fieldKey
	for entry := range strings.SplitSeq(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		target, source, ok := strings.Cut(entry, "=")
		if !ok || target == "" || source == "" {
			return nil, fmt.Errorf("--field: want <path>=<key:|env:|file:…>, got %q", entry)
		}
		k := &fieldKey{spec: target}
		if dbColl, path, found := strings.Cut(target, "."); found && strings.Contains(dbColl, "/") {
			k.database, k.collection, _ = strings.Cut(dbColl, "/")
			k.path = path
		} else {
			k.path = target
		}
		text, err := readKeySource(source, getenv, warn)
		if err != nil {
			return nil, fmt.Errorf("--field %s: %w", target, err)
		}
		if k.key, err = auth.DecodeKey(text); err != nil {
			return nil, fmt.Errorf("--field %s: %w", target, err)
		}
		k.fingerprint = auth.KeyFingerprint(k.key)
		out = append(out, k)
	}
	return out, nil
}

// packageKeys reads the wrapped keys of a package, by id.
func packageKeys(dir string) (map[string]fieldcrypt.Record, error) {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(exportpkg.DataKeysName)))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]fieldcrypt.Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readKeyRecords(f)
}

func readKeyRecords(r io.Reader) (map[string]fieldcrypt.Record, error) {
	out := map[string]fieldcrypt.Record{}
	sc := bufio.NewReaderSize(r, 1<<20)
	for n := 1; ; n++ {
		line, err := sc.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			rec, derr := fieldcrypt.DecodeKeyLine(line)
			if derr != nil {
				return nil, fmt.Errorf("%s line %d: %w", exportpkg.DataKeysName, n, derr)
			}
			out[rec.ID] = rec
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// decryptor opens, with the keys given, the encrypted fields of a package's documents.
type decryptor struct {
	realm string
	keys  []*fieldKey
	data  map[string]fieldcrypt.Record
	// opened, by <database>/<collection>, are the fields it opens: only those with a key.
	opened map[string][]exportpkg.EncryptedField
}

// newDecryptor matches the keys to the fields of the manifest that are still ciphertext. A key
// whose fingerprint isn't the field's is an error naming the field and the key; a key that
// matches no field is an error too (a typo would otherwise leave the data encrypted unnoticed).
func newDecryptor(m *exportpkg.Manifest, keys []*fieldKey, data map[string]fieldcrypt.Record) (*decryptor, error) {
	d := &decryptor{realm: m.Realm, keys: keys, data: data, opened: map[string][]exportpkg.EncryptedField{}}
	for _, e := range m.Encrypted {
		if e.Form != exportpkg.FormCiphertext {
			continue
		}
		for _, k := range keys {
			if !k.matches(e) {
				continue
			}
			k.used = true
			if e.Fingerprint != "" && k.fingerprint != e.Fingerprint {
				return nil, fmt.Errorf("the key given for %s has the fingerprint %s, but %s/%s.%s is encrypted with the key %s (fingerprint %s): this is not that key",
					k.name(), k.fingerprint, e.Database, e.Collection, e.Path, e.Key, e.Fingerprint)
			}
			key := e.Database + "/" + e.Collection
			d.opened[key] = append(d.opened[key], e)
			break
		}
	}
	for _, k := range keys {
		if !k.used {
			already := false
			for _, e := range m.Encrypted {
				already = already || (e.Form == exportpkg.FormPlaintext && k.matches(e))
			}
			if !already {
				return nil, fmt.Errorf("--field %s matches no encrypted field of the package", k.spec)
			}
		}
	}
	return d, nil
}

func (d *decryptor) any() bool { return len(d.opened) > 0 }

func (d *decryptor) key(e exportpkg.EncryptedField) []byte {
	for _, k := range d.keys {
		if k.matches(e) {
			return k.key
		}
	}
	return nil
}

// line opens the encrypted fields of one document line. It returns the line unchanged when
// the collection has none to open.
func (d *decryptor) line(database, collection string, line []byte) ([]byte, error) {
	entries := d.opened[database+"/"+collection]
	if len(entries) == 0 {
		return line, nil
	}
	doc, err := exportpkg.DecodeDocument(line)
	if err != nil {
		return nil, err
	}
	id, _ := doc["id"].(string)
	changed := false
	for _, e := range entries {
		v, ok := docutil.GetPath(doc, e.Path)
		if !ok {
			continue
		}
		ct, sealed := v.(fieldcrypt.Ciphertext)
		if !sealed {
			continue
		}
		kid, err := fieldcrypt.ParseID(ct)
		if err != nil {
			return nil, fmt.Errorf("document %s: %s is not a well-formed encrypted value", id, e.Path)
		}
		rec, found := d.data[fmt.Sprintf("%x", kid)]
		if !found {
			return nil, fmt.Errorf("document %s: the data key of %s is not in the package", id, e.Path)
		}
		plain, err := fieldcrypt.OpenWith(d.key(e), rec, d.realm, ct, id)
		if err != nil {
			return nil, fmt.Errorf("document %s: %s: %w", id, e.Path, err)
		}
		docutil.SetPathExisting(doc, e.Path, plain)
		changed = true
	}
	if !changed {
		return line, nil
	}
	return exportpkg.EncodeDocument(doc)
}

// part rewrites a collection's part of the package with its fields opened, and returns what
// the part is now.
func (d *decryptor) part(dir, partPath, database, collection string) (exportedPart, error) {
	src := filepath.Join(dir, filepath.FromSlash(partPath))
	in, err := os.Open(src)
	if err != nil {
		return exportedPart{}, err
	}
	defer in.Close()
	tmp := src + ".decrypting"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return exportedPart{}, err
	}
	h := exportpkg.NewHasher()
	w := io.MultiWriter(out, h)
	var docs int64
	err = eachLine(in, func(line []byte) error {
		got, err := d.line(database, collection, line)
		if err != nil {
			return fmt.Errorf("%s: %w", partPath, err)
		}
		_, _ = w.Write(got)
		_, _ = w.Write([]byte{'\n'})
		docs++
		return nil
	})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, src)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return exportedPart{}, err
	}
	return exportedPart{Checksum: h.Sum(), Size: h.Size(), Documents: docs}, nil
}

// eachLine calls fn with each non-empty line of r, without its newline.
func eachLine(r io.Reader, fn func(line []byte) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimRight(line, "\r\n"); len(bytes.TrimSpace(t)) > 0 {
			if ferr := fn(t); ferr != nil {
				return ferr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// decryptPackage opens, in the package at dir, every field the keys open, and records that in
// the manifest (form plaintext, new checksums). It returns how many fields it opened.
func decryptPackage(dir string, m *exportpkg.Manifest, d *decryptor) (int, error) {
	n := 0
	for _, db := range m.Databases {
		for ci, col := range db.Collections {
			entries := d.opened[db.Name+"/"+col.Name]
			if len(entries) == 0 {
				continue
			}
			got, err := d.part(dir, col.Part, db.Name, col.Name)
			if err != nil {
				return n, err
			}
			m.Checksums[col.Part] = got.Checksum
			_ = ci
			n += len(entries)
		}
	}
	for i := range m.Encrypted {
		e := &m.Encrypted[i]
		for _, opened := range d.opened[e.Database+"/"+e.Collection] {
			if opened.Path == e.Path {
				e.Form = exportpkg.FormPlaintext
			}
		}
	}
	return n, nil
}

// exportDecrypt handles `backd export-decrypt`: a copy of a package with some encrypted fields
// opened, made offline.
func exportDecrypt(c *kli.CommandContext) error {
	stderr := c.Stderr()
	keys, err := parseFieldKeys(str(c, "field"), c.Getenv, stderr)
	if err != nil {
		return usageErr(err)
	}
	if len(keys) == 0 {
		return usageErr(errors.New("--field names no key"))
	}
	dir, cleanup, err := openPackage(str(c, "from"))
	if err != nil {
		return err
	}
	defer cleanup()
	m, err := verifyPackage(dir, stderr)
	if err != nil {
		return err
	}
	out := strings.TrimRight(str(c, "out"), "/")
	if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty: choose another --out", out)
	}
	data, err := packageKeys(dir)
	if err != nil {
		return err
	}
	dec, err := newDecryptor(m, keys, data)
	if err != nil {
		return usageErr(err)
	}
	if !dec.any() {
		return fmt.Errorf("the package holds no encrypted field as ciphertext that these keys open")
	}
	if err := copyPackageTree(dir, out); err != nil {
		return err
	}
	n, err := decryptPackage(out, m, dec)
	if err != nil {
		_ = os.RemoveAll(out)
		return err
	}
	if err := m.Write(out); err != nil {
		return err
	}
	if left := ciphertextFields(m); len(left) > 0 {
		fmt.Fprintf(stderr, "still encrypted (no key given): %s\n", strings.Join(left, ", "))
	}
	fmt.Fprintf(stderr, "%d encrypted fields are in plaintext in %s: keep it as carefully as the keys\n", n, out)
	fmt.Fprintln(c.Stdout(), out)
	return nil
}

// copyTree copies the files of a directory, readable only by the owner.
func copyPackageTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// chunk opens the encrypted fields of a chunk of a collection's JSON Lines.
func (d *decryptor) chunk(database, collection string, chunk []byte) ([]byte, error) {
	if d == nil || len(d.opened[database+"/"+collection]) == 0 {
		return chunk, nil
	}
	var out bytes.Buffer
	err := eachLine(bytes.NewReader(chunk), func(line []byte) error {
		got, err := d.line(database, collection, line)
		if err != nil {
			return err
		}
		out.Write(got)
		out.WriteByte('\n')
		return nil
	})
	return out.Bytes(), err
}
