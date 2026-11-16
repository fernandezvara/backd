package sso_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/pkce"
	"github.com/fernandezvara/backd/sso/ssotest"
)

// Every row of design/sso.md §8.1.
func TestDecide(t *testing.T) {
	realm := sso.Provider{Name: "google", Kind: sso.Google, Link: sso.LinkVerifiedEmail}
	untrusted := sso.Provider{Name: "microsoft", Kind: sso.Microsoft}
	conn := sso.Provider{Name: "google", Kind: sso.Google, Org: "o1", Link: sso.LinkVerifiedEmail}
	ada := &sso.Account{ID: "u1", Email: "ada@example.com", EmailVerified: true}
	unverified := &sso.Account{ID: "u1", Email: "ada@example.com"}
	me := &sso.Account{ID: "me"}
	for _, tc := range []struct {
		name     string
		p        sso.Provider
		f        sso.Facts
		action   sso.Action
		err      error
		verified bool
	}{
		{"linked identity signs in", realm, sso.Facts{Intent: sso.IntentSignIn, Known: ada}, sso.SignInKnown, nil, false},
		{"trusted provider, verified both sides, links", realm, sso.Facts{Intent: sso.IntentSignIn, ByEmail: ada, Email: "ada@example.com", EmailVerified: true}, sso.LinkToExisting, nil, false},
		{"provider says unverified", realm, sso.Facts{Intent: sso.IntentSignIn, ByEmail: ada, Email: "ada@example.com"}, sso.Refuse, sso.ErrAccountExists, false},
		{"backd account unverified (pre-hijacking)", realm, sso.Facts{Intent: sso.IntentSignIn, ByEmail: unverified, Email: "ada@example.com", EmailVerified: true}, sso.Refuse, sso.ErrAccountExists, false},
		{"untrusted provider", untrusted, sso.Facts{Intent: sso.IntentSignIn, ByEmail: ada, Email: "ada@example.com", EmailVerified: true}, sso.Refuse, sso.ErrAccountExists, false},
		{"a connection never links by email", conn, sso.Facts{Intent: sso.IntentSignIn, ByEmail: ada, Email: "ada@example.com", EmailVerified: true}, sso.Refuse, sso.ErrAccountExists, false},
		{"new person, trusted and verified", realm, sso.Facts{Intent: sso.IntentSignIn, Email: "new@example.com", EmailVerified: true}, sso.Create, nil, true},
		{"new person, untrusted provider", untrusted, sso.Facts{Intent: sso.IntentSignIn, Email: "new@example.com", EmailVerified: true}, sso.Create, nil, false},
		{"new person, provider says unverified", realm, sso.Facts{Intent: sso.IntentSignIn, Email: "new@example.com"}, sso.Create, nil, false},
		{"no email", realm, sso.Facts{Intent: sso.IntentSignIn}, sso.Refuse, sso.ErrEmailRequired, false},
		{"link, identity free", realm, sso.Facts{Intent: sso.IntentLink, LinkUser: me, LinkUserID: "me"}, sso.LinkToSignedIn, nil, false},
		{"link, identity of another user", realm, sso.Facts{Intent: sso.IntentLink, Known: ada, LinkUser: me, LinkUserID: "me"}, sso.Refuse, sso.ErrLinkConflict, false},
		{"link, own identity again", realm, sso.Facts{Intent: sso.IntentLink, Known: &sso.Account{ID: "me"}, LinkUser: me, LinkUserID: "me"}, sso.SignInKnown, nil, false},
		{"link, user gone", realm, sso.Facts{Intent: sso.IntentLink, LinkUserID: "me"}, sso.Refuse, &sso.Refused{}, false},
		{"link, user disabled", realm, sso.Facts{Intent: sso.IntentLink, LinkUser: &sso.Account{ID: "me", Disabled: true}, LinkUserID: "me"}, sso.Refuse, &sso.Refused{}, false},
	} {
		d := sso.Decide(tc.p, tc.f)
		if d.Action != tc.action || d.Verified != tc.verified {
			t.Errorf("%s: %+v", tc.name, d)
			continue
		}
		switch want := tc.err.(type) {
		case nil:
		case *sso.Refused:
			var got *sso.Refused
			if !errors.As(d.Err, &got) {
				t.Errorf("%s: err = %v", tc.name, d.Err)
			}
			_ = want
		default:
			if !errors.Is(d.Err, tc.err) {
				t.Errorf("%s: err = %v", tc.name, d.Err)
			}
		}
	}
}

type fixture struct {
	e   *sso.Engine
	dir *ssotest.Directory
	pr  *ssotest.Protocol
	ev  *ssotest.Recorder
	st  *ssotest.MemStore
	now time.Time
}

func newFixture(t *testing.T, providers ...sso.Provider) *fixture {
	t.Helper()
	f := &fixture{dir: &ssotest.Directory{}, ev: &ssotest.Recorder{}, now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	f.st = &ssotest.MemStore{Now: func() time.Time { return f.now }}
	f.pr = &ssotest.Protocol{Claims: map[string]sso.Claims{}, Codes: map[string]string{}}
	cat := &ssotest.Catalog{Providers: map[string]map[string]sso.Provider{"realm": {}}}
	for _, p := range providers {
		cat.Providers["realm"][p.Name] = p
	}
	f.e = &sso.Engine{Store: f.st, Catalog: cat, Directory: f.dir, Events: f.ev, Protocol: f.pr, Now: func() time.Time { return f.now }}
	return f
}

var googleP = sso.Provider{Name: "google", Kind: sso.Google, Method: "google", ClientID: "g-client", Link: sso.LinkVerifiedEmail}

// start begins a sign-in and returns the state the provider would echo.
func (f *fixture) start(t *testing.T, p sso.Provider, intent sso.Intent, challenge string) string {
	t.Helper()
	u, err := f.e.Start(context.Background(), sso.StartInput{Scope: "realm", Provider: p, CallbackURL: "https://api.example/cb", Intent: intent, Challenge: challenge, RedirectTo: "https://app.example/home", LinkUserID: "me"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(u)
	return parsed.Query().Get("state")
}

func params(o sso.Outcome) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(o.Params); i += 2 {
		m[o.Params[i]] = o.Params[i+1]
	}
	return m
}

func TestSignInEndsWithACodeThatIsRedeemedOnce(t *testing.T) {
	f := newFixture(t, googleP)
	f.pr.Codes["auth-code"] = "tok"
	f.pr.Claims["tok"] = sso.Claims{Subject: "g-1", Email: "Ada@Example.com", EmailVerified: true, Name: "Ada"}
	verifier, challenge, _ := pkce.New()
	state := f.start(t, googleP, sso.IntentSignIn, challenge)
	out := f.e.Callback(context.Background(), sso.CallbackInput{Scope: "realm", Provider: "google", State: state, Code: "auth-code"})
	got := params(out)
	if out.Page || out.RedirectTo != "https://app.example/home" || got[sso.ParamCode] == "" || out.UserID == "" || got[sso.ParamError] != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(f.dir.Created) != 1 || f.dir.Created[0].Email != "ada@example.com" || !f.dir.Created[0].EmailVerified {
		t.Errorf("created = %+v", f.dir.Created)
	}
	if len(f.pr.Nonces) != 1 || f.pr.Nonces[0] == "" || f.pr.Exchanged[0] == "" {
		t.Errorf("nonce %v, verifier %v", f.pr.Nonces, f.pr.Exchanged)
	}
	// A wrong verifier burns the code; the right one on a fresh code works once.
	if _, err := f.e.Redeem(context.Background(), got[sso.ParamCode], "wrong"); !errors.Is(err, sso.ErrInvalidCode) {
		t.Errorf("wrong verifier: %v", err)
	}
	if _, err := f.e.Redeem(context.Background(), got[sso.ParamCode], verifier); !errors.Is(err, sso.ErrInvalidCode) {
		t.Errorf("a burnt code: %v", err)
	}
	state = f.start(t, googleP, sso.IntentSignIn, challenge)
	out = f.e.Callback(context.Background(), sso.CallbackInput{Scope: "realm", Provider: "google", State: state, Code: "auth-code"})
	code := params(out)[sso.ParamCode]
	c, err := f.e.Redeem(context.Background(), code, verifier)
	if err != nil || c.UserID != out.UserID || c.Method != "google" || c.NewUser {
		t.Fatalf("redeem = %+v, %v", c, err)
	}
	if _, err := f.e.Redeem(context.Background(), code, verifier); !errors.Is(err, sso.ErrInvalidCode) {
		t.Errorf("a code twice: %v", err)
	}
	if len(f.dir.Created) != 1 {
		t.Errorf("the second sign-in made another user: %+v", f.dir.Created)
	}
}

func TestNewUserGetsTheProfileOnce(t *testing.T) {
	f := newFixture(t, googleP)
	f.pr.Codes["c"] = "tok"
	f.pr.Claims["tok"] = sso.Claims{Subject: "g-1", Email: "ada@example.com", EmailVerified: true, Name: "Ada L"}
	verifier, challenge, _ := pkce.New()
	out := f.e.Callback(context.Background(), sso.CallbackInput{Scope: "realm", Provider: "google", State: f.start(t, googleP, sso.IntentSignIn, challenge), Code: "c"})
	c, err := f.e.Redeem(context.Background(), params(out)[sso.ParamCode], verifier)
	if err != nil || !c.NewUser || c.Profile["name"] != "Ada L" {
		t.Fatalf("redeem = %+v, %v", c, err)
	}
}

func TestCallbackThatCannotBeTracedToAnApp(t *testing.T) {
	f := newFixture(t, googleP)
	_, challenge, _ := pkce.New()
	ctx := context.Background()
	for name, in := range map[string]sso.CallbackInput{
		"no state":         {Scope: "realm", Provider: "google", Code: "c"},
		"unknown state":    {Scope: "realm", Provider: "google", State: "nope", Code: "c"},
		"no scope":         {Provider: "google", State: f.start(t, googleP, sso.IntentSignIn, challenge), Code: "c"},
		"another scope":    {Scope: "org:o1", Provider: "google", State: f.start(t, googleP, sso.IntentSignIn, challenge), Code: "c"},
		"another provider": {Scope: "realm", Provider: "microsoft", State: f.start(t, googleP, sso.IntentSignIn, challenge), Code: "c"},
	} {
		if out := f.e.Callback(ctx, in); !out.Page {
			t.Errorf("%s: %+v", name, out)
		}
	}
	// A state is good for one callback.
	f.pr.Codes["c"] = "tok"
	f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "a@example.com", EmailVerified: true}
	state := f.start(t, googleP, sso.IntentSignIn, challenge)
	if out := f.e.Callback(ctx, sso.CallbackInput{Scope: "realm", Provider: "google", State: state, Code: "c"}); out.Page {
		t.Fatalf("first use: %+v", out)
	}
	if out := f.e.Callback(ctx, sso.CallbackInput{Scope: "realm", Provider: "google", State: state, Code: "c"}); !out.Page {
		t.Error("a state was used twice")
	}
	// An expired attempt is untraceable too.
	state = f.start(t, googleP, sso.IntentSignIn, challenge)
	f.now = f.now.Add(11 * time.Minute)
	if out := f.e.Callback(ctx, sso.CallbackInput{Scope: "realm", Provider: "google", State: state, Code: "c"}); !out.Page {
		t.Error("an expired attempt finished")
	}
}

func TestCallbackFailuresGoBackToTheApp(t *testing.T) {
	_, challenge, _ := pkce.New()
	ctx := context.Background()
	run := func(t *testing.T, f *fixture, in sso.CallbackInput) map[string]string {
		t.Helper()
		in.Scope, in.Provider = "realm", "google"
		if in.State == "" {
			in.State = f.start(t, googleP, sso.IntentSignIn, challenge)
		}
		out := f.e.Callback(ctx, in)
		if out.Page || out.RedirectTo != "https://app.example/home" {
			t.Fatalf("outcome = %+v", out)
		}
		return params(out)
	}
	t.Run("cancelled", func(t *testing.T) {
		if got := run(t, newFixture(t, googleP), sso.CallbackInput{Error: "access_denied"}); got[sso.ParamError] != sso.CodeCancelled {
			t.Errorf("%v", got)
		}
	})
	t.Run("provider error", func(t *testing.T) {
		if got := run(t, newFixture(t, googleP), sso.CallbackInput{Error: "server_error"}); got[sso.ParamError] != sso.CodeProviderError {
			t.Errorf("%v", got)
		}
	})
	t.Run("no code", func(t *testing.T) {
		if got := run(t, newFixture(t, googleP), sso.CallbackInput{}); got[sso.ParamError] != sso.CodeProviderError {
			t.Errorf("%v", got)
		}
	})
	t.Run("exchange refused", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.pr.ExchangeErr = sso.ErrExchange
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeProviderError {
			t.Errorf("%v", got)
		}
	})
	t.Run("token not valid", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.pr.Codes["c"] = "tok"
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeProviderError {
			t.Errorf("%v", got)
		}
	})
	t.Run("account exists", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.dir.AddUser(sso.Account{ID: "u1", Email: "ada@example.com"}) // unverified in backd
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "ada@example.com", EmailVerified: true}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeAccountExists || got[sso.ParamProvider] != "google" {
			t.Errorf("%v", got)
		}
	})
	t.Run("sign-up closed", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.dir.Mode = "closed"
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "new@example.com", EmailVerified: true}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeSignupClosed {
			t.Errorf("%v", got)
		}
	})
	t.Run("no email", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g"}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeEmailRequired {
			t.Errorf("%v", got)
		}
	})
	t.Run("disabled user is refused and audited", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.dir.AddUser(sso.Account{ID: "u1", Email: "ada@example.com", EmailVerified: true, Disabled: true})
		f.dir.AddIdentity(sso.IdentityKey{Provider: "google", Subject: "g"}, "u1")
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "ada@example.com", EmailVerified: true}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeSignInRefused {
			t.Errorf("%v", got)
		}
		if len(f.ev.Events) != 1 || f.ev.Events[0].Kind != sso.EventRefused || f.ev.Events[0].Reason != "disabled" || f.ev.Events[0].UserID != "u1" {
			t.Errorf("events = %+v", f.ev.Events)
		}
	})
	t.Run("the connection does not admit the person", func(t *testing.T) {
		p := googleP
		p.Admit = func(c sso.Claims) bool { return c.HostedDomain == "acme.example" }
		f := newFixture(t, p)
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "x@example.com", EmailVerified: true, HostedDomain: "other.example"}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeNotAllowed {
			t.Errorf("%v", got)
		}
		if len(f.dir.Created) != 0 {
			t.Error("a person the connection does not admit was made a user")
		}
	})
	t.Run("a connection that lost its authority", func(t *testing.T) {
		p := googleP
		p.Authority = func(context.Context) error { return errors.New("setter lost the roles") }
		f := newFixture(t, p)
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "x@example.com", EmailVerified: true}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeProviderError || len(f.dir.Created) != 0 {
			t.Errorf("%v, created %v", got, f.dir.Created)
		}
	})
	t.Run("the place now requires another way in", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.dir.Replaced = sso.CodeSSORequired
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeSSORequired {
			t.Errorf("%v", got)
		}
	})
	t.Run("the person may not enter", func(t *testing.T) {
		f := newFixture(t, googleP)
		f.dir.Deny = true
		f.pr.Codes["c"] = "tok"
		f.pr.Claims["tok"] = sso.Claims{Subject: "g", Email: "x@example.com", EmailVerified: true}
		if got := run(t, f, sso.CallbackInput{Code: "c"}); got[sso.ParamError] != sso.CodeNotAllowed {
			t.Errorf("%v", got)
		}
	})
}

func TestLinkingEndsAtTheAppWithoutACode(t *testing.T) {
	f := newFixture(t, googleP)
	f.dir.AddUser(sso.Account{ID: "me", Email: "me@example.com", EmailVerified: true})
	f.pr.Codes["c"] = "tok"
	f.pr.Claims["tok"] = sso.Claims{Subject: "g-9", Email: "other@example.com", EmailVerified: true}
	_, challenge, _ := pkce.New()
	out := f.e.Callback(context.Background(), sso.CallbackInput{Scope: "realm", Provider: "google", State: f.start(t, googleP, sso.IntentLink, challenge), Code: "c"})
	got := params(out)
	if got[sso.ParamLinked] != "google" || got[sso.ParamCode] != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if id, _ := f.dir.Identity(context.Background(), sso.IdentityKey{Provider: "google", Subject: "g-9"}); id.ID != "me" {
		t.Errorf("identity of %q", id.ID)
	}
	// A provider account that belongs to someone else cannot be added to another user.
	f.dir.AddUser(sso.Account{ID: "u1", Email: "u1@example.com", EmailVerified: true})
	f.dir.AddIdentity(sso.IdentityKey{Provider: "google", Subject: "g-owned"}, "u1")
	f.pr.Claims["tok"] = sso.Claims{Subject: "g-owned", Email: "u1@example.com", EmailVerified: true}
	out = f.e.Callback(context.Background(), sso.CallbackInput{Scope: "realm", Provider: "google", State: f.start(t, googleP, sso.IntentLink, challenge), Code: "c"})
	if got := params(out); got[sso.ParamError] != sso.CodeLinkConflict {
		t.Errorf("a taken identity: %+v", out)
	}
}

func TestAppleTokenIsKeptForRevocation(t *testing.T) {
	apple := sso.Provider{Name: "apple", Kind: sso.Apple, Method: "apple", ClientID: "com.acme.web", Link: sso.LinkVerifiedEmail}
	f := newFixture(t, apple)
	f.pr.Refresh = "refresh-1"
	f.pr.Codes["c"] = "tok"
	f.pr.Claims["tok"] = sso.Claims{Subject: "a-1", Email: "x@privaterelay.appleid.com", EmailVerified: true}
	_, challenge, _ := pkce.New()
	out := f.e.Callback(context.Background(), sso.CallbackInput{Scope: "realm", Provider: "apple", State: f.start(t, apple, sso.IntentSignIn, challenge), Code: "c", User: "Ada Lovelace"})
	if params(out)[sso.ParamCode] == "" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(f.ev.Events) != 1 || f.ev.Events[0].Kind != sso.EventAppleToken || f.ev.Events[0].Token != "refresh-1" || f.ev.Events[0].Audience != "com.acme.web" {
		t.Errorf("events = %+v", f.ev.Events)
	}
}

func TestNativeIDToken(t *testing.T) {
	f := newFixture(t, googleP)
	f.pr.Claims["good"] = sso.Claims{Subject: "g-1", Email: "ada@example.com", EmailVerified: true}
	ctx := context.Background()
	n, err := f.e.IDToken(ctx, sso.IDTokenInput{Scope: "realm", Provider: googleP, IDToken: "good", Nonce: "n", Intent: sso.IntentSignIn})
	if err != nil || !n.NewUser || n.Account.Email != "ada@example.com" {
		t.Fatalf("IDToken = %+v, %v", n, err)
	}
	if _, err := f.e.IDToken(ctx, sso.IDTokenInput{Scope: "realm", Provider: googleP, IDToken: "bad", Nonce: "n", Intent: sso.IntentSignIn}); !errors.Is(err, sso.ErrInvalidToken) {
		t.Errorf("bad token: %v", err)
	}
	p := googleP
	p.Admit = func(sso.Claims) bool { return false }
	if _, err := f.e.IDToken(ctx, sso.IDTokenInput{Scope: "realm", Provider: p, IDToken: "good", Nonce: "n", Intent: sso.IntentSignIn}); !errors.Is(err, sso.ErrNotAllowed) {
		t.Errorf("not admitted: %v", err)
	}
	f.dir.Mode = "closed"
	f.pr.Claims["new"] = sso.Claims{Subject: "g-2", Email: "new@example.com", EmailVerified: true}
	if _, err := f.e.IDToken(ctx, sso.IDTokenInput{Scope: "realm", Provider: googleP, IDToken: "new", Nonce: "n", Intent: sso.IntentSignIn}); !errors.Is(err, sso.ErrSignupClosed) {
		t.Errorf("closed: %v", err)
	}
	apple := sso.Provider{Name: "apple", Kind: sso.Apple, ClientID: "com.acme.web", RevokeOnDelete: true}
	f.pr.Claims["a"] = sso.Claims{Subject: "a-1", Email: "a@example.com", EmailVerified: true, Audience: "com.acme.web"}
	if _, err := f.e.IDToken(ctx, sso.IDTokenInput{Scope: "realm", Provider: apple, IDToken: "a", Nonce: "n", Intent: sso.IntentSignIn}); !errors.Is(err, sso.ErrAuthorizationCodeRequired) {
		t.Errorf("Apple without the code: %v", err)
	}
	f.dir.Mode = "open"
	f.pr.Refresh = "r-1"
	if _, err := f.e.IDToken(ctx, sso.IDTokenInput{Scope: "realm", Provider: apple, IDToken: "a", Nonce: "n", AuthorizationCode: "ac", Intent: sso.IntentSignIn}); err != nil {
		t.Fatal(err)
	}
	if last := f.ev.Events[len(f.ev.Events)-1]; last.Kind != sso.EventAppleToken || last.Token != "r-1" {
		t.Errorf("events = %+v", f.ev.Events)
	}
}

// fakeClock is a clock a test moves.
type fakeClock struct{ t time.Time }

func atomicNow() *fakeClock              { return &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }
func (c *fakeClock) get() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
