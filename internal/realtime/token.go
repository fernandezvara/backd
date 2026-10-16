// Package realtime serves subscriptions to document changes as server-sent events
// (design/triggers-and-realtime.md §17 and §20). It is driver-neutral and knows nothing of
// HTTP: the Hub owns the limits, buffers and the shared change feed; a Stream is one open
// connection with its subscriptions; the httpapi package turns requests into streams and
// frames into bytes.
package realtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Token is a place in the change history that a client can resume after. It is signed with
// a per-realm key, so any instance can verify it after a reconnect and nobody can forge one
// or present another's.
type Token struct {
	Pos       []byte `json:"p"`
	Realm     string `json:"r"`
	Principal string `json:"u"`
	Issued    int64  `json:"t"` // unix seconds
}

// ErrBadToken means a resume token is malformed, was not signed with this realm's key, or
// belongs to another realm or principal.
var ErrBadToken = errors.New("realtime: invalid resume token")

var b64 = base64.RawURLEncoding

// Seal renders t as text: the payload and its MAC.
func Seal(key []byte, t Token) string {
	raw, _ := json.Marshal(t)
	p := b64.EncodeToString(raw)
	return p + "." + b64.EncodeToString(mac(key, p))
}

// Open verifies and decodes a token made by Seal.
func Open(key []byte, s string) (Token, error) {
	p, sig, ok := strings.Cut(s, ".")
	if !ok {
		return Token{}, ErrBadToken
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(key, p)) {
		return Token{}, ErrBadToken
	}
	raw, err := b64.DecodeString(p)
	if err != nil {
		return Token{}, ErrBadToken
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return Token{}, ErrBadToken
	}
	return t, nil
}

func mac(key []byte, payload string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(payload))
	return h.Sum(nil)
}
