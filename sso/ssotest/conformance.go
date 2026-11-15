package ssotest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/sso"
)

// TestStore is the suite every sso.Store must pass. newStore makes an empty store that reads
// the clock now gives.
func TestStore(t *testing.T, newStore func(now func() time.Time) sso.Store) {
	t.Helper()
	ctx := context.Background()
	clock := time.Date(2126, 10, 9, 12, 0, 0, 0, time.UTC) // far ahead, so a TTL index never removes a record under test
	now := func() time.Time { return clock }
	t.Run("an attempt is taken once", func(t *testing.T) {
		s := newStore(now)
		a := sso.Attempt{Provider: "google", Scope: "realm", Intent: sso.IntentSignIn, Challenge: "c", Verifier: "v", Nonce: "n", RedirectTo: "https://app.example/", Host: []byte(`{"x":1}`), ExpiresAt: clock.Add(time.Minute)}
		if err := s.PutAttempt(ctx, "k1", a); err != nil {
			t.Fatal(err)
		}
		got, err := s.TakeAttempt(ctx, "k1")
		if err != nil || got.Provider != "google" || got.Scope != "realm" || got.Verifier != "v" || got.Nonce != "n" || string(got.Host) != `{"x":1}` {
			t.Fatalf("take = %+v, %v", got, err)
		}
		if _, err := s.TakeAttempt(ctx, "k1"); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("second take: %v", err)
		}
		if _, err := s.TakeAttempt(ctx, "missing"); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("unknown key: %v", err)
		}
	})
	t.Run("an expired attempt is gone", func(t *testing.T) {
		s := newStore(now)
		if err := s.PutAttempt(ctx, "k", sso.Attempt{Provider: "google", ExpiresAt: clock.Add(-time.Second)}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TakeAttempt(ctx, "k"); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("expired: %v", err)
		}
	})
	t.Run("a code is taken once and keeps its record", func(t *testing.T) {
		s := newStore(now)
		c := sso.Code{UserID: "u1", Method: "google", Strategy: "web", Challenge: "ch", NewUser: true, Profile: sso.Profile{"name": "Ada"}, Scope: "org1", ExpiresAt: clock.Add(time.Minute)}
		if err := s.PutCode(ctx, "c1", c); err != nil {
			t.Fatal(err)
		}
		got, err := s.TakeCode(ctx, "c1")
		if err != nil || got.UserID != "u1" || got.Method != "google" || got.Strategy != "web" || got.Challenge != "ch" || !got.NewUser || got.Profile["name"] != "Ada" || got.Scope != "org1" {
			t.Fatalf("take = %+v, %v", got, err)
		}
		if _, err := s.TakeCode(ctx, "c1"); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("second take: %v", err)
		}
	})
	t.Run("one take wins a race", func(t *testing.T) {
		s := newStore(now)
		if err := s.PutCode(ctx, "r", sso.Code{UserID: "u", ExpiresAt: clock.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		wins := make(chan bool, 8)
		for range 8 {
			go func() { _, err := s.TakeCode(ctx, "r"); wins <- err == nil }()
		}
		n := 0
		for range 8 {
			if <-wins {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d takes succeeded", n)
		}
	})
}

// DirectoryHarness is a directory with a way to put an account in it, for TestDirectory.
type DirectoryHarness struct {
	Directory sso.Directory
	// Seed adds an account (id, email, verified).
	Seed func(sso.Account)
}

// TestDirectory is the suite every sso.Directory must pass: the lookups and the effects the engine
// relies on. newDir makes an empty directory whose realm accepts open sign-up.
func TestDirectory(t *testing.T, newDir func(t *testing.T) DirectoryHarness) {
	t.Helper()
	ctx := context.Background()
	login := func(provider, subject, email string) sso.Login {
		return sso.Login{Provider: sso.Provider{Name: provider, Kind: sso.Kind(provider), Link: sso.LinkVerifiedEmail}, Claims: sso.Claims{Subject: subject, Email: email, EmailVerified: true}, Intent: sso.IntentSignIn}
	}
	t.Run("emails are normalized", func(t *testing.T) {
		h := newDir(t)
		if got := h.Directory.NormalizeEmail("  Ada@Example.COM "); got != "ada@example.com" {
			t.Errorf("normalized = %q", got)
		}
		if got := h.Directory.NormalizeEmail("not an address"); got != "" {
			t.Errorf("a non-address normalized to %q", got)
		}
	})
	t.Run("lookups", func(t *testing.T) {
		h := newDir(t)
		h.Seed(sso.Account{ID: "u1", Email: "ada@example.com", EmailVerified: true})
		if a, err := h.Directory.User(ctx, "u1"); err != nil || a.Email != "ada@example.com" || !a.EmailVerified {
			t.Errorf("User = %+v, %v", a, err)
		}
		if a, err := h.Directory.UserByEmail(ctx, "ada@example.com"); err != nil || a.ID != "u1" {
			t.Errorf("UserByEmail = %+v, %v", a, err)
		}
		if _, err := h.Directory.User(ctx, "nobody"); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("unknown user: %v", err)
		}
		if _, err := h.Directory.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("unknown email: %v", err)
		}
		if _, err := h.Directory.Identity(ctx, sso.IdentityKey{Provider: "google", Subject: "s"}); !errors.Is(err, sso.ErrNotFound) {
			t.Errorf("unknown identity: %v", err)
		}
	})
	t.Run("create makes the user and its identity", func(t *testing.T) {
		h := newDir(t)
		l := login("google", "g-1", "new@example.com")
		a, err := h.Directory.Create(ctx, l, "new@example.com", true)
		if err != nil || a.Email != "new@example.com" || !a.EmailVerified || a.ID == "" {
			t.Fatalf("Create = %+v, %v", a, err)
		}
		if got, err := h.Directory.Identity(ctx, sso.IdentityKey{Provider: "google", Subject: "g-1"}); err != nil || got.ID != a.ID {
			t.Errorf("Identity = %+v, %v", got, err)
		}
	})
	t.Run("link refuses a taken identity and a second one of the provider", func(t *testing.T) {
		h := newDir(t)
		h.Seed(sso.Account{ID: "u1", Email: "one@example.com", EmailVerified: true})
		h.Seed(sso.Account{ID: "u2", Email: "two@example.com", EmailVerified: true})
		if err := h.Directory.Link(ctx, sso.Account{ID: "u1"}, login("google", "g-1", "one@example.com"), "one@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := h.Directory.Link(ctx, sso.Account{ID: "u2"}, login("google", "g-1", "two@example.com"), "two@example.com"); !errors.Is(err, sso.ErrLinkConflict) {
			t.Errorf("an identity of another user: %v", err)
		}
		if err := h.Directory.Link(ctx, sso.Account{ID: "u1"}, login("google", "g-2", "one@example.com"), "one@example.com"); !errors.Is(err, sso.ErrLinkConflict) {
			t.Errorf("a second identity of the provider: %v", err)
		}
		if err := h.Directory.Link(ctx, sso.Account{ID: "u1"}, login("microsoft", "m-1", "one@example.com"), "one@example.com"); err != nil {
			t.Errorf("an identity of another provider: %v", err)
		}
	})
}
