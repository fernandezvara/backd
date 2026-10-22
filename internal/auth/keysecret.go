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

	"github.com/fernandezvara/backd/internal/fieldcrypt"
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

// KeyInUseError is a refusal to delete a key that fields still name, or that still wraps data
// keys (and with them, values).
type KeyInUseError struct {
	Name     string
	Uses     []string
	DataKeys int64
}

func (e *KeyInUseError) Error() string {
	var parts []string
	if len(e.Uses) > 0 {
		parts = append(parts, strings.Join(e.Uses, ", "))
	}
	if e.DataKeys > 0 {
		parts = append(parts, fmt.Sprintf("%d data key(s) that encrypted values depend on", e.DataKeys))
	}
	return fmt.Sprintf("the key %s is used by %s", e.Name, strings.Join(parts, " and "))
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
		Name: name, Type: SecretTypeKey, Fingerprint: fp, Version: 1, Ciphertext: ct, Nonce: nonce, KeyID: s.Cipher.KeyID,
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
	var dataKeys int64
	if ds, ok := s.Store.(fieldcrypt.Store); ok {
		if dataKeys, err = ds.DataKeysByKey(ctx, name); err != nil {
			return err
		}
	}
	if uses := s.KeyUsedBy(name); len(uses) > 0 || dataKeys > 0 {
		return &KeyInUseError{Name: name, Uses: uses, DataKeys: dataKeys}
	}
	if err := s.Store.DeleteSecret(ctx, "", name); err != nil {
		return err
	}
	s.clearSecretCache("", name)
	s.Audit(ctx, AuditKeyDelete, secretTarget("", name), map[string]any{"fingerprint": have.Fingerprint})
	return nil
}

// ErrNoKey means the secret is missing or is not a key.
var ErrNoKey = errors.New("no such key")

// Errors of a rotation.
var (
	// ErrRotationPending means a rotation started and was not finished: resume it first.
	ErrRotationPending = errors.New("a rotation of this key is unfinished: resume it first")
	// ErrNoRotation means there is nothing to resume.
	ErrNoRotation = errors.New("no rotation of this key is pending")
)

// KeyMaterial returns a field key's value for the data keys it wraps; it implements
// fieldcrypt.Ring.Keys. The value is opened from the sealed store and never leaves the process.
func (s *Users) KeyMaterial(ctx context.Context, name string) (fieldcrypt.Material, error) {
	if s.Cipher == nil {
		return fieldcrypt.Material{}, fieldcrypt.ErrUnavailable
	}
	sec, err := s.Store.SecretByScope(ctx, "", name)
	if err != nil || sec.Type != SecretTypeKey {
		return fieldcrypt.Material{}, fmt.Errorf("%w: the key %s does not exist", fieldcrypt.ErrUnavailable, name)
	}
	open := func(ct, nonce string) ([]byte, error) {
		text, err := s.Cipher.Open(ct, nonce, sec.KeyID)
		if err != nil {
			return nil, fieldcrypt.ErrUnavailable
		}
		return DecodeKey(text)
	}
	m := fieldcrypt.Material{Version: max(sec.Version, 1)}
	if m.Current, err = open(sec.Ciphertext, sec.Nonce); err != nil {
		return fieldcrypt.Material{}, fmt.Errorf("%w: the key %s can't be opened", fieldcrypt.ErrUnavailable, name)
	}
	if sec.PrevCiphertext != "" {
		if m.Previous, err = open(sec.PrevCiphertext, sec.PrevNonce); err != nil {
			return fieldcrypt.Material{}, fmt.Errorf("%w: the key %s can't be opened", fieldcrypt.ErrUnavailable, name)
		}
		m.PreviousVersion = sec.PrevVersion
	}
	return m, nil
}

// RotatedKey is what a rotation answers. Value is set only for a generated key.
type RotatedKey struct {
	Name        string
	Fingerprint string
	Previous    string
	Version     int
	Rewrapped   int
	Resumed     bool
	Value       string
}

// RotateKey replaces a field key's value and re-wraps every data key it wrapped, so no document
// is rewritten (design/field-encryption.md §5.3). value is the new key in base64url, or empty
// to generate one; resume continues a rotation that stopped before it finished, with the value
// that rotation chose. While it runs the previous value stays, so every data key stays
// readable; it is dropped only when none is wrapped by it any more.
func (s *Users) RotateKey(ctx context.Context, name, value string, resume bool, by string) (RotatedKey, error) {
	if s.Cipher == nil {
		return RotatedKey{}, ErrSecretsNotConfigured
	}
	ds, _ := s.Store.(fieldcrypt.Store)
	have, err := s.Store.SecretByScope(ctx, "", name)
	if errors.Is(err, ErrSecretNotFound) || (err == nil && have.Type != SecretTypeKey) {
		return RotatedKey{}, ErrNoKey
	}
	if err != nil {
		return RotatedKey{}, err
	}
	pending := have.PrevCiphertext != ""
	out := RotatedKey{Name: name, Resumed: pending}
	switch {
	case pending && !resume:
		return RotatedKey{}, ErrRotationPending
	case !pending && resume:
		return RotatedKey{}, ErrNoRotation
	case !pending:
		generated := value == ""
		var raw []byte
		if generated {
			raw, err = NewKey()
		} else {
			raw, err = DecodeKey(value)
		}
		if err != nil {
			return RotatedKey{}, err
		}
		text := base64.RawURLEncoding.EncodeToString(raw)
		ct, nonce, err := s.Cipher.Seal(text)
		if err != nil {
			return RotatedKey{}, err
		}
		next := have
		next.PrevCiphertext, next.PrevNonce, next.PrevVersion = have.Ciphertext, have.Nonce, max(have.Version, 1)
		next.Ciphertext, next.Nonce, next.KeyID = ct, nonce, s.Cipher.KeyID
		next.Version, next.Fingerprint = next.PrevVersion+1, KeyFingerprint(raw)
		next.UpdatedAt, next.UpdatedBy = s.now(), by
		ok, err := s.Store.ReplaceKeySecret(ctx, next, next.PrevVersion)
		if err != nil {
			return RotatedKey{}, err
		}
		if !ok {
			return RotatedKey{}, ErrRotationPending
		}
		if generated {
			out.Value = text
		}
		have = next
	}
	out.Fingerprint, out.Version = have.Fingerprint, max(have.Version, 1)
	out.Previous = s.previousFingerprint(ctx, have)
	s.clearSecretCache("", name)
	m, err := s.KeyMaterial(ctx, name)
	if err != nil {
		return out, err
	}
	if ds != nil {
		if out.Rewrapped, err = fieldcrypt.Rewrap(ctx, ds, s.Realm, name, m); err != nil {
			return out, err
		}
	}
	// Nothing is wrapped by the previous value any more: drop it.
	done := have
	done.PrevCiphertext, done.PrevNonce, done.PrevVersion = "", "", 0
	done.UpdatedAt = s.now()
	if _, err := s.Store.ReplaceKeySecret(ctx, done, out.Version); err != nil {
		return out, err
	}
	s.clearSecretCache("", name)
	s.Audit(ctx, AuditKeyRotate, secretTarget("", name), map[string]any{"fingerprint": out.Fingerprint, "previous": out.Previous, "rewrapped": out.Rewrapped})
	return out, nil
}

// previousFingerprint is the fingerprint of the value a rotation replaced.
func (s *Users) previousFingerprint(ctx context.Context, sec Secret) string {
	if sec.PrevCiphertext == "" {
		return ""
	}
	text, err := s.Cipher.Open(sec.PrevCiphertext, sec.PrevNonce, sec.KeyID)
	if err != nil {
		return ""
	}
	raw, err := DecodeKey(text)
	if err != nil {
		return ""
	}
	return KeyFingerprint(raw)
}
