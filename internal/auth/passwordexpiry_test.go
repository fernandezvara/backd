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
		if j.Email != nil && j.Email.Kind == "reset-password" {
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
	if err := signIn(); !errors.Is(err, ErrPasswordExpired) {
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
