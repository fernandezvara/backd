package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

type linkFixture struct {
	svc   *Users
	store *authtest.MemStore
	clock *time.Time
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	svc, store, clock, _ := enrollFixture(t)
	svc.Settings.MagicLink = registry.MagicLinkSettings{Enabled: true}
	svc.Settings.SignInAppURL = "https://app.example/in"
	return &linkFixture{svc: svc, store: store, clock: clock}
}

// ask requests a link and plays the worker: it returns the request id, the link's token and the code.
func (f *linkFixture) ask(t *testing.T, address, challenge string) (id, token, code string) {
	t.Helper()
	ctx := context.Background()
	before := len(f.jobs(t))
	id, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: address, CodeChallenge: challenge})
	if err != nil || id == "" {
		t.Fatalf("request: %q %v", id, err)
	}
	jobs := f.jobs(t)
	if len(jobs) == before {
		return id, "", ""
	}
	var j Job
	for _, c := range jobs {
		if c.Email != nil && c.Email.ChallengeID == HashToken(id) {
			j = c
		}
	}
	if j.Email == nil || (j.Email.Kind != "magic-link" && j.Email.Kind != "no-account") {
		t.Fatalf("no job for the request: %+v", jobs)
	}
	if j.Email.Kind == "magic-link" {
		var err error
		if token, code, _, err = f.svc.MakeMagicLinkSecrets(ctx, j.Email.ChallengeID); err != nil {
			t.Fatal(err)
		}
	}
	return id, token, code
}

func (f *linkFixture) jobs(t *testing.T) []Job {
	t.Helper()
	jobs, _, err := f.svc.Jobs(context.Background(), JobFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func TestMagicLinkSignsAKnownUserIn(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	verifier, challenge, _ := NewPKCE()
	id, token, code := f.ask(t, " Ada@Example.com ", challenge)
	if len(code) != 6 || token == "" || id == "" {
		t.Fatalf("secrets: %q %q %q", id, token, code)
	}
	// Opening the page does not use the link up.
	if _, err := f.svc.MagicLinkByToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	login, dest, err := f.svc.UseMagicLink(ctx, token, "")
	if err != nil || login == "" || dest != "https://app.example/in" {
		t.Fatalf("use: %q %q %v", login, dest, err)
	}
	// The browser carries the code to the app that holds the verifier; it is a session of method "link".
	red, err := f.svc.RedeemLoginCode(ctx, login, verifier)
	if err != nil || red.Token == "" || red.NewUser || red.Principal.Session.Methods[0] != "link" || red.Principal.Session.MFA {
		t.Fatalf("redeem: %+v %v", red, err)
	}
	u, _ := f.svc.Find(ctx, "ada@example.com")
	if !u.EmailVerified {
		t.Error("the user isn't verified by their own mailbox")
	}
	id2, err := f.store.IdentityOf(ctx, u.ID, ProviderEmail)
	if err != nil || id2.Subject != u.ID {
		t.Errorf("email identity: %+v %v", id2, err)
	}
	// Used up: not the link again, and not the code that came with it.
	if _, _, err := f.svc.UseMagicLink(ctx, token, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("link again: %v", err)
	}
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("code after the link: %v", err)
	}
	// A wrong verifier makes a stolen login code worthless.
	_, token2, _ := f.ask(t, "ada@example.com", challenge)
	login2, _, _ := f.svc.UseMagicLink(ctx, token2, "")
	if _, err := f.svc.RedeemLoginCode(ctx, login2, "somebody-elses-verifier"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong verifier: %v", err)
	}
}

func TestMagicLinkCodeNeedsItsVerifierAndFiveTries(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	verifier, challenge, _ := NewPKCE()
	id, token, code := f.ask(t, "ada@example.com", challenge)
	var bad *InvalidCodeError
	// The right code from the wrong app (another verifier) is wrong.
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, "another-verifier-another-verifier-another", ""); !errors.As(err, &bad) || bad.AttemptsLeft != 4 {
		t.Fatalf("wrong verifier: %v", err)
	}
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, "000000", verifier, ""); !errors.As(err, &bad) || bad.AttemptsLeft != 3 {
		t.Fatalf("wrong code: %v", err)
	}
	out, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, "")
	if err != nil || out.Token == "" || out.Principal.Session.Methods[0] != "link" {
		t.Fatalf("right code: %+v %v", out, err)
	}
	// Redeeming the code uses the link up too.
	if _, _, err := f.svc.UseMagicLink(ctx, token, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("link after the code: %v", err)
	}
	// Five wrong tries end a request, even for the right code after.
	id, _, code = f.ask(t, "ada@example.com", challenge)
	for range MagicLinkCodeAttempts {
		_, _ = f.svc.VerifyMagicLinkCode(ctx, id, "000000", verifier, "")
	}
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("after five wrong tries: %v", err)
	}
	// An unknown request and an expired one look the same.
	if _, err := f.svc.VerifyMagicLinkCode(ctx, "nope", code, verifier, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("unknown: %v", err)
	}
	id, _, code = f.ask(t, "ada@example.com", challenge)
	*f.clock = f.clock.Add(MagicLinkTTL + time.Second)
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("expired: %v", err)
	}
}

func TestMagicLinkForAnAddressWithNoAccount(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	verifier, challenge, _ := NewPKCE()
	// With sign-up by link off: a message that there is no account, and no way to use it.
	id, token, code := f.ask(t, "new@example.com", challenge)
	if id == "" || token != "" || code != "" {
		t.Fatalf("no account: %q %q %q", id, token, code)
	}
	if j := f.jobs(t)[0]; j.Email.Kind != "no-account" || j.Email.UserID != "" || j.Email.ChallengeID != HashToken(id) {
		t.Errorf("job: %+v", j.Email)
	}
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, "123456", verifier, ""); !errors.Is(err, ErrInvalidMagicLink) {
		t.Errorf("code of a no-account request: %v", err)
	}
	if _, err := f.svc.Find(ctx, "new@example.com"); !errors.Is(err, ErrNotFound) {
		t.Error("an account was made at request time")
	}
	// With it on (and the realm's sign-up open): the account is made when the link is used,
	// verified, and the on_signup hook runs.
	f.svc.Settings.MagicLink.Signup = true
	f.svc.Settings.Account.OnSignup = "app/create-profile"
	id, token, code = f.ask(t, "new@example.com", challenge)
	if _, err := f.svc.Find(ctx, "new@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatal("an account was made at request time")
	}
	login, _, err := f.svc.UseMagicLink(ctx, token, "")
	if err != nil {
		t.Fatal(err)
	}
	red, err := f.svc.RedeemLoginCode(ctx, login, verifier)
	if err != nil || !red.NewUser || !red.Principal.User.EmailVerified {
		t.Fatalf("redeem: %+v %v", red, err)
	}
	if u, err := f.svc.Find(ctx, "new@example.com"); err != nil || !u.EmailVerified {
		t.Errorf("user: %+v %v", u, err)
	}
	foundHook := false
	for _, j := range f.jobs(t) {
		foundHook = foundHook || j.Origin == OnSignupOrigin
	}
	if !foundHook {
		t.Error("account.on_signup was not queued")
	}
	// Typing the code works the same for a second newcomer.
	id2, _, code2 := f.ask(t, "another@example.com", challenge)
	out, err := f.svc.VerifyMagicLinkCode(ctx, id2, code2, verifier, "")
	if err != nil || !out.NewUser {
		t.Errorf("new user by code: %+v %v", out, err)
	}
	_, _ = id, code
	// A realm whose sign-up is closed doesn't let them in this way even when magic_link.signup is on.
	f.svc.Settings.Signup = registry.SignupClosed
	if _, _, code3 := f.ask(t, "third@example.com", challenge); code3 != "" {
		t.Error("a link was made for a newcomer in a closed realm")
	}
}

func TestMagicLinkAnswersTheSameForEveryone(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	_, challenge, _ := NewPKCE()
	disabled, _ := f.svc.Create(ctx, "off@example.com", new(mfaPassword))
	if err := f.svc.SetDisabled(ctx, disabled.Email, true); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"ada@example.com", "nobody@example.com", "off@example.com"} {
		id, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: address, CodeChallenge: challenge})
		if err != nil || len(id) < 40 {
			t.Errorf("%s: %q %v", address, id, err)
		}
	}
	// Nothing was sent to the one that can't sign in.
	kinds := ""
	for _, j := range f.jobs(t) {
		kinds += j.Email.Kind + ","
	}
	if strings.Count(kinds, "magic-link") != 1 || strings.Count(kinds, "no-account") != 1 {
		t.Errorf("jobs: %s", kinds)
	}
	// Bad input is told, since it says nothing about anyone.
	if _, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: "not an address", CodeChallenge: challenge}); err == nil {
		t.Error("an invalid address was accepted")
	}
	if _, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: "ada@example.com", CodeChallenge: "short"}); err == nil {
		t.Error("an invalid challenge was accepted")
	}
	f.svc.Settings.MagicLink.Enabled = false
	if _, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: "ada@example.com", CodeChallenge: challenge}); !errors.Is(err, ErrMagicLinkDisabled) {
		t.Errorf("disabled: %v", err)
	}
}

func TestMagicLinkWithNowhereToGo(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	f.svc.Settings.SignInAppURL = ""
	verifier, challenge, _ := NewPKCE()
	id, token, code := f.ask(t, "ada@example.com", challenge)
	// The link can't carry a session anywhere, and isn't spent by trying.
	if _, _, err := f.svc.UseMagicLink(ctx, token, ""); !errors.Is(err, ErrNoRedirect) {
		t.Fatalf("no destination: %v", err)
	}
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, ""); err != nil {
		t.Errorf("the code still works: %v", err)
	}
	// A request's own redirect_to is a destination.
	f.svc.Settings.SignInAppURL = ""
	id2, err := f.svc.RequestMagicLink(ctx, MagicLinkInput{Email: "ada@example.com", CodeChallenge: challenge, RedirectTo: "acme://signed-in"})
	if err != nil || id2 == "" {
		t.Fatal(err)
	}
	token2, _, _, _ := f.svc.MakeMagicLinkSecrets(ctx, HashToken(id2))
	if _, dest, err := f.svc.UseMagicLink(ctx, token2, ""); err != nil || dest != "acme://signed-in" {
		t.Errorf("redirect_to: %q %v", dest, err)
	}
}

func TestMagicLinkEndsInTheSecondStepForAUserWithMFA(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	u, _ := f.svc.Find(ctx, "ada@example.com")
	f.svc.Cipher, _ = NewSecretCipher([]byte("01234567890123456789012345678901"))
	mf := &mfaFixture{svc: f.svc, store: f.store, clock: f.clock, user: u}
	mf.secret, mf.codes = mf.enroll(t, u)
	verifier, challenge, _ := NewPKCE()
	id, _, code := f.ask(t, "ada@example.com", challenge)
	out, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, "")
	if err != nil || out.Challenge == nil || out.Token != "" {
		t.Fatalf("outcome: %+v %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, mf.code(t, 0), "")
	if err != nil || v.Factor != "link" || v.Principal.Session.Methods[0] != "link" || !v.Principal.Session.MFA {
		t.Errorf("second step: %+v %v", v, err)
	}
}

func TestMagicLinkRespectsLoginNetworks(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	nets, _ := registry.ParseNetworks([]string{"10.0.0.0/8"})
	if _, err := f.svc.SetNetworks(ctx, "ada@example.com", nil, nets); err != nil {
		t.Fatal(err)
	}
	verifier, challenge, _ := NewPKCE()
	id, _, code := f.ask(t, "ada@example.com", challenge)
	var refused *RefusedError
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, "192.0.2.1"); !errors.As(err, &refused) || refused.Reason != "network" {
		t.Errorf("outside the networks: %v", err)
	}
}

// A squatter makes an account with the victim's address and leaves it unverified. When the
// victim signs in by a link, what the squatter set up goes: the password, a provider, the second
// factor and the sessions.
func TestMagicLinkClaimsAnUnverifiedAccount(t *testing.T) {
	ctx := context.Background()
	f := newLinkFixture(t)
	if _, _, err := f.svc.Signup(ctx, "victim@example.com", mfaPassword, ""); err != nil {
		t.Fatal(err)
	}
	_, squatterToken, err := f.svc.Login(ctx, "victim@example.com", mfaPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := f.svc.Find(ctx, "victim@example.com")
	if u.EmailVerified {
		t.Fatal("a sign-up is verified")
	}
	if err := f.store.PutIdentity(ctx, Identity{ID: "i1", UserID: u.ID, Provider: "google", Subject: "g-squatter"}); err != nil {
		t.Fatal(err)
	}
	verifier, challenge, _ := NewPKCE()
	id, _, code := f.ask(t, "victim@example.com", challenge)
	if _, err := f.svc.VerifyMagicLinkCode(ctx, id, code, verifier, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Login(ctx, "victim@example.com", mfaPassword, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the squatter's password still works: %v", err)
	}
	if _, err := f.store.IdentityOf(ctx, u.ID, "google"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the squatter's provider is still linked: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, squatterToken); err == nil {
		t.Error("the squatter's session survived")
	}
	if u, _ := f.svc.Find(ctx, "victim@example.com"); !u.EmailVerified {
		t.Error("the address isn't verified")
	}
}
