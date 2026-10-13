package oauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/fernandezvara/backd/internal/oauth"
	"github.com/fernandezvara/backd/internal/oauth/oauthtest"
	"github.com/fernandezvara/backd/internal/registry"
)

var (
	google    = &registry.Provider{Name: registry.ProviderGoogle, ClientID: "g-client", ClientSecret: "G", NativeClientIDs: []string{"g-native"}}
	microsoft = &registry.Provider{Name: registry.ProviderMicrosoft, ClientID: "00000000-0000-0000-0000-000000000001", ClientSecret: "M", Tenant: "common"}
	apple     = &registry.Provider{Name: registry.ProviderApple, ClientID: "com.acme.web", NativeClientIDs: []string{"com.acme.ios"}, TeamID: "ABCDE12345", KeyID: "XYZ9876543", PrivateKey: "A"}
)

func googleClaims(extra map[string]any) map[string]any {
	c := map[string]any{"iss": "https://accounts.google.com", "aud": "g-client", "sub": "g-123", "email": "ada@example.com", "email_verified": true}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func verify(t *testing.T, p *oauthtest.Provider, prov *registry.Provider, claims map[string]any, nonce string, native bool) (oauth.Claims, error) {
	t.Helper()
	return p.Service().Verify(context.Background(), prov, p.IDToken(t, claims), nonce, native)
}

func TestVerifyGoogle(t *testing.T) {
	p := oauthtest.New(t)
	c, err := verify(t, p, google, googleClaims(map[string]any{"nonce": "n1", "name": "Ada L", "given_name": "Ada", "picture": "https://x/p.png"}), "n1", false)
	if err != nil || c.Subject != "g-123" || c.Email != "ada@example.com" || !c.EmailVerified || c.GivenName != "Ada" || c.Picture != "https://x/p.png" {
		t.Fatalf("claims = %+v, %v", c, err)
	}
	// The older issuer form is Google's too, and an audience may be a list.
	if _, err := verify(t, p, google, googleClaims(map[string]any{"nonce": "n1", "iss": "accounts.google.com", "aud": []string{"other", "g-client"}}), "n1", false); err != nil {
		t.Errorf("issuer without scheme, audience list: %v", err)
	}
	for name, claims := range map[string]map[string]any{
		"another issuer":    googleClaims(map[string]any{"nonce": "n1", "iss": "https://evil.example"}),
		"another audience":  googleClaims(map[string]any{"nonce": "n1", "aud": "someone-else"}),
		"the native client": googleClaims(map[string]any{"nonce": "n1", "aud": "g-native"}),
		"expired":           googleClaims(map[string]any{"nonce": "n1", "exp": time.Now().Add(-time.Hour).Unix()}),
		"not yet valid":     googleClaims(map[string]any{"nonce": "n1", "nbf": time.Now().Add(time.Hour).Unix()}),
		"issued later":      googleClaims(map[string]any{"nonce": "n1", "iat": time.Now().Add(time.Hour).Unix()}),
		"another nonce":     googleClaims(map[string]any{"nonce": "n2"}),
		"no nonce":          googleClaims(nil),
		"no subject":        googleClaims(map[string]any{"nonce": "n1", "sub": ""}),
	} {
		if _, err := verify(t, p, google, claims, "n1", false); !errors.Is(err, oauth.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An empty nonce to check is never a pass.
	if _, err := verify(t, p, google, googleClaims(map[string]any{"nonce": ""}), "", false); !errors.Is(err, oauth.ErrInvalidToken) {
		t.Errorf("empty nonce: %v", err)
	}
	// A token signed by someone else's key, and garbage.
	other := oauthtest.New(t)
	if _, err := p.Service().Verify(context.Background(), google, other.IDToken(t, googleClaims(map[string]any{"nonce": "n1"})), "n1", false); !errors.Is(err, oauth.ErrInvalidToken) {
		t.Errorf("a token of another key: %v", err)
	}
	if _, err := p.Service().Verify(context.Background(), google, "not.a.token", "n1", false); !errors.Is(err, oauth.ErrInvalidToken) {
		t.Errorf("garbage: %v", err)
	}
}

func TestVerifyNativeNonceAndAudiences(t *testing.T) {
	p := oauthtest.New(t)
	sum := sha256.Sum256([]byte("raw-nonce"))
	for name, nonce := range map[string]string{"raw": "raw-nonce", "hex": hex.EncodeToString(sum[:]), "base64url": base64.RawURLEncoding.EncodeToString(sum[:])} {
		if _, err := verify(t, p, google, googleClaims(map[string]any{"nonce": nonce, "aud": "g-native"}), "raw-nonce", true); err != nil {
			t.Errorf("native, nonce as %s: %v", name, err)
		}
	}
	// The hashed form is only for native apps: the web flow compares exactly.
	if _, err := verify(t, p, google, googleClaims(map[string]any{"nonce": hex.EncodeToString(sum[:])}), "raw-nonce", false); !errors.Is(err, oauth.ErrInvalidToken) {
		t.Errorf("web flow with a hashed nonce: %v", err)
	}
	if _, err := verify(t, p, google, googleClaims(map[string]any{"nonce": "raw-nonce", "aud": "g-native"}), "raw-nonce", false); !errors.Is(err, oauth.ErrInvalidToken) {
		t.Errorf("a native audience in the web flow: %v", err)
	}
}

func TestVerifyMicrosoftTenants(t *testing.T) {
	p := oauthtest.New(t)
	tid := "11111111-2222-3333-4444-555555555555"
	claims := func(tid string, extra map[string]any) map[string]any {
		c := map[string]any{"iss": "https://login.microsoftonline.com/" + tid + "/v2.0", "aud": microsoft.ClientID, "sub": "pairwise", "tid": tid, "oid": "obj-1", "email": "ada@corp.example", "nonce": "n"}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	at := func(tenant string) *registry.Provider { m := *microsoft; m.Tenant = tenant; return &m }
	c, err := verify(t, p, at("common"), claims(tid, nil), "n", false)
	if err != nil || c.Subject != tid+":obj-1" || c.EmailVerified {
		t.Fatalf("common: %+v, %v", c, err)
	}
	// An address is never verified, whatever the token says.
	if c, _ := verify(t, p, at("common"), claims(tid, map[string]any{"email_verified": true}), "n", false); c.EmailVerified {
		t.Error("a Microsoft address was taken as verified")
	}
	msa := "9188040d-6c67-4c5b-b112-36a304b66dad"
	for name, tc := range map[string]struct {
		tenant, tid string
		ok          bool
	}{
		"specific tenant, same":     {tid, tid, true},
		"specific tenant, other":    {tid, "99999999-2222-3333-4444-555555555555", false},
		"organizations, a company":  {"organizations", tid, true},
		"organizations, a personal": {"organizations", msa, false},
		"consumers, a personal":     {"consumers", msa, true},
		"consumers, a company":      {"consumers", tid, false},
	} {
		_, err := verify(t, p, at(tc.tenant), claims(tc.tid, nil), "n", false)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The issuer must name the token's own tenant, and the tenant and object ids are needed.
	if _, err := verify(t, p, at("common"), claims(tid, map[string]any{"iss": "https://login.microsoftonline.com/other/v2.0"}), "n", false); err == nil {
		t.Error("an issuer of another tenant")
	}
	if _, err := verify(t, p, at("common"), claims(tid, map[string]any{"oid": ""}), "n", false); err == nil {
		t.Error("no object id")
	}
}

func TestVerifyApple(t *testing.T) {
	p := oauthtest.New(t)
	claims := map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.web", "sub": "a-1", "email": "x@privaterelay.appleid.com", "email_verified": "true", "nonce": "n"}
	c, err := verify(t, p, apple, claims, "n", false)
	if err != nil || c.Subject != "a-1" || !c.EmailVerified {
		t.Fatalf("apple: %+v, %v", c, err)
	}
	claims["aud"] = "com.acme.ios"
	if _, err := verify(t, p, apple, claims, "n", true); err != nil {
		t.Errorf("a bundle id as audience, natively: %v", err)
	}
	if _, err := verify(t, p, apple, claims, "n", false); err == nil {
		t.Error("a bundle id as audience, in the web flow")
	}
}

func TestKeysAreCachedAndRefetchedOnlyOnce(t *testing.T) {
	p := oauthtest.New(t)
	now := time.Now()
	svc := p.Service()
	svc.Now = func() time.Time { return now }
	tok := func() string {
		return p.IDToken(t, googleClaims(map[string]any{"nonce": "n", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}))
	}
	for range 3 {
		if _, err := svc.Verify(context.Background(), google, tok(), "n", false); err != nil {
			t.Fatal(err)
		}
	}
	if p.KeyFetches() != 1 {
		t.Errorf("keys fetched %d times for 3 verifications", p.KeyFetches())
	}
	// A key it doesn't know: the keys are fetched again, once a minute at most.
	p.RotateKey(t, "key-2")
	now = now.Add(2 * time.Minute)
	if _, err := svc.Verify(context.Background(), google, tok(), "n", false); err != nil {
		t.Fatalf("after the provider rotated its key: %v", err)
	}
	if p.KeyFetches() != 2 {
		t.Errorf("fetches after rotation: %d", p.KeyFetches())
	}
	p.RotateKey(t, "key-3")
	for range 3 {
		if _, err := svc.Verify(context.Background(), google, tok(), "n", false); !errors.Is(err, oauth.ErrInvalidToken) {
			t.Errorf("an unknown key within the minute: %v", err)
		}
	}
	if p.KeyFetches() != 2 {
		t.Errorf("a forged kid made the keys be fetched again: %d", p.KeyFetches())
	}
}

func TestAuthorizeURL(t *testing.T) {
	svc := &oauth.Service{}
	u, _ := url.Parse(svc.AuthorizeURL(google, oauth.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "n", CodeChallenge: "ch"}))
	q := u.Query()
	if u.Host != "accounts.google.com" || q.Get("client_id") != "g-client" || q.Get("scope") != "openid email profile" || q.Get("code_challenge") != "ch" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "s" || q.Get("nonce") != "n" || q.Get("response_mode") != "" {
		t.Errorf("google: %s", u)
	}
	u, _ = url.Parse(svc.AuthorizeURL(apple, oauth.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "n", CodeChallenge: "ch"}))
	q = u.Query()
	if u.Host != "appleid.apple.com" || q.Get("scope") != "name email" || q.Get("response_mode") != "form_post" || q.Has("code_challenge") {
		t.Errorf("apple: %s", u)
	}
	u, _ = url.Parse(svc.AuthorizeURL(microsoft, oauth.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "n"}))
	if u.Host != "login.microsoftonline.com" || !strings.HasPrefix(u.Path, "/common/oauth2/v2.0/authorize") || u.Query().Has("code_challenge") {
		t.Errorf("microsoft: %s", u)
	}
}

func TestExchange(t *testing.T) {
	p := oauthtest.New(t)
	svc := p.Service()
	p.SetUser(googleClaims(nil))
	redirect := p.Authorize(svc.AuthorizeURL(google, oauth.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "nn", CodeChallenge: "ch"}))
	code := mustQuery(t, redirect, "code")
	p.GiveRefreshToken("refresh-1")
	tok, err := svc.Exchange(context.Background(), google, "g-client", "the-secret", code, "https://api.example/cb", "verifier")
	if err != nil || tok.RefreshToken != "refresh-1" {
		t.Fatalf("Exchange = %+v, %v", tok, err)
	}
	if c, err := svc.Verify(context.Background(), google, tok.IDToken, "nn", false); err != nil || c.Subject != "g-123" {
		t.Errorf("the token the exchange gave: %+v, %v", c, err)
	}
	form := p.TokenRequests()[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("client_secret") != "the-secret" || form.Get("client_id") != "g-client" || form.Get("code_verifier") != "verifier" || form.Get("redirect_uri") != "https://api.example/cb" {
		t.Errorf("token request: %v", form)
	}
	// A code works once; the provider's refusal is an ErrExchange; an outage is not.
	if _, err := svc.Exchange(context.Background(), google, "g-client", "s", code, "", ""); !errors.Is(err, oauth.ErrExchange) {
		t.Errorf("code reuse: %v", err)
	}
	p.FailToken(500, "server_error")
	if _, err := svc.Exchange(context.Background(), google, "g-client", "s", "x", "", ""); !errors.Is(err, oauth.ErrUnavailable) {
		t.Errorf("provider outage: %v", err)
	}
	// Apple's web flow sends no PKCE verifier.
	p.FailToken(0, "")
	p.SetUser(map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.web", "sub": "a"})
	code = mustQuery(t, p.Authorize(svc.AuthorizeURL(apple, oauth.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "n"})), "code")
	if _, err := svc.Exchange(context.Background(), apple, "com.acme.web", "jwt", code, "https://api.example/cb", "verifier"); err != nil {
		t.Fatal(err)
	}
	if forms := p.TokenRequests(); forms[len(forms)-1].Has("code_verifier") {
		t.Error("a PKCE verifier was sent to Apple")
	}
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Query().Get(key) == "" {
		t.Fatalf("%q has no %s", raw, key)
	}
	return u.Query().Get(key)
}

func TestAppleClientSecret(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	svc := &oauth.Service{Now: func() time.Time { return now }}
	secret, err := svc.AppleClientSecret(apple, "com.acme.web", pemKey)
	if err != nil {
		t.Fatal(err)
	}
	jws, err := jose.ParseSigned(secret, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := jws.Verify(&key.PublicKey)
	if err != nil || jws.Signatures[0].Header.KeyID != "XYZ9876543" {
		t.Fatalf("verify: %v, kid %q", err, jws.Signatures[0].Header.KeyID)
	}
	for _, want := range []string{`"iss":"ABCDE12345"`, `"sub":"com.acme.web"`, `"aud":"https://appleid.apple.com"`} {
		if !strings.Contains(string(payload), want) {
			t.Errorf("payload %s lacks %s", payload, want)
		}
	}
	// It is reused while it lives, and renewed before it ends.
	if again, _ := svc.AppleClientSecret(apple, "com.acme.web", pemKey); again != secret {
		t.Error("a new secret was signed while the first was good")
	}
	now = now.Add(50 * time.Minute)
	if renewed, _ := svc.AppleClientSecret(apple, "com.acme.web", pemKey); renewed == secret {
		t.Error("the secret was not renewed before it expired")
	}
	for name, bad := range map[string]string{"not PEM": "nope", "an RSA key": rsaPEM(t)} {
		if _, err := svc.AppleClientSecret(apple, "com.acme.web", bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func rsaPEM(t *testing.T) string {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalECPrivateKey(k) // SEC 1, not PKCS #8
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func TestClientDialsOnlyAllowedPublicHosts(t *testing.T) {
	p := oauthtest.New(t)
	// A host that is not the provider's is refused before anything is sent.
	c := oauth.NewClient(func(h string) bool { return h == "accounts.google.com" }, false)
	if _, err := c.Get(p.URL); !errors.Is(err, oauth.ErrHostNotAllowed) {
		t.Errorf("a host outside the list: %v", err)
	}
	// An allowed name that resolves to a private address is refused too.
	c = oauth.NewClient(func(h string) bool { return h == p.Host() }, false)
	if _, err := c.Get(p.URL + "/keys"); !errors.Is(err, oauth.ErrHostNotAllowed) {
		t.Errorf("a loopback address: %v", err)
	}
	// With private addresses allowed (a development stack) it works, and follows no redirect.
	c = oauth.NewClient(func(h string) bool { return h == p.Host() }, true)
	resp, err := c.Get(p.URL + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !oauth.BuiltinHostAllowed("appleid.apple.com") || oauth.BuiltinHostAllowed("evil.example") || oauth.BuiltinHostAllowed("169.254.169.254") {
		t.Error("BuiltinHostAllowed")
	}
}
