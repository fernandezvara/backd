package mongodb

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreChallengesWorkOnce(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 14, 12, 0, 0, 0, time.UTC)
	c := auth.AuthChallenge{ID: "h1", Kind: auth.ChallengeMFA, UserID: "u1", Factor: "google", NewUser: true,
		Profile: map[string]string{"name": "Ada"}, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := s.PutChallenge(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChallenge(ctx, "h1", now.Add(time.Minute))
	if err != nil || got.UserID != "u1" || got.Factor != "google" || !got.NewUser || got.Profile["name"] != "Ada" || !got.ExpiresAt.Equal(c.ExpiresAt) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// Attempts are counted in the store, one at a time.
	for want := 1; want <= 3; want++ {
		if cur, err := s.CountChallengeAttempt(ctx, "h1", now); err != nil || cur.Attempts != want {
			t.Fatalf("attempt %d: %+v, %v", want, cur, err)
		}
	}
	// Claiming deletes it: two claims, one winner.
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimChallenge(ctx, "h1", now); err == nil {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("claims won: %d", won.Load())
	}
	if _, err := s.GetChallenge(ctx, "h1", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("after claim: %v", err)
	}
	// Expired ones are not returned even before the TTL index removes them.
	_ = s.PutChallenge(ctx, auth.AuthChallenge{ID: "h2", Kind: auth.ChallengeEnroll, UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if _, err := s.GetChallenge(ctx, "h2", now.Add(2*time.Minute)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired: %v", err)
	}
	// The user's pending steps go with them.
	_ = s.PutChallenge(ctx, auth.AuthChallenge{ID: "h3", Kind: auth.ChallengeMFA, UserID: "u2", CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err := s.DeleteChallengesOfUser(ctx, "u2", auth.ChallengeEnroll); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChallenge(ctx, "h3", now); err != nil {
		t.Errorf("another kind survives: %v", err)
	}
	if err := s.DeleteChallengesOfUser(ctx, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChallenge(ctx, "h3", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
	// The validator refuses a kind it doesn't know.
	if err := s.PutChallenge(ctx, auth.AuthChallenge{ID: "h4", Kind: "root", UserID: "u1", CreatedAt: now, ExpiresAt: now}); err == nil {
		t.Error("an unknown kind was stored")
	}
}

func TestAuthStoreSecondFactors(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 14, 12, 0, 0, 0, time.UTC)
	if _, err := s.MFAOf(ctx, "u1"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("none: %v", err)
	}
	if err := s.PutMFA(ctx, auth.MFARecord{UserID: "u1", Secret: "k.n.c", EnabledAt: now, RecoveryCodes: []string{"h1", "h2", "h3"}}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.MFAOf(ctx, "u1")
	if err != nil || rec.Secret != "k.n.c" || len(rec.RecoveryCodes) != 3 || rec.LastStep != 0 || !rec.EnabledAt.Equal(now) {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	// A time step is accepted once, and only forward.
	for _, c := range []struct {
		step int64
		want bool
	}{{100, true}, {100, false}, {99, false}, {101, true}} {
		if got, err := s.AdvanceMFAStep(ctx, "u1", c.step); err != nil || got != c.want {
			t.Errorf("step %d = %v, %v", c.step, got, err)
		}
	}
	// A recovery code is spent once, even by concurrent requests.
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := s.UseRecoveryCode(ctx, "u1", "h2"); err == nil && ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("recovery code spent %d times", won.Load())
	}
	if left, ok, err := s.UseRecoveryCode(ctx, "u1", "h1"); err != nil || !ok || left != 1 {
		t.Errorf("use h1: %d %v %v", left, ok, err)
	}
	if _, ok, _ := s.UseRecoveryCode(ctx, "u1", "nope"); ok {
		t.Error("an unknown code was accepted")
	}
	ids, err := s.MFAUserIDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "u1" {
		t.Errorf("ids = %v, %v", ids, err)
	}
	// Deleting the user takes the second factors with them.
	u := auth.User{ID: "u1", Email: "u1@example.com", Roles: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	_ = s.PutChallenge(ctx, auth.AuthChallenge{ID: "h9", Kind: auth.ChallengeMFA, UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err := s.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MFAOf(ctx, "u1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if _, err := s.GetChallenge(ctx, "h9", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("challenge after delete: %v", err)
	}
}

func TestAuthStoreKeepsHowASessionWasMade(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 14, 12, 0, 0, 0, time.UTC)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "u1@example.com", Roles: []string{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sess := auth.Session{ID: "s1", UserID: "u1", TokenHash: "th", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour), Methods: []string{"password", "totp"}, MFA: true}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.SessionByTokenHash(ctx, "th")
	if err != nil || !got.MFA || len(got.Methods) != 2 || got.Methods[1] != "totp" {
		t.Errorf("session = %+v, %v", got, err)
	}
}
