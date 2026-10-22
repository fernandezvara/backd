package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// SecretTypeKey marks a secret as a field key (design/field-encryption.md §3.1): 32 bytes the
// developer holds a copy of, that only field encryption reads.
const SecretTypeKey = "key"

// KeyLength is the size of a field key in bytes.
const KeyLength = 32

// Errors of key secrets.
var (
	// ErrSecretExists means a secret already has that name; a key is never overwritten by creating one.
	ErrSecretExists = errors.New("a secret with that name already exists")
	// ErrSecretIsKey means a plain secret write names a key: keys change only through the key calls.
	ErrSecretIsKey = errors.New("this name is a field key: keys are created, rotated and deleted as keys")
	// ErrKeyValue means an imported key isn't 32 bytes of base64url.
	ErrKeyValue = errors.New("a key is 32 bytes, written in base64url")
)

// KeyInUseError is a refusal to delete a key that fields still name.
type KeyInUseError struct {
	Name string
	Uses []string
}

func (e *KeyInUseError) Error() string {
	return fmt.Sprintf("the key %s is used by %s", e.Name, strings.Join(e.Uses, ", "))
}

// KeyFingerprint is the first 8 hex characters of SHA-256 over a fixed label and the key: it
// lets someone check that a saved copy is the right one without the key being shown again.
func KeyFingerprint(key []byte) string {
	h := sha256.New()
	h.Write([]byte("backd-field-key-v1|"))
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// DecodeKey reads a key written in base64url, with or without padding.
func DecodeKey(text string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(text), "="))
	if err != nil || len(b) != KeyLength {
		return nil, ErrKeyValue
	}
	return b, nil
}

// NewKey is a CreatedKey's source of a generated key: 32 random bytes.
func NewKey() ([]byte, error) {
	b := make([]byte, KeyLength)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// CreatedKey is what creating a key answers once. Value is set only for a generated key (an
// imported one is already in the developer's hands) and is never available again.
type CreatedKey struct {
	Name        string
	Fingerprint string
	Value       string
}

// CreateKey stores a new realm-scope field key: a generated one (value empty), or the 32 bytes
// given in base64url. A name already taken, by a key or by any secret, is refused. The value of a
// generated key is returned here and nowhere else.
func (s *Users) CreateKey(ctx context.Context, name, value, by string) (CreatedKey, error) {
	if s.Cipher == nil {
		return CreatedKey{}, ErrSecretsNotConfigured
	}
	if !registry.ValidSecretName(name) {
		return CreatedKey{}, fmt.Errorf("invalid key name %q: must be upper-case letters, digits and _, starting with a letter", name)
	}
	var raw []byte
	var err error
	generated := value == ""
	if generated {
		raw, err = NewKey()
	} else {
		raw, err = DecodeKey(value)
	}
	if err != nil {
		return CreatedKey{}, err
	}
	text := base64.RawURLEncoding.EncodeToString(raw)
	ct, nonce, err := s.Cipher.Seal(text)
	if err != nil {
		return CreatedKey{}, err
	}
	now := s.now()
	fp := KeyFingerprint(raw)
	if err := s.Store.CreateSecret(ctx, Secret{
		Name: name, Type: SecretTypeKey, Fingerprint: fp, Ciphertext: ct, Nonce: nonce, KeyID: s.Cipher.KeyID,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: by,
	}); err != nil {
		return CreatedKey{}, err
	}
	s.clearSecretCache("", name)
	s.Audit(ctx, AuditKeyCreate, secretTarget("", name), map[string]any{"fingerprint": fp, "generated": generated})
	out := CreatedKey{Name: name, Fingerprint: fp}
	if generated {
		out.Value = text
	}
	return out, nil
}

// KeyUsedBy lists where a key is used, as "database/collection.path" entries.
func (s *Users) KeyUsedBy(name string) []string {
	if s.KeyUses == nil {
		return nil
	}
	return s.KeyUses(name)
}

// DeleteKey removes a key that nothing uses; ErrSecretNotFound if there is no such key.
func (s *Users) DeleteKey(ctx context.Context, name string) error {
	have, err := s.Store.SecretByScope(ctx, "", name)
	if err != nil {
		return err
	}
	if have.Type != SecretTypeKey {
		return ErrSecretNotFound
	}
	if uses := s.KeyUsedBy(name); len(uses) > 0 {
		return &KeyInUseError{Name: name, Uses: uses}
	}
	if err := s.Store.DeleteSecret(ctx, "", name); err != nil {
		return err
	}
	s.clearSecretCache("", name)
	s.Audit(ctx, AuditKeyDelete, secretTarget("", name), map[string]any{"fingerprint": have.Fingerprint})
	return nil
}
