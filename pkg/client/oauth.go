package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sync"
	"time"
)

// OAuthStorage keeps what a redirect sign-in needs between its start and its end: the PKCE
// verifier and where the sign-in began. A program that starts a sign-in and finishes it in another
// process (a CLI that opens a browser and waits on a local address) keeps it in memory; one that
// is restarted in between keeps it somewhere durable.
type OAuthStorage interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, value string) error
	Remove(ctx context.Context) error
}

// MemoryOAuthStorage keeps the pending sign-in in memory. It is the default.
type MemoryOAuthStorage struct {
	mu   sync.Mutex
	held string
}

func (m *MemoryOAuthStorage) Get(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held, nil
}

func (m *MemoryOAuthStorage) Set(_ context.Context, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = v
	return nil
}

func (m *MemoryOAuthStorage) Remove(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = ""
	return nil
}

const (
	oauthParamPrefix = "backd_"
	pendingLife      = 10 * time.Minute // the server's state lives ten minutes
)

// pkce makes a code verifier and its S256 challenge.
func newPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("backd: no randomness: " + err.Error())
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, pkceChallenge(verifier)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type pending struct {
	Verifier  string `json:"verifier"`
	Provider  string `json:"provider"`
	Intent    string `json:"intent"`
	RequestID string `json:"request_id,omitempty"`
	At        int64  `json:"at"`
}

func savePending(ctx context.Context, s OAuthStorage, p pending) error {
	p.At = time.Now().UnixMilli()
	b, _ := json.Marshal(p)
	return s.Set(ctx, string(b))
}

// loadPending returns the pending sign-in, or nil when there is none, it is unreadable or it is
// older than the server keeps its side.
func loadPending(ctx context.Context, s OAuthStorage) *pending {
	raw, err := s.Get(ctx)
	if err != nil || raw == "" {
		return nil
	}
	var p pending
	if json.Unmarshal([]byte(raw), &p) != nil || p.Verifier == "" || time.Since(time.UnixMilli(p.At)) >= pendingLife {
		return nil
	}
	return &p
}

// OAuthReturn is what backd put on the address it sent the person back to.
type OAuthReturn struct {
	Code     string // the one-time code to exchange for a session
	Error    string // why the sign-in failed
	Linked   string // the provider that was linked, for a link made from a signed-in session
	Provider string
	Org      string
}

// ParseOAuthReturn reads the backd_* parameters of the address a sign-in with a provider (or a
// magic link) returns to. It returns nil when the address carries none.
func ParseOAuthReturn(address string) *OAuthReturn {
	u, err := url.Parse(address)
	if err != nil {
		return nil
	}
	q := u.Query()
	r := &OAuthReturn{
		Code: q.Get(oauthParamPrefix + "code"), Error: q.Get(oauthParamPrefix + "error"),
		Linked: q.Get(oauthParamPrefix + "linked"), Provider: q.Get(oauthParamPrefix + "provider"),
		Org: q.Get(oauthParamPrefix + "org"),
	}
	if r.Code == "" && r.Error == "" && r.Linked == "" {
		return nil
	}
	return r
}

var oauthErrors = map[string]struct {
	status  int
	message string
	kind    *kind
}{
	"cancelled":          {401, "the sign-in was cancelled at the provider", kAuthentication},
	"account_exists":     {409, "an account with that address exists: sign in another way and link this provider from there", kConflict},
	"signup_closed":      {403, "sign-up is not open in this realm", kForbidden},
	"invitation_invalid": {400, "sign-up needs a valid invitation", kValidation},
	"link_conflict":      {409, "that provider account can not be linked to this user", kConflict},
	"email_not_verified": {403, "the address of this account is not verified", kForbidden},
	"email_required":     {400, "the provider gave no email address", kValidation},
	"signin_refused":     {403, "this account can not sign in", kForbidden},
	"provider_error":     {502, "the provider failed or answered something backd can not accept", kBackd},
}

// oauthError is the error for a backd_error the sign-in came back with.
func oauthError(code, provider string) *Error {
	known, ok := oauthErrors[code]
	if !ok {
		code, known = "provider_error", oauthErrors["provider_error"]
	}
	e := newError(known.status, code, known.message, known.kind)
	if provider != "" {
		e.Details = []Detail{{Path: "provider", Reason: provider}}
	}
	return e
}
