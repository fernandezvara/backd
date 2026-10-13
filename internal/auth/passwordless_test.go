package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestRemovingThePasswordNeedsAnotherWayIn(t *testing.T) {
	ctx := context.Background()
	svc, store, clock, u := enrollFixture(t)
	if err := svc.SetEmailVerified(ctx, "ada@example.com", false); err != nil {
		t.Fatal(err)
	}
	p, _, err := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	// Alone: nothing else to sign in with.
	if err := svc.RemovePassword(ctx, p); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("only the password: %v", err)
	}
	// Magic links need a verified address, and being on.
	svc.Settings.MagicLink = registry.MagicLinkSettings{Enabled: true}
	if err := svc.RemovePassword(ctx, p); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("an unverified address: %v", err)
	}
	if err := svc.SetEmailVerified(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	other, tok2, _ := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	_ = other
	if err := svc.RemovePassword(ctx, p); err != nil {
		t.Fatalf("with magic links and a verified address: %v", err)
	}
	if _, err := store.IdentityOf(ctx, u.ID, ProviderPassword); !errors.Is(err, ErrNotFound) {
		t.Error("the password is still there")
	}
	// Other sessions ended with the password; the one that asked stays.
	if _, err := svc.Authenticate(ctx, tok2); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("another session after removing the password: %v", err)
	}
	// A password can't sign in any more, and removing it again is a 404.
	if _, _, err := svc.Login(ctx, "ada@example.com", mfaPassword, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("login with the removed password: %v", err)
	}
	if err := svc.RemovePassword(ctx, p); !errors.Is(err, ErrNotFound) {
		t.Errorf("again: %v", err)
	}
	methods, _ := svc.SignInMethods(ctx, u)
	if strings.Join(methods, ",") != "magic_link" {
		t.Errorf("methods: %v", methods)
	}
	// Setting one again is an ordinary change of password with its own notice.
	if err := svc.ChangePassword(ctx, p, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!2", ""); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
	if methods, _ = svc.SignInMethods(ctx, u); strings.Join(methods, ",") != "password,magic_link" {
		t.Errorf("methods after: %v", methods)
	}
	trail, _, _ := svc.AuditTrail(ctx, AuditFilter{Limit: 50})
	var got []string
	for _, r := range trail {
		got = append(got, r.Action)
	}
	have := strings.Join(got, " ")
	for _, want := range []string{AuditPasswordRemoved, AuditPasswordSet} {
		if !strings.Contains(have, want) {
			t.Errorf("no %s in the audit: %s", want, have)
		}
	}
	noticed := strings.Join(notices(t, svc), " ")
	if !strings.Contains(noticed, "password-removed") || !strings.Contains(noticed, "password-set") {
		t.Errorf("notices: %s", noticed)
	}
	_ = clock
	// Turning magic links off leaves the password the only way again.
	svc.Settings.MagicLink.Enabled = false
	p2, _, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!2", "")
	if err := svc.RemovePassword(ctx, p2); !errors.Is(err, ErrLastSignInMethod) {
		t.Errorf("magic links off: %v", err)
	}
}

func TestProvidersAndMagicLinksCountAsWaysToSignIn(t *testing.T) {
	ctx := context.Background()
	svc, _, _, u := enrollFixture(t)
	if _, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "google", Subject: "g-ada", Email: "ada@example.com", EmailVerified: true, TrustsEmail: true, Intent: IntentSignIn}); err != nil {
		t.Fatal(err)
	}
	// Password and Google: either may go, never both.
	if err := svc.Unlink(ctx, u.ID, "google", "self"); err != nil {
		t.Fatalf("google with a password left: %v", err)
	}
	if _, err := svc.ResolveProviderLogin(ctx, ProviderLogin{Provider: "google", Subject: "g-ada", Email: "ada@example.com", EmailVerified: true, TrustsEmail: true, Intent: IntentSignIn}); err != nil {
		t.Fatal(err)
	}
	p, _, _ := svc.Login(ctx, "ada@example.com", mfaPassword, "")
	if err := svc.RemovePassword(ctx, p); err != nil {
		t.Fatalf("a password with google left: %v", err)
	}
	if err := svc.Unlink(ctx, u.ID, "google", "self"); !errors.Is(err, ErrLastSignInMethod) {
		t.Errorf("the last provider: %v", err)
	}
	// The mailbox is a way in while links are on and the address is verified.
	svc.Settings.MagicLink = registry.MagicLinkSettings{Enabled: true}
	if err := svc.SetEmailVerified(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Unlink(ctx, u.ID, "google", "admin"); err != nil {
		t.Errorf("google with magic links left: %v", err)
	}
	// The email identity is not something to unlink.
	if err := svc.Unlink(ctx, u.ID, ProviderEmail, "self"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the email method: %v", err)
	}
}
