package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

const mfaPassword = "dev-p4ssw0rd!"

// mfaFixture is a realm with a secrets cipher and a user who has TOTP and recovery codes.
type mfaFixture struct {
	svc    *Users
	store  *authtest.MemStore
	clock  *time.Time
	user   User
	secret string
	codes  []string
}

func newMFAFixture(t *testing.T) *mfaFixture {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Realm = "acme"
	var err error
	if svc.Cipher, err = NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	u, err := svc.Create(context.Background(), "ada@example.com", new(mfaPassword))
	if err != nil {
		t.Fatal(err)
	}
	f := &mfaFixture{svc: svc, store: store, clock: &clock, user: u}
	f.secret, f.codes = f.enroll(t, u)
	return f
}

// enroll gives a user a TOTP secret and recovery codes, as enrolling does.
func (f *mfaFixture) enroll(t *testing.T, u User) (string, []string) {
	t.Helper()
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "acme", AccountName: u.Email})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.svc.SealToken(key.Secret())
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{"AAAAA-BBBBB", "CCCCC-DDDDD", "EEEEE-FFFFF"}
	var hashes []string
	for _, c := range codes {
		normalized := strings.ReplaceAll(c, "-", "")
		hashes = append(hashes, f.svc.Cipher.MAC("recovery", f.svc.Realm, u.ID, normalized))
	}
	if err := f.store.PutMFA(context.Background(), MFARecord{UserID: u.ID, Secret: sealed, EnabledAt: *f.clock, RecoveryCodes: hashes}); err != nil {
		t.Fatal(err)
	}
	return key.Secret(), codes
}

// code is the user's TOTP code at the fixture's clock plus offset.
func (f *mfaFixture) code(t *testing.T, offset time.Duration) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(f.secret, f.clock.Add(offset), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *mfaFixture) challenge(t *testing.T) string {
	t.Helper()
	out, err := f.svc.SignIn(context.Background(), "ada@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil {
		t.Fatalf("sign-in: %+v, %v", out, err)
	}
	return out.Challenge.Token
}

func TestSecondFactorIsAskedAfterTheFirst(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	out, err := f.svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Token != "" || out.Challenge == nil || out.Challenge.Kind != ChallengeMFA || len(out.Challenge.Methods) != 2 {
		t.Fatalf("outcome: %+v", out)
	}
	// A wrong password stays what it was: no challenge, no hint that MFA exists.
	if _, err := f.svc.SignIn(ctx, "ada@example.com", "not-the-password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	// The one-step entry point refuses rather than start a half-authenticated session.
	if _, _, err := f.svc.Login(ctx, "ada@example.com", mfaPassword, ""); !errors.Is(err, ErrSecondStepRequired) {
		t.Errorf("Login: %v", err)
	}
	// Someone without a second factor still gets their session at once.
	if _, err := f.svc.Create(ctx, "bob@example.com", new(mfaPassword)); err != nil {
		t.Fatal(err)
	}
	p, token, err := f.svc.Login(ctx, "bob@example.com", mfaPassword, "")
	if err != nil || token == "" || p.Session.MFA || len(p.Session.Methods) != 1 || p.Session.Methods[0] != ProviderPassword {
		t.Errorf("user without MFA: %+v, %v", p.Session, err)
	}
}

func TestVerifyMFAIssuesTheSessionOnce(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	ch := f.challenge(t)
	v, err := f.svc.VerifyMFA(ctx, ch, MethodTOTP, f.code(t, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	s := v.Principal.Session
	if v.Token == "" || !s.MFA || len(s.Methods) != 2 || s.Methods[0] != "password" || s.Methods[1] != "totp" {
		t.Errorf("session: %+v token %q", s, v.Token)
	}
	// The session is the stored one: authenticating with the token sees the methods.
	p, err := f.svc.Authenticate(ctx, v.Token)
	if err != nil || !p.Session.MFA || len(p.Session.Methods) != 2 {
		t.Errorf("authenticate: %+v, %v", p.Session, err)
	}
	// The challenge is used up, and so is the time step the code came from.
	if _, err := f.svc.VerifyMFA(ctx, ch, MethodTOTP, f.code(t, 0), ""); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("a used challenge: %v", err)
	}
	var bad *InvalidCodeError
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, 0), ""); !errors.As(err, &bad) {
		t.Errorf("the same code again: %v", err)
	}
	// An older step can't follow a newer one either, but a later one works.
	*f.clock = f.clock.Add(time.Minute)
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, -30*time.Second), ""); err != nil {
		t.Errorf("the next window: %v", err)
	}
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, -60*time.Second), ""); !errors.As(err, &bad) {
		t.Errorf("an older step after a newer one: %v", err)
	}
}

func TestChallengesLookAlikeWhenInvalid(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	ch := f.challenge(t)
	*f.clock = f.clock.Add(ChallengeTTL + time.Second)
	for name, c := range map[string]string{"unknown": "nope", "empty": "", "expired": ch} {
		if _, err := f.svc.VerifyMFA(ctx, c, MethodTOTP, f.code(t, 0), ""); !errors.Is(err, ErrInvalidChallenge) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestChallengeAllowsFiveAttempts(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	ch := f.challenge(t)
	for i := 1; i <= MaxChallengeAttempts; i++ {
		_, err := f.svc.VerifyMFA(ctx, ch, MethodTOTP, "000000", "")
		var bad *InvalidCodeError
		if !errors.As(err, &bad) || bad.AttemptsLeft != MaxChallengeAttempts-i {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// The challenge is gone: even the right code is refused now.
	if _, err := f.svc.VerifyMFA(ctx, ch, MethodTOTP, f.code(t, 0), ""); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("after five attempts: %v", err)
	}
}

func TestRecoveryCodesWorkOnce(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	v, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodRecovery, "aaaaa bbbbb", "")
	if err != nil || v.RecoveryCodesLeft == nil || *v.RecoveryCodesLeft != 2 || v.Principal.Session.Methods[1] != "recovery" {
		t.Fatalf("recovery: %+v, %v", v, err)
	}
	var bad *InvalidCodeError
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodRecovery, "AAAAA-BBBBB", ""); !errors.As(err, &bad) {
		t.Errorf("a used recovery code: %v", err)
	}
	// An unknown method is a wrong code, not a way around.
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), "sms", "123456", ""); !errors.As(err, &bad) {
		t.Errorf("unknown method: %v", err)
	}
}

func TestFailedSecondFactorsLockTheUser(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	// Each challenge only gives five tries; the lock counts across them.
	for range 10 {
		ch := f.challenge(t)
		if _, err := f.svc.VerifyMFA(ctx, ch, MethodTOTP, "000000", ""); err == nil {
			t.Fatal("a wrong code was accepted")
		}
	}
	var locked *MFALockedError
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, 0), ""); !errors.As(err, &locked) || locked.RetryAfter <= 0 {
		t.Fatalf("after ten failures: %v", err)
	}
	// Waiting always works: there is no lockout that needs an administrator.
	*f.clock = f.clock.Add(16 * time.Minute)
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, 0), ""); err != nil {
		t.Errorf("after the wait: %v", err)
	}
}

func TestProviderSignInGetsTheSecondStepToo(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Providers = nil
	res, err := f.svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "google", Subject: "g-ada", Email: "ada@example.com", EmailVerified: true, TrustsEmail: true, Intent: IntentSignIn})
	if err != nil || res.NewUser || res.User.ID != f.user.ID {
		t.Fatalf("link by address: %+v, %v", res, err)
	}
	out, err := f.svc.StartProviderSession(ctx, res.User, "google", false, nil)
	if err != nil || out.Challenge == nil || out.Token != "" {
		t.Fatalf("native: %+v, %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil || v.Factor != "google" || v.Principal.Session.Methods[0] != "google" {
		t.Errorf("verify: %+v, %v", v, err)
	}
	// The hand-over of a new user survives the second step.
	other := User{ID: "u-new", Email: "new@example.com", EmailVerified: true, Roles: []string{}}
	if err := f.store.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	f.enroll(t, other)
	out, err = f.svc.StartProviderSession(ctx, other, "google", true, map[string]string{"name": "New Person"})
	if err != nil || out.Challenge == nil {
		t.Fatal(out, err)
	}
	f.secret = "" // the secret of `other` is not the fixture's: read it back
	rec, _ := f.store.MFAOf(ctx, other.ID)
	secret, _ := f.svc.OpenToken(rec.Secret)
	code, _ := totp.GenerateCodeCustom(secret, *f.clock, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	v, err = f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, code, "")
	if err != nil || !v.NewUser || v.Profile["name"] != "New Person" {
		t.Errorf("hand-over: %+v, %v", v, err)
	}
}

func TestRequiredRoleWithoutMFAIsAskedToEnroll(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	boss, err := f.svc.Create(ctx, "boss@example.com", new(mfaPassword))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddRoles(ctx, boss.ID, []string{"owner"}, *f.clock); err != nil {
		t.Fatal(err)
	}
	f.svc.Settings.MFA.RequiredRoles = []string{"owner"}
	out, err := f.svc.SignIn(ctx, "boss@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll || out.Token != "" {
		t.Fatalf("outcome: %+v, %v", out, err)
	}
	// An enrollment challenge is not a way to a session.
	if _, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, "123456", ""); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("verify with an enroll challenge: %v", err)
	}
	// `all` asks everyone who has no second factor yet.
	f.svc.Settings.MFA = registry.MFASettings{RequiredAll: true}
	if _, err := f.svc.Create(ctx, "bob@example.com", new(mfaPassword)); err != nil {
		t.Fatal(err)
	}
	out, err = f.svc.SignIn(ctx, "bob@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll {
		t.Errorf("all: %+v, %v", out, err)
	}
}

func TestCallerSignInMethods(t *testing.T) {
	sess := Session{Methods: []string{"google", "totp"}, MFA: true}
	mfa, methods := Caller{User: &Principal{Session: sess}}.SignInMethods()
	if !mfa || len(methods) != 2 {
		t.Errorf("session: %v %v", mfa, methods)
	}
	// An API key, even on behalf of a user who has MFA, never does.
	mfa, methods = Caller{Key: &APIKey{}, User: &Principal{Session: sess}}.SignInMethods()
	if mfa || len(methods) != 1 || methods[0] != "key" {
		t.Errorf("key: %v %v", mfa, methods)
	}
	if mfa, methods = (Caller{}).SignInMethods(); mfa || methods == nil || len(methods) != 0 {
		t.Errorf("anonymous: %v %v", mfa, methods)
	}
}

// The organization a sign-in came through survives the second step into the session.
func TestOrganizationOfASignInSurvivesTheSecondStep(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	out, err := f.svc.StartProviderSessionIn(ctx, f.user, "google", false, nil, "org-7")
	if err != nil || out.Challenge == nil {
		t.Fatalf("start: %+v, %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil || v.Principal.Session.Org != "org-7" {
		t.Errorf("session org = %q, %v", v.Principal.Session.Org, err)
	}
	// And a sign-in that did not come through one records none.
	out, _ = f.svc.StartProviderSession(ctx, f.user, "google", false, nil)
	f.secret = ""
	rec, _ := f.store.MFAOf(ctx, f.user.ID)
	secret, _ := f.svc.OpenToken(rec.Secret)
	code, _ := totp.GenerateCodeCustom(secret, f.clock.Add(30*time.Second), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if v, err = f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, code, ""); err != nil || v.Principal.Session.Org != "" {
		t.Errorf("session org = %q, %v", v.Principal.Session.Org, err)
	}
}
