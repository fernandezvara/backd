package fieldcrypt

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
)

// Kinds of keys a field has: the data key encrypts its values, the index key keys its blind index.
const (
	KindData  = "data"
	KindIndex = "index"
)

// Statuses of a data key.
const (
	StatusActive      = "active"
	StatusDecryptOnly = "decrypt_only"
	StatusRetired     = "retired"
)

// Record is a data key as stored in <realm>___system.data_keys: one per field and generation.
type Record struct {
	ID          string // 32 hex characters
	Database    string
	Collection  string
	Path        string
	Kind        string
	Generation  int
	Key         string // the field key's name
	Wrapped     []byte
	WrapVersion int // the version of the field key that wrapped it
	Status      string
	CreatedAt   time.Time
}

// Field returns where the record's key is used, in a realm.
func (r Record) Field(realm string) Field {
	return Field{Realm: realm, Database: r.Database, Collection: r.Collection, Path: r.Path}
}

// Store keeps data keys. Implemented by the system database of a realm.
type Store interface {
	// ActiveDataKey returns the key used for writes of a field, if there is one.
	ActiveDataKey(ctx context.Context, database, collection, path, kind string) (Record, bool, error)
	// DataKeyByID returns a key by its id (32 hex characters).
	DataKeyByID(ctx context.Context, id string) (Record, bool, error)
	// CreateDataKey stores a key; false when that field already has the generation.
	CreateDataKey(ctx context.Context, r Record) (bool, error)
	// DataKeysToRewrap returns, ordered by id after afterID, up to limit keys wrapped by the
	// named field key at a version below belowVersion.
	DataKeysToRewrap(ctx context.Context, key string, belowVersion int, afterID string, limit int) ([]Record, error)
	// RewrapDataKey replaces a key's wrapping if it still has wrap version from; false when it
	// no longer does (another process did it).
	RewrapDataKey(ctx context.Context, id string, wrapped []byte, from, to int) (bool, error)
	// DataKeysByKey counts the keys wrapped by a field key that are not retired.
	DataKeysByKey(ctx context.Context, key string) (int64, error)
	// DataKeysOf lists every key of a field (all kinds, generations and statuses), oldest first.
	DataKeysOf(ctx context.Context, database, collection, path string) ([]Record, error)
	// DataKeyPaths lists the paths of a collection that have data keys: the fields that are, or
	// once were, encrypted.
	DataKeyPaths(ctx context.Context, database, collection string) ([]string, error)
	// SetDataKeyStatus changes a key's status (active, decrypt_only, retired).
	SetDataKeyStatus(ctx context.Context, id, status string) error
}

// Material is what the secrets store gives of a field key: its value and version, and, while
// a rotation is unfinished, the previous value and version.
type Material struct {
	Current         []byte
	Version         int
	Previous        []byte
	PreviousVersion int
}

// Ring encrypts and decrypts the documents of one realm. It caches unwrapped data keys, which
// never change (rotating a field key only changes how they are wrapped), and the key chosen for
// writes, for a minute so a new generation applies without a restart.
type Ring struct {
	Realm string
	Store Store
	// Keys returns a field key by name; ErrUnavailable when it can't.
	Keys func(ctx context.Context, name string) (Material, error)
	Now  func() time.Time

	mu     sync.Mutex
	active map[string]activeKey
	byID   map[string][]byte
	paths  map[string]cachedPaths
}

type cachedPaths struct {
	paths   []string
	expires time.Time
}

type activeKey struct {
	id      []byte
	key     []byte
	expires time.Time
}

// activeTTL is how long the key chosen for writes is remembered.
const activeTTL = 60 * time.Second

func (r *Ring) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Ring) field(c *registry.Collection, path string) Field {
	return Field{Realm: r.Realm, Database: c.Database, Collection: c.Name, Path: path}
}

func (r *Ring) activeKey(field Field) string {
	return field.Database + "\x00" + field.Collection + "\x00" + field.Path
}

// unwrap opens a record's data key with the field key that wrapped it.
func (r *Ring) unwrap(ctx context.Context, rec Record) ([]byte, error) {
	m, err := r.Keys(ctx, rec.Key)
	if err != nil {
		return nil, err
	}
	var fk []byte
	switch {
	case rec.WrapVersion == m.Version:
		fk = m.Current
	case m.Previous != nil && rec.WrapVersion == m.PreviousVersion:
		fk = m.Previous
	default:
		return nil, fmt.Errorf("%w: the key %s no longer holds the version that wrapped this data key", ErrUnavailable, rec.Key)
	}
	id, err := hex.DecodeString(rec.ID)
	if err != nil {
		return nil, ErrCorrupt
	}
	return Unwrap(fk, rec.Wrapped, id, rec.Field(r.Realm), rec.Kind)
}

// Ensure makes sure every encrypted field of c has a data key to write with, creating the first
// generation under the field's key when none exists. Several instances may race: the unique
// (field, generation) makes one win, and the others use its key.
func (r *Ring) Ensure(ctx context.Context, c *registry.Collection) error {
	for _, ef := range c.Encrypted {
		kinds := []string{KindData}
		if ef.Search != "" {
			kinds = append(kinds, KindIndex)
		}
		for _, kind := range kinds {
			if err := r.ensureKey(ctx, c, ef, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Ring) ensureKey(ctx context.Context, c *registry.Collection, ef registry.EncryptedField, kind string) error {
	f := r.field(c, ef.Path)
	prev, ok, err := r.Store.ActiveDataKey(ctx, f.Database, f.Collection, f.Path, kind)
	if err != nil {
		return err
	}
	if ok && prev.Key == ef.Key {
		return nil
	}
	if !ok {
		prev = Record{}
	}
	// A field moved to another key starts a new generation under it; the old one stays to read.
	return r.newGeneration(ctx, f, ef.Key, kind, prev)
}

// newGeneration creates the key after prev (the first one when prev is zero), wrapped by the
// field key name, and makes it the one writes use; prev, if any, only decrypts from now on.
func (r *Ring) newGeneration(ctx context.Context, f Field, keyName, kind string, prev Record) error {
	m, err := r.Keys(ctx, keyName)
	if err != nil {
		return fmt.Errorf("%s/%s.%s: %w", f.Database, f.Collection, f.Path, err)
	}
	id, dk, err := NewDataKey()
	if err != nil {
		return err
	}
	wrapped, err := Wrap(m.Current, dk, id, f, kind)
	if err != nil {
		return err
	}
	created, err := r.Store.CreateDataKey(ctx, Record{
		ID: hex.EncodeToString(id), Database: f.Database, Collection: f.Collection, Path: f.Path, Kind: kind,
		Generation: prev.Generation + 1, Key: keyName, Wrapped: wrapped, WrapVersion: m.Version, Status: StatusActive, CreatedAt: r.now().UTC(),
	})
	if err != nil {
		return err
	}
	if created && prev.ID != "" {
		if err := r.Store.SetDataKeyStatus(ctx, prev.ID, StatusDecryptOnly); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.active = nil // the next write picks the new key up
	r.mu.Unlock()
	return nil
}

// Rekey starts a new generation of the data key of an encrypted field, so writes use a fresh
// key; values under the old one stay readable until a migration re-encrypts them. For data keys
// that may have leaked; replacing the field key alone never needs it.
func (r *Ring) Rekey(ctx context.Context, c *registry.Collection, path string) (Record, error) {
	ef, ok := c.EncryptedAt(path)
	if !ok {
		return Record{}, fmt.Errorf("%s is not an encrypted field of %s/%s", path, c.Database, c.Name)
	}
	f := r.field(c, path)
	prev, found, err := r.Store.ActiveDataKey(ctx, f.Database, f.Collection, f.Path, KindData)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("%w: the field %s has no data key yet", ErrUnavailable, path)
	}
	if err := r.newGeneration(ctx, f, ef.Key, KindData, prev); err != nil {
		return Record{}, err
	}
	rec, _, err := r.Store.ActiveDataKey(ctx, f.Database, f.Collection, f.Path, KindData)
	return rec, err
}

// ActiveID is the id (32 hex characters) of the data key new values of a field are sealed with.
func (r *Ring) ActiveID(ctx context.Context, c *registry.Collection, path string) (string, error) {
	f := r.field(c, path)
	rec, ok, err := r.Store.ActiveDataKey(ctx, f.Database, f.Collection, f.Path, KindData)
	if err != nil || !ok {
		return "", err
	}
	return rec.ID, nil
}

// Retire marks as retired the keys of a field no value uses any more: used(id) says how many
// values are still sealed with a key. The active data key of a field that is no longer encrypted
// is retired too, once none is left. It returns the ids it retired.
func (r *Ring) Retire(ctx context.Context, c *registry.Collection, path string, used func(id string) (int64, error)) ([]string, error) {
	f := r.field(c, path)
	recs, err := r.Store.DataKeysOf(ctx, f.Database, f.Collection, f.Path)
	if err != nil {
		return nil, err
	}
	_, configured := c.EncryptedAt(path)
	var retired []string
	for _, rec := range recs {
		if rec.Kind != KindData || rec.Status == StatusRetired || (rec.Status == StatusActive && configured) {
			continue
		}
		n, err := used(rec.ID)
		if err != nil {
			return retired, err
		}
		if n == 0 {
			if err := r.Store.SetDataKeyStatus(ctx, rec.ID, StatusRetired); err != nil {
				return retired, err
			}
			retired = append(retired, rec.ID)
		}
	}
	return retired, nil
}

// writeKey returns the data key a field's new values are encrypted with.
func (r *Ring) writeKey(ctx context.Context, f Field, kind string) (id, key []byte, err error) {
	k := kind + "\x00" + r.activeKey(f)
	now := r.now()
	r.mu.Lock()
	if a, ok := r.active[k]; ok && now.Before(a.expires) {
		r.mu.Unlock()
		return a.id, a.key, nil
	}
	r.mu.Unlock()
	rec, ok, err := r.Store.ActiveDataKey(ctx, f.Database, f.Collection, f.Path, kind)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("%w: the field %s/%s.%s has no %s key yet", ErrUnavailable, f.Database, f.Collection, f.Path, kind)
	}
	dk, err := r.unwrap(ctx, rec)
	if err != nil {
		return nil, nil, err
	}
	id, _ = hex.DecodeString(rec.ID)
	r.mu.Lock()
	if r.active == nil {
		r.active, r.byID = map[string]activeKey{}, map[string][]byte{}
	}
	r.active[k] = activeKey{id: id, key: dk, expires: now.Add(activeTTL)}
	r.byID[rec.ID] = dk
	r.mu.Unlock()
	return id, dk, nil
}

// readKey returns the data key a stored value names.
func (r *Ring) readKey(ctx context.Context, f Field, id []byte) ([]byte, error) {
	hexID := hex.EncodeToString(id)
	r.mu.Lock()
	dk, ok := r.byID[hexID]
	r.mu.Unlock()
	if ok {
		return dk, nil
	}
	rec, found, err := r.Store.DataKeyByID(ctx, hexID)
	if err != nil {
		return nil, err
	}
	if !found || rec.Database != f.Database || rec.Collection != f.Collection || rec.Path != f.Path {
		return nil, fmt.Errorf("%w: no data key for a value of %s/%s.%s", ErrUnavailable, f.Database, f.Collection, f.Path)
	}
	if dk, err = r.unwrap(ctx, rec); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.byID == nil {
		r.active, r.byID = map[string]activeKey{}, map[string][]byte{}
	}
	r.byID[hexID] = dk
	r.mu.Unlock()
	return dk, nil
}

// Encrypt returns doc with its encrypted fields sealed. doc is not modified: the maps along
// each encrypted path are copied, because the caller still answers with the plaintext. A field
// that is absent stays absent; one already sealed is left as it is.
func (r *Ring) Encrypt(ctx context.Context, c *registry.Collection, doc map[string]any) (map[string]any, error) {
	if len(c.Encrypted) == 0 {
		return doc, nil
	}
	id, _ := doc["id"].(string)
	out := doc
	copied := false
	if _, hasBlind := doc[registry.BlindRoot]; hasBlind || doc[registry.EncRoot] != nil {
		// Blind indexes and key ids are the server's: whatever a client sent under those names is dropped.
		out, copied = shallow(doc), true
		delete(out, registry.BlindRoot)
		delete(out, registry.EncRoot)
	}
	for _, ef := range c.Encrypted {
		v, ok := lookup(doc, ef.Path)
		if !ok {
			continue
		}
		if _, sealed := v.(Ciphertext); sealed {
			continue
		}
		ct, kid, err := r.SealAt(ctx, c, ef.Path, id, v)
		if err != nil {
			return nil, err
		}
		f := r.field(c, ef.Path)
		if !copied {
			out, copied = shallow(doc), true
		}
		store(out, ef.Path, ct)
		enc, _ := out[registry.EncRoot].(map[string]any)
		nextEnc := make(map[string]any, len(enc)+1)
		for k, x := range enc {
			nextEnc[k] = x
		}
		nextEnc[registry.MarkerField(ef.Path)] = kid
		out[registry.EncRoot] = nextEnc
		if ef.Search != "" {
			b, err := r.blind(ctx, f, ef, v)
			if err != nil {
				return nil, err
			}
			bidx, _ := out[registry.BlindRoot].(map[string]any)
			next := make(map[string]any, len(bidx)+1)
			for k, x := range bidx {
				next[k] = x
			}
			next[registry.MarkerField(ef.Path)] = b
			out[registry.BlindRoot] = next
		}
	}
	return out, nil
}

// SealAt encrypts a plaintext value of the field at path for the document with that id, with the
// field's active data key, and returns the id (32 hex characters) of the key it used.
func (r *Ring) SealAt(ctx context.Context, c *registry.Collection, path, docID string, v any) (Ciphertext, string, error) {
	f := r.field(c, path)
	plain, err := CanonicalJSON(v)
	if err != nil {
		return nil, "", fmt.Errorf("the field %s can't be encrypted: %w", path, errors.New("not a JSON value"))
	}
	kid, dk, err := r.writeKey(ctx, f, KindData)
	if err != nil {
		return nil, "", err
	}
	ct, err := Seal(dk, kid, plain, valueAD(f, docID))
	if err != nil {
		return nil, "", err
	}
	return ct, hex.EncodeToString(kid), nil
}

// blind computes the blind index of a plaintext value of a searchable field: an HMAC-SHA-256,
// keyed by the field's index key, of the normalized value, so equal values have equal indexes
// and nothing else about them shows (design/field-encryption.md §6.1).
func (r *Ring) blind(ctx context.Context, f Field, ef registry.EncryptedField, v any) ([]byte, error) {
	_, key, err := r.writeKey(ctx, f, KindIndex)
	if err != nil {
		return nil, err
	}
	plain, err := CanonicalJSON(Normalize(ef.Normalize, v))
	if err != nil {
		return nil, errors.New("not a JSON value")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(ad("backd-field-blind-v1", ef.Path))
	mac.Write(plain)
	return mac.Sum(nil), nil
}

// Blind returns the blind index to look a value of a searchable field up by.
func (r *Ring) Blind(ctx context.Context, c *registry.Collection, path string, v any) ([]byte, error) {
	ef, ok := c.EncryptedAt(path)
	if !ok || ef.Search == "" {
		return nil, fmt.Errorf("%s is not a searchable encrypted field", path)
	}
	return r.blind(ctx, r.field(c, path), ef, v)
}

// Normalize applies a field's normalizations (trim, lower, upper) to a value; only strings change.
func Normalize(ops []string, v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	for _, op := range ops {
		switch op {
		case "trim":
			s = strings.TrimSpace(s)
		case "lower":
			s = strings.ToLower(s)
		case "upper":
			s = strings.ToUpper(s)
		}
	}
	return s
}

// Decrypt opens the encrypted fields of a document read from storage, in place: the ones the
// collection configures and the ones it once did (data keys remain for them, and their values
// stay encrypted until a migration opens them). A value that isn't encrypted (data from before
// the field was encrypted) is left as it is.
func (r *Ring) Decrypt(ctx context.Context, c *registry.Collection, doc map[string]any) error {
	paths, err := r.openPaths(ctx, c)
	if err != nil {
		return err
	}
	id, _ := doc["id"].(string)
	for _, path := range paths {
		v, ok := lookup(doc, path)
		if !ok {
			continue
		}
		ct, sealed := v.(Ciphertext)
		if !sealed {
			continue
		}
		val, err := r.OpenAt(ctx, c, path, id, ct)
		if err != nil {
			return err
		}
		set(doc, path, val)
	}
	return nil
}

// OpenAt opens one encrypted value of the field at path in the document with that id.
func (r *Ring) OpenAt(ctx context.Context, c *registry.Collection, path, docID string, ct Ciphertext) (any, error) {
	f := r.field(c, path)
	kid, err := ParseID(ct)
	if err != nil {
		return nil, err
	}
	dk, err := r.readKey(ctx, f, kid)
	if err != nil {
		return nil, err
	}
	plain, err := Open(dk, ct, valueAD(f, docID))
	if err != nil {
		return nil, err
	}
	val, err := decodeJSON(plain)
	if err != nil {
		return nil, ErrCorrupt
	}
	return val, nil
}

// pathsTTL is how long the list of fields that have data keys is remembered.
const pathsTTL = time.Minute

// openPaths lists the paths of a collection a read must try to open: the configured ones and the
// ones that have data keys from before. Nearly every collection has none of the second kind, and
// the answer is remembered.
func (r *Ring) openPaths(ctx context.Context, c *registry.Collection) ([]string, error) {
	key := c.Database + "\x00" + c.Name
	now := r.now()
	r.mu.Lock()
	if p, ok := r.paths[key]; ok && now.Before(p.expires) {
		r.mu.Unlock()
		return p.paths, nil
	}
	r.mu.Unlock()
	historic, err := r.Store.DataKeyPaths(ctx, c.Database, c.Name)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, ef := range c.Encrypted {
		seen[ef.Path] = true
		paths = append(paths, ef.Path)
	}
	for _, p := range historic {
		if !seen[p] {
			paths = append(paths, p)
		}
	}
	r.mu.Lock()
	if r.paths == nil {
		r.paths = map[string]cachedPaths{}
	}
	r.paths[key] = cachedPaths{paths: paths, expires: now.Add(pathsTTL)}
	r.mu.Unlock()
	return paths, nil
}

// HistoricPaths lists the paths of a collection with data keys that its configuration no longer
// encrypts: values may still be stored encrypted there until a migration opens them.
func (r *Ring) HistoricPaths(ctx context.Context, c *registry.Collection) ([]string, error) {
	paths, err := r.openPaths(ctx, c)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range paths {
		if _, ok := c.EncryptedAt(p); !ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// CanonicalJSON serializes a value the way the format specifies: JSON without insignificant
// white space, object keys sorted, and no HTML escaping (`<`, `>` and `&` stay as they are).
func CanonicalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return jsonnum.Normalize(v), nil
}

// lookup reads a dot path of a document.
func lookup(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// set writes a dot path of a document in place; the maps along it exist.
func set(doc map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// shallow copies the top level of a document; store copies the maps it descends into.
func shallow(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		out[k] = v
	}
	return out
}

// store writes a dot path into a document made by shallow, copying each nested map before it
// changes it, so the original's maps are never touched.
func store(doc map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	cur := doc
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cp := shallow(next)
		cur[p] = cp
		cur = cp
	}
	cur[parts[len(parts)-1]] = v
}

// rewrapBatch is how many data keys a rotation re-wraps per read.
const rewrapBatch = 100

// Rewrap re-wraps, with the current version of a field key, every data key wrapped by an older
// one, and returns how many it changed. It is safe to run again after an interruption, and
// alongside another process doing the same: a key already re-wrapped is skipped.
func Rewrap(ctx context.Context, store Store, realm, key string, m Material) (int, error) {
	n, after := 0, ""
	for {
		recs, err := store.DataKeysToRewrap(ctx, key, m.Version, after, rewrapBatch)
		if err != nil {
			return n, err
		}
		for _, rec := range recs {
			after = rec.ID
			id, err := hex.DecodeString(rec.ID)
			if err != nil {
				return n, ErrCorrupt
			}
			var old []byte
			switch {
			case rec.WrapVersion == m.PreviousVersion && m.Previous != nil:
				old = m.Previous
			default:
				return n, fmt.Errorf("%w: a data key was wrapped by version %d of %s, which is gone", ErrUnavailable, rec.WrapVersion, key)
			}
			f := rec.Field(realm)
			dk, err := Unwrap(old, rec.Wrapped, id, f, rec.Kind)
			if err != nil {
				return n, err
			}
			wrapped, err := Wrap(m.Current, dk, id, f, rec.Kind)
			if err != nil {
				return n, err
			}
			changed, err := store.RewrapDataKey(ctx, rec.ID, wrapped, rec.WrapVersion, m.Version)
			if err != nil {
				return n, err
			}
			if changed {
				n++
			}
		}
		if len(recs) < rewrapBatch {
			return n, nil
		}
	}
}

// Reset forgets every cached key, so the next use reads and unwraps them again.
func (r *Ring) Reset() {
	r.mu.Lock()
	r.active, r.byID, r.paths = nil, nil, nil
	r.mu.Unlock()
}
