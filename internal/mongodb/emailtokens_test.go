package mongodb

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestEmailTokensOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(hash, purpose string, ttl time.Duration) auth.EmailToken {
		return auth.EmailToken{Hash: hash, Purpose: purpose, UserID: "u1", RedirectTo: "https://app.example/welcome", CreatedAt: now, ExpiresAt: now.Add(ttl)}
	}
	for _, tk := range []auth.EmailToken{mk("h1", "reset-password", time.Hour), mk("h2", "reset-password", time.Hour), mk("h3", "verify-email", time.Hour), mk("h4", "verify-email", time.Minute)} {
		if err := s.CreateEmailToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.GetEmailToken(ctx, "h1"); err != nil || got.UserID != "u1" || got.Purpose != "reset-password" || !got.UsedAt.IsZero() {
		t.Errorf("GetEmailToken: %+v, %v", got, err)
	}
	if _, err := s.GetEmailToken(ctx, "nope"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("GetEmailToken of an unknown hash: %v", err)
	}

	// Eight parallel redemptions of one token: exactly one succeeds.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tk, err := s.RedeemEmailToken(ctx, "h1", "reset-password", now); err == nil {
				if tk.UserID != "u1" || tk.RedirectTo != "https://app.example/welcome" || !tk.UsedAt.Equal(now) {
					t.Errorf("redeemed token: %+v", tk)
				}
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, auth.ErrInvalidToken) {
				t.Errorf("redeem: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d parallel redemptions succeeded, want 1", wins)
	}

	// Wrong purpose, unknown and expired tokens are all the same error.
	for name, call := range map[string]func() error{
		"wrong purpose": func() error { _, err := s.RedeemEmailToken(ctx, "h3", "reset-password", now); return err },
		"unknown":       func() error { _, err := s.RedeemEmailToken(ctx, "nope", "reset-password", now); return err },
		"expired": func() error {
			_, err := s.RedeemEmailToken(ctx, "h4", "verify-email", now.Add(2*time.Minute))
			return err
		},
	} {
		if err := call(); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A success invalidates the siblings of its purpose, not other purposes.
	if err := s.InvalidateEmailTokens(ctx, "u1", "reset-password", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemEmailToken(ctx, "h2", "reset-password", now); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("a sibling after InvalidateEmailTokens: %v", err)
	}
	if _, err := s.RedeemEmailToken(ctx, "h3", "verify-email", now); err != nil {
		t.Errorf("a token of another purpose: %v", err)
	}
}

// An email job round-trips through MongoDB with only its small fields.
func TestEmailJobOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	j := auth.Job{ID: "e1", Database: "notify", Function: "deliver", CallerActor: "backd", Origin: "backd:email.verify-email",
		Email:     &auth.EmailJob{Kind: "verify-email", UserID: "u1", Locale: "es", RedirectTo: "https://app.example/welcome"},
		TimeoutMS: 60000, Status: auth.JobQueued, CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}
	if err := s.EnqueueJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetJob(ctx, "e1")
	if err != nil || !found || got.Email == nil || *got.Email != *j.Email || got.Origin != "backd:email.verify-email" {
		t.Fatalf("GetJob = %+v, %v, %v", got, found, err)
	}
	claimed, found, err := s.ClaimJob(ctx, "w", t0, 30*time.Second)
	if err != nil || !found || claimed.Email == nil || claimed.Email.Kind != "verify-email" {
		t.Fatalf("ClaimJob = %+v, %v, %v", claimed, found, err)
	}
}

func TestUserLocaleOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "ana@example.com", Roles: []string{}, Locale: "es", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, auth.User{ID: "u2", Email: "bob@example.com", Roles: []string{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if u, err := s.UserByID(ctx, "u1"); err != nil || u.Locale != "es" {
		t.Fatalf("created with a locale: %+v %v", u, err)
	}
	if u, _ := s.UserByEmail(ctx, "bob@example.com"); u.Locale != "" {
		t.Errorf("a user without one has none stored: %q", u.Locale)
	}
	en := "en"
	if err := s.UpdateUser(ctx, "u1", auth.UserUpdate{Locale: &en}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, "u1"); u.Locale != "en" {
		t.Errorf("after the change: %q", u.Locale)
	}
}
