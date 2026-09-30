package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// CallbackTokenPrefix starts every callback token: the credential a
// function uses to reach the realm's data (ctx.db, ctx.admin.db) for the
// duration of one invocation.
const CallbackTokenPrefix = "bdf_"

// CallbackClaims are what a callback token carries: the function, the
// caller it acts as (a user, an API key, both for a key acting on behalf of
// a user, or neither for an anonymous caller) or full access (Admin), and
// when it expires (the invocation's deadline).
type CallbackClaims struct {
	Realm    string    `json:"r"`
	Function string    `json:"f"` // <database>/<name>
	UserID   string    `json:"u,omitempty"`
	KeyHash  string    `json:"k,omitempty"`
	Admin    bool      `json:"a,omitempty"`
	Expires  time.Time `json:"e"`
}

// ErrInvalidCallback means a callback token is malformed, forged or expired.
var ErrInvalidCallback = errors.New("invalid or expired callback token")

func callbackMAC(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("bdf1|" + payload))
	return m.Sum(nil)
}

// SignCallback returns a callback token for c, signed with key.
func SignCallback(key []byte, c CallbackClaims) string {
	data, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(data)
	return CallbackTokenPrefix + payload + "." + base64.RawURLEncoding.EncodeToString(callbackMAC(key, payload))
}

// VerifyCallback checks a callback token's signature and expiry.
func VerifyCallback(key []byte, token string, now time.Time) (CallbackClaims, error) {
	var c CallbackClaims
	rest, ok := strings.CutPrefix(token, CallbackTokenPrefix)
	if !ok || len(key) == 0 {
		return c, ErrInvalidCallback
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return c, ErrInvalidCallback
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, callbackMAC(key, payload)) {
		return c, ErrInvalidCallback
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Realm == "" || c.Function == "" {
		return c, ErrInvalidCallback
	}
	if !now.Before(c.Expires) {
		return c, ErrInvalidCallback
	}
	return c, nil
}

// FuncCaller marks a caller as a function acting through a callback token.
type FuncCaller struct {
	Name  string // <realm>/<database>/<function>
	Admin bool   // ctx.admin.db: full access, recorded as the function
}

// CallbackCaller resolves a verified token into the caller it acts as,
// checking the user and key still exist and may act: a user disabled or a
// key revoked during the invocation loses access at once.
func (s *Users) CallbackCaller(ctx context.Context, c CallbackClaims) (Caller, error) {
	fn := &FuncCaller{Name: c.Realm + "/" + c.Function, Admin: c.Admin}
	if c.Admin {
		return Caller{Func: fn}, nil
	}
	caller := Caller{Func: fn}
	if c.KeyHash != "" {
		k, err := s.Store.APIKeyByHash(ctx, c.KeyHash)
		if err != nil || (k.ExpiresAt != nil && !s.now().Before(*k.ExpiresAt)) {
			return Caller{}, ErrInvalidCallback
		}
		caller.Key = &k
	}
	if c.UserID != "" {
		u, err := s.Store.UserByID(ctx, c.UserID)
		if err != nil || u.Disabled {
			return Caller{}, ErrInvalidCallback
		}
		caller.User = &Principal{User: u}
	}
	return caller, nil
}
