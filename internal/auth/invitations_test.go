package auth_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func newInviteUsers(t *testing.T) (*Users, *authtest.MemStore, *time.Time) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Settings.Signup = registry.SignupInvite
	return svc, store, &clock
}

func TestInvitationSignup(t *testing.T) {
	svc, _, clock := newInviteUsers(t)
	ctx := context.Background()

	inv, token, err := svc.CreateInvitation(ctx, "", 0, "key:svc")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^bdi_[A-Za-z0-9_-]{43}$`).MatchString(token) || inv.TokenHash != HashToken(token) ||
		!inv.ExpiresAt.Equal(clock.Add(7*24*time.Hour)) || inv.CreatedBy != "key:svc" {
		t.Errorf("invitation %+v, token %q", inv, token)
	}

	// Checks that fail before the invitation is used don't use it up.
	var pe *PolicyError
	if _, _, err := svc.Signup(ctx, "ada@example.com", "short", token); !errors.As(err, &pe) {
		t.Errorf("weak password: %v", err)
	}
	if _, err := svc.Create(ctx, "taken@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Signup(ctx, "taken@example.com", "dev-p4ssw0rd!", token); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("taken email: %v", err)
	}

	// The invitation works once.
	p, _, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", token)
	if err != nil || p.User.Email != "ada@example.com" {
		t.Fatalf("signup: %+v, %v", p, err)
	}
	if _, _, err := svc.Signup(ctx, "bob@example.com", "dev-p4ssw0rd!", token); !errors.Is(err, ErrInvalidInvitation) {
		t.Errorf("second use: %v", err)
	}
	if list, _ := svc.Invitations(ctx); len(list) != 0 {
		t.Errorf("used invitation still listed: %+v", list)
	}

	for _, bad := range []string{"bdi_nope", "bds_" + token[4:], "x"} {
		if _, _, err := svc.Signup(ctx, "carl@example.com", "dev-p4ssw0rd!", bad); !errors.Is(err, ErrInvalidInvitation) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
}

func TestInvitationBoundToEmail(t *testing.T) {
	svc, _, _ := newInviteUsers(t)
	ctx := context.Background()
	inv, token, err := svc.CreateInvitation(ctx, " Ada@Example.com ", 0, "key:svc")
	if err != nil || inv.Email != "ada@example.com" {
		t.Fatalf("%+v, %v", inv, err)
	}
	if _, _, err := svc.Signup(ctx, "eve@example.com", "dev-p4ssw0rd!", token); !errors.Is(err, ErrInvalidInvitation) {
		t.Errorf("other email: %v", err)
	}
	// Still usable by the right person after the mismatch.
	if _, _, err := svc.Signup(ctx, "ADA@example.com", "dev-p4ssw0rd!", token); err != nil {
		t.Errorf("right email: %v", err)
	}
	if _, _, err := svc.CreateInvitation(ctx, "not-an-email", 0, "key:svc"); err == nil {
		t.Error("invalid email accepted")
	}
}

func TestInvitationExpiryAndRevoke(t *testing.T) {
	svc, _, clock := newInviteUsers(t)
	ctx := context.Background()
	_, short, _ := svc.CreateInvitation(ctx, "", time.Hour, "key:svc")
	keep, _, _ := svc.CreateInvitation(ctx, "", 0, "key:svc")
	*clock = clock.Add(time.Hour)
	if _, _, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", short); !errors.Is(err, ErrInvalidInvitation) {
		t.Errorf("expired: %v", err)
	}
	list, _ := svc.Invitations(ctx)
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Errorf("list = %+v", list)
	}
	if err := svc.RevokeInvitation(ctx, keep.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeInvitation(ctx, keep.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke twice: %v", err)
	}
	for _, ttl := range []time.Duration{time.Second, 91 * 24 * time.Hour} {
		if _, _, err := svc.CreateInvitation(ctx, "", ttl, "key:svc"); err == nil {
			t.Errorf("ttl %v accepted", ttl)
		}
	}
	// Open realms ignore invitations; closed ones refuse even with one.
	_, tok, _ := svc.CreateInvitation(ctx, "", 0, "key:svc")
	svc.Settings.Signup = registry.SignupClosed
	if _, _, err := svc.Signup(ctx, "bob@example.com", "dev-p4ssw0rd!", tok); !errors.Is(err, ErrSignupClosed) {
		t.Errorf("closed with invitation: %v", err)
	}
}
