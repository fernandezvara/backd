// Package fieldcrypt encrypts the fields a collection marks `encrypt` before they reach the
// database, and decrypts them for readers (design/field-encryption.md §4 and §5).
//
// Envelope encryption: a field key (a `key` secret the developer holds a copy of) only wraps
// data keys; data keys encrypt values. Replacing a field key re-wraps a handful of data keys
// and rewrites no document.
package fieldcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// Version is the first byte of every encrypted value.
const Version byte = 1

// Sizes of the parts of an encrypted value.
const (
	IDSize    = 16
	NonceSize = 12
	KeySize   = 32
	// Overhead is what encrypting adds to a value: version, data key id, nonce and GCM tag.
	Overhead = 1 + IDSize + NonceSize + 16
)

// Ciphertext is an encrypted value as a document holds it: the stored form of BSON binary
// subtype 6 (version, data key id, nonce, AES-256-GCM ciphertext and tag).
type Ciphertext []byte

// Errors. None of them ever carries a value.
var (
	// ErrUnavailable means a key needed to read or write an encrypted field can't be used: it
	// does not exist, the instance has no secrets key, or a data key can't be opened with it.
	ErrUnavailable = errors.New("the key of an encrypted field is not available")
	// ErrCorrupt means a stored value is not a well-formed encrypted value, or fails its
	// authentication: it was altered, or moved to another document or field.
	ErrCorrupt = errors.New("an encrypted value can't be authenticated")
)

// Field names one encrypted field: where its data keys live.
type Field struct {
	Realm, Database, Collection, Path string
}

// valueAD is the associated data of a value: the realm, database, collection, field path and
// document id, so a value copied into another document or field fails to decrypt.
func valueAD(f Field, docID string) []byte {
	return ad("backd-field-value-v1", f.Realm, f.Database, f.Collection, f.Path, docID)
}

// wrapAD is the associated data of a wrapped data key: its id and the field it belongs to.
func wrapAD(f Field, kind string, id []byte) []byte {
	return ad("backd-field-wrap-v1", f.Realm, f.Database, f.Collection, f.Path, kind, hex.EncodeToString(id))
}

// ad joins parts unambiguously: each is prefixed with its length.
func ad(parts ...string) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(p)))
		b.Write(n[:])
		b.WriteString(p)
	}
	return b.Bytes()
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: a key is %d bytes", ErrUnavailable, KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext under the data key with the given id.
func Seal(dataKey, id, plaintext, associated []byte) (Ciphertext, error) {
	if len(id) != IDSize {
		return nil, errors.New("a data key id is 16 bytes")
	}
	g, err := gcm(dataKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return sealWithNonce(g, id, nonce, plaintext, associated), nil
}

// sealWithNonce is Seal with the nonce given: random in use, fixed in the test vectors.
func sealWithNonce(g cipher.AEAD, id, nonce, plaintext, associated []byte) Ciphertext {
	out := make([]byte, 0, Overhead+len(plaintext))
	out = append(out, Version)
	out = append(out, id...)
	out = append(out, nonce...)
	return g.Seal(out, nonce, plaintext, associated)
}

// ParseID returns the data key id of an encrypted value without opening it.
func ParseID(c Ciphertext) ([]byte, error) {
	if len(c) < Overhead || c[0] != Version {
		return nil, ErrCorrupt
	}
	return c[1 : 1+IDSize], nil
}

// Open decrypts a value with the data key its id names; a value that fails authentication
// (altered, or from another document or field) gives ErrCorrupt.
func Open(dataKey []byte, c Ciphertext, associated []byte) ([]byte, error) {
	if _, err := ParseID(c); err != nil {
		return nil, err
	}
	g, err := gcm(dataKey)
	if err != nil {
		return nil, err
	}
	nonce := c[1+IDSize : 1+IDSize+NonceSize]
	plain, err := g.Open(nil, nonce, c[1+IDSize+NonceSize:], associated)
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

// NewDataKey makes a random data key and its id.
func NewDataKey() (id, key []byte, err error) {
	id, key = make([]byte, IDSize), make([]byte, KeySize)
	if _, err = rand.Read(id); err != nil {
		return nil, nil, err
	}
	if _, err = rand.Read(key); err != nil {
		return nil, nil, err
	}
	return id, key, nil
}

// Wrap encrypts a data key under a field key (AES-256-GCM, with the id and the field as
// associated data): nonce followed by ciphertext and tag.
func Wrap(fieldKey, dataKey, id []byte, f Field, kind string) ([]byte, error) {
	g, err := gcm(fieldKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, dataKey, wrapAD(f, kind, id)), nil
}

// ValueAD, WrapAD and BlindAD are the associated data the format specifies, exported for the
// tools that implement it and the tests that pin it down.
func ValueAD(f Field, docID string) []byte { return valueAD(f, docID) }

// WrapAD is the associated data of a wrapped data key (see ValueAD).
func WrapAD(f Field, kind string, id []byte) []byte { return wrapAD(f, kind, id) }

// Unwrap is the inverse of Wrap; a wrong field key gives ErrUnavailable.
func Unwrap(fieldKey, wrapped, id []byte, f Field, kind string) ([]byte, error) {
	g, err := gcm(fieldKey)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < NonceSize+16 {
		return nil, ErrCorrupt
	}
	key, err := g.Open(nil, wrapped[:NonceSize], wrapped[NonceSize:], wrapAD(f, kind, id))
	if err != nil {
		return nil, ErrUnavailable
	}
	return key, nil
}
