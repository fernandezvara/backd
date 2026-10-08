package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/fernandezvara/backd/internal/registry"
)

// Errors a provider's answers can lead to. None of them carries anything the
// provider said beyond its own error code.
var (
	// ErrExchange: the provider refused the authorization code.
	ErrExchange = errors.New("oauth: the provider refused the authorization code")
	// ErrInvalidToken: an ID token that does not verify (signature, issuer,
	// audience, expiry or nonce), or is missing what is needed.
	ErrInvalidToken = errors.New("oauth: the ID token is not valid")
	// ErrUnavailable: the provider could not be reached, or answered nonsense.
	ErrUnavailable = errors.New("oauth: the provider is not available")
)

const (
	clockSkew    = time.Minute
	minJWKSTTL   = 5 * time.Minute
	maxJWKSTTL   = 24 * time.Hour
	defaultTTL   = time.Hour
	refetchEvery = time.Minute
	microsoftMSA = "9188040d-6c67-4c5b-b112-36a304b66dad" // the tenant id of personal accounts
)

// Service is what the sign-in flow asks of providers.
type Service struct {
	// HTTP reaches the providers: NewClient(BuiltinHostAllowed, …) in production.
	HTTP *http.Client
	// Endpoints gives a provider's URLs; BuiltinEndpoints when nil.
	Endpoints func(*registry.Provider) Endpoints
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu    sync.Mutex
	jwks  map[string]*jwksEntry  // by URL
	apple map[string]appleSecret // by provider key id and team
}

type jwksEntry struct {
	keys      jose.JSONWebKeySet
	expires   time.Time
	fetchedAt time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) endpoints(p *registry.Provider) Endpoints {
	if s.Endpoints != nil {
		return s.Endpoints(p)
	}
	return BuiltinEndpoints(p)
}

// AuthorizeParams are what an authorization request carries.
type AuthorizeParams struct {
	RedirectURI   string
	State         string
	Nonce         string
	CodeChallenge string // PKCE (S256) between backd and the provider; empty: none
}

// UsesPKCE says whether backd sends a PKCE challenge to the provider (Apple's web flow
// does not take one).
func UsesPKCE(p *registry.Provider) bool { return p.Name != registry.ProviderApple }

// AuthorizeURL is where to send the user to sign in with the provider.
func (s *Service) AuthorizeURL(p *registry.Provider, a AuthorizeParams) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", a.RedirectURI)
	q.Set("state", a.State)
	q.Set("nonce", a.Nonce)
	switch p.Name {
	case registry.ProviderApple:
		q.Set("scope", "name email")
		q.Set("response_mode", "form_post")
	default:
		q.Set("scope", "openid email profile")
	}
	if a.CodeChallenge != "" && UsesPKCE(p) {
		q.Set("code_challenge", a.CodeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	return s.endpoints(p).Authorize + "?" + q.Encode()
}

// Tokens are what the code exchange gives back that backd keeps using: the ID token
// and, from Apple, the refresh token (kept only to revoke it).
type Tokens struct {
	IDToken      string
	RefreshToken string
}

// Exchange trades an authorization code for the user's ID token. clientID is the
// application the code was issued to (the provider's web client, or for a native
// app its own id), and secret the client secret of Google or Microsoft, or Apple's
// already-signed one.
func (s *Service) Exchange(ctx context.Context, p *registry.Provider, clientID, secret, code, redirectURI, codeVerifier string) (Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	if codeVerifier != "" && UsesPKCE(p) {
		form.Set("code_verifier", codeVerifier)
	}
	var out struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	status, err := s.postForm(ctx, s.endpoints(p).Token, form, &out)
	switch {
	case err != nil:
		return Tokens{}, err
	case status == http.StatusBadRequest || status == http.StatusUnauthorized:
		return Tokens{}, fmt.Errorf("%w (%s)", ErrExchange, sanitize(out.Error))
	case status != http.StatusOK || out.IDToken == "":
		return Tokens{}, fmt.Errorf("%w: status %d", ErrUnavailable, status)
	}
	return Tokens{IDToken: out.IDToken, RefreshToken: out.RefreshToken}, nil
}

// sanitize keeps a provider's error code short and plain before it is logged.
func sanitize(s string) string {
	if len(s) > 64 || strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r > '~' }) {
		return "?"
	}
	return s
}

func (s *Service) postForm(ctx context.Context, endpoint string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(data) > 0 && out != nil {
		if err := json.Unmarshal(data, out); err != nil && resp.StatusCode == http.StatusOK {
			return 0, fmt.Errorf("%w: unreadable answer", ErrUnavailable)
		}
	}
	return resp.StatusCode, nil
}

// Claims are what backd takes from a verified ID token.
type Claims struct {
	// Subject identifies the person for the provider: sub, or for Microsoft the tenant
	// and object ids, so the same person in two tenants is two identities.
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	GivenName     string
	FamilyName    string
	Picture       string
	// Audience is the client id the token was issued to (the web client's, or a native app's).
	Audience string
}

type idClaims struct {
	Issuer        string      `json:"iss"`
	Subject       string      `json:"sub"`
	Audience      audience    `json:"aud"`
	Expires       numericDate `json:"exp"`
	NotBefore     numericDate `json:"nbf"`
	IssuedAt      numericDate `json:"iat"`
	Nonce         string      `json:"nonce"`
	Email         string      `json:"email"`
	EmailVerified flexBool    `json:"email_verified"`
	Name          string      `json:"name"`
	GivenName     string      `json:"given_name"`
	FamilyName    string      `json:"family_name"`
	Picture       string      `json:"picture"`
	TenantID      string      `json:"tid"`
	ObjectID      string      `json:"oid"`
}

// audience is a string or a list of them.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

type numericDate int64

func (n *numericDate) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = numericDate(f)
	return nil
}

// flexBool reads true/false or "true"/"false" (Apple sends either).
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	var v bool
	if json.Unmarshal(b, &v) == nil {
		*f = flexBool(v)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return err
	}
	*f = flexBool(str == "true")
	return nil
}

// Verify checks an ID token of the provider and returns what it says about the user:
// its signature against the provider's keys, issuer, audience (the web client's, and,
// when native, the mobile apps'), expiry and nonce. nonce is what the token must carry
// (exactly, or, when native, also as its SHA-256 in hex or base64url, as the platform
// sign-in SDKs put it).
func (s *Service) Verify(ctx context.Context, p *registry.Provider, idToken, nonce string, native bool) (Claims, error) {
	if nonce == "" {
		return Claims{}, fmt.Errorf("%w: no nonce to check", ErrInvalidToken)
	}
	c, err := s.check(ctx, p, idToken, native)
	if err != nil {
		return Claims{}, err
	}
	bad := func(why string) (Claims, error) { return Claims{}, fmt.Errorf("%w: %s", ErrInvalidToken, why) }
	if !nonceOK(c.Nonce, nonce, native) {
		return bad("nonce")
	}
	out := Claims{Subject: c.Subject, Email: strings.TrimSpace(c.Email), EmailVerified: bool(c.EmailVerified), Name: c.Name, GivenName: c.GivenName, FamilyName: c.FamilyName, Picture: c.Picture}
	if p.Name == registry.ProviderMicrosoft {
		// The tenant and the object id name the person; an address in a token is
		// whatever the tenant's administrator set, so it is never taken as verified.
		if c.TenantID == "" || c.ObjectID == "" {
			return bad("no tenant or object id")
		}
		out.Subject, out.EmailVerified = c.TenantID+":"+c.ObjectID, false
	}
	if out.Subject == "" {
		return bad("no subject")
	}
	for _, a := range p.Audiences(native) {
		if audienceOK(c.Audience, []string{a}) {
			out.Audience = a
			break
		}
	}
	return out, nil
}

// Subject verifies an ID token like Verify, except for the nonce (the one the code exchange
// gives back has none), and returns who it is for: the subject and the client id it was issued
// to. It is how backd checks that an authorization code and the ID token a native app sent
// belong to the same person.
func (s *Service) Subject(ctx context.Context, p *registry.Provider, idToken string, native bool) (subject, audience string, err error) {
	c, err := s.check(ctx, p, idToken, native)
	if err != nil {
		return "", "", err
	}
	for _, a := range p.Audiences(native) {
		if audienceOK(c.Audience, []string{a}) {
			return c.Subject, a, nil
		}
	}
	return "", "", fmt.Errorf("%w: audience", ErrInvalidToken)
}

// check verifies an ID token's signature, expiry, issuer and audience.
func (s *Service) check(ctx context.Context, p *registry.Provider, idToken string, native bool) (idClaims, error) {
	bad := func(why string) (idClaims, error) { return idClaims{}, fmt.Errorf("%w: %s", ErrInvalidToken, why) }
	jws, err := jose.ParseSigned(idToken, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil || len(jws.Signatures) != 1 {
		return bad("not a signed token")
	}
	payload, err := s.verifySignature(ctx, p, jws, jws.Signatures[0].Header.KeyID)
	if err != nil {
		return idClaims{}, err
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return bad("unreadable claims")
	}
	now := s.now()
	switch {
	case c.Expires == 0 || now.After(time.Unix(int64(c.Expires), 0).Add(clockSkew)):
		return bad("expired")
	case c.NotBefore != 0 && now.Add(clockSkew).Before(time.Unix(int64(c.NotBefore), 0)):
		return bad("not valid yet")
	case c.IssuedAt != 0 && now.Add(clockSkew).Before(time.Unix(int64(c.IssuedAt), 0)):
		return bad("issued in the future")
	}
	if !s.issuerOK(p, c) {
		return bad("issuer")
	}
	if !audienceOK(c.Audience, p.Audiences(native)) {
		return bad("audience")
	}
	return c, nil
}

func (s *Service) issuerOK(p *registry.Provider, c idClaims) bool {
	switch p.Name {
	case registry.ProviderGoogle:
		return c.Issuer == "https://accounts.google.com" || c.Issuer == "accounts.google.com"
	case registry.ProviderApple:
		return c.Issuer == "https://appleid.apple.com"
	case registry.ProviderMicrosoft:
		if c.TenantID == "" || c.Issuer != "https://login.microsoftonline.com/"+c.TenantID+"/v2.0" {
			return false
		}
		switch p.Tenant {
		case "common":
			return true
		case "organizations":
			return c.TenantID != microsoftMSA
		case "consumers":
			return c.TenantID == microsoftMSA
		default:
			return strings.EqualFold(c.TenantID, p.Tenant)
		}
	}
	return false
}

func audienceOK(got audience, want []string) bool {
	for _, g := range got {
		for _, w := range want {
			if subtle.ConstantTimeCompare([]byte(g), []byte(w)) == 1 {
				return true
			}
		}
	}
	return false
}

func nonceOK(got, want string, native bool) bool {
	if got == "" {
		return false
	}
	candidates := []string{want}
	if native {
		sum := sha256.Sum256([]byte(want))
		candidates = append(candidates, hex.EncodeToString(sum[:]), base64.RawURLEncoding.EncodeToString(sum[:]))
	}
	for _, c := range candidates {
		if subtle.ConstantTimeCompare([]byte(got), []byte(c)) == 1 {
			return true
		}
	}
	return false
}

// verifySignature checks the token's signature with the key the provider published
// under kid, fetching the keys when they are not cached, and again (at most once a
// minute) when the key is not among them.
func (s *Service) verifySignature(ctx context.Context, p *registry.Provider, jws *jose.JSONWebSignature, kid string) ([]byte, error) {
	url := s.endpoints(p).JWKS
	for attempt := 0; attempt < 2; attempt++ {
		keys, err := s.keys(ctx, url, attempt > 0)
		if err != nil {
			return nil, err
		}
		for _, k := range keys.Key(kid) {
			if !k.Valid() || !k.IsPublic() || (k.Use != "" && k.Use != "sig") {
				continue
			}
			if payload, err := jws.Verify(k.Key); err == nil {
				return payload, nil
			}
		}
		if len(keys.Key(kid)) > 0 {
			break // the key is known and the signature is not its
		}
	}
	return nil, fmt.Errorf("%w: signature", ErrInvalidToken)
}

// keys returns the provider's published keys. force asks for a fresh copy, which is
// given only when the cached one is at least a minute old.
func (s *Service) keys(ctx context.Context, url string, force bool) (jose.JSONWebKeySet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if e := s.jwks[url]; e != nil {
		if (!force && now.Before(e.expires)) || (force && now.Sub(e.fetchedAt) < refetchEvery) {
			return e.keys, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return jose.JSONWebKeySet{}, err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return s.staleKeys(url, err)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	var set jose.JSONWebKeySet
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("status %d", resp.StatusCode)
	}
	if err == nil {
		err = json.Unmarshal(data, &set)
	}
	if err != nil {
		return s.staleKeys(url, err)
	}
	if s.jwks == nil {
		s.jwks = map[string]*jwksEntry{}
	}
	s.jwks[url] = &jwksEntry{keys: set, fetchedAt: now, expires: now.Add(cacheTTL(resp.Header.Get("Cache-Control")))}
	return set, nil
}

// staleKeys falls back on the keys already held when the provider can't be reached.
func (s *Service) staleKeys(url string, cause error) (jose.JSONWebKeySet, error) {
	if e := s.jwks[url]; e != nil {
		return e.keys, nil
	}
	return jose.JSONWebKeySet{}, fmt.Errorf("%w: the signing keys: %v", ErrUnavailable, cause)
}

// cacheTTL reads max-age from a Cache-Control header, within sane limits.
func cacheTTL(h string) time.Duration {
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(k, "max-age") {
			if n, err := strconv.Atoi(v); err == nil {
				return min(max(time.Duration(n)*time.Second, minJWKSTTL), maxJWKSTTL)
			}
		}
	}
	return defaultTTL
}
