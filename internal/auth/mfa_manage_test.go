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

// enrollFixture is a realm with email and a user with no second factor.
func enrollFixture(t *testing.T) (*Users, *authtest.MemStore, *time.Time, User) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Realm = "acme"
	svc.Cipher, _ = NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc.Settings.Email = &registry.EmailSettings{Function: "app/deliver", From: "a@example.com", DefaultLocale: "en", Limits: registry.EmailLimits{PerKindPerHour: 100, PerDay: 100, PerIPPerHour: 100}}
	u, err := svc.Create(context.Background(), "ada@example.com", ptr(mfaPassword))
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, &clock, u
}

func totpAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func notices(t *testing.T, svc *Users) []string {
	t.Helper()
	jobs, _, err := svc.Jobs(context.Background(), JobFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, j := range jobs {
		if j.Email != nil {
			out = append(out, j.Email.Kind+":"+j.Email.Event)
		}
	}
	return out
}

func TestEnrollingTOTP(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	other, _, err := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	here, hereToken, _ := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	_ = other

	st, err := svc.MFAStatusOf(ctx, u)
	if err != nil || st.Enabled || !st.Available || len(st.Methods) != 0 || st.RecoveryCodesLeft != 0 {
		t.Fatalf("status before: %+v, %v", st, err)
	}
	setup, err := svc.BeginTOTPSetup(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(setup.URI, "otpauth://totp/acme:ada@example.com?") || !strings.Contains(setup.URI, "secret="+setup.Secret) ||
		!strings.Contains(setup.URI, "issuer=acme") || !strings.HasPrefix(setup.QR, "data:image/png;base64,") || len(setup.Secret) < 32 {
		t.Errorf("setup: %+v", setup)
	}
	// Nothing is on until the first code arrives, and a wrong one doesn't turn it on.
	var bad *InvalidCodeError
	if _, err := svc.ConfirmTOTP(ctx, u, here.Session.ID, setup.SetupID, "000000"); !errors.As(err, &bad) || bad.AttemptsLeft != 4 {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := store.MFAOf(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("MFA is on after a wrong code")
	}
	// Someone else's setup id is no use.
	otherUser, _ := svc.Create(ctx, "bob@example.com", ptr(mfaPassword))
	if _, err := svc.ConfirmTOTP(ctx, otherUser, "", setup.SetupID, totpAt(t, setup.Secret, *clock)); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("another user's setup: %v", err)
	}
	codes, err := svc.ConfirmTOTP(ctx, u, here.Session.ID, setup.SetupID, totpAt(t, setup.Secret, *clock))
	if err != nil || len(codes) != RecoveryCodeCount {
		t.Fatalf("confirm: %v %v", codes, err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || seen[c] || strings.ContainsAny(c, "01ILO") {
			t.Errorf("recovery code %q", c)
		}
		seen[c] = true
	}
	// Other sessions ended; this one stays.
	if _, err := svc.Authenticate(ctx, hereToken); err != nil {
		t.Errorf("the session that enabled it: %v", err)
	}
	if _, err := svc.Authenticate(ctx, ""); err == nil {
		t.Error("empty token")
	}
	sessions, _ := svc.UserSessions(ctx, u.ID)
	if len(sessions) != 1 {
		t.Errorf("sessions after enabling: %d", len(sessions))
	}
	// The recovery codes are stored as keyed hashes, never as typed.
	rec, _ := store.MFAOf(ctx, u.ID)
	if len(rec.RecoveryCodes) != RecoveryCodeCount || strings.Contains(strings.Join(rec.RecoveryCodes, ","), strings.ReplaceAll(codes[0], "-", "")) || rec.Secret == setup.Secret {
		t.Errorf("record leaks: %+v", rec)
	}
	st, _ = svc.MFAStatusOf(ctx, u)
	if !st.Enabled || st.RecoveryCodesLeft != RecoveryCodeCount || len(st.Methods) != 2 {
		t.Errorf("status after: %+v", st)
	}
	// The pending setup is spent, and a second enrollment is refused.
	if _, err := svc.ConfirmTOTP(ctx, u, "", setup.SetupID, totpAt(t, setup.Secret, *clock)); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("a spent setup: %v", err)
	}
	if _, err := svc.BeginTOTPSetup(ctx, u); !errors.Is(err, ErrMFAAlreadyEnabled) {
		t.Errorf("setup when enabled: %v", err)
	}
	// The code that enabled it can't be used to sign in right after (the step is spent).
	out, _ := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	if out.Challenge == nil {
		t.Fatal("no challenge after enabling")
	}
	if _, err := svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, totpAt(t, setup.Secret, *clock), ""); !errors.As(err, &bad) {
		t.Errorf("the enrolling code again: %v", err)
	}
	// Recovery codes sign in, once.
	out, _ = svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	if v, err := svc.VerifyMFA(ctx, out.Challenge.Token, MethodRecovery, strings.ToLower(codes[0]), ""); err != nil || *v.RecoveryCodesLeft != RecoveryCodeCount-1 {
		t.Errorf("recovery sign-in: %+v, %v", v, err)
	}
	got := strings.Join(notices(t, svc), " ")
	if !strings.Contains(got, "security-notice:mfa-enabled") || !strings.Contains(got, "security-notice:recovery-code-used") {
		t.Errorf("notices: %s", got)
	}
}

func TestSetupExpiresAndIsReplaced(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	first, _ := svc.BeginTOTPSetup(ctx, u)
	second, _ := svc.BeginTOTPSetup(ctx, u)
	if first.Secret == second.Secret {
		t.Error("the same secret twice")
	}
	if _, err := svc.ConfirmTOTP(ctx, u, "", first.SetupID, totpAt(t, first.Secret, *clock)); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("a replaced setup: %v", err)
	}
	*clock = clock.Add(TOTPSetupTTL + time.Minute)
	if _, err := svc.ConfirmTOTP(ctx, u, "", second.SetupID, totpAt(t, second.Secret, *clock)); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("an expired setup: %v", err)
	}
}

func TestSetupEndsAfterFiveWrongCodes(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	setup, _ := svc.BeginTOTPSetup(ctx, u)
	for range MaxChallengeAttempts {
		if _, err := svc.ConfirmTOTP(ctx, u, "", setup.SetupID, "000000"); err == nil {
			t.Fatal("accepted")
		}
	}
	if _, err := svc.ConfirmTOTP(ctx, u, "", setup.SetupID, totpAt(t, setup.Secret, *clock)); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("after five: %v", err)
	}
}

func TestDisablingAndRegenerating(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	if err := svc.DisableMFA(ctx, u); !errors.Is(err, ErrMFANotEnabled) {
		t.Errorf("disable when off: %v", err)
	}
	if _, err := svc.RegenerateRecoveryCodes(ctx, u); !errors.Is(err, ErrMFANotEnabled) {
		t.Errorf("regenerate when off: %v", err)
	}
	setup, _ := svc.BeginTOTPSetup(ctx, u)
	old, err := svc.ConfirmTOTP(ctx, u, "", setup.SetupID, totpAt(t, setup.Secret, *clock))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.RegenerateRecoveryCodes(ctx, u)
	if err != nil || len(fresh) != RecoveryCodeCount || fresh[0] == old[0] {
		t.Fatalf("regenerate: %v %v", fresh, err)
	}
	out, _ := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	var bad *InvalidCodeError
	if _, err := svc.VerifyMFA(ctx, out.Challenge.Token, MethodRecovery, old[0], ""); !errors.As(err, &bad) {
		t.Errorf("an old recovery code: %v", err)
	}
	// A role that requires MFA keeps it on.
	svc.Settings.MFA.RequiredAll = true
	if err := svc.DisableMFA(ctx, u); !errors.Is(err, ErrMFARequiredByRole) {
		t.Errorf("disable when required: %v", err)
	}
	svc.Settings.MFA.RequiredAll = false
	if err := svc.DisableMFA(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MFAOf(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Error("still enabled")
	}
	if p, _, err := svc.Login(ctx, "ada@example.com", mfaPassword, ""); err != nil || p.Session.MFA {
		t.Errorf("login after disabling: %v", err)
	}
	got := strings.Join(notices(t, svc), " ")
	for _, want := range []string{"mfa-enabled", "recovery-codes-regenerated", "mfa-disabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %s notice in %s", want, got)
		}
	}
}

func TestTurnedOffRealmRefusesEnrollmentButKeepsAsking(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	setup, _ := svc.BeginTOTPSetup(ctx, u)
	if _, err := svc.ConfirmTOTP(ctx, u, "", setup.SetupID, totpAt(t, setup.Secret, *clock)); err != nil {
		t.Fatal(err)
	}
	svc.Settings.MFA.Off = true
	if _, err := svc.BeginTOTPSetup(ctx, User{ID: "someone"}); !errors.Is(err, ErrMFADisabled) {
		t.Errorf("setup: %v", err)
	}
	// Turning the feature off never weakens an account that already has it.
	out, err := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil {
		t.Errorf("sign-in with the feature off: %+v %v", out, err)
	}
}

func TestWrongCodesAfterTheRightPasswordWarnTheUserOncePerHour(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	setup, _ := svc.BeginTOTPSetup(ctx, u)
	if _, err := svc.ConfirmTOTP(ctx, u, "", setup.SetupID, totpAt(t, setup.Secret, *clock)); err != nil {
		t.Fatal(err)
	}
	out, _ := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	for range 2 {
		_, _ = svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, "000000", "")
	}
	if strings.Contains(strings.Join(notices(t, svc), " "), "mfa-failed") {
		t.Fatal("warned after two")
	}
	_, _ = svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, "000000", "")
	count := func() int { return strings.Count(strings.Join(notices(t, svc), " "), "mfa-failed:") }
	if count() != 1 {
		t.Fatalf("after three: %d", count())
	}
	out, _ = svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	for range 3 {
		_, _ = svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, "000000", "")
	}
	if count() != 1 {
		t.Errorf("a second warning within the hour: %d", count())
	}
	*clock = clock.Add(61 * time.Minute)
	out, _ = svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	for range 3 {
		_, _ = svc.VerifyMFA(ctx, out.Challenge.Token, MethodTOTP, "000000", "")
	}
	if count() != 2 {
		t.Errorf("after the hour: %d", count())
	}
}

func TestEnrollingFromASignInThatMustSetItUp(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	svc.Settings.MFA.RequiredAll = true
	sessions := func() int { s, _ := svc.UserSessions(ctx, u.ID); return len(s) }
	out, err := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll {
		t.Fatalf("sign-in: %+v %v", out, err)
	}
	if sessions() != 0 {
		t.Fatal("a session exists before the second factor")
	}
	// An MFA challenge (or nothing) is not an enrollment challenge.
	if _, err := svc.BeginEnrollment(ctx, ""); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("empty: %v", err)
	}
	setup, err := svc.BeginEnrollment(ctx, out.Challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	var bad *InvalidCodeError
	if _, _, err := svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, "000000"); !errors.As(err, &bad) {
		t.Fatalf("wrong code: %v", err)
	}
	if sessions() != 0 {
		t.Fatal("a wrong code gave a session")
	}
	v, codes, err := svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, totpAt(t, setup.Secret, *clock))
	if err != nil || len(codes) != RecoveryCodeCount || v.Token == "" || !v.Principal.Session.MFA ||
		len(v.Principal.Session.Methods) != 2 || v.Principal.Session.Methods[1] != "totp" || v.Factor != ProviderPassword {
		t.Fatalf("complete: %+v %v %v", v, codes, err)
	}
	if _, err := store.MFAOf(ctx, u.ID); err != nil {
		t.Errorf("not enabled: %v", err)
	}
	// The sign-in ended: the challenge is spent.
	if _, err := svc.BeginEnrollment(ctx, out.Challenge.Token); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("a spent challenge: %v", err)
	}
	// From now on the user is asked for a code like anyone with a second factor.
	again, _ := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	if again.Challenge == nil || again.Challenge.Kind != ChallengeMFA {
		t.Errorf("next sign-in: %+v", again)
	}
}

func TestEnrollmentChallengeAllowsFiveWrongCodes(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, _ := enrollFixture(t)
	svc.Settings.MFA.RequiredAll = true
	out, _ := svc.SignIn(ctx, "ada@example.com", mfaPassword, "")
	setup, _ := svc.BeginEnrollment(ctx, out.Challenge.Token)
	for range MaxChallengeAttempts - 1 {
		if _, _, err := svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, "000000"); err == nil {
			t.Fatal("accepted")
		}
	}
	// The fifth wrong code ends the sign-in: even the right code is refused after.
	if _, _, err := svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, "000000"); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("fifth: %v", err)
	}
	if _, _, err := svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, totpAt(t, setup.Secret, *clock)); !errors.Is(err, ErrInvalidChallenge) {
		t.Errorf("after: %v", err)
	}
}

func TestHandOverSurvivesEnrollment(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	svc.Settings.MFA.RequiredAll = true
	out, err := svc.StartProviderSession(ctx, u, "google", true, map[string]string{"name": "Ada"})
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll {
		t.Fatalf("native: %+v %v", out, err)
	}
	setup, _ := svc.BeginEnrollment(ctx, out.Challenge.Token)
	v, _, err := svc.CompleteEnrollment(ctx, out.Challenge.Token, setup.SetupID, totpAt(t, setup.Secret, *clock))
	if err != nil || !v.NewUser || v.Profile["name"] != "Ada" || v.Factor != "google" || v.Principal.Session.Methods[0] != "google" {
		t.Errorf("hand-over: %+v %v", v, err)
	}
}
