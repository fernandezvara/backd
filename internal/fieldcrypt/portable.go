package fieldcrypt

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// A package carries the wrapped keys of its encrypted fields as JSON Lines
// (`_system/data_keys.jsonl`), one record per key: with them and the field keys, whoever holds the
// keys can read the values, with no server and no master key.

type packageKey struct {
	ID          string `json:"id"`
	Database    string `json:"database"`
	Collection  string `json:"collection"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Generation  int    `json:"generation"`
	Key         string `json:"key"`
	Wrapped     string `json:"wrapped"`
	WrapVersion int    `json:"wrap_version"`
	Status      string `json:"status"`
}

// EncodeKeyLine renders a key record as one line of a package (no newline).
func EncodeKeyLine(r Record) ([]byte, error) {
	return json.Marshal(packageKey{
		ID: r.ID, Database: r.Database, Collection: r.Collection, Path: r.Path, Kind: r.Kind, Generation: r.Generation,
		Key: r.Key, Wrapped: base64.StdEncoding.EncodeToString(r.Wrapped), WrapVersion: r.WrapVersion, Status: r.Status,
	})
}

// DecodeKeyLine reads a line EncodeKeyLine wrote.
func DecodeKeyLine(line []byte) (Record, error) {
	var p packageKey
	if err := json.Unmarshal(line, &p); err != nil {
		return Record{}, err
	}
	wrapped, err := base64.StdEncoding.DecodeString(p.Wrapped)
	if err != nil || p.ID == "" || p.Path == "" {
		return Record{}, errors.New("not a data key record")
	}
	return Record{ID: p.ID, Database: p.Database, Collection: p.Collection, Path: p.Path, Kind: p.Kind, Generation: p.Generation,
		Key: p.Key, Wrapped: wrapped, WrapVersion: p.WrapVersion, Status: p.Status}, nil
}

// OpenWith opens a value of an export package with the field key that wraps the value's data key:
// rec is the data key's record from the package, sourceRealm the realm the package was exported
// from (it is part of what the value is bound to), docID the id of the document. It returns the
// value as JSON text. Neither the server nor a master key is involved.
func OpenWith(fieldKey []byte, rec Record, sourceRealm string, ct Ciphertext, docID string) (any, error) {
	id, err := ParseID(ct)
	if err != nil {
		return nil, err
	}
	f := rec.Field(sourceRealm)
	dk, err := Unwrap(fieldKey, rec.Wrapped, id, f, rec.Kind)
	if err != nil {
		return nil, fmt.Errorf("the key does not open the data key of %s/%s.%s", rec.Database, rec.Collection, rec.Path)
	}
	plain, err := Open(dk, ct, valueAD(f, docID))
	if err != nil {
		return nil, ErrCorrupt
	}
	return decodeJSON(plain)
}
