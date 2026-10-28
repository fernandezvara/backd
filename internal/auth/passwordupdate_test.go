package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
)

func TestAPasswordIsUpdatedWithTheCurrentOne(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	// A session that must end.
	if _, _, err := svc.Login(ctx, u.Email, mfaPassword, ""); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	in := PasswordUpdate{Email: u.Email, Password: mfaPassword, NewPassword: "dev-p4ssw0rd!2"}
	if err := svc.UpdatePassword(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!2", ""); err != nil {
		t.Errorf("the new password: %v", err)
	}
	if _, err := svc.SignIn(ctx, u.Email, mfaPassword, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the old password: %v", err)
	}
	if list, err := svc.Store.ListSessions(ctx, u.ID); err != nil || len(list) != 1 { // only the sign-in just above
		t.Errorf("sessions after the update: %d, %v", len(list), err)
	}
}

func TestAnIndividuallyExpiredPasswordCanBeUpdated(t *testing.T) {
	ctx := context.Background()
	svc, _, clock, u := enrollFixture(t)
	*clock = clock.Add(time.Hour)
	if err := svc.ExpirePassword(ctx, u.Email, "support case"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, u.Email, mfaPassword, ""); !errors.Is(err, ErrPasswordExpired) {
		t.Fatalf("expired: %v", err)
	}
	*clock = clock.Add(time.Minute)
	if err := svc.UpdatePassword(ctx, PasswordUpdate{Email: u.Email, Password: mfaPassword, NewPassword: "dev-p4ssw0rd!2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!2", ""); err != nil {
		t.Errorf("after the update: %v", err)
	}
}

func TestAPasswordExpiredForTheRealmCannotBeUpdatedWithItself(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	*clock = clock.Add(time.Hour)
	if ok, err := store.SetPasswordEpoch(ctx, PasswordEpoch{ValidAfter: *clock, SetBy: "cli", SetAt: *clock}); !ok || err != nil {
		t.Fatal(ok, err)
	}
	*clock = clock.Add(time.Minute)
	err := svc.UpdatePassword(ctx, PasswordUpdate{Email: u.Email, Password: mfaPassword, NewPassword: "dev-p4ssw0rd!2"})
	var pe *PasswordExpiredError
	if !errors.As(err, &pe) || !pe.EmailQueued {
		t.Fatalf("a leaked password: %v", err)
	}
	if _, err := svc.SignIn(ctx, u.Email, "dev-p4ssw0rd!2", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the refused update changed the password: %v", err)
	}
}

func TestUpdatingAPasswordRefusesWhatSigningInRefuses(t *testing.T) {
	ctx := context.Background()
	svc, _, _, u := enrollFixture(t)
	for name, in := range map[string]PasswordUpdate{
		"wrong password": {Email: u.Email, Password: "dev-p4ssw0rd!0", NewPassword: "dev-p4ssw0rd!2"},
		"unknown user":   {Email: "nobody@example.com", Password: mfaPassword, NewPassword: "dev-p4ssw0rd!2"},
	} {
		if err := svc.UpdatePassword(ctx, in); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var pol *PolicyError
	if err := svc.UpdatePassword(ctx, PasswordUpdate{Email: u.Email, Password: mfaPassword, NewPassword: "short"}); !errors.As(err, &pol) {
		t.Errorf("a weak password: %v", err)
	}
	if err := svc.UpdatePassword(ctx, PasswordUpdate{Email: u.Email, Password: mfaPassword, NewPassword: mfaPassword}); !errors.As(err, &pol) {
		t.Errorf("the same password: %v", err)
	}
	// Wrong proofs are throttled like wrong sign-ins.
	var throttled *ThrottledError
	for range 50 {
		err := svc.UpdatePassword(ctx, PasswordUpdate{Email: u.Email, Password: "dev-p4ssw0rd!0", NewPassword: "dev-p4ssw0rd!2"})
		if errors.As(err, &throttled) {
			return
		}
	}
	t.Error("fifty wrong passwords were never throttled")
}

func TestUpdatingThePasswordOfAnAccountWithASecondFactorNeedsACode(t *testing.T) {
	ctx := context.Background()
	f := newMFAFixture(t)
	in := PasswordUpdate{Email: f.user.Email, Password: mfaPassword, NewPassword: "dev-p4ssw0rd!2"}
	if err := f.svc.UpdatePassword(ctx, in); !errors.Is(err, ErrCodeRequired) {
		t.Fatalf("no code: %v", err)
	}
	in.Code = "000000"
	var bad *InvalidCodeError
	if err := f.svc.UpdatePassword(ctx, in); !errors.As(err, &bad) {
		t.Fatalf("wrong code: %v", err)
	}
	in.Method = "sms"
	if err := f.svc.UpdatePassword(ctx, in); !errors.Is(err, ErrReauthMethod) {
		t.Fatalf("unknown method: %v", err)
	}
	in.Method, in.Code = "", f.code(t, 0)
	if err := f.svc.UpdatePassword(ctx, in); err != nil {
		t.Fatalf("right code: %v", err)
	}
	// A recovery code works too, once.
	in.Password, in.NewPassword, in.Method, in.Code = "dev-p4ssw0rd!2", "dev-p4ssw0rd!3", MethodRecovery, f.codes[0]
	if err := f.svc.UpdatePassword(ctx, in); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	in.Password, in.NewPassword = "dev-p4ssw0rd!3", "dev-p4ssw0rd!4"
	if err := f.svc.UpdatePassword(ctx, in); !errors.As(err, &bad) {
		t.Fatalf("a used recovery code: %v", err)
	}
}
