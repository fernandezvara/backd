package sso_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/oauth2"
	"github.com/fernandezvara/backd/sso/pkce"
	"github.com/fernandezvara/backd/sso/protocol"
	"github.com/fernandezvara/backd/sso/ssotest"
)

func githubProvider(o *ssotest.OAuth2) sso.Provider {
	return sso.Provider{Name: "github", Kind: sso.GitHub, Method: "github", Issuer: "https://github.com", ClientID: "Iv1.abc", Secret: "gh-secret", Endpoints: o.Endpoints(), Hosts: []string{o.Host()}, Link: sso.LinkVerifiedEmail}
}

func partnerProvider(o *ssotest.OAuth2) sso.Provider {
	e := o.Endpoints()
	e.Emails = ""
	return sso.Provider{
		Name: "partner", Kind: sso.OAuth2, Method: "partner", Issuer: "https://partner.example", ClientID: "backd", Secret: "p-secret", Endpoints: e, Hosts: []string{o.Host()},
		Scopes: []string{"profile", "email"},
		Claims: map[string]string{"subject": "id", "email": "mail", "email_verified": "mail_ok", "name": "display", "picture": "img", "given_name": "profile.first"},
	}
}

func authorizeQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestAuthorizeURLsOfTheOAuth2Kinds(t *testing.T) {
	o := ssotest.NewOAuth2(t)
	c := &oauth2.Client{}
	q := authorizeQuery(t, c.AuthorizeURL(githubProvider(o), oauth2.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "n", CodeChallenge: "ch"}))
	if q.Get("scope") != "read:user user:email" || q.Has("nonce") || q.Get("code_challenge") != "ch" || q.Get("state") != "s" || q.Get("client_id") != "Iv1.abc" {
		t.Errorf("github: %v", q)
	}
	q = authorizeQuery(t, c.AuthorizeURL(partnerProvider(o), oauth2.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", Nonce: "n", CodeChallenge: "ch"}))
	if q.Get("scope") != "profile email" || q.Has("nonce") || q.Get("code_challenge") != "ch" {
		t.Errorf("partner: %v", q)
	}
	p := partnerProvider(o)
	p.NoPKCE = true
	if q = authorizeQuery(t, c.AuthorizeURL(p, oauth2.AuthorizeParams{RedirectURI: "x", State: "s", CodeChallenge: "ch"})); q.Has("code_challenge") {
		t.Errorf("pkce off: %v", q)
	}
}

func TestGitHubPeopleAreTheirNumericIdAndPrimaryVerifiedAddress(t *testing.T) {
	o := ssotest.NewOAuth2(t)
	pc := (&protocol.Client{HTTP: o.Client(), Dev: true}).Init()
	ctx := context.Background()
	p := githubProvider(o)
	exchange := func() sso.Tokens {
		t.Helper()
		code := authorizeQuery(t, o.Authorize(pc.AuthorizeURL(p, sso.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s", CodeChallenge: "ch"}))).Get("code")
		tok, err := pc.Exchange(ctx, p, code, "https://api.example/cb", "verifier")
		if err != nil || tok.AccessToken == "" || tok.IDToken != "" {
			t.Fatalf("exchange = %+v, %v", tok, err)
		}
		return tok
	}
	o.SetUser(map[string]any{"id": 583231, "login": "octocat", "name": "The Octocat", "avatar_url": "https://avatars.example/u/583231"})
	o.SetEmails([]map[string]any{
		{"email": "secondary@example.com", "primary": false, "verified": true},
		{"email": "octo@example.com", "primary": true, "verified": true},
	})
	c, err := pc.Identify(ctx, p, exchange(), "")
	if err != nil || c.Subject != "583231" || c.Email != "octo@example.com" || !c.EmailVerified || c.Name != "The Octocat" || c.Issuer != "https://github.com" || c.Picture == "" {
		t.Fatalf("claims = %+v, %v", c, err)
	}
	// The login is renamed and reused by someone else: the subject is still the numeric id.
	o.SetUser(map[string]any{"id": 583231, "login": "renamed"})
	if c, _ := pc.Identify(ctx, p, exchange(), ""); c.Subject != "583231" {
		t.Errorf("after a rename: %+v", c)
	}
	// A primary address that is not verified, or no primary at all: no address, never the unverified one.
	for name, list := range map[string][]map[string]any{
		"primary unverified": {{"email": "a@example.com", "primary": true, "verified": false}, {"email": "b@example.com", "primary": false, "verified": true}},
		"none":               {},
	} {
		o.SetEmails(list)
		if c, err := pc.Identify(ctx, p, exchange(), ""); err != nil || c.Email != "" || c.EmailVerified {
			t.Errorf("%s: %+v, %v", name, c, err)
		}
	}
	// A user with no id is refused; a refused token is invalid, an outage unavailable.
	o.SetUser(map[string]any{"login": "x"})
	if _, err := pc.Identify(ctx, p, exchange(), ""); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("no id: %v", err)
	}
	o.SetUser(map[string]any{"id": 1.5})
	if _, err := pc.Identify(ctx, p, exchange(), ""); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("a fractional id: %v", err)
	}
	o.FailUser(500)
	if _, err := pc.Identify(ctx, p, exchange(), ""); !errors.Is(err, sso.ErrUnavailable) {
		t.Errorf("outage: %v", err)
	}
	o.FailUser(401)
	if _, err := pc.Identify(ctx, p, exchange(), ""); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("refused: %v", err)
	}
	if _, err := pc.Identify(ctx, p, sso.Tokens{}, ""); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("no access token: %v", err)
	}
}

func TestOAuth2ExchangeErrors(t *testing.T) {
	o := ssotest.NewOAuth2(t)
	pc := (&protocol.Client{HTTP: o.Client(), Dev: true}).Init()
	ctx := context.Background()
	p := githubProvider(o)
	// GitHub answers a bad code with 200 and an error.
	if _, err := pc.Exchange(ctx, p, "nope", "https://api.example/cb", "v"); !errors.Is(err, sso.ErrExchange) {
		t.Errorf("a bad code: %v", err)
	}
	o.RefuseWith200("bad_verification_code")
	if _, err := pc.Exchange(ctx, p, "x", "https://api.example/cb", "v"); !errors.Is(err, sso.ErrExchange) {
		t.Errorf("a 200 with an error: %v", err)
	}
	o.RefuseWith200("")
	o.FailToken(500, "server_error")
	if _, err := pc.Exchange(ctx, p, "x", "https://api.example/cb", "v"); !errors.Is(err, sso.ErrUnavailable) {
		t.Errorf("an outage: %v", err)
	}
}

func TestPlainOAuth2ReadsTheConfiguredFields(t *testing.T) {
	o := ssotest.NewOAuth2(t)
	pc := (&protocol.Client{HTTP: o.Client(), Dev: true}).Init()
	ctx := context.Background()
	p := partnerProvider(o)
	who := func(user map[string]any) (sso.Claims, error) {
		t.Helper()
		o.SetUser(user)
		code := authorizeQuery(t, o.Authorize(pc.AuthorizeURL(p, sso.AuthorizeParams{RedirectURI: "https://api.example/cb", State: "s"}))).Get("code")
		tok, err := pc.Exchange(ctx, p, code, "https://api.example/cb", "")
		if err != nil {
			t.Fatal(err)
		}
		return pc.Identify(ctx, p, tok, "")
	}
	c, err := who(map[string]any{"id": "u-77", "mail": "ada@partner.example", "mail_ok": true, "display": "Ada", "img": "https://i.example/a.png", "profile": map[string]any{"first": "Ada"}})
	if err != nil || c.Subject != "u-77" || c.Email != "ada@partner.example" || !c.EmailVerified || c.Name != "Ada" || c.GivenName != "Ada" || c.Issuer != "https://partner.example" {
		t.Fatalf("claims = %+v, %v", c, err)
	}
	// The verified flag counts only when it is true; "true" the string is accepted.
	for v, want := range map[any]bool{true: true, "true": true, false: false, "yes": false, nil: false} {
		user := map[string]any{"id": 1, "mail": "a@partner.example"}
		if v != nil {
			user["mail_ok"] = v
		}
		if c, err := who(user); err != nil || c.EmailVerified != want {
			t.Errorf("mail_ok %v: %+v, %v", v, c, err)
		}
	}
	// A numeric id is read as written; no subject is refused.
	if c, _ := who(map[string]any{"id": json.Number("9007199254740993")}); c.Subject != "9007199254740993" {
		t.Errorf("a large id: %q", c.Subject)
	}
	if _, err := who(map[string]any{"mail": "a@partner.example"}); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("no subject: %v", err)
	}
	// Without a mapped verified flag an address is never verified.
	p.Claims = map[string]string{"subject": "id", "email": "mail"}
	if c, err := who(map[string]any{"id": 3, "mail": "a@partner.example", "mail_ok": true}); err != nil || c.EmailVerified {
		t.Errorf("no verified flag mapped: %+v, %v", c, err)
	}
}

// GitHub through the whole engine, with the account decision the design gives it (Google's).
func TestGitHubSignsPeopleIn(t *testing.T) {
	o := ssotest.NewOAuth2(t)
	pc := (&protocol.Client{HTTP: o.Client(), Dev: true}).Init()
	dir := &ssotest.Directory{}
	dir.AddUser(sso.Account{ID: "ada", Email: "ada@example.com", EmailVerified: true})
	e := &sso.Engine{Store: &ssotest.MemStore{}, Catalog: &ssotest.Catalog{Providers: map[string]map[string]sso.Provider{"realm": {"github": githubProvider(o)}}}, Directory: dir, Events: &ssotest.Recorder{}, Protocol: pc}
	ctx := context.Background()
	verifier, challenge, _ := pkce.New()
	run := func(user map[string]any, emails []map[string]any) sso.Outcome {
		t.Helper()
		o.SetUser(user)
		o.SetEmails(emails)
		u, err := e.Start(ctx, sso.StartInput{Scope: "realm", Provider: githubProvider(o), CallbackURL: "https://api.example/cb", Intent: sso.IntentSignIn, Challenge: challenge, RedirectTo: "https://app.example/home"})
		if err != nil {
			t.Fatal(err)
		}
		back, _ := url.Parse(o.Authorize(u))
		return e.Callback(ctx, sso.CallbackInput{Scope: "realm", Provider: "github", State: back.Query().Get("state"), Code: back.Query().Get("code")})
	}
	// A verified primary address that an account has: linked, like Google.
	out := run(map[string]any{"id": 1, "login": "ada"}, []map[string]any{{"email": "ada@example.com", "primary": true, "verified": true}})
	c, err := e.Redeem(ctx, params(out)[sso.ParamCode], verifier)
	if err != nil || c.UserID != "ada" || c.NewUser {
		t.Fatalf("linked sign-in: %+v %+v, %v", out, c, err)
	}
	// A new person with a verified address is made, verified.
	out = run(map[string]any{"id": 2, "login": "new"}, []map[string]any{{"email": "new@example.com", "primary": true, "verified": true}})
	if len(dir.Created) != 1 || !dir.Created[0].EmailVerified || params(out)[sso.ParamCode] == "" {
		t.Errorf("new: %+v %+v", out, dir.Created)
	}
	// No verified primary address: email_required, nobody is made.
	out = run(map[string]any{"id": 3, "login": "noemail"}, nil)
	if params(out)[sso.ParamError] != sso.CodeEmailRequired || len(dir.Created) != 1 {
		t.Errorf("no address: %+v", out)
	}
	// The access token is not kept anywhere: the events and the code record carry none.
	for _, ev := range e.Events.(*ssotest.Recorder).Events {
		if strings.Contains(ev.Token, "at-") {
			t.Error("an access token was passed on")
		}
	}
}
