package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

func TestUserWithSeveralSignInMethods(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	u, err := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	add := func(id, provider, subject string, at time.Time) error {
		return store.PutIdentity(ctx, Identity{ID: id, UserID: u.ID, Provider: provider, Subject: subject, Email: "ada@gmail.com", EmailVerified: true, CreatedAt: at, UpdatedAt: at})
	}
	if err := add("g1", "google", "sub-1", clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := add("a1", "apple", "sub-2", clock.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// One identity per provider per user, and a provider account belongs to one user.
	if err := add("g2", "google", "sub-other", clock.Add(3*time.Hour)); !errors.Is(err, ErrIdentityExists) {
		t.Errorf("a second Google identity: %v", err)
	}
	ids, err := svc.Identities(ctx, u.ID)
	if err != nil || len(ids) != 3 || ids[0].Provider != ProviderPassword || ids[1].Provider != "google" || ids[2].Provider != "apple" {
		t.Fatalf("identities = %+v, %v", ids, err)
	}

	// Signing in with the password records its use; the others are left alone.
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	pw, _ := store.IdentityOf(ctx, u.ID, ProviderPassword)
	g, _ := store.IdentityOf(ctx, u.ID, "google")
	if !pw.LastUsedAt.Equal(clock) || !g.LastUsedAt.IsZero() {
		t.Errorf("last used: password %v, google %v", pw.LastUsedAt, g.LastUsedAt)
	}
	// A failed login does not.
	clock = clock.Add(time.Hour)
	_, _, _ = svc.Login(ctx, "ada@example.com", "wrong password here", "")
	if pw, _ = store.IdentityOf(ctx, u.ID, ProviderPassword); pw.LastUsedAt.Equal(clock) {
		t.Error("a failed login recorded a use")
	}

	// Unlinking: any method but the last, audited.
	if err := svc.Unlink(ctx, u.ID, "twitter", "self"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unlink a method the user lacks: %v", err)
	}
	for _, p := range []string{"google", ProviderPassword} {
		if err := svc.Unlink(ctx, u.ID, p, "self"); err != nil {
			t.Errorf("unlink %s: %v", p, err)
		}
	}
	if err := svc.Unlink(ctx, u.ID, "apple", "self"); !errors.Is(err, ErrLastSignInMethod) {
		t.Errorf("unlink the last: %v", err)
	}
	if ids, _ := svc.Identities(ctx, u.ID); len(ids) != 1 || ids[0].Provider != "apple" {
		t.Errorf("identities left: %+v", ids)
	}
	recs, _, _ := svc.AuditTrail(ctx, AuditFilter{Action: AuditIdentityUnlinked})
	if len(recs) != 2 {
		t.Errorf("audit records: %+v", recs)
	}

	// A user with no password identity can still sign in with the others and never with a password.
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("password login without a password identity: %v", err)
	}
}
