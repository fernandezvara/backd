package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
	"github.com/fernandezvara/backd/internal/registry"
)

func adminPasskeyStrategies(svc *Users) {
	svc.Settings.Strategies = []registry.Strategy{
		{Name: "password", Methods: []string{"password"}},
		{Name: "admin_passkey", Methods: []string{"password", "passkey"}},
	}
}

// stepAssertion makes the answer to the options of the passkey step a challenge waits for.
func stepAssertion(t *testing.T, svc *Users, a *passkeytest.Authenticator, challenge string) []byte {
	t.Helper()
	opts, err := svc.BeginPasskeyStep(context.Background(), challenge)
	if err != nil {
		t.Fatalf("step options: %v", err)
	}
	return a.Assert(t, opts)
}

func TestAPasskeyAsALaterStepFinishesTheSignIn(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	adminPasskeyStrategies(svc)
	a, info := addPasskey(t, svc, u, "Key")
	out, err := svc.SignInWith(ctx, "admin_passkey", u.Email, mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeMFA || !slices.Equal(out.Challenge.Next, []string{"passkey"}) || !slices.Equal(out.Challenge.Methods, []string{"passkey"}) {
		t.Fatalf("challenge: %+v %v", out, err)
	}
	v, err := svc.VerifyMFAPasskey(ctx, out.Challenge.Token, stepAssertion(t, svc, a, out.Challenge.Token), "")
	if err != nil {
		t.Fatal(err)
	}
	s := v.Principal.Session
	if !slices.Equal(s.Methods, []string{"password", "passkey"}) || !s.MFA || s.Strategy != "admin_passkey" || s.Passkey != info.ID || v.Method != "passkey" {
		t.Errorf("session = %+v", s)
	}
	// The challenge is used once.
	if _, err := svc.VerifyMFAPasskey(ctx, out.Challenge.Token, []byte(`{}`), ""); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("reuse: %v", err)
	}
}

func TestOnlyTheProofTheStrategyAsksForCounts(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	adminPasskeyStrategies(svc)
	a, _ := addPasskey(t, svc, u, "Key")
	out, _ := svc.SignInWith(ctx, "admin_passkey", u.Email, mfaPassword, "")
	var bad *InvalidCodeError
	// A code does not answer a passkey step.
	if _, err := svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, "123456", ""); !errors.As(err, &bad) {
		t.Errorf("a code for a passkey step: %v", err)
	}
	// A wrong ceremony counts against the challenge's attempts, like a wrong code.
	a.WrongKey = true
	_, err := svc.VerifyMFAPasskey(ctx, out.Challenge.Token, stepAssertion(t, svc, a, out.Challenge.Token), "")
	if !errors.As(err, &bad) || bad.AttemptsLeft != MaxChallengeAttempts-1 {
		t.Fatalf("a wrong ceremony: %v (attempts left %v)", err, bad)
	}
	// A passkey does not answer a code step.
	f := newMFAFixture(t)
	f.svc.Settings.Passkeys = registry.PasskeySettings{Enabled: true, RPID: "example.com", Origins: []string{passkeyOrigin}}
	ch := f.challenge(t)
	if _, err := f.svc.VerifyMFAPasskey(ctx, ch, []byte(`{}`), ""); !errors.As(err, &bad) {
		t.Errorf("a passkey for a code step: %v", err)
	}
}

func TestAPersonWithoutAPasskeyAddsOneToFinishSigningIn(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	adminPasskeyStrategies(svc)
	out, err := svc.SignInWith(ctx, "admin_passkey", u.Email, mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll || !slices.Equal(out.Challenge.Next, []string{"passkey"}) {
		t.Fatalf("enrolment challenge: %+v %v", out, err)
	}
	// The challenge asks for a passkey, not an authenticator.
	if _, err := svc.BeginEnrollment(ctx, out.Challenge.Token); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("totp enrolment for a passkey step: %v", err)
	}
	a := passkeytest.New(passkeyOrigin)
	opts, err := svc.BeginPasskeyEnrollment(ctx, out.Challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.CompletePasskeyEnrollment(ctx, out.Challenge.Token, a.Register(t, opts), "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	s := v.Principal.Session
	if v.Token == "" || !slices.Equal(s.Methods, []string{"password", "passkey"}) || !s.MFA || s.Passkey == "" {
		t.Errorf("session = %+v", s)
	}
	if n, _ := svc.PasskeyCount(ctx, u.ID); n != 1 {
		t.Errorf("passkeys = %d", n)
	}
	if !slices.Contains(notices(t, svc), "security-notice:passkey-added") {
		t.Errorf("notices = %v", notices(t, svc))
	}
}

func TestAnEnrolmentChallengeEndsAfterFiveWrongCredentials(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	adminPasskeyStrategies(svc)
	out, _ := svc.SignInWith(ctx, "admin_passkey", u.Email, mfaPassword, "")
	for range MaxChallengeAttempts - 1 {
		a := passkeytest.New("https://evil.example")
		opts, err := svc.BeginPasskeyEnrollment(ctx, out.Challenge.Token)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CompletePasskeyEnrollment(ctx, out.Challenge.Token, a.Register(t, opts), "x"); err == nil || errors.Is(err, ErrInvalidChallenge) {
			t.Fatalf("a wrong credential: %v", err)
		}
	}
	a := passkeytest.New("https://evil.example")
	opts, _ := svc.BeginPasskeyEnrollment(ctx, out.Challenge.Token)
	if _, err := svc.CompletePasskeyEnrollment(ctx, out.Challenge.Token, a.Register(t, opts), "x"); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("the fifth: %v", err)
	}
	if _, err := svc.BeginPasskeyEnrollment(ctx, out.Challenge.Token); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("after the end: %v", err)
	}
}

func TestAPasskeyIsAProofForReauthentication(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	a, _ := addPasskey(t, svc, u, "Key")
	// A user with a passkey (and no TOTP) may prove who they are with it; the password is no longer the proof.
	if m, _ := svc.ReauthMethodsOf(ctx, u); !slices.Equal(m, []string{"passkey"}) {
		t.Fatalf("reauth methods = %v", m)
	}
	p, _, err := svc.Login(ctx, u.Email, mfaPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := svc.BeginPasskeyReauth(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	g, err := svc.Reauthenticate(ctx, p, ReauthPasskey, string(a.Assert(t, opts)), "")
	if err != nil || g.Token == "" {
		t.Fatalf("reauth: %+v %v", g, err)
	}
	// A wrong ceremony is a wrong code.
	opts, _ = svc.BeginPasskeyReauth(ctx, p)
	a.WrongKey = true
	var bad *InvalidCodeError
	if _, err := svc.Reauthenticate(ctx, p, ReauthPasskey, string(a.Assert(t, opts)), ""); !errors.As(err, &bad) {
		t.Errorf("a wrong ceremony: %v", err)
	}
	// The password is not accepted any more, and a user with no passkey cannot ask for its options.
	if _, err := svc.Reauthenticate(ctx, p, ReauthPassword, mfaPassword, ""); !errors.Is(err, ErrReauthMethod) {
		t.Errorf("password with a passkey: %v", err)
	}
}

func TestTheOnlyCredentialOfAnAdministratorStays(t *testing.T) {
	ctx := context.Background()
	svc, store, u := passkeyFixture(t)
	svc.Settings.Roles = map[string]registry.Role{"owner": {Admin: registry.AllRights, Strategies: []string{"owner_passkey"}}}
	svc.Settings.Strategies = []registry.Strategy{{Name: "owner_passkey", Methods: []string{"passkey"}}, {Name: "password", Methods: []string{"password"}}}
	if err := store.AddRoles(ctx, u.ID, []string{"owner"}, *new(time.Time)); err != nil {
		t.Fatal(err)
	}
	_, info := addPasskey(t, svc, u, "Only")
	// The role accepts only the passkey strategy: the passkey is the administrator's way in.
	if err := svc.RemovePasskey(ctx, u, info.ID); !errors.Is(err, ErrCredentialRequired) {
		t.Fatalf("the only credential: %v", err)
	}
	// With a second passkey the first can go.
	addPasskey(t, svc, u, "Second")
	if err := svc.RemovePasskey(ctx, u, info.ID); err != nil {
		t.Errorf("one of two: %v", err)
	}
}

func TestTheAdminResetRemovesPasskeysToo(t *testing.T) {
	ctx := context.Background()
	svc, _, u := passkeyFixture(t)
	if err := svc.ResetMFA(ctx, u.ID, "admin-1"); !errors.Is(err, ErrMFANotEnabled) {
		t.Fatalf("nothing to reset: %v", err)
	}
	addPasskey(t, svc, u, "A")
	addPasskey(t, svc, u, "B")
	if err := svc.ResetMFA(ctx, u.ID, "admin-1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.PasskeyCount(ctx, u.ID); n != 0 {
		t.Errorf("%d passkeys survive the reset", n)
	}
}

func TestAPasskeyFirstStepKeepsItsIdThroughATotpStep(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	f.svc.Settings.Passkeys = registry.PasskeySettings{Enabled: true, RPID: "example.com", Origins: []string{passkeyOrigin}}
	f.svc.Settings.Strategies = []registry.Strategy{{Name: "pk_totp", Methods: []string{"passkey", "totp"}}}
	a, info := addPasskeyWith(t, f.svc, f.user, "Key", func(a *passkeytest.Authenticator) { a.BE = true })
	out, err := passkeySignIn(t, f.svc, a, "", "")
	if err != nil || out.Challenge == nil || !slices.Equal(out.Challenge.Next, []string{"totp"}) {
		t.Fatalf("first step: %+v %v", out, err)
	}
	v, err := f.svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, f.code(t, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	s := v.Principal.Session
	if s.Passkey != info.ID || !s.PasskeySynced || !slices.Equal(s.Methods, []string{"passkey", "totp"}) {
		t.Errorf("session = %+v", s)
	}
}
