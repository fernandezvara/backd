package passkey_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth/passkey"
	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
)

const origin = "https://app.acme.example"

type fixture struct {
	svc   *passkey.Service
	store *passkeytest.Store
	clock *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	store := passkeytest.NewStore()
	svc, err := passkey.New(passkey.Config{RPID: "acme.example", RPName: "Acme", Origins: []string{origin}}, store, passkeytest.NewCeremonies(func() time.Time { return now }), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{svc: svc, store: store, clock: &now}
}

var ada = passkey.UserInfo{ID: "u1", Name: "ada@example.com", DisplayName: "Ada"}

// register adds a passkey of the authenticator for ada.
func (f *fixture) register(t *testing.T, a *passkeytest.Authenticator) passkey.Credential {
	t.Helper()
	opts, id, err := f.svc.BeginRegistration(context.Background(), ada)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.FinishRegistration(context.Background(), id, a.Register(t, opts), "Phone")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return c
}

func TestRegistrationStoresAPublicCredential(t *testing.T) {
	f := newFixture(t)
	a := passkeytest.New(origin)
	a.BE = true
	c := f.register(t, a)
	if c.UserID != "u1" || c.RPID != "acme.example" || string(c.CredentialID) != string(a.CredID) || len(c.PublicKey) == 0 || c.Name != "Phone" || !c.BackupEligible || c.SignCount != 1 {
		t.Errorf("credential = %+v", c)
	}
	got, err := f.store.ByCredentialID(context.Background(), a.CredID)
	if err != nil || got.ID != c.ID {
		t.Errorf("stored: %+v %v", got, err)
	}
	// The user handle is 32 random bytes, never the id or the email.
	h, _ := f.store.UserHandle(context.Background(), "u1")
	if len(h) != 32 || string(h) == "u1" {
		t.Errorf("handle = %x", h)
	}
	// A second registration excludes the first credential, so the same authenticator cannot add itself twice.
	opts, _, _ := f.svc.BeginRegistration(context.Background(), ada)
	if !contains(string(opts), `"excludeCredentials"`) {
		t.Errorf("options do not exclude the user's credentials: %s", opts)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDiscoverableAndKnownUserSignIn(t *testing.T) {
	f := newFixture(t)
	a := passkeytest.New(origin)
	cred := f.register(t, a)

	// Discoverable: nobody is named, the answer says who.
	opts, id, err := f.svc.BeginLogin(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts))
	if err != nil || res.UserID != "u1" || res.Credential.ID != cred.ID || !res.UserVerified || res.CounterRegressed {
		t.Fatalf("discoverable: %+v %v", res, err)
	}
	// Known user: only their credentials are allowed.
	opts, id, err = f.svc.BeginLogin(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if res, err = f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts)); err != nil || res.UserID != "u1" {
		t.Fatalf("known user: %+v %v", res, err)
	}
	got, _ := f.store.ByCredentialID(context.Background(), a.CredID)
	if got.SignCount != 3 || got.LastUsedAt.IsZero() {
		t.Errorf("the counter and the last use are kept: %+v", got)
	}
	// A user with no passkeys cannot start a known-user sign-in.
	if _, _, err := f.svc.BeginLogin(context.Background(), "nobody"); !errors.Is(err, passkey.ErrNoCredentials) {
		t.Errorf("no credentials: %v", err)
	}
}

func TestACeremonyIsUsedOnce(t *testing.T) {
	f := newFixture(t)
	a := passkeytest.New(origin)
	f.register(t, a)
	opts, id, _ := f.svc.BeginLogin(context.Background(), "")
	answer := a.Assert(t, opts)
	if _, err := f.svc.FinishLogin(context.Background(), id, answer); err != nil {
		t.Fatal(err)
	}
	// A replay of the same answer finds no ceremony.
	if _, err := f.svc.FinishLogin(context.Background(), id, answer); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("replay: %v", err)
	}
	// A ceremony of the other kind does not serve either.
	ropts, rid, _ := f.svc.BeginRegistration(context.Background(), ada)
	b := passkeytest.New(origin)
	if _, err := f.svc.FinishLogin(context.Background(), rid, b.Register(t, ropts)); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("a registration ceremony used to sign in: %v", err)
	}
	// And it waits only five minutes.
	opts, id, _ = f.svc.BeginLogin(context.Background(), "")
	*f.clock = f.clock.Add(6 * time.Minute)
	if _, err := f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts)); !errors.Is(err, passkey.ErrCeremony) {
		t.Errorf("expired: %v", err)
	}
}

func TestWhatDoesNotVerifyIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*passkeytest.Authenticator)
		step string // "register" or "login"
	}{
		"wrong origin at registration":     {func(a *passkeytest.Authenticator) { a.Origin = "https://evil.example" }, "register"},
		"wrong rp id at registration":      {func(a *passkeytest.Authenticator) { a.RPID = "evil.example" }, "register"},
		"no user verification at register": {func(a *passkeytest.Authenticator) { a.UV = false }, "register"},
		"wrong origin at sign-in":          {func(a *passkeytest.Authenticator) { a.Origin = "https://evil.example" }, "login"},
		"wrong rp id at sign-in":           {func(a *passkeytest.Authenticator) { a.RPID = "evil.example" }, "login"},
		"no user verification at sign-in":  {func(a *passkeytest.Authenticator) { a.UV = false }, "login"},
		"a signature of another key":       {func(a *passkeytest.Authenticator) { a.WrongKey = true }, "login"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := passkeytest.New(origin)
			if tc.step == "register" {
				tc.set(a)
				opts, id, _ := f.svc.BeginRegistration(context.Background(), ada)
				_, err := f.svc.FinishRegistration(context.Background(), id, a.Register(t, opts), "x")
				if !errors.Is(err, passkey.ErrInvalid) {
					t.Fatalf("registration: %v", err)
				}
				if n, _ := f.store.CountByUser(context.Background(), "u1"); n != 0 {
					t.Errorf("a refused registration stored %d credentials", n)
				}
				return
			}
			f.register(t, a)
			tc.set(a)
			opts, id, _ := f.svc.BeginLogin(context.Background(), "")
			var pe *passkey.Error
			_, err := f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts))
			if !errors.Is(err, passkey.ErrInvalid) || !errors.As(err, &pe) || pe.Reason == "" {
				t.Fatalf("sign-in: %v (an *Error with a reason, for the audit log)", err)
			}
		})
	}
}

func TestACredentialNobodyRegisteredIsUnknown(t *testing.T) {
	f := newFixture(t)
	stranger := passkeytest.New(origin)
	// The stranger registers elsewhere (another store), then tries here.
	other := newFixture(t)
	other.register(t, stranger)
	opts, id, _ := f.svc.BeginLogin(context.Background(), "")
	if _, err := f.svc.FinishLogin(context.Background(), id, stranger.Assert(t, opts)); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Errorf("unknown credential: %v", err)
	}
}

func TestACredentialOfAnotherUserIsRefusedForAKnownUser(t *testing.T) {
	f := newFixture(t)
	a := passkeytest.New(origin)
	f.register(t, a)
	// Bob has a passkey of his own, so he can start a sign-in. Ada's authenticator answers it anyway
	// (a browser would not offer it, but a client can send anything): the server refuses.
	bob := passkeytest.New(origin)
	opts, id, _ := f.svc.BeginRegistration(context.Background(), passkey.UserInfo{ID: "u2", Name: "bob@example.com"})
	if _, err := f.svc.FinishRegistration(context.Background(), id, bob.Register(t, opts), "Bob's"); err != nil {
		t.Fatal(err)
	}
	opts, id, _ = f.svc.BeginLogin(context.Background(), "u2")
	a.IgnoreAllowList = true
	_, err := f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts))
	var pe *passkey.Error
	if !errors.As(err, &pe) || !contains(pe.Reason, "another user") {
		t.Errorf("another user's credential for bob's ceremony: %v", err)
	}
}

func TestTheCounterRule(t *testing.T) {
	f := newFixture(t)
	a := passkeytest.New(origin)
	f.register(t, a)
	signIn := func() passkey.LoginResult {
		t.Helper()
		opts, id, _ := f.svc.BeginLogin(context.Background(), "")
		res, err := f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := signIn(); res.CounterRegressed {
		t.Fatal("a counter that goes forward is no regression")
	}
	// The authenticator's counter goes backwards: the sign-in verifies, the result says so, the stored
	// counter stays and the credential is flagged.
	before, _ := f.store.ByCredentialID(context.Background(), a.CredID)
	a.Counter = 1
	res := signIn()
	if !res.CounterRegressed || !res.Credential.CloneSuspected {
		t.Fatalf("regression: %+v", res)
	}
	after, _ := f.store.ByCredentialID(context.Background(), a.CredID)
	if after.SignCount != before.SignCount || !after.CloneSuspected {
		t.Errorf("stored: before %d, after %+v", before.SignCount, after)
	}
}

func TestACounterOfZeroStaysZero(t *testing.T) {
	f := newFixture(t)
	a := passkeytest.New(origin)
	a.Counts = false // many synced passkeys never count
	c := f.register(t, a)
	if c.SignCount != 0 {
		t.Fatalf("registered with a counter of %d", c.SignCount)
	}
	for range 3 {
		opts, id, _ := f.svc.BeginLogin(context.Background(), "")
		res, err := f.svc.FinishLogin(context.Background(), id, a.Assert(t, opts))
		if err != nil || res.CounterRegressed {
			t.Fatalf("a counter that stays 0 is not a regression: %+v %v", res, err)
		}
	}
	if got, _ := f.store.ByCredentialID(context.Background(), a.CredID); got.SignCount != 0 || got.CloneSuspected {
		t.Errorf("stored: %+v", got)
	}
}

func TestTheConfigurationIsChecked(t *testing.T) {
	good := passkey.Config{RPID: "acme.example", Origins: []string{"https://app.acme.example", "https://acme.example", "android:apk-key-hash:abc"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]passkey.Config{
		"no rp id":              {Origins: []string{origin}},
		"no origin":             {RPID: "acme.example"},
		"http":                  {RPID: "acme.example", Origins: []string{"http://app.acme.example"}},
		"another domain":        {RPID: "acme.example", Origins: []string{"https://evil.example"}},
		"a lookalike suffix":    {RPID: "acme.example", Origins: []string{"https://notacme.example"}},
		"a path":                {RPID: "acme.example", Origins: []string{"https://app.acme.example/login"}},
		"loopback without flag": {RPID: "localhost", Origins: []string{"http://localhost:8080"}},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dev := passkey.Config{RPID: "localhost", Origins: []string{"http://localhost:8080"}, AllowLoopbackHTTP: true}
	if err := dev.Validate(); err != nil {
		t.Errorf("loopback in dev: %v", err)
	}
}
