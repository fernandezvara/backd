package auth_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/passkey/passkeytest"
	"github.com/fernandezvara/backd/internal/registry"
)

// passkeySignIn runs both calls of a sign-in with the authenticator.
func passkeySignIn(t *testing.T, svc *Users, a *passkeytest.Authenticator, strategy, org string) (Redeemed, error) {
	t.Helper()
	opts, err := svc.BeginPasskeySignIn(context.Background(), strategy, org, "10.0.0.1")
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	return svc.SignInWithPasskey(context.Background(), PasskeySignIn{Ceremony: opts.Ceremony, Credential: a.Assert(t, opts.Options), IP: "10.0.0.1", Org: org})
}

func withPasskeyStrategies(svc *Users, list ...registry.Strategy) {
	svc.Settings.Strategies = append([]registry.Strategy{{Name: "password", Methods: []string{"password"}}}, list...)
}

func TestAPasskeyAloneSignsInAndCountsAsAnotherFactor(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "users_passkey", Methods: []string{"passkey"}})
	a, info := addPasskeyWith(t, svc, u, "Phone", func(a *passkeytest.Authenticator) { a.BE = true }) // it syncs
	out, err := passkeySignIn(t, svc, a, "", "")
	if err != nil || out.Challenge != nil {
		t.Fatalf("sign in: %+v %v", out, err)
	}
	s := out.Principal.Session
	if !slices.Equal(s.Methods, []string{"passkey"}) || !s.MFA || s.Strategy != "users_passkey" || s.Passkey != info.ID || !s.PasskeySynced || out.Principal.User.ID != u.ID {
		t.Errorf("session = %+v", s)
	}
	// Passkeys are a way to sign in now.
	if m, _ := svc.SignInMethods(context.Background(), u); !slices.Contains(m, "passkey") {
		t.Errorf("sign-in methods = %v", m)
	}
	// Removing the passkey ends the session it opened.
	if err := svc.RemovePasskey(context.Background(), u, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), out.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("the session of a removed passkey: %v", err)
	}
}

func TestASyncedPasskeyIsToldApartFromAHardwareKey(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "users_passkey", Methods: []string{"passkey"}})
	hw, _ := addPasskey(t, svc, u, "YubiKey") // BE false: a hardware key
	out, err := passkeySignIn(t, svc, hw, "", "")
	if err != nil || out.Principal.Session.PasskeySynced {
		t.Fatalf("hardware key: %+v %v", out.Principal.Session, err)
	}
	caller := Caller{User: &out.Principal}
	if caller.SignInPasskeySynced() {
		t.Error("a hardware key is not synced")
	}
}

func TestAPasskeyFollowedByAStepAsksForIt(t *testing.T) {
	svc, _, _, u := enrollFixture(t)
	svc.Settings.Passkeys = registry.PasskeySettings{Enabled: true, RPID: "example.com", Origins: []string{passkeyOrigin}}
	f := &mfaFixture{svc: svc, user: u}
	_ = f
	withPasskeyStrategies(svc, registry.Strategy{Name: "pk_totp", Methods: []string{"passkey", "totp"}})
	a, _ := addPasskey(t, svc, u, "Phone")
	// No authenticator yet: the strategy sends the person to enrol one.
	out, err := passkeySignIn(t, svc, a, "pk_totp", "")
	if err != nil || out.Challenge == nil || out.Challenge.Kind != ChallengeEnroll || out.Challenge.Strategy != "pk_totp" || out.Token != "" {
		t.Fatalf("outcome: %+v %v", out, err)
	}
}

func TestEveryFailureIsTheSameAndIsAudited(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		setup func(*Users, User, *passkeytest.Authenticator)
	}{
		"a wrong origin":       {func(_ *Users, _ User, a *passkeytest.Authenticator) { a.Origin = "https://evil.example" }},
		"a wrong signature":    {func(_ *Users, _ User, a *passkeytest.Authenticator) { a.WrongKey = true }},
		"no user verification": {func(_ *Users, _ User, a *passkeytest.Authenticator) { a.UV = false }},
		"a banned user":        {func(s *Users, u User, _ *passkeytest.Authenticator) { disable(t, s, u) }},
		"an unverified address": {func(s *Users, u User, _ *passkeytest.Authenticator) {
			s.Settings.Account.RequireVerifiedEmail = true
			unverify(t, s, u)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, u := passkeyFixture(t)
			withPasskeyStrategies(svc, registry.Strategy{Name: "users_passkey", Methods: []string{"passkey"}})
			a, _ := addPasskey(t, svc, u, "Phone")
			tc.setup(svc, u, a)
			if _, err := passkeySignIn(t, svc, a, "", ""); !errors.Is(err, ErrInvalidPasskey) {
				t.Fatalf("error = %v", err)
			}
			if !auditHas(t, svc, AuditPasskeyLoginFailed) {
				t.Error("the failure is not in the audit log")
			}
		})
	}
	_ = ctx
}

func TestAnUnknownCredentialAndAnExpiredChallengeAreInvalidToo(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "users_passkey", Methods: []string{"passkey"}})
	addPasskey(t, svc, u, "Phone")
	stranger := passkeytest.New(passkeyOrigin)
	if _, err := passkeySignIn(t, svc, stranger, "", ""); !errors.Is(err, ErrInvalidPasskey) {
		t.Errorf("unknown credential: %v", err)
	}
	if _, err := svc.SignInWithPasskey(context.Background(), PasskeySignIn{Ceremony: "nope", Credential: []byte(`{}`), IP: "10.0.0.1"}); !errors.Is(err, ErrInvalidPasskey) {
		t.Errorf("no ceremony: %v", err)
	}
}

func TestStrategyChoiceAndDisabledPasskeys(t *testing.T) {
	svc, _, _ := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "a", Methods: []string{"passkey"}}, registry.Strategy{Name: "b", Methods: []string{"passkey", "totp"}})
	ctx := context.Background()
	if _, err := svc.BeginPasskeySignIn(ctx, "", "", "1.1.1.1"); !errors.Is(err, ErrStrategyRequired) {
		t.Errorf("two start with passkey: %v", err)
	}
	if _, err := svc.BeginPasskeySignIn(ctx, "password", "", "1.1.1.1"); !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("a strategy that starts with another method: %v", err)
	}
	if o, err := svc.BeginPasskeySignIn(ctx, "b", "", "1.1.1.1"); err != nil || o.Strategy != "b" || len(o.Options) == 0 {
		t.Errorf("named: %+v %v", o, err)
	}
	svc.Settings.Passkeys.Enabled = false
	if _, err := svc.BeginPasskeySignIn(ctx, "a", "", "1.1.1.1"); !errors.Is(err, ErrPasskeysDisabled) {
		t.Errorf("disabled: %v", err)
	}
}

func TestTheOptionsCallsAreCountedPerAddress(t *testing.T) {
	svc, _, _ := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "a", Methods: []string{"passkey"}})
	var limited error
	for range 80 {
		if _, err := svc.BeginPasskeySignIn(context.Background(), "a", "", "9.9.9.9"); err != nil {
			limited = err
			break
		}
	}
	if limited == nil {
		t.Fatal("a flood of options was never limited")
	}
	// Another address is not affected.
	if _, err := svc.BeginPasskeySignIn(context.Background(), "a", "", "8.8.8.8"); err != nil {
		t.Errorf("another address: %v", err)
	}
}

func TestFailuresCountPerAddressAndPerUser(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "a", Methods: []string{"passkey"}})
	a, _ := addPasskey(t, svc, u, "Phone")
	a.WrongKey = true // the credential is known; the signature is wrong
	var locked *MFALockedError
	var throttled *ThrottledError
	for range 40 {
		_, err := passkeySignIn(t, svc, a, "", "")
		if errors.As(err, &locked) || errors.As(err, &throttled) {
			break
		}
	}
	if locked == nil && throttled == nil {
		t.Fatal("forty failures were never limited")
	}
}

func TestACounterGoingBackwardsFailsAndWarns(t *testing.T) {
	svc, _, u := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "a", Methods: []string{"passkey"}})
	a, _ := addPasskey(t, svc, u, "Hardware")
	if _, err := passkeySignIn(t, svc, a, "", ""); err != nil {
		t.Fatal(err)
	}
	a.Counter = 1 // the key was cloned and the clone is behind
	if _, err := passkeySignIn(t, svc, a, "", ""); !errors.Is(err, ErrInvalidPasskey) {
		t.Fatalf("regressed counter: %v", err)
	}
	if !auditHas(t, svc, AuditPasskeyCloneSuspected) || !slices.Contains(notices(t, svc), "security-notice:passkey-clone-suspected") {
		t.Errorf("audit/notice missing: %v", notices(t, svc))
	}
}

func TestAPasskeyThroughAnOrganizationNeedsMembership(t *testing.T) {
	svc, store, u := passkeyFixture(t)
	withPasskeyStrategies(svc, registry.Strategy{Name: "a", Methods: []string{"passkey"}})
	a, _ := addPasskey(t, svc, u, "Phone")
	_ = store
	// A realm without organizations has no organization to sign in to.
	if _, err := passkeySignIn(t, svc, a, "", "acme"); !errors.Is(err, ErrInvalidPasskey) {
		t.Errorf("an organization that is not there: %v", err)
	}
}

func disable(t *testing.T, svc *Users, u User) {
	t.Helper()
	if err := svc.SetDisabled(context.Background(), u.Email, true); err != nil {
		t.Fatal(err)
	}
}

func unverify(t *testing.T, svc *Users, u User) {
	t.Helper()
	if err := svc.SetEmailVerified(context.Background(), u.Email, false); err != nil {
		t.Fatal(err)
	}
}

// auditHas says the realm's audit log holds an event.
func auditHas(t *testing.T, svc *Users, action string) bool {
	t.Helper()
	recs, _, err := svc.Store.ListAudit(context.Background(), AuditFilter{Action: action, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return len(recs) > 0
}
