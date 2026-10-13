package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
)

func TestReauthMethodsAreTheStrongestTheUserHas(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t) // a user with a password and a second factor
	f.svc.Settings.Email = nil
	methods, err := f.svc.ReauthMethodsOf(ctx, f.user)
	if err != nil || strings.Join(methods, ",") != "totp,recovery_code" {
		t.Errorf("MFA user: %v %v", methods, err)
	}
	plain, _ := f.svc.Create(ctx, "plain@example.com", ptr(mfaPassword))
	if methods, _ = f.svc.ReauthMethodsOf(ctx, plain); strings.Join(methods, ",") != "password" {
		t.Errorf("password user: %v", methods)
	}
	none, _ := f.svc.Create(ctx, "none@example.com", nil)
	if methods, _ = f.svc.ReauthMethodsOf(ctx, none); len(methods) != 0 || methods == nil {
		t.Errorf("no proof and no email: %v", methods)
	}
	f.svc.Settings.Email = enrollEmail()
	if methods, _ = f.svc.ReauthMethodsOf(ctx, none); strings.Join(methods, ",") != "email_code" {
		t.Errorf("no password with email: %v", methods)
	}
}

func TestReauthenticatingWithASecondFactor(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	v, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, 0), "")
	if err != nil {
		t.Fatal(err)
	}
	p := v.Principal
	// A password alone is not enough for a user with a second factor.
	if _, err := f.svc.Reauthenticate(ctx, p, ReauthPassword, mfaPassword, ""); !errors.Is(err, ErrReauthMethod) {
		t.Errorf("password: %v", err)
	}
	var bad *InvalidCodeError
	if _, err := f.svc.Reauthenticate(ctx, p, ReauthTOTP, "000000", ""); !errors.As(err, &bad) {
		t.Errorf("wrong code: %v", err)
	}
	// The step that signed in is spent; the next one proves them again.
	if _, err := f.svc.Reauthenticate(ctx, p, ReauthTOTP, f.code(t, 0), ""); !errors.As(err, &bad) {
		t.Errorf("the spent step: %v", err)
	}
	g, err := f.svc.Reauthenticate(ctx, p, ReauthTOTP, f.code(t, 30*time.Second), "")
	if err != nil || g.Token == "" || !g.Expires.Equal(f.clock.Add(5*time.Minute)) {
		t.Fatalf("reauth: %+v %v", g, err)
	}
	if err := f.svc.CheckReauth(ctx, p, g.Token); err != nil {
		t.Errorf("a good token: %v", err)
	}
	// Bound to the session, and not to be guessed or reused elsewhere.
	other, _ := f.svc.VerifyMFA(ctx, f.challenge(t), MethodRecovery, f.codes[0], "")
	for name, tok := range map[string]string{"another session": g.Token, "empty": "", "unknown": "nope"} {
		who := p
		if name == "another session" {
			who = other.Principal
		}
		if err := f.svc.CheckReauth(ctx, who, tok); !errors.Is(err, ErrReauthRequired) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// It lasts the realm's ttl and no longer.
	*f.clock = f.clock.Add(5*time.Minute + time.Second)
	if err := f.svc.CheckReauth(ctx, p, g.Token); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("an expired token: %v", err)
	}
	// A recovery code proves them too, and is spent like at sign-in.
	g, err = f.svc.Reauthenticate(ctx, p, ReauthRecovery, f.codes[1], "")
	if err != nil || g.Token == "" {
		t.Errorf("recovery: %v", err)
	}
	if _, err := f.svc.Reauthenticate(ctx, p, ReauthRecovery, f.codes[1], ""); !errors.As(err, &bad) {
		t.Errorf("recovery again: %v", err)
	}
}

func TestWrongProofsLockLikeSignIn(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	v, _ := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, 0), "")
	var locked *MFALockedError
	for range 10 {
		_, _ = f.svc.Reauthenticate(ctx, v.Principal, ReauthTOTP, "000000", "")
	}
	if _, err := f.svc.Reauthenticate(ctx, v.Principal, ReauthTOTP, f.code(t, 30*time.Second), ""); !errors.As(err, &locked) {
		t.Errorf("after ten wrong codes: %v", err)
	}
	// The lock is the user's, so signing in with a second factor waits too.
	if _, err := f.svc.VerifyMFA(ctx, f.challenge(t), MethodTOTP, f.code(t, 30*time.Second), ""); !errors.As(err, &locked) {
		t.Errorf("sign-in under the lock: %v", err)
	}
}

func TestSensitiveActionsAskNothingOfAUserWithNoProof(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := enrollFixture(t)
	svc.Settings.Email = nil
	u, _ := svc.Create(ctx, "p@example.com", nil)
	out, err := svc.StartProviderSession(ctx, u, "google", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckReauth(ctx, out.Principal, ""); err != nil {
		t.Errorf("no way to prove: %v", err)
	}
	if _, err := svc.Reauthenticate(ctx, out.Principal, ReauthPassword, "x", ""); !errors.Is(err, ErrReauthMethod) {
		t.Errorf("no password: %v", err)
	}
}

func TestEmailedCodeProvesAUserWithNothingElse(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, _ := enrollFixture(t)
	u, _ := svc.Create(ctx, "p@example.com", nil)
	out, _ := svc.StartProviderSession(ctx, u, "google", false, nil)
	p := out.Principal
	// A user with a password proves it with the password: no code for them.
	withPassword, _, _ := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	if err := svc.RequestReauthCode(ctx, withPassword, "", ""); !errors.Is(err, ErrReauthMethod) {
		t.Errorf("a password user asking for a code: %v", err)
	}
	// Nothing to check before one was asked for.
	if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, "123456", ""); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("before asking: %v", err)
	}
	if err := svc.RequestReauthCode(ctx, p, "", "req-1"); err != nil {
		t.Fatal(err)
	}
	jobs, _, _ := svc.Jobs(ctx, JobFilter{Limit: 10})
	if len(jobs) != 1 || jobs[0].Email == nil || jobs[0].Email.Kind != "reauth-code" || jobs[0].Email.ChallengeID == "" {
		t.Fatalf("jobs: %+v", jobs)
	}
	// The job holds no code: the worker makes it and keeps only its hash.
	code, expires, err := svc.MakeReauthCode(ctx, jobs[0].Email.ChallengeID)
	if err != nil || len(code) != 6 || !expires.Equal(clock.Add(ReauthCodeTTL)) {
		t.Fatalf("code: %q %v %v", code, expires, err)
	}
	c, _ := store.ChallengeOfUser(ctx, u.ID, ChallengeReauthCode, *clock)
	if c.CodeHash == "" || strings.Contains(c.CodeHash, code) {
		t.Errorf("stored: %+v", c)
	}
	var bad *InvalidCodeError
	if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, "000000", ""); !errors.As(err, &bad) || bad.AttemptsLeft != 4 {
		t.Errorf("wrong: %v", err)
	}
	// Another session of the same user can't use it.
	other, _ := svc.StartProviderSession(ctx, u, "google", false, nil)
	if _, err := svc.Reauthenticate(ctx, other.Principal, ReauthEmailCode, code, ""); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("another session: %v", err)
	}
	g, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, code, "")
	if err != nil || g.Token == "" {
		t.Fatalf("right code: %v", err)
	}
	if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, code, ""); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("the code twice: %v", err)
	}
	if err := svc.CheckReauth(ctx, p, g.Token); err != nil {
		t.Errorf("token: %v", err)
	}
}

func TestEmailedCodeEndsAfterFiveWrongTriesAndAsksAgain(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, _ := enrollFixture(t)
	u, _ := svc.Create(ctx, "p@example.com", nil)
	out, _ := svc.StartProviderSession(ctx, u, "google", false, nil)
	p := out.Principal
	ask := func() string {
		if err := svc.RequestReauthCode(ctx, p, "", ""); err != nil {
			t.Fatal(err)
		}
		jobs, _, _ := svc.Jobs(ctx, JobFilter{Limit: 10})
		code, _, err := svc.MakeReauthCode(ctx, jobs[0].Email.ChallengeID)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	code := ask()
	for range ReauthCodeAttempts {
		if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, "000000", ""); err == nil {
			t.Fatal("accepted")
		}
	}
	if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, code, ""); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("after five wrong tries: %v", err)
	}
	// A new request is a new code, and the earlier one stops working.
	first := ask()
	second := ask()
	if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, first, ""); err == nil && first != second {
		t.Error("an old code still works")
	}
	// A code that waited too long is gone.
	*clock = clock.Add(ReauthCodeTTL + time.Second)
	if _, err := svc.Reauthenticate(ctx, p, ReauthEmailCode, second, ""); !errors.Is(err, ErrReauthRequired) {
		t.Errorf("an expired code: %v", err)
	}
}
