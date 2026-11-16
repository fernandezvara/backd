package sso_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/oidc"
	"github.com/fernandezvara/backd/sso/pkce"
	"github.com/fernandezvara/backd/sso/protocol"
	"github.com/fernandezvara/backd/sso/ssotest"
)

// keycloak is a generic OpenID Connect provider whose issuer is the fake.
func keycloak(idp *ssotest.Provider) sso.Provider {
	return sso.Provider{Name: "keycloak", Kind: sso.OIDC, Method: "keycloak", Issuer: idp.URL, ClientID: "backd", Secret: "s3cret", Hosts: []string{idp.Host()}}
}

func discoverer(idp *ssotest.Provider, dev bool) *oidc.Discoverer {
	return &oidc.Discoverer{HTTP: idp.Client(), Dev: dev}
}

func TestDiscoveryReadsTheEndpointsOfTheIssuer(t *testing.T) {
	idp := ssotest.New(t)
	doc, err := discoverer(idp, true).Discover(context.Background(), idp.URL, false)
	if err != nil || doc.Authorization != idp.URL+"/authorize" || doc.Token != idp.URL+"/token" || doc.JWKS != idp.URL+"/keys" || len(doc.Hosts) != 1 || doc.Hosts[0] != idp.Host() {
		t.Fatalf("doc = %+v, %v", doc, err)
	}
}

func TestDiscoveryRefusesWhatTheOperatorDidNotName(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		over map[string]any
		dev  bool
		want string
	}{
		"another issuer":                {map[string]any{"issuer": "https://evil.example"}, true, "names the issuer"},
		"an endpoint on a foreign host": {map[string]any{"token_endpoint": "http://localhost:1/token"}, true, "outside the issuer's domain"},
		"jwks on another domain":        {map[string]any{"jwks_uri": "https://keys.evil.example/jwks"}, true, "outside the issuer's domain"},
		"no S256":                       {map[string]any{"code_challenge_methods_supported": []string{"plain"}}, true, "S256"},
		"no token endpoint":             {map[string]any{"token_endpoint": nil}, true, "no token_endpoint"},
		"http without dev":              {nil, false, "must be an https URL"},
	} {
		t.Run(name, func(t *testing.T) {
			idp := ssotest.New(t)
			over := tc.over
			for k, v := range over {
				if v == nil {
					over[k] = ""
				}
			}
			idp.Discovery(over)
			_, err := discoverer(idp, tc.dev).Discover(ctx, idp.URL, false)
			if !errors.Is(err, sso.ErrUnavailable) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	for _, bad := range []string{"https://user:pw@sso.example", "https://sso.example/?x=1", "https://sso.example/#f", "ftp://sso.example", "sso.example", ""} {
		if oidc.IssuerProblem(bad, false) == "" {
			t.Errorf("issuer %q was accepted", bad)
		}
	}
	if oidc.IssuerProblem("http://localhost:8080/realms/x", true) != "" || oidc.IssuerProblem("https://sso.example.com/realms/acme", false) != "" {
		t.Error("a good issuer was refused")
	}
}

func TestDiscoveryIsCachedAndSurvivesAnOutage(t *testing.T) {
	var hits atomic.Int32
	var down atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Cache-Control", "max-age=600")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t", "jwks_uri": srv.URL + "/k"})
	}))
	defer srv.Close()
	now := atomicNow()
	d := &oidc.Discoverer{HTTP: ssotest.ClientFor(srv), Dev: true, Now: now.get}
	for range 3 {
		if _, err := d.Discover(context.Background(), srv.URL, false); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("fetched %d times for three discoveries", hits.Load())
	}
	// Past the lifetime, with the issuer down: the known document keeps working.
	now.add(2 * time.Hour)
	down.Store(true)
	if doc, err := d.Discover(context.Background(), srv.URL, false); err != nil || doc.Token != srv.URL+"/t" {
		t.Errorf("during an outage: %+v, %v", doc, err)
	}
	// A new issuer that is down is unavailable.
	if _, err := (&oidc.Discoverer{HTTP: ssotest.ClientFor(srv), Dev: true}).Discover(context.Background(), srv.URL, false); !errors.Is(err, sso.ErrUnavailable) {
		t.Errorf("never reached: %v", err)
	}
}

func TestGenericTokensFollowTheRules(t *testing.T) {
	idp := ssotest.New(t)
	pc := (&protocol.Client{HTTP: idp.Client(), Dev: true}).Init()
	ctx := context.Background()
	p, err := pc.Prepare(ctx, keycloak(idp))
	if err != nil || p.Endpoints.JWKS != idp.URL+"/keys" {
		t.Fatalf("Prepare = %+v, %v", p, err)
	}
	good := map[string]any{"iss": idp.URL, "aud": "backd", "sub": "kc-1", "email": "ada@example.com", "email_verified": true, "nonce": "n"}
	with := func(over map[string]any) map[string]any {
		c := map[string]any{}
		for k, v := range good {
			c[k] = v
		}
		for k, v := range over {
			c[k] = v
		}
		return c
	}
	if c, err := pc.Verify(ctx, p, idp.IDToken(t, good), "n", false); err != nil || c.Subject != "kc-1" || c.Issuer != idp.URL || !c.EmailVerified {
		t.Fatalf("a good token: %+v, %v", c, err)
	}
	// PS256 and EdDSA are allowed; none and HS256 never are, whatever the key.
	if _, err := pc.Verify(ctx, p, idp.SignAs(t, "PS256", good, nil), "n", false); err != nil {
		t.Errorf("PS256: %v", err)
	}
	for _, alg := range []string{"none", "HS256"} {
		if _, err := pc.Verify(ctx, p, idp.SignAs(t, alg, good, []byte("0123456789abcdef0123456789abcdef")), "n", false); !errors.Is(err, sso.ErrInvalidToken) {
			t.Errorf("%s: %v", alg, err)
		}
	}
	for name, claims := range map[string]map[string]any{
		"another issuer":                 with(map[string]any{"iss": "https://evil.example"}),
		"another audience":               with(map[string]any{"aud": "other"}),
		"several audiences, no azp":      with(map[string]any{"aud": []string{"backd", "other"}}),
		"several audiences, another azp": with(map[string]any{"aud": []string{"backd", "other"}, "azp": "other"}),
	} {
		if _, err := pc.Verify(ctx, p, idp.IDToken(t, claims), "n", false); !errors.Is(err, sso.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := pc.Verify(ctx, p, idp.IDToken(t, with(map[string]any{"aud": []string{"other", "backd"}, "azp": "backd"})), "n", false); err != nil {
		t.Errorf("several audiences with azp: %v", err)
	}
	// An address is verified only when the provider says so, as a boolean or as the string "true".
	for v, want := range map[any]bool{true: true, "true": true, false: false, "false": false, nil: false} {
		claims := with(map[string]any{"email_verified": v})
		if v == nil {
			delete(claims, "email_verified")
		}
		if c, err := pc.Verify(ctx, p, idp.IDToken(t, claims), "n", false); err != nil || c.EmailVerified != want {
			t.Errorf("email_verified %v: %+v, %v", v, c, err)
		}
	}
	// A token over 16 KiB is refused before it is parsed.
	if _, err := pc.Verify(ctx, p, idp.IDToken(t, with(map[string]any{"pad": strings.Repeat("x", 20<<10)})), "n", false); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("a huge token: %v", err)
	}
}

// A generic provider through the whole engine: start, the provider, the callback, a login code.
func TestGenericProviderSignsPeopleIn(t *testing.T) {
	idp := ssotest.New(t)
	pc := (&protocol.Client{HTTP: idp.Client(), Dev: true}).Init()
	dir := &ssotest.Directory{}
	st := &ssotest.MemStore{}
	cat := &ssotest.Catalog{Providers: map[string]map[string]sso.Provider{"realm": {"keycloak": keycloak(idp)}}}
	e := &sso.Engine{Store: st, Catalog: cat, Directory: dir, Events: &ssotest.Recorder{}, Protocol: pc}
	ctx := context.Background()
	verifier, challenge, _ := pkce.New()

	run := func(user map[string]any, iss string) sso.Outcome {
		t.Helper()
		idp.SetUser(user)
		u, err := e.Start(ctx, sso.StartInput{Scope: "realm", Provider: keycloak(idp), CallbackURL: "https://api.example/cb", Intent: sso.IntentSignIn, Challenge: challenge, RedirectTo: "https://app.example/home"})
		if err != nil {
			t.Fatal(err)
		}
		back, _ := url.Parse(idp.Authorize(u))
		in := sso.CallbackInput{Scope: "realm", Provider: "keycloak", State: back.Query().Get("state"), Code: back.Query().Get("code"), Issuer: iss}
		return e.Callback(ctx, in)
	}
	user := map[string]any{"iss": idp.URL, "aud": "backd", "sub": "kc-1", "email": "ada@example.com", "email_verified": true}
	out := run(user, idp.URL)
	if p := params(out); p[sso.ParamCode] == "" {
		t.Fatalf("outcome = %+v", out)
	}
	// The user was made and is not verified: a generic provider doesn't vouch for addresses.
	if len(dir.Created) != 1 || dir.Created[0].EmailVerified {
		t.Errorf("created = %+v", dir.Created)
	}
	// The identity records the issuer.
	if _, err := dir.Identity(ctx, sso.IdentityKey{Provider: "keycloak", Issuer: idp.URL, Subject: "kc-1"}); err != nil {
		t.Errorf("the identity under its issuer: %v", err)
	}
	// The authorization server named on the callback must be the one the person was sent to.
	refused := run(map[string]any{"iss": idp.URL, "aud": "backd", "sub": "kc-2", "email": "eve@example.com", "email_verified": true}, "https://evil.example")
	if params(refused)[sso.ParamError] != sso.CodeProviderError || params(refused)[sso.ParamCode] != "" {
		t.Errorf("a callback naming another issuer: %+v", refused)
	}
	if c, err := e.Redeem(ctx, params(out)[sso.ParamCode], verifier); err != nil || c.Method != "keycloak" {
		t.Errorf("the good sign-in's code: %+v, %v", c, err)
	}
	// An unreachable issuer ends the start as unavailable.
	bad := keycloak(idp)
	bad.Issuer = "http://127.0.0.1:1"
	bad.Hosts = []string{"127.0.0.1"}
	if _, err := e.Start(ctx, sso.StartInput{Scope: "realm", Provider: bad, CallbackURL: "x", Intent: sso.IntentSignIn, Challenge: challenge, RedirectTo: "https://app.example/home"}); !errors.Is(err, sso.ErrUnavailable) {
		t.Errorf("an unreachable issuer: %v", err)
	}
}
