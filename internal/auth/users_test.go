package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// testParams keep tests quick; production uses DefaultArgon2Params.
var testParams = Argon2Params{Memory: 64, Time: 1, Threads: 1}

func newUsers(store *authtest.MemStore, clock *time.Time) *Users {
	settings := registry.RealmSettings{
		Signup:            registry.SignupOpen,
		IdleTimeout:       30 * 24 * time.Hour,
		MaxLifetime:       90 * 24 * time.Hour,
		PasswordMinLength: 12,
	}
	return &Users{Store: store, Hasher: NewHasher(2, testParams), Settings: settings, Now: func() time.Time { return *clock }}
}

func ptr[T any](v T) *T { return &v }

// addSessions stores n sessions for the user.
func addSessions(store *authtest.MemStore, userID string, n int) {
	for i := range n {
		_ = store.CreateSession(context.Background(), Session{ID: userID + "-" + string(rune('a'+i)), UserID: userID, TokenHash: userID + string(rune('a'+i))})
	}
}

func TestCreateUser(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)

	u, err := svc.Create(ctx, "  Ada@Example.com ", ptr("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ada@example.com" || u.ID == "" || !u.EmailVerified || u.Disabled || u.Roles == nil || !u.CreatedAt.Equal(clock) {
		t.Errorf("user = %+v", u)
	}
	id, err := store.Identity(ctx, ProviderPassword, u.ID)
	if err != nil || id.UserID != u.ID || !strings.HasPrefix(id.PasswordHash, "$argon2id$") {
		t.Fatalf("password identity = %+v, %v", id, err)
	}
	if ok, _ := svc.Hasher.Verify(ctx, "dev-p4ssw0rd!", id.PasswordHash); !ok {
		t.Error("stored hash doesn't verify")
	}

	if _, err := svc.Create(ctx, "ADA@example.com", nil); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email: %v", err)
	}
	if _, err := svc.Create(ctx, "bob@example.com", ptr("short")); err == nil || !strings.Contains(err.Error(), "at least 12") {
		t.Errorf("short password: %v", err)
	}
	if _, err := store.UserByEmail(ctx, "bob@example.com"); !errors.Is(err, ErrNotFound) {
		t.Error("user created despite a rejected password")
	}
	if _, err := svc.Create(ctx, "not-an-email", nil); err == nil {
		t.Error("invalid email accepted")
	}

	// Without a password the user exists but has no password identity:
	// sign-in methods are separate from the user.
	nopw, err := svc.Create(ctx, "carol@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Identity(ctx, ProviderPassword, nopw.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("password identity for a user created without one: %v", err)
	}

	// If storing the identity fails, the user is removed again.
	store.FailPutIdentity = errors.New("boom")
	if _, err := svc.Create(ctx, "dan@example.com", ptr("dev-p4ssw0rd!")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := store.UserByEmail(ctx, "dan@example.com"); !errors.Is(err, ErrNotFound) {
		t.Error("user left behind after identity failure")
	}
}

func TestSetPassword(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	u, _ := svc.Create(ctx, "ada@example.com", nil)
	addSessions(store, u.ID, 3)

	clock = clock.Add(time.Hour)
	if err := svc.SetPassword(ctx, "ADA@example.com", "dev-p4ssw0rd!"); err != nil {
		t.Fatal(err)
	}
	first, _ := store.Identity(ctx, ProviderPassword, u.ID)
	if store.SessionCount(u.ID) != 0 {
		t.Error("sessions not revoked")
	}

	clock = clock.Add(time.Hour)
	if err := svc.SetPassword(ctx, "ada@example.com", "another good passphrase"); err != nil {
		t.Fatal(err)
	}
	second, _ := store.Identity(ctx, ProviderPassword, u.ID)
	if second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.Equal(clock) || second.PasswordHash == first.PasswordHash {
		t.Errorf("identity not updated in place: first %+v, second %+v", first, second)
	}

	if err := svc.SetPassword(ctx, "ada@example.com", "short"); err == nil {
		t.Error("short password accepted")
	}
	if err := svc.SetPassword(ctx, "nobody@example.com", "dev-p4ssw0rd!"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestUserFlagsAndDelete(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	u, _ := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!"))
	addSessions(store, u.ID, 2)

	if err := svc.SetEmailVerified(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDisabled(ctx, "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	got, _ := store.UserByEmail(ctx, "ada@example.com")
	if !got.EmailVerified || !got.Disabled || store.SessionCount(u.ID) != 0 {
		t.Errorf("after verify+disable: %+v, sessions %d", got, store.SessionCount(u.ID))
	}
	if err := svc.SetDisabled(ctx, "ada@example.com", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.UserByEmail(ctx, "ada@example.com"); got.Disabled {
		t.Error("still disabled")
	}

	list, _ := svc.List(ctx)
	if len(list) != 1 || list[0].ID != u.ID {
		t.Errorf("list = %+v", list)
	}

	if err := svc.Delete(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Identity(ctx, ProviderPassword, u.ID); !errors.Is(err, ErrNotFound) {
		t.Error("identity survived user deletion")
	}
	for _, err := range []error{
		svc.Delete(ctx, "ada@example.com"),
		svc.SetDisabled(ctx, "ada@example.com", true),
		svc.SetEmailVerified(ctx, "bad email", true),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}
}
