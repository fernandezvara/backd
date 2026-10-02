package auth_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// A sign-up with a registered address takes about as long as one with a new
// address: the password is hashed either way, so timing can't tell them apart.
func TestSignupTimingDoesNotRevealRegistration(t *testing.T) {
	store := authtest.NewMemStore()
	svc := &Users{
		Store:  store,
		Hasher: NewHasher(2, Argon2Params{Memory: 16 * 1024, Time: 2, Threads: 1}),
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
	measure := func(email func(i int) string) time.Duration {
		var ds []time.Duration
		for i := 0; i < 15; i++ {
			start := time.Now()
			res, err := svc.SignUp(ctx, SignupRequest{Email: email(i), Password: "dev-p4ssw0rd!"})
			if err != nil || !res.Pending {
				t.Fatalf("sign-up: %+v %v", res, err)
			}
			ds = append(ds, time.Since(start))
		}
		slices.Sort(ds)
		return ds[len(ds)/2]
	}
	fresh := measure(func(i int) string { return fmt.Sprintf("new%d@example.com", i) })
	taken := measure(func(int) string { return "taken@example.com" })
	hash := func() time.Duration { // what creating a user costs, which hashes once
		start := time.Now()
		_, _ = svc.Create(ctx, "reference@example.com", ptr("dev-p4ssw0rd!"))
		return time.Since(start)
	}()
	if taken < hash/2 {
		t.Errorf("a registered address took %s, less than half of one hash (%s): the password wasn't hashed", taken, hash)
	}
	if ratio := float64(taken) / float64(fresh); ratio < 0.6 || ratio > 1.6 {
		t.Errorf("registered %s vs new %s (ratio %.2f): the timing tells them apart", taken, fresh, ratio)
	}
}
