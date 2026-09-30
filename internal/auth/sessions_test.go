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

func TestSessionToken(t *testing.T) {
	a, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSessionToken()
	if !regexp.MustCompile(`^bds_[A-Za-z0-9_-]{43}$`).MatchString(a) || a == b {
		t.Errorf("tokens %q, %q", a, b)
	}
	if h := HashToken(a); len(h) != 64 || h == a || HashToken(a) != h {
		t.Errorf("hash %q", h)
	}
}

func TestSignup(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)

	p, token, err := svc.Signup(ctx, "Ada@Example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.User.Email != "ada@example.com" || p.Session.UserID != p.User.ID || p.Session.TokenHash != HashToken(token) ||
		!p.Session.ExpiresAt.Equal(clock.Add(30*24*time.Hour)) {
		t.Errorf("principal = %+v", p)
	}
	if got, err := svc.Authenticate(ctx, token); err != nil || got.User.ID != p.User.ID {
		t.Errorf("Authenticate after signup = %+v, %v", got, err)
	}
	if _, _, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("second signup: %v", err)
	}

	svc.Settings.Signup = registry.SignupClosed
	if _, _, err := svc.Signup(ctx, "bob@example.com", "dev-p4ssw0rd!", ""); !errors.Is(err, ErrSignupClosed) {
		t.Errorf("signup in closed mode: %v", err)
	}
	svc.Settings.Signup = registry.SignupInvite
	if _, _, err := svc.Signup(ctx, "bob@example.com", "dev-p4ssw0rd!", ""); !errors.Is(err, ErrInvitationRequired) {
		t.Errorf("signup in invite mode without invitation: %v", err)
	}
}

func TestLogin(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	if _, err := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "nopw@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "off@example.com", ptr("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDisabled(ctx, "off@example.com", true); err != nil {
		t.Fatal(err)
	}

	p, token, err := svc.Login(ctx, " ADA@example.com", "dev-p4ssw0rd!", "")
	if err != nil || p.User.Email != "ada@example.com" || token == "" {
		t.Fatalf("Login = %+v, %q, %v", p, token, err)
	}

	for _, tc := range []struct{ email, password string }{
		{"ada@example.com", "wrong password here"},
		{"nobody@example.com", "dev-p4ssw0rd!"},
		{"not an email", "dev-p4ssw0rd!"},
		{"off@example.com", "dev-p4ssw0rd!"},
		{"nopw@example.com", "dev-p4ssw0rd!"},
		// The dummy hash's password must never sign anyone in.
		{"nopw@example.com", "backd-dummy-password"},
		{"nobody@example.com", "backd-dummy-password"},
	} {
		if _, _, err := svc.Login(ctx, tc.email, tc.password, ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%q, %q) = %v, want ErrInvalidCredentials", tc.email, tc.password, err)
		}
	}
}

func TestAuthenticateExpiry(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	clock := start
	svc := newUsers(store, &clock)
	svc.Settings.IdleTimeout = time.Hour
	svc.Settings.MaxLifetime = 3 * time.Hour
	p, token, err := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}

	// Uses within a minute don't write.
	clock = start.Add(30 * time.Second)
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Session(p.Session.ID); !s.LastUsedAt.Equal(start) {
		t.Errorf("last use written after 30s: %v", s.LastUsedAt)
	}

	// Each use after a minute or more slides the idle timeout forward.
	for _, at := range []time.Duration{50 * time.Minute, 100 * time.Minute, 150 * time.Minute} {
		clock = start.Add(at)
		if _, err := svc.Authenticate(ctx, token); err != nil {
			t.Fatalf("at %v: %v", at, err)
		}
		s, _ := store.Session(p.Session.ID)
		want := clock.Add(time.Hour)
		if cap := start.Add(3 * time.Hour); cap.Before(want) {
			want = cap
		}
		if !s.LastUsedAt.Equal(clock) || !s.ExpiresAt.Equal(want) {
			t.Errorf("at %v: last used %v, expires %v; want %v, %v", at, s.LastUsedAt, s.ExpiresAt, clock, want)
		}
	}

	// The absolute cap ends it even though it was used recently.
	clock = start.Add(3 * time.Hour)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("past max lifetime: %v", err)
	}

	// Idle expiry.
	clock = start
	_, token2, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	clock = start.Add(time.Hour)
	if _, err := svc.Authenticate(ctx, token2); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("past idle timeout: %v", err)
	}

	for _, bad := range []string{"", "bds_nope", "bdk_" + token[4:], token + "x"} {
		if _, err := svc.Authenticate(ctx, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("Authenticate(%q) = %v", bad, err)
		}
	}
}

func TestAuthenticateDisabledAndDeleted(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	_, token, _ := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")

	// Disabling revokes sessions; the token stops working at once.
	if err := svc.SetDisabled(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("disabled user: %v", err)
	}
}

func TestSessionManagement(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	me, tokenA, _ := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	clock = clock.Add(time.Minute)
	_, tokenB, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	clock = clock.Add(time.Minute)
	other, _, _ := svc.Signup(ctx, "bob@example.com", "dev-p4ssw0rd!", "")

	list, err := svc.Sessions(ctx, me)
	if err != nil || len(list) != 2 || list[1].ID != me.Session.ID {
		t.Fatalf("Sessions = %+v, %v", list, err)
	}

	// Nobody can revoke someone else's session.
	if err := svc.RevokeSession(ctx, other, me.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke other's session: %v", err)
	}
	if err := svc.RevokeSession(ctx, me, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, tokenB); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("revoked session still works: %v", err)
	}

	if err := svc.Logout(ctx, me); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, tokenA); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("logged-out session still works: %v", err)
	}

	p1, t1, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	_, t2, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	if err := svc.LogoutAll(ctx, p1); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{t1, t2} {
		if _, err := svc.Authenticate(ctx, tok); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("session survived logout-all: %v", err)
		}
	}
	if store.SessionCount(other.User.ID) != 1 {
		t.Error("logout-all touched another user's sessions")
	}

	// Expired sessions are not listed.
	p3, _, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	clock = clock.Add(31 * 24 * time.Hour)
	if list, _ := svc.Sessions(ctx, p3); len(list) != 0 {
		t.Errorf("expired sessions listed: %+v", list)
	}
}

func TestChangePasswordAndDeleteAccount(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	me, current, _ := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	_, other, _ := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "")

	if err := svc.ChangePassword(ctx, me, "dev-p4ssw0rd!0", "dev-p4ssw0rd!2"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong current password: %v", err)
	}
	var pe *PolicyError
	if err := svc.ChangePassword(ctx, me, "dev-p4ssw0rd!", "short"); !errors.As(err, &pe) {
		t.Errorf("weak new password: %v", err)
	}
	if err := svc.ChangePassword(ctx, me, "dev-p4ssw0rd!", "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, current); err != nil {
		t.Errorf("current session ended by password change: %v", err)
	}
	if _, err := svc.Authenticate(ctx, other); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("other session survived password change: %v", err)
	}
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!2", ""); err != nil {
		t.Errorf("login with new password: %v", err)
	}

	if err := svc.DeleteAccount(ctx, me, "dev-p4ssw0rd!"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("delete with old password: %v", err)
	}
	if err := svc.DeleteAccount(ctx, me, "dev-p4ssw0rd!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, current); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("session survived account deletion: %v", err)
	}
	if _, err := store.UserByEmail(ctx, "ada@example.com"); !errors.Is(err, ErrNotFound) {
		t.Error("user still exists")
	}
}
