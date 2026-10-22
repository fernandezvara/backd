package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
)

const expiryPassword = mfaPassword

func resetJobs(t *testing.T, svc *Users) int {
	t.Helper()
	jobs, _, err := svc.Jobs(context.Background(), JobFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, j := range jobs {
		if j.Email != nil && j.Email.Kind == "password-expired" {
			n++
		}
	}
	return n
}

func TestAPasswordSetBeforeTheEpochIsExpired(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	signIn := func() error { _, err := svc.SignIn(ctx, u.Email, expiryPassword, ""); return err }
	if err := signIn(); err != nil {
		t.Fatalf("before the epoch: %v", err)
	}

	*clock = clock.Add(time.Hour)
	if ok, err := store.SetPasswordEpoch(ctx, PasswordEpoch{ValidAfter: *clock, SetBy: "cli", SetAt: *clock}); !ok || err != nil {
		t.Fatalf("set epoch: %v %v", ok, err)
	}
	err := signIn()
	var pe *PasswordExpiredError
	if !errors.Is(err, ErrPasswordExpired) || !errors.As(err, &pe) || !pe.EmailQueued {
		t.Fatalf("expired password: %v", err)
	}
	// A wrong password is still a wrong password: the answer doesn't tell it expired.
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!0", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := svc.SignIn(ctx, "nobody@example.com", expiryPassword, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: %v", err)
	}
	// Expired attempts are not failed attempts: they never lock the account.
	for range 20 {
		if err := signIn(); !errors.Is(err, ErrPasswordExpired) {
			t.Fatalf("repeat: %v", err)
		}
	}
	if resetJobs(t, svc) == 0 {
		t.Error("the refused sign-in queued no reset email")
	}

	// A new password after the epoch is valid again, and so is one set by an administrator.
	*clock = clock.Add(time.Minute)
	if err := svc.SetPassword(ctx, u.Email, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!2", ""); err != nil {
		t.Errorf("after set-password: %v", err)
	}
	if _, err := svc.SignIn(ctx, u.Email, expiryPassword, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the old password: %v", err)
	}
}

func TestTheEpochOnlyMovesForward(t *testing.T) {
	ctx := context.Background()
	_, store, clock, _ := enrollFixture(t)
	later := clock.Add(time.Hour)
	if ok, _ := store.SetPasswordEpoch(ctx, PasswordEpoch{ValidAfter: later}); !ok {
		t.Fatal("first epoch refused")
	}
	if ok, _ := store.SetPasswordEpoch(ctx, PasswordEpoch{ValidAfter: *clock}); ok {
		t.Error("an earlier epoch replaced a later one")
	}
	if e, _ := store.PasswordEpoch(ctx); !e.ValidAfter.Equal(later) {
		t.Errorf("epoch = %v", e.ValidAfter)
	}
}

func TestOneUsersPasswordCanBeExpired(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, ada := enrollFixture(t)
	bob, err := svc.Create(ctx, "bob@example.com", ptr(expiryPassword))
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	if err := svc.ExpirePassword(ctx, ada.Email, "support case"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, ada.Email, expiryPassword, ""); !errors.Is(err, ErrPasswordExpired) {
		t.Errorf("expired user: %v", err)
	}
	if _, err := svc.SignIn(ctx, bob.Email, expiryPassword, ""); err != nil {
		t.Errorf("another user: %v", err)
	}
	// Reauthentication with the password refuses an expired one too.
	p, _, err := svc.Login(ctx, bob.Email, expiryPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	if err := svc.ExpirePassword(ctx, bob.Email, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reauthenticate(ctx, p, ReauthPassword, expiryPassword, ""); !errors.Is(err, ErrPasswordExpired) {
		t.Errorf("reauth: %v", err)
	}
	// ... and the account recovers with a reset: the new password counts.
	*clock = clock.Add(time.Minute)
	if err := svc.SetPassword(ctx, ada.Email, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, ada.Email, "dev-p4ssw0rd!2", ""); err != nil {
		t.Errorf("after the new password: %v", err)
	}
}

func TestIdentitiesWithoutAChangeTimeCountAsChangedWhenCreated(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	id, err := store.IdentityOf(ctx, u.ID, ProviderPassword)
	if err != nil || id.PasswordChangedAt.IsZero() {
		t.Fatalf("a created password records its time: %+v %v", id, err)
	}
	// An identity from before the field existed: put it back without the time.
	id.PasswordChangedAt = time.Time{}
	if err := store.ForgetPasswordChangedAt(id); err != nil {
		t.Fatal(err)
	}
	*clock = id.CreatedAt.Add(-time.Hour)
	_, _ = store.SetPasswordEpoch(ctx, PasswordEpoch{ValidAfter: id.CreatedAt.Add(time.Hour)})
	*clock = id.CreatedAt.Add(2 * time.Hour)
	if _, err := svc.SignIn(ctx, u.Email, expiryPassword, ""); !errors.Is(err, ErrPasswordExpired) {
		t.Errorf("legacy identity: %v", err)
	}
}

func TestAnExpiredPasswordDoesNotStartTheSecondFactor(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	*f.clock = f.clock.Add(time.Hour)
	if err := f.svc.ExpirePassword(ctx, f.user.Email, ""); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.SignIn(ctx, f.user.Email, mfaPassword, "")
	if !errors.Is(err, ErrPasswordExpired) || out.Challenge != nil {
		t.Errorf("= %+v, %v", out, err)
	}
}

func TestExpiringEveryPasswordEndsTheSessions(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, ada := enrollFixture(t)
	if _, err := svc.Create(ctx, "bob@example.com", ptr(expiryPassword)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "nopass@example.com", nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{ada.Email, "bob@example.com"} {
		if _, _, err := svc.Login(ctx, e, expiryPassword, ""); err != nil {
			t.Fatal(err)
		}
	}

	*clock = clock.Add(time.Hour)
	res, err := svc.ExpireAllPasswords(ctx, "leaked backup", false)
	if err != nil || res.Accounts != 2 || res.Sessions != 2 || !res.ValidAfter.Equal(*clock) {
		t.Fatalf("= %+v, %v", res, err)
	}
	if n, _ := store.DeleteAllSessions(ctx); n != 0 {
		t.Errorf("%d sessions left", n)
	}
	for _, e := range []string{ada.Email, "bob@example.com"} {
		if _, err := svc.SignIn(ctx, e, expiryPassword, ""); !errors.Is(err, ErrPasswordExpired) {
			t.Errorf("%s: %v", e, err)
		}
	}
	// The epoch only moves forward: the same instant is refused, a later one is not.
	if _, err := svc.ExpireAllPasswords(ctx, "", false); !errors.Is(err, ErrEpochNotLater) {
		t.Errorf("same instant: %v", err)
	}
	*clock = clock.Add(time.Minute)
	if _, err := svc.ExpireAllPasswords(ctx, "", false); err != nil {
		t.Errorf("later: %v", err)
	}
	recs, _, err := svc.AuditTrail(ctx, AuditFilter{Action: AuditPasswordExpire, Limit: 10})
	if err != nil || len(recs) != 2 {
		t.Fatalf("audit: %v %v", recs, err)
	}
	var found bool
	for _, r := range recs {
		if r.Details["reason"] == "leaked backup" && r.Details["accounts"] == int64(2) {
			found = true
		}
	}
	if !found {
		t.Errorf("audit details: %+v", recs)
	}
}

func TestExpiringEveryPasswordCanKeepTheSessions(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, ada := enrollFixture(t)
	p, _, err := svc.Login(ctx, ada.Email, expiryPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	res, err := svc.ExpireAllPasswords(ctx, "planned rotation", true)
	if err != nil || res.Sessions != 0 {
		t.Fatalf("= %+v, %v", res, err)
	}
	if left, _ := store.ListSessions(ctx, ada.ID); len(left) != 1 || left[0].ID != p.Session.ID {
		t.Errorf("sessions left: %+v", left)
	}
	n, err := svc.RevokeAllSessions(ctx)
	if err != nil || n != 1 {
		t.Errorf("revoke all: %d %v", n, err)
	}
	// Passwords are untouched by revoking sessions: the earlier epoch still applies.
	if _, err := svc.SignIn(ctx, ada.Email, expiryPassword, ""); !errors.Is(err, ErrPasswordExpired) {
		t.Errorf("after revoke: %v", err)
	}
}

// A second step that was waiting when the passwords were expired must not turn into a session.
func TestAChallengeMadeBeforeTheExpiryGivesNoSession(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	challenge := f.challenge(t)
	*f.clock = f.clock.Add(time.Minute)
	if _, err := f.svc.ExpireAllPasswords(ctx, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyMFA(ctx, challenge, MethodTOTP, f.code(t, 0), ""); !errors.Is(err, ErrPasswordExpired) {
		t.Fatalf("the second step after the expiry: %v", err)
	}
	if sessions, _ := f.store.ListSessions(ctx, f.user.ID); len(sessions) != 0 {
		t.Errorf("%d sessions after an expired first factor", len(sessions))
	}
	// A new password, then a new sign-in, is fine.
	*f.clock = f.clock.Add(time.Minute)
	if err := f.svc.SetPassword(ctx, f.user.Email, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.SignIn(ctx, f.user.Email, "dev-p4ssw0rd!2", "")
	if err != nil || out.Challenge == nil {
		t.Fatalf("sign in again: %+v %v", out, err)
	}
}

// A first factor that is not a password has nothing to expire.
func TestAChallengeOfAnotherFirstFactorIsNotRefused(t *testing.T) {
	f := newMFAFixture(t)
	if err := f.svc.RefuseExpiredFactor(context.Background(), f.user, "google"); err != nil {
		t.Errorf("= %v", err)
	}
}

func TestUsingAResetLinkUsesUpTheOthers(t *testing.T) {
	ctx := context.Background()
	svc, _, _, ada := enrollFixture(t)
	first, _, err := svc.CreateResetLink(ctx, ada.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := svc.CreateResetLink(ctx, ada.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetPassword(ctx, first, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetPassword(ctx, second, "dev-p4ssw0rd!3"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the other link after one was used: %v", err)
	}
}
