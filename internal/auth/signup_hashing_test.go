package auth_test

import (
	"context"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// A sign-up with a registered address must not be told apart from one with a
// new address by how long it takes. What takes the time is hashing the
// password, so the property is that both hash it, once, whatever the store
// says about the address. Counting hashes shows that exactly; a stopwatch on
// the two paths only showed it on a quiet machine.
func TestSignupHashesTheSameWhetherOrNotTheAddressIsRegistered(t *testing.T) {
	hasher := NewHasher(2, Argon2Params{Memory: 16 * 1024, Time: 2, Threads: 1})
	svc := &Users{
		Store:  authtest.NewMemStore(),
		Hasher: hasher,
		Settings: registry.RealmSettings{
			AuthEnabled: true, Signup: registry.SignupOpen, PasswordMinLength: 12,
			Email:   &registry.EmailSettings{Function: "app/deliver", From: "a@example.com", DefaultLocale: "en", Limits: registry.EmailLimits{PerKindPerHour: 1000, PerDay: 1000, PerIPPerHour: 1000}},
			Account: registry.AccountSettings{RequireVerifiedEmail: true, VerifyEmailTTL: time.Hour},
		},
	}
	ctx := context.Background()
	if _, err := svc.Create(ctx, "taken@example.com", ptr("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}

	// hashesFor is how many passwords one sign-up hashes, and that it answered "pending".
	hashesFor := func(email string) uint64 {
		before := hasher.Hashes()
		res, err := svc.SignUp(ctx, SignupRequest{Email: email, Password: "dev-p4ssw0rd!"})
		if err != nil || !res.Pending {
			t.Fatalf("sign-up %s: %+v %v", email, res, err)
		}
		return hasher.Hashes() - before
	}
	if n := hashesFor("new@example.com"); n != 1 {
		t.Errorf("a new address hashed %d passwords, want 1", n)
	}
	if n := hashesFor("taken@example.com"); n != 1 {
		t.Errorf("a registered address hashed %d passwords, want 1: its sign-up would be quicker, and the timing would tell it is registered", n)
	}
}
