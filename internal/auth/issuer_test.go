package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// A subject is only unique within one issuer (design/sso.md §6.1): an identity matches on the issuer
// that vouched for it, so a changed issuer never hands someone's account to a stranger.
func TestIdentityMatchesOnTheIssuerThatVouchedForIt(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := providerFixture(t)
	const a, b = "https://sso.a.example", "https://sso.b.example"
	login := func(issuer, subject, email string) auth.ProviderLogin {
		return auth.ProviderLogin{Provider: "keycloak", Issuer: issuer, Subject: subject, Email: email, EmailVerified: true, Intent: auth.IntentSignIn}
	}
	ada, err := svc.ResolveProviderLogin(ctx, login(a, "sub-1", "ada@example.com"))
	if err != nil || !ada.NewUser {
		t.Fatalf("ada: %+v, %v", ada, err)
	}
	ids, _ := svc.Identities(ctx, ada.User.ID)
	if len(ids) != 1 || ids[0].Issuer != a {
		t.Fatalf("the identity records its issuer: %+v", ids)
	}
	// The same person again, at the same issuer.
	if again, err := svc.ResolveProviderLogin(ctx, login(a, "sub-1", "ada@example.com")); err != nil || again.NewUser || again.User.ID != ada.User.ID {
		t.Errorf("same issuer: %+v, %v", again, err)
	}
	// Someone else who happens to have the same subject at another issuer is a stranger: a new
	// user, never Ada.
	eve, err := svc.ResolveProviderLogin(ctx, login(b, "sub-1", "eve@example.com"))
	if err != nil || !eve.NewUser || eve.User.ID == ada.User.ID {
		t.Fatalf("another issuer, same subject: %+v, %v", eve, err)
	}
	// And with Ada's address they are told the account exists, not signed in as Ada.
	if _, err := svc.ResolveProviderLogin(ctx, login(b, "sub-9", "ada@example.com")); !errors.Is(err, auth.ErrAccountExists) {
		t.Errorf("another issuer, Ada's address: %v", err)
	}
}

// A signed-in person who links the connection again replaces their own dormant identity; no
// sign-in does.
func TestLinkingReplacesADormantIdentityOnlyWhenSignedIn(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := providerFixture(t)
	const a, b = "https://sso.a.example", "https://sso.b.example"
	ada, err := svc.ResolveProviderLogin(ctx, auth.ProviderLogin{Provider: "keycloak", Issuer: a, Subject: "s-1", Email: "ada@example.com", EmailVerified: true, Intent: auth.IntentSignIn})
	if err != nil {
		t.Fatal(err)
	}
	// A password too, so the identity can go.
	link := func(issuer, subject string) error {
		_, err := svc.ResolveProviderLogin(ctx, auth.ProviderLogin{Provider: "keycloak", Issuer: issuer, Subject: subject, Email: "ada@example.com", EmailVerified: true, Intent: auth.IntentLink, LinkUserID: ada.User.ID})
		return err
	}
	if err := link(b, "s-2"); err != nil {
		t.Fatalf("link the new issuer: %v", err)
	}
	ids, _ := svc.Identities(ctx, ada.User.ID)
	if len(ids) != 1 || ids[0].Issuer != b || ids[0].Subject != "s-2" {
		t.Fatalf("the dormant identity was not replaced: %+v", ids)
	}
	// The old issuer's person signing in does not get the account back.
	if _, err := svc.ResolveProviderLogin(ctx, auth.ProviderLogin{Provider: "keycloak", Issuer: a, Subject: "s-1", Email: "ada@example.com", EmailVerified: true, Intent: auth.IntentSignIn}); !errors.Is(err, auth.ErrAccountExists) {
		t.Errorf("the old issuer: %v", err)
	}
	// A new identity that is someone else's is a conflict, and the old one stays.
	eve, err := svc.ResolveProviderLogin(ctx, auth.ProviderLogin{Provider: "keycloak", Issuer: "https://sso.c.example", Subject: "s-3", Email: "eve@example.com", EmailVerified: true, Intent: auth.IntentSignIn})
	if err != nil {
		t.Fatal(err)
	}
	_ = eve
	if err := link("https://sso.c.example", "s-3"); !errors.Is(err, auth.ErrLinkConflict) {
		t.Errorf("someone else's identity: %v", err)
	}
	if ids, _ := svc.Identities(ctx, ada.User.ID); len(ids) != 1 || ids[0].Issuer != b {
		t.Errorf("the identity changed on a conflict: %+v", ids)
	}
}
