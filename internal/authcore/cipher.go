package authcore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SecretCipher encrypts and decrypts secret values with AES-256-GCM,
// under a key derived from the operator's master key (never the master
// key's raw bytes directly, so an accidental log of the derived key
// alone can't be turned back into the configured secret). KeyID is a
// fingerprint safe to store and log: it identifies which master key a
// value was encrypted with, for rotation, without revealing it.
type SecretCipher struct {
	aead  cipher.AEAD
	mac   []byte
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
	mac := sha256.Sum256(append([]byte("backd-secrets-mac|"), raw...))
	return &SecretCipher{aead: aead, mac: mac[:], KeyID: hex.EncodeToString(id[:])[:16]}, nil
}

// MAC is a keyed hash (hex HMAC-SHA-256) of the parts, under a key derived from the master
// key and kept apart from the encryption key: for values that must be compared but never
// read back, such as recovery codes. The parts are length-prefixed, so ("a","bc") and
// ("ab","c") differ.
func (c *SecretCipher) MAC(parts ...string) string {
	m := hmac.New(sha256.New, c.mac)
	for _, p := range parts {
		fmt.Fprintf(m, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(m.Sum(nil))
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

// SealWith encrypts a token under a cipher, in the form "<key id>.<nonce>.<ciphertext>".
func SealWith(c *SecretCipher, token string) (string, error) {
	ct, nonce, err := c.Seal(token)
	if err != nil {
		return "", err
	}
	return c.KeyID + "." + nonce + "." + ct, nil
}

// OpenWith decrypts what SealWith made.
func OpenWith(c *SecretCipher, sealed string) (string, error) {
	parts := strings.SplitN(sealed, ".", 3)
	if len(parts) != 3 {
		return "", errors.New("the stored token is not in the sealed form")
	}
	return c.Open(parts[2], parts[1], parts[0])
}
