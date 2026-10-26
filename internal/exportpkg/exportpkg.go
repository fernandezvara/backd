// Package exportpkg is the format of an export package: a directory (or archive) with a
// manifest, one JSON Lines file per collection in the API representation, the files the
// documents reference and the wrapped keys of encrypted fields. The server streams the parts
// and the CLI writes them; import reads them back.
package exportpkg

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
)

// FormatVersion is the version of the package layout; import refuses other versions.
const FormatVersion = 1

// Fixed names inside a package.
const (
	ManifestName = "manifest.json"
	DataKeysName = "_system/data_keys.jsonl"
	FilesDir     = "_files"
)

// Form says whether an encrypted field's values travel as they are stored or decrypted.
const (
	FormCiphertext = "ciphertext"
	FormPlaintext  = "plaintext"
)

// Manifest describes a package.
type Manifest struct {
	Format       int               `json:"format"`
	BackdVersion string            `json:"backd_version"`
	Realm        string            `json:"realm"`
	CreatedAt    time.Time         `json:"created_at"`
	Operator     string            `json:"operator"`
	Fingerprint  string            `json:"config_fingerprint"`
	Flags        Flags             `json:"flags"`
	Databases    []Database        `json:"databases"`
	Encrypted    []EncryptedField  `json:"encrypted_fields"`
	Checksums    map[string]string `json:"checksums"`
	Files        int64             `json:"files"`
	// Auth lists the authentication data parts (with --include auth); AuthKey is the fingerprint
	// of the one-time export key their realm-sealed fields are sealed under.
	Auth    []Collection `json:"auth,omitempty"`
	AuthKey string       `json:"auth_key_fingerprint,omitempty"`
	// Subject is the user a personal data export is about; Exclusions lists what it leaves out
	// and why. Neither is in the export of a realm.
	Subject    string      `json:"subject,omitempty"`
	Exclusions []Exclusion `json:"exclusions,omitempty"`
}

// Exclusion is something a personal data export does not take, with the reason.
type Exclusion struct {
	What   string `json:"what"`
	Reason string `json:"reason"`
}

// Flags are the package's properties an import has to respect.
type Flags struct {
	Partial    bool `json:"partial"`
	Anonymized bool `json:"anonymized"`
	Auth       bool `json:"auth"`
	NoFiles    bool `json:"no_files"`
	// CSV marks a flat reporting extract: lossy, never importable.
	CSV bool `json:"csv"`
	// Personal marks the export of one user's personal data (always partial: it is not a copy of
	// the realm and is never importable).
	Personal bool `json:"personal,omitempty"`
}

// Database is a database of the realm in a package.
type Database struct {
	Name        string       `json:"name"`
	Collections []Collection `json:"collections"`
}

// Collection is a collection in a package: its part and how many documents it holds.
type Collection struct {
	Name      string `json:"name"`
	Documents int64  `json:"documents"`
	Part      string `json:"part"`
}

// EncryptedField records an encrypted field and whether the package holds it as ciphertext or
// decrypted.
type EncryptedField struct {
	Database    string `json:"database"`
	Collection  string `json:"collection"`
	Path        string `json:"path"`
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Form        string `json:"form"`
}

// CollectionPart is the package path of a collection's documents.
func CollectionPart(database, collection string) string {
	return database + "/" + collection + ".jsonl"
}

// AuthPart is the package path of a collection of authentication data.
func AuthPart(collection string) string { return "_system/auth/" + collection + ".jsonl" }

// FilePart is the package path of a file's object.
func FilePart(database, fileID string) string {
	return database + "/" + FilesDir + "/" + fileID
}

// Marshal renders the manifest, with its checksums in a stable order.
func (m *Manifest) Marshal() ([]byte, error) {
	sort.Slice(m.Databases, func(i, j int) bool { return m.Databases[i].Name < m.Databases[j].Name })
	for i := range m.Databases {
		cs := m.Databases[i].Collections
		sort.Slice(cs, func(a, b int) bool { return cs[a].Name < cs[b].Name })
	}
	if m.Encrypted == nil {
		m.Encrypted = []EncryptedField{}
	}
	if m.Checksums == nil {
		m.Checksums = map[string]string{}
	}
	return json.MarshalIndent(m, "", "  ")
}

// ParseManifest reads a manifest and refuses a format this backd doesn't know.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Format != FormatVersion {
		return nil, fmt.Errorf("manifest.json: package format %d is not supported (this backd reads format %d)", m.Format, FormatVersion)
	}
	return &m, nil
}

// Hasher computes the SHA-256 of what passes through it.
type Hasher struct {
	h hash.Hash
	n int64
}

// NewHasher starts a checksum.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

// Write implements io.Writer.
func (s *Hasher) Write(p []byte) (int, error) {
	s.n += int64(len(p))
	return s.h.Write(p)
}

// Sum is the checksum as "sha256:<hex>".
func (s *Hasher) Sum() string { return "sha256:" + hex.EncodeToString(s.h.Sum(nil)) }

// Size is how many bytes passed through.
func (s *Hasher) Size() int64 { return s.n }

// EncodeDocument renders a document as one line of JSON in the API representation, with typed
// values in MongoDB Extended JSON (relaxed): a date is {"$date": "…"}, binary is
// {"$binary": {"base64": "…", "subType": "00"}}. The line has no newline.
func EncodeDocument(doc map[string]any) ([]byte, error) {
	return json.Marshal(toExtended(doc))
}

func toExtended(v any) any {
	switch t := v.(type) {
	case time.Time:
		return map[string]any{"$date": t.UTC().Format("2006-01-02T15:04:05.000Z")}
	case []byte:
		return map[string]any{"$binary": map[string]any{"base64": base64.StdEncoding.EncodeToString(t), "subType": "00"}}
	case fieldcrypt.Ciphertext:
		// An encrypted value, as stored: BSON binary subtype 6.
		return map[string]any{"$binary": map[string]any{"base64": base64.StdEncoding.EncodeToString(t), "subType": "06"}}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = toExtended(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = toExtended(e)
		}
		return out
	}
	return v
}

// DecodeDocument reads a line EncodeDocument wrote: integers come back as int64, other numbers
// as float64, and Extended JSON dates and binary as time.Time and []byte (an encrypted value, binary
// of subtype 6, as fieldcrypt.Ciphertext).
func DecodeDocument(line []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one value on a line")
	}
	out, err := fromExtended(raw)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func fromExtended(v any) (any, error) {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, nil
		}
		return t.Float64()
	case []any:
		for i, e := range t {
			x, err := fromExtended(e)
			if err != nil {
				return nil, err
			}
			t[i] = x
		}
		return t, nil
	case map[string]any:
		if s, ok := t["$date"].(string); ok && len(t) == 1 {
			d, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return nil, fmt.Errorf("invalid $date %q", s)
			}
			return d.UTC(), nil
		}
		if b, ok := t["$binary"].(map[string]any); ok && len(t) == 1 {
			s, _ := b["base64"].(string)
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return nil, errors.New("invalid $binary")
			}
			if sub, _ := b["subType"].(string); sub == "06" {
				return fieldcrypt.Ciphertext(raw), nil
			}
			return raw, nil
		}
		for k, e := range t {
			x, err := fromExtended(e)
			if err != nil {
				return nil, err
			}
			t[k] = x
		}
		return t, nil
	}
	return v, nil
}

// ReadDocuments calls fn with every document of a JSON Lines stream, with its line number.
func ReadDocuments(r io.Reader, fn func(line int, doc map[string]any) error) error {
	sc := bufio.NewReaderSize(r, 1<<20)
	for n := 1; ; n++ {
		line, err := sc.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			doc, derr := DecodeDocument(line)
			if derr != nil {
				return fmt.Errorf("line %d: %w", n, derr)
			}
			if ferr := fn(n, doc); ferr != nil {
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

// SafePart reports whether a package path is one this package would write: relative, with no
// dot segments, so a hostile archive or manifest can't name a file outside the package.
func SafePart(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.ContainsRune(p, 0) {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
