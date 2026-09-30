// Package egress runs backd's egress role: the only way a function
// process reaches the network. It is an HTTP forward/CONNECT proxy that
// resolves every target itself and refuses loopback, private, link-local
// (metadata), CGNAT, multicast and unspecified addresses, with backd's
// callback listener as its only exception. Each function process
// authenticates with a token scoped to its own invocation, naming the
// hosts it may reach, so the proxy enforces that allowlist too, in
// addition to the per-process --allow-net the executor already sets.
package egress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
)

// TokenPrefix starts every egress credential.
const TokenPrefix = "bdg_"

// Claims are what an egress token carries: the hosts (as function.yaml's
// network list, plus backd's callback address) the process may reach
// through the proxy, and when the token expires.
type Claims struct {
	Hosts   []string  `json:"h"`
	Expires time.Time `json:"e"`
}

// ErrInvalidToken means an egress token is malformed, forged or expired.
var ErrInvalidToken = errors.New("invalid or expired egress token")

func mac(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("bdg1|" + payload))
	return m.Sum(nil)
}

// Sign returns an egress token for c, signed with key.
func Sign(key []byte, c Claims) string {
	data, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(data)
	return TokenPrefix + payload + "." + base64.RawURLEncoding.EncodeToString(mac(key, payload))
}

// Verify checks an egress token's signature and expiry.
func Verify(key []byte, token string, now time.Time) (Claims, error) {
	var c Claims
	rest, ok := strings.CutPrefix(token, TokenPrefix)
	if !ok || len(key) == 0 {
		return c, ErrInvalidToken
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return c, ErrInvalidToken
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(key, payload)) {
		return c, ErrInvalidToken
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, ErrInvalidToken
	}
	if !now.Before(c.Expires) {
		return c, ErrInvalidToken
	}
	return c, nil
}

// Allows reports whether c's hosts include hostport: an exact host:port
// entry matches only that port, a bare host entry (as Deno's --allow-net
// treats it) matches hostport on any port.
func (c Claims) Allows(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	for _, h := range c.Hosts {
		if strings.EqualFold(h, hostport) || strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}
