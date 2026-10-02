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
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
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
	t0 := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
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
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
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

func TestEmailTokenAddressAndInvitationOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := s.CreateEmailToken(ctx, auth.EmailToken{Hash: "inv", Purpose: "invitation", InvitationID: "i1", Address: "new@example.com", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RedeemEmailToken(ctx, "inv", "invitation", now)
	if err != nil || got.Address != "new@example.com" || got.InvitationID != "i1" || got.UserID != "" {
		t.Errorf("redeemed: %+v %v", got, err)
	}
	if got, err = s.GetEmailToken(ctx, "inv"); err != nil || got.Address != "new@example.com" || got.InvitationID != "i1" || got.UsedAt.IsZero() {
		t.Errorf("get: %+v %v", got, err)
	}
}

func TestEmailChangeStoreOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, u := range []auth.User{{ID: "u1", Email: "a@example.com"}, {ID: "u2", Email: "b@example.com"}} {
		u.CreatedAt, u.UpdatedAt = now, now
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	pending, previous, mine := "c@example.com", "a@example.com", "c@example.com"
	if err := s.UpdateUser(ctx, "u1", auth.UserUpdate{PendingEmail: &pending}, now); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, "u1"); u.PendingEmail != "c@example.com" {
		t.Errorf("pending: %+v", u)
	}
	empty := ""
	if err := s.UpdateUser(ctx, "u1", auth.UserUpdate{Email: &mine, PendingEmail: &empty, PreviousEmail: &previous}, now); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, "u1"); u.Email != "c@example.com" || u.PendingEmail != "" || u.PreviousEmail != "a@example.com" {
		t.Errorf("after the change: %+v", u)
	}
	taken := "b@example.com"
	if err := s.UpdateUser(ctx, "u1", auth.UserUpdate{Email: &taken}, now); !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("a taken address: %v", err)
	}
}

func TestClaimInvitationByIDOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, inv := range []auth.Invitation{
		{ID: "live", TokenHash: "h1", Email: "a@example.com", CreatedBy: "key:k", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "old", TokenHash: "h2", Email: "b@example.com", CreatedBy: "key:k", CreatedAt: now, ExpiresAt: now.Add(-time.Minute)},
	} {
		if err := s.CreateInvitation(ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimInvitationByID(ctx, "old", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("an expired invitation: %v", err)
	}
	got, err := s.ClaimInvitationByID(ctx, "live", now)
	if err != nil || got.Email != "a@example.com" {
		t.Errorf("claim: %+v %v", got, err)
	}
	if _, err := s.ClaimInvitationByID(ctx, "live", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("claimed twice: %v", err)
	}
}
