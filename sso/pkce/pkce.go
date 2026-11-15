// Package pkce makes and checks PKCE (RFC 7636) verifiers and S256 challenges.
package pkce

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// Verifier is a URL-safe random string of 256 bits.
func Verifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// New makes a PKCE verifier and its S256 challenge.
func New() (verifier, challenge string, err error) {
	if verifier, err = Verifier(); err != nil {
		return "", "", err
	}
	return verifier, Challenge(verifier), nil
}

// Challenge is the S256 challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Valid says whether s looks like an S256 challenge (43 URL-safe characters).
func Valid(s string) bool {
	if len(s) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}
