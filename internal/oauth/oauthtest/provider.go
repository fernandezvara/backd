// Package oauthtest is a small identity provider for tests: it signs ID tokens with
// its own key, publishes them, and plays the parts of a provider's authorize, token
// and keys endpoints, so the sign-in flow can be run end to end without a real one.
package oauthtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/fernandezvara/backd/internal/oauth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Provider is the fake. Its endpoints serve any provider kind: tests point the
// service's Endpoints at it.
type Provider struct {
	*httptest.Server

	mu        sync.Mutex
	key       *rsa.PrivateKey
	kid       string
	user      map[string]any
	codes     map[string]grant
	tokenForm []url.Values
	status    int    // a status to answer the token endpoint with instead (0: none)
	errCode   string // and the error code that goes with it
	refresh   string // a refresh token to give with the ID token
	revoke    int    // a status to answer the revoke endpoint with instead (0: 200)
	revokes   []url.Values
	keysHits  int
}

type grant struct {
	nonce, challenge string
	claims           map[string]any
}

// New starts a provider and stops it when the test ends.
func New(t testing.TB) *Provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{key: key, kid: "test-key-1", codes: map[string]grant{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

// Endpoints points a service at the fake.
func (p *Provider) Endpoints(*registry.Provider) oauth.Endpoints {
	return oauth.Endpoints{Authorize: p.URL + "/authorize", Token: p.URL + "/token", JWKS: p.URL + "/keys", Revoke: p.URL + "/revoke"}
}

// Host is the fake's host:port without the port, for an allowed-hosts check.
func (p *Provider) Host() string {
	u, _ := url.Parse(p.URL)
	return u.Hostname()
}

// Client is an HTTP client that reaches the fake only (it listens on a private address).
func (p *Provider) Client() *http.Client {
	return oauth.NewClient(func(h string) bool { return h == p.Host() }, true)
}

// Service is an oauth.Service wired to the fake.
func (p *Provider) Service() *oauth.Service {
	return &oauth.Service{HTTP: p.Client(), Endpoints: p.Endpoints}
}

// SetUser sets the claims of the ID tokens the next authorizations will produce
// (iss, aud, sub, email, …; the nonce, iat and exp are added unless given).
func (p *Provider) SetUser(claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.user = claims
}

// FailToken makes the token endpoint answer status with the OAuth error code, until
// FailToken(0, "").
func (p *Provider) FailToken(status int, errCode string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.errCode = status, errCode
}

// GiveRefreshToken makes the token endpoint return a refresh token too.
func (p *Provider) GiveRefreshToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refresh = token
}

// GrantCode registers an authorization code that a native app holds: exchanging it gives an ID
// token with the claims (no nonce, as with Apple's native flow).
func (p *Provider) GrantCode(code string, claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codes[code] = grant{claims: claims}
}

// FailRevoke makes the revoke endpoint answer status, until FailRevoke(0).
func (p *Provider) FailRevoke(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revoke = status
}

// Revocations are the forms the revoke endpoint received.
func (p *Provider) Revocations() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.revokes...)
}

// TokenRequests are the forms the token endpoint received.
func (p *Provider) TokenRequests() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.tokenForm...)
}

// KeyFetches is how many times the keys were fetched.
func (p *Provider) KeyFetches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keysHits
}

// RotateKey replaces the signing key (a new kid): tokens signed from now on are not
// verifiable with the keys fetched before.
func (p *Provider) RotateKey(t testing.TB, kid string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key, p.kid = key, kid
}

// IDToken signs a token with the claims (iat and exp are added unless given).
func (p *Provider) IDToken(t testing.TB, claims map[string]any) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sign(t, claims)
}

func (p *Provider) sign(t testing.TB, claims map[string]any) string {
	c := map[string]any{}
	for k, v := range claims {
		c[k] = v
	}
	if _, ok := c["iat"]; !ok {
		c["iat"] = time.Now().Unix()
	}
	if _, ok := c["exp"]; !ok {
		c["exp"] = time.Now().Add(time.Hour).Unix()
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: p.kid}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(c)
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Authorize plays the browser: it follows an authorize URL the service made, as a user
// who signs in and consents, and returns the URL the provider sends the browser back
// to (backd's callback, with the code and the state).
func (p *Provider) Authorize(authorizeURL string) string {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	code := "code-" + randomString()
	p.mu.Lock()
	p.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), claims: p.user}
	p.mu.Unlock()
	back, _ := url.Parse(q.Get("redirect_uri"))
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

// Deny is Authorize for a user who refuses: the provider sends back an error.
func (p *Provider) Deny(authorizeURL string) string {
	u, _ := url.Parse(authorizeURL)
	q := u.Query()
	back, _ := url.Parse(q.Get("redirect_uri"))
	bq := back.Query()
	bq.Set("error", "access_denied")
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

func randomString() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/keys":
		p.mu.Lock()
		p.keysHits++
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: p.kid, Algorithm: "RS256", Use: "sig"}}}
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(set)
	case r.URL.Path == "/token" && r.Method == http.MethodPost:
		p.token(w, r)
	case r.URL.Path == "/revoke" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		p.mu.Lock()
		p.revokes = append(p.revokes, r.PostForm)
		status := p.revoke
		p.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	default:
		http.NotFound(w, r)
	}
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenForm = append(p.tokenForm, r.PostForm)
	w.Header().Set("Content-Type", "application/json")
	if p.status != 0 {
		w.WriteHeader(p.status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": p.errCode})
		return
	}
	g, ok := p.codes[r.PostForm.Get("code")]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	delete(p.codes, r.PostForm.Get("code")) // a code works once
	claims := map[string]any{}
	for k, v := range g.claims {
		claims[k] = v
	}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	out := map[string]any{"id_token": p.sign(nil2{}, claims), "token_type": "Bearer"}
	if p.refresh != "" {
		out["refresh_token"] = p.refresh
	}
	_ = json.NewEncoder(w).Encode(out)
}

// nil2 lets sign be called from a handler, where there is no *testing.T: signing
// with a key this package made does not fail.
type nil2 struct{ testing.TB }

func (nil2) Fatal(args ...any) { panic(args) }
