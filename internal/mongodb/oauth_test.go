package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreOAuthStatesAndCodesWorkOnce(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC)
	st := auth.OAuthState{ID: "h1", Provider: "google", Intent: auth.IntentSignIn, CodeChallenge: "c", Verifier: "v", Nonce: "n", RedirectTo: "https://app.example/x",
		InvitationID: "inv", LinkUserID: "u1", Locales: []string{"es"}, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if err := s.PutOAuthState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimOAuthState(ctx, "h1", now.Add(time.Minute))
	if err != nil || got.Provider != "google" || got.Verifier != "v" || got.Nonce != "n" || got.InvitationID != "inv" || got.LinkUserID != "u1" || len(got.Locales) != 1 || !got.ExpiresAt.Equal(st.ExpiresAt) {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	if _, err := s.ClaimOAuthState(ctx, "h1", now.Add(time.Minute)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second claim: %v", err)
	}
	// An expired attempt isn't returned even if the TTL index hasn't removed it yet.
	if err := s.PutOAuthState(ctx, auth.OAuthState{ID: "h2", Provider: "google", Intent: auth.IntentLink, CodeChallenge: "c", Verifier: "v", Nonce: "n", RedirectTo: "x", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimOAuthState(ctx, "h2", now.Add(2*time.Minute)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired claim: %v", err)
	}
	// The validator refuses an intent it doesn't know.
	if err := s.PutOAuthState(ctx, auth.OAuthState{ID: "h3", Provider: "google", Intent: "hijack", CodeChallenge: "c", Verifier: "v", Nonce: "n", RedirectTo: "x", CreatedAt: now, ExpiresAt: now}); err == nil {
		t.Error("an unknown intent was stored")
	}

	if err := s.PutLoginCode(ctx, auth.LoginCode{ID: "c1", UserID: "u1", CodeChallenge: "ch", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	lc, err := s.ClaimLoginCode(ctx, "c1", now)
	if err != nil || lc.UserID != "u1" || lc.CodeChallenge != "ch" {
		t.Fatalf("claim code = %+v, %v", lc, err)
	}
	if _, err := s.ClaimLoginCode(ctx, "c1", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second code claim: %v", err)
	}
	_ = s.PutLoginCode(ctx, auth.LoginCode{ID: "c2", UserID: "u1", CodeChallenge: "ch", CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if _, err := s.ClaimLoginCode(ctx, "c2", now.Add(time.Hour)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired code: %v", err)
	}
}
