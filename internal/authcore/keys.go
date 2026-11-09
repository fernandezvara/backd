package authcore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrKeyValue means an imported key isn't 32 bytes of base64url.
var ErrKeyValue = errors.New("a key is 32 bytes, written in base64url")

// KeyLength is the size of a field key in bytes.
const KeyLength = 32

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
