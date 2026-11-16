package ssotest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/backd/sso"
)

// MemStore is a sso.Store in memory.
type MemStore struct {
	Now func() time.Time

	mu       sync.Mutex
	attempts map[string]sso.Attempt
	codes    map[string]sso.Code
}

func (s *MemStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// PutAttempt implements sso.Store.
func (s *MemStore) PutAttempt(_ context.Context, key string, a sso.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts == nil {
		s.attempts = map[string]sso.Attempt{}
	}
	s.attempts[key] = a
	return nil
}

// TakeAttempt implements sso.Store.
func (s *MemStore) TakeAttempt(_ context.Context, key string) (sso.Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[key]
	delete(s.attempts, key)
	if !ok || !a.ExpiresAt.After(s.now()) {
		return sso.Attempt{}, sso.ErrNotFound
	}
	return a, nil
}

// PutCode implements sso.Store.
func (s *MemStore) PutCode(_ context.Context, key string, c sso.Code) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = map[string]sso.Code{}
	}
	s.codes[key] = c
	return nil
}

// TakeCode implements sso.Store.
func (s *MemStore) TakeCode(_ context.Context, key string) (sso.Code, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[key]
	delete(s.codes, key)
	if !ok || !c.ExpiresAt.After(s.now()) {
		return sso.Code{}, sso.ErrNotFound
	}
	return c, nil
}

// Catalog is a sso.Catalog over a fixed list of providers per scope.
type Catalog struct {
	// Providers[scope][name]
	Providers map[string]map[string]sso.Provider
	// NoURL: backd's public address is unknown.
	NoURL bool
}

// Provider implements sso.Catalog.
func (c *Catalog) Provider(_ context.Context, scope, name string) (sso.Provider, error) {
	if p, ok := c.Providers[scope][name]; ok {
		return p, nil
	}
	return sso.Provider{}, sso.ErrNotFound
}

// CallbackURL implements sso.Catalog.
func (c *Catalog) CallbackURL(scope string, p sso.Provider) (string, bool) {
	if c.NoURL {
		return "", false
	}
	return "https://api.example/v1/r/_auth/oauth/" + p.Name + "/callback", true
}

// Directory is a sso.Directory in memory, with the sign-up rules of a realm in Mode.
type Directory struct {
	// Mode is the sign-up mode: "open" (the default), "invite" or "closed".
	Mode string
	// Deny makes Enter refuse; Scope is what it scopes sessions to; Replaced is Superseded's answer.
	Deny     bool
	Scope    string
	Replaced string
	// Seed accounts by id.
	mu         sync.Mutex
	Users      map[string]sso.Account
	Identities map[sso.IdentityKey]string
	Touched    []sso.IdentityKey
	Created    []sso.Account
	Entered    []sso.Entry
	next       int
}

// NormalizeEmail implements sso.Directory.
func (d *Directory) NormalizeEmail(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if strings.Count(raw, "@") != 1 || strings.HasPrefix(raw, "@") || strings.HasSuffix(raw, "@") {
		return ""
	}
	return raw
}

// AddUser puts an account in.
func (d *Directory) AddUser(a sso.Account) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Users == nil {
		d.Users = map[string]sso.Account{}
	}
	d.Users[a.ID] = a
}

// AddIdentity links a provider account to a user.
func (d *Directory) AddIdentity(k sso.IdentityKey, userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Identities == nil {
		d.Identities = map[sso.IdentityKey]string{}
	}
	d.Identities[k] = userID
}

// Identity implements sso.Directory.
func (d *Directory) Identity(_ context.Context, k sso.IdentityKey) (sso.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, ok := d.Identities[k]
	if !ok {
		return sso.Account{}, sso.ErrNotFound
	}
	return d.Users[id], nil
}

// User implements sso.Directory.
func (d *Directory) User(_ context.Context, id string) (sso.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.Users[id]
	if !ok {
		return sso.Account{}, sso.ErrNotFound
	}
	return a, nil
}

// UserByEmail implements sso.Directory.
func (d *Directory) UserByEmail(_ context.Context, email string) (sso.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.Users {
		if a.Email == email {
			return a, nil
		}
	}
	return sso.Account{}, sso.ErrNotFound
}

// MaySignIn implements sso.Directory.
func (d *Directory) MaySignIn(_ context.Context, a sso.Account, _ sso.Request) error {
	switch {
	case a.Erased:
		return &sso.Refused{UserID: a.ID, Reason: "erased"}
	case a.Disabled:
		return &sso.Refused{UserID: a.ID, Reason: "disabled"}
	}
	return nil
}

// Touch implements sso.Directory.
func (d *Directory) Touch(_ context.Context, k sso.IdentityKey, _ sso.Login, _ string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Touched = append(d.Touched, k)
}

// Link implements sso.Directory.
func (d *Directory) Link(_ context.Context, a sso.Account, l sso.Login, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	k := sso.IdentityKey{Provider: l.Provider.Name, Org: l.Provider.Org, Issuer: l.Claims.Issuer, Subject: l.Claims.Subject}
	if _, taken := d.Identities[k]; taken {
		return sso.ErrLinkConflict
	}
	for ik, uid := range d.Identities {
		if uid == a.ID && ik.Provider == k.Provider && ik.Org == k.Org {
			return sso.ErrLinkConflict
		}
	}
	if d.Identities == nil {
		d.Identities = map[sso.IdentityKey]string{}
	}
	d.Identities[k] = a.ID
	return nil
}

// Create implements sso.Directory.
func (d *Directory) Create(_ context.Context, l sso.Login, email string, verified bool) (sso.Account, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if l.Provider.Org == "" {
		switch d.Mode {
		case "", "open":
		case "invite":
			return sso.Account{}, sso.ErrInvitationInvalid
		default:
			return sso.Account{}, sso.ErrSignupClosed
		}
	}
	d.next++
	a := sso.Account{ID: fmt.Sprintf("u%d", d.next), Email: email, EmailVerified: verified}
	if d.Users == nil {
		d.Users = map[string]sso.Account{}
	}
	if d.Identities == nil {
		d.Identities = map[sso.IdentityKey]string{}
	}
	d.Users[a.ID] = a
	d.Identities[sso.IdentityKey{Provider: l.Provider.Name, Org: l.Provider.Org, Issuer: l.Claims.Issuer, Subject: l.Claims.Subject}] = a.ID
	d.Created = append(d.Created, a)
	return a, nil
}

// Enter implements sso.Directory.
func (d *Directory) Enter(_ context.Context, e sso.Entry) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Entered = append(d.Entered, e)
	return d.Scope, !d.Deny
}

// Superseded implements sso.Directory.
func (d *Directory) Superseded(context.Context, sso.Attempt, sso.Provider) string { return d.Replaced }

// CleanProfile implements sso.Directory.
func (d *Directory) CleanProfile(c sso.Claims, extra map[string]string) sso.Profile {
	out := sso.Profile{}
	for k, v := range map[string]string{"name": c.Name, "given_name": c.GivenName, "family_name": c.FamilyName, "picture": c.Picture} {
		if v == "" {
			v = extra[k]
		}
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// Recorder is a sso.Events that keeps what it was given.
type Recorder struct {
	mu     sync.Mutex
	Events []sso.Event
}

// Emit implements sso.Events.
func (r *Recorder) Emit(_ context.Context, e sso.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, e)
}

// Protocol is a sso.Protocol that plays the provider from a script: an ID token is the key of
// Tokens, and what it says is the value.
type Protocol struct {
	// Claims by ID token. The code exchange gives the token named by Tokens[code].
	Claims map[string]sso.Claims
	// Codes maps an authorization code to the ID token the exchange gives.
	Codes map[string]string
	// ExchangeErr and VerifyErr make those calls fail.
	ExchangeErr, VerifyErr error
	// Refresh is the Apple refresh token AppleRefresh gives; RefreshErr makes it fail.
	Refresh    string
	RefreshErr error
	NoPKCE     bool

	mu        sync.Mutex
	Exchanged []string // verifier of each exchange
	Nonces    []string // nonce each Verify was asked for
}

// UsesPKCE implements sso.Protocol.
func (f *Protocol) UsesPKCE(sso.Provider) bool { return !f.NoPKCE }

// AuthorizeURL implements sso.Protocol.
func (f *Protocol) AuthorizeURL(p sso.Provider, a sso.AuthorizeParams) string {
	return "https://idp.example/authorize?client_id=" + p.ClientID + "&state=" + a.State + "&nonce=" + a.Nonce + "&code_challenge=" + a.CodeChallenge
}

// Exchange implements sso.Protocol.
func (f *Protocol) Exchange(_ context.Context, _ sso.Provider, code, _, verifier string) (sso.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Exchanged = append(f.Exchanged, verifier)
	if f.ExchangeErr != nil {
		return sso.Tokens{}, f.ExchangeErr
	}
	tok, ok := f.Codes[code]
	if !ok {
		return sso.Tokens{}, sso.ErrExchange
	}
	return sso.Tokens{IDToken: tok, RefreshToken: f.Refresh}, nil
}

// Verify implements sso.Protocol.
func (f *Protocol) Verify(_ context.Context, _ sso.Provider, idToken, nonce string, _ bool) (sso.Claims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Nonces = append(f.Nonces, nonce)
	if f.VerifyErr != nil {
		return sso.Claims{}, f.VerifyErr
	}
	c, ok := f.Claims[idToken]
	if !ok {
		return sso.Claims{}, sso.ErrInvalidToken
	}
	return c, nil
}

// AppleRefresh implements sso.Protocol.
func (f *Protocol) AppleRefresh(context.Context, sso.Provider, sso.Claims, string) (string, error) {
	return f.Refresh, f.RefreshErr
}

// FirstAuthorization implements sso.Protocol.
func (f *Protocol) FirstAuthorization(p sso.Provider, raw string) map[string]string {
	if p.Kind == sso.Apple && raw != "" {
		return map[string]string{"name": raw}
	}
	return nil
}
