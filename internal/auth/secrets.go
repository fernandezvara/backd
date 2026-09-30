package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// ErrSecretNotFound means no secret is stored at that scope and name.
var ErrSecretNotFound = errors.New("secret not found")

// ErrSecretsNotConfigured means BACKD_SECRETS_KEY isn't set: secrets can
// be neither stored nor read.
var ErrSecretsNotConfigured = errors.New("the secrets master key is not configured (BACKD_SECRETS_KEY)")

// secretCacheTTL is how long a decrypted value is kept in memory, so a
// key rotation or a value change applies within this long.
const secretCacheTTL = 60 * time.Second

// Secret is one encrypted value, bound to a realm (implicitly, by which
// Users it's stored through), a scope and a name. Database empty means
// the realm scope; otherwise it's the database scope.
type Secret struct {
	Database   string // "": realm scope
	Name       string
	Ciphertext string // base64
	Nonce      string // base64
	KeyID      string // fingerprint of the key it was encrypted with
	CreatedAt  time.Time
	UpdatedAt  time.Time
	UpdatedBy  string
}

// SecretMeta is a secret without its value, as listed: names, scope and
// who last changed it, never the value.
type SecretMeta struct {
	Database  string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy string
}

// SecretCipher encrypts and decrypts secret values with AES-256-GCM,
// under a key derived from the operator's master key (never the master
// key's raw bytes directly, so an accidental log of the derived key
// alone can't be turned back into the configured secret). KeyID is a
// fingerprint safe to store and log: it identifies which master key a
// value was encrypted with, for rotation, without revealing it.
type SecretCipher struct {
	aead  cipher.AEAD
	KeyID string
}

// NewSecretCipher derives a cipher from raw master key material (any
// length, at least 32 characters is enforced by settings). The same raw
// material always derives the same cipher and KeyID.
func NewSecretCipher(raw []byte) (*SecretCipher, error) {
	if len(raw) < 32 {
		return nil, errors.New("the secrets master key must be at least 32 characters")
	}
	key := sha256.Sum256(append([]byte("backd-secrets-encrypt|"), raw...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	id := sha256.Sum256(append([]byte("backd-secrets-keyid|"), raw...))
	return &SecretCipher{aead: aead, KeyID: hex.EncodeToString(id[:])[:16]}, nil
}

// Seal encrypts plaintext, returning base64 ciphertext and nonce.
func (c *SecretCipher) Seal(plaintext string) (ciphertext, nonce string, err error) {
	n := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return "", "", err
	}
	ct := c.aead.Seal(nil, n, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), base64.StdEncoding.EncodeToString(n), nil
}

// Open decrypts a value sealed with a cipher whose KeyID matches keyID.
func (c *SecretCipher) Open(ciphertext, nonce, keyID string) (string, error) {
	if keyID != c.KeyID {
		return "", fmt.Errorf("secret was encrypted under key %s, not the configured one (%s): run `backd secret rotate-key`", keyID, c.KeyID)
	}
	ct, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", errors.New("invalid ciphertext")
	}
	n, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", errors.New("invalid nonce")
	}
	pt, err := c.aead.Open(nil, n, ct, nil)
	if err != nil {
		return "", errors.New("secret does not decrypt under the configured key")
	}
	return string(pt), nil
}

type cachedSecret struct {
	value   string
	cached  time.Time
	missing bool
}

// SecretCache holds decrypted secret values (and confirmed-missing
// lookups) for secretCacheTTL, shared across every realm's Users:
// NewSecretCache once, assigned to each Users.Cache. Each key includes
// the realm, so it's safe for every realm to share one instance.
type SecretCache struct {
	mu    sync.Mutex
	items map[string]cachedSecret
}

// NewSecretCache returns an empty cache.
func NewSecretCache() *SecretCache { return &SecretCache{items: map[string]cachedSecret{}} }

func (c *SecretCache) get(key string, now time.Time) (value string, ok, missing bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, found := c.items[key]
	if !found || now.Sub(v.cached) > secretCacheTTL {
		return "", false, false
	}
	return v.value, true, v.missing
}

func (c *SecretCache) put(key, value string, missing bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cachedSecret{value: value, cached: now, missing: missing}
}

func (c *SecretCache) clear(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// SetSecret creates or replaces the value at scope (database, or "" for
// the realm) and name, encrypting it with Cipher. by is the actor for
// UpdatedBy (see Caller.Actor).
func (s *Users) SetSecret(ctx context.Context, database, name, value, by string) error {
	if s.Cipher == nil {
		return ErrSecretsNotConfigured
	}
	if !registry.ValidSecretName(name) {
		return fmt.Errorf("invalid secret name %q: must be upper-case letters, digits and _, starting with a letter", name)
	}
	ct, nonce, err := s.Cipher.Seal(value)
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.Store.UpsertSecret(ctx, Secret{
		Database: database, Name: name, Ciphertext: ct, Nonce: nonce, KeyID: s.Cipher.KeyID,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: by,
	}); err != nil {
		return err
	}
	s.clearSecretCache(database, name)
	s.Audit(ctx, AuditSecretSet, secretTarget(database, name), map[string]any{"database": database})
	return nil
}

// ListSecrets returns every secret's metadata (never a value), sorted by
// database then name (realm-scope secrets, database "", first).
func (s *Users) ListSecrets(ctx context.Context) ([]SecretMeta, error) {
	secrets, err := s.Store.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SecretMeta, len(secrets))
	for i, sec := range secrets {
		out[i] = SecretMeta{Database: sec.Database, Name: sec.Name, CreatedAt: sec.CreatedAt, UpdatedAt: sec.UpdatedAt, UpdatedBy: sec.UpdatedBy}
	}
	return out, nil
}

// DeleteSecret removes the value at scope and name; ErrSecretNotFound if
// none. Functions that declare it start answering secret_missing again.
func (s *Users) DeleteSecret(ctx context.Context, database, name string) error {
	if err := s.Store.DeleteSecret(ctx, database, name); err != nil {
		return err
	}
	s.clearSecretCache(database, name)
	s.Audit(ctx, AuditSecretDelete, secretTarget(database, name), map[string]any{"database": database})
	return nil
}

// ResolveSecrets decrypts the values a function declared (refs), for
// database (its own). It returns values keyed exactly as declared
// (SecretRef.String(): NAME or realm.NAME) and, separately, the refs
// with no stored value, so the caller can answer secret_missing.
// Decrypted values are cached for a short time, so a rotation or a
// changed value applies within a minute without restarting anything.
func (s *Users) ResolveSecrets(ctx context.Context, refs []registry.SecretRef, database string) (values map[string]string, missing []string, err error) {
	if len(refs) == 0 {
		return nil, nil, nil
	}
	if s.Cipher == nil {
		for _, ref := range refs {
			missing = append(missing, ref.String())
		}
		return nil, missing, nil
	}
	values = make(map[string]string, len(refs))
	now := s.now()
	for _, ref := range refs {
		db := database
		if ref.Realm {
			db = ""
		}
		if v, ok, isMissing := s.cachedSecret(db, ref.Name, now); ok {
			if isMissing {
				missing = append(missing, ref.String())
			} else {
				values[ref.String()] = v
			}
			continue
		}
		sec, err := s.Store.SecretByScope(ctx, db, ref.Name)
		if errors.Is(err, ErrSecretNotFound) {
			s.putSecretCache(db, ref.Name, "", true, now)
			missing = append(missing, ref.String())
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		v, err := s.Cipher.Open(sec.Ciphertext, sec.Nonce, sec.KeyID)
		if err != nil {
			return nil, nil, fmt.Errorf("secret %s: %w", secretTarget(db, ref.Name), err)
		}
		s.putSecretCache(db, ref.Name, v, false, now)
		values[ref.String()] = v
	}
	return values, missing, nil
}

// secretCacheKey includes the realm: Cache may be shared by every realm.
func (s *Users) secretCacheKey(database, name string) string {
	return s.Realm + "\x00" + database + "\x00" + name
}

func (s *Users) cachedSecret(database, name string, now time.Time) (value string, ok, missing bool) {
	if s.Cache == nil {
		return "", false, false
	}
	return s.Cache.get(s.secretCacheKey(database, name), now)
}

func (s *Users) putSecretCache(database, name, value string, missing bool, now time.Time) {
	if s.Cache == nil {
		return
	}
	s.Cache.put(s.secretCacheKey(database, name), value, missing, now)
}

func (s *Users) clearSecretCache(database, name string) {
	if s.Cache == nil {
		return
	}
	s.Cache.clear(s.secretCacheKey(database, name))
}

// secretTarget names a secret for audit records and error messages:
// "secret:<database>/<name>", or "secret:realm/<name>" for the realm scope.
func secretTarget(database, name string) string {
	scope := database
	if scope == "" {
		scope = "realm"
	}
	return "secret:" + scope + "/" + name
}
