package auth_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	clock := start
	svc := newUsers(store, &clock)

	k, key, err := svc.CreateAPIKey(ctx, "billing", KeyOptions{TTL: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^bdk_[A-Za-z0-9_-]{43}$`).MatchString(key) || k.Prefix != key[:10] || k.Hash != HashToken(key) || k.ExpiresAt != nil {
		t.Errorf("key %q, record %+v", key, k)
	}
	if _, _, err := svc.CreateAPIKey(ctx, "billing", KeyOptions{TTL: 0}); !errors.Is(err, ErrKeyNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	for _, bad := range []string{"Billing", "_sys", "a b", ""} {
		if _, _, err := svc.CreateAPIKey(ctx, bad, KeyOptions{}); err == nil || !strings.Contains(err.Error(), "invalid API key name") {
			t.Errorf("name %q: %v", bad, err)
		}
	}

	got, err := svc.AuthenticateKey(ctx, key)
	if err != nil || got.Name != "billing" || got.LastUsedAt == nil || !got.LastUsedAt.Equal(start) {
		t.Fatalf("AuthenticateKey = %+v, %v", got, err)
	}
	// Uses within a minute aren't written.
	clock = start.Add(30 * time.Second)
	_, _ = svc.AuthenticateKey(ctx, key)
	if list, _ := svc.ListAPIKeys(ctx); !list[0].LastUsedAt.Equal(start) {
		t.Errorf("last use written after 30s: %v", list[0].LastUsedAt)
	}
	clock = start.Add(2 * time.Minute)
	_, _ = svc.AuthenticateKey(ctx, key)
	if list, _ := svc.ListAPIKeys(ctx); !list[0].LastUsedAt.Equal(clock) {
		t.Errorf("last use not written after 2m: %v", list[0].LastUsedAt)
	}

	for _, bad := range []string{"bdk_nope", "bds_" + key[4:], key + "x", ""} {
		if _, err := svc.AuthenticateKey(ctx, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("AuthenticateKey(%q) = %v", bad, err)
		}
	}

	if err := svc.RevokeAPIKey(ctx, "billing"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateKey(ctx, key); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("revoked key: %v", err)
	}
	if err := svc.RevokeAPIKey(ctx, "billing"); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("revoke twice: %v", err)
	}
}

func TestAPIKeyExpiry(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	clock := start
	svc := newUsers(store, &clock)
	k, key, err := svc.CreateAPIKey(ctx, "temp", KeyOptions{TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if k.ExpiresAt == nil || !k.ExpiresAt.Equal(start.Add(24*time.Hour)) {
		t.Errorf("expires = %v", k.ExpiresAt)
	}
	clock = start.Add(24*time.Hour - time.Second)
	if _, err := svc.AuthenticateKey(ctx, key); err != nil {
		t.Errorf("before expiry: %v", err)
	}
	clock = start.Add(24 * time.Hour)
	if _, err := svc.AuthenticateKey(ctx, key); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("at expiry: %v", err)
	}
	if _, _, err := svc.CreateAPIKey(ctx, "neg", KeyOptions{TTL: -time.Hour}); err == nil {
		t.Error("negative expiry accepted")
	}
}

func TestIdentify(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	p, token, _ := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	_, key, _ := svc.CreateAPIKey(ctx, "billing", KeyOptions{TTL: 0})

	if c, err := svc.Identify(ctx, ""); err != nil || c.User != nil || c.Key != nil || c.Actor() != "anonymous" {
		t.Errorf("anonymous: %+v, %v", c, err)
	}
	if c, err := svc.Identify(ctx, token); err != nil || c.User == nil || c.Actor() != "user:"+p.User.ID {
		t.Errorf("session: %+v, %v", c, err)
	}
	if c, err := svc.Identify(ctx, key); err != nil || c.Key == nil || c.Actor() != "key:billing" {
		t.Errorf("key: %+v, %v", c, err)
	}
	for _, bad := range []string{"bds_x", "bdk_x", "something"} {
		if _, err := svc.Identify(ctx, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("Identify(%q) = %v", bad, err)
		}
	}
}

func TestCallerNames(t *testing.T) {
	u := &Principal{User: User{ID: "u1"}}
	k := &APIKey{Name: "svc"}
	for _, tt := range []struct {
		c              Caller
		subject, actor string
		bypasses       bool
	}{
		{Caller{}, "anonymous", "anonymous", false},
		{Caller{User: u}, "user:u1", "user:u1", false},
		{Caller{Key: k}, "key:svc", "key:svc", true},
		{Caller{Key: k, User: u}, "user:u1", "key:svc as user:u1", false},
	} {
		if tt.c.Subject() != tt.subject || tt.c.Actor() != tt.actor || tt.c.BypassesRules() != tt.bypasses {
			t.Errorf("%+v: %q %q %v", tt.c, tt.c.Subject(), tt.c.Actor(), tt.c.BypassesRules())
		}
	}
}

func TestAPIKeyRoles(t *testing.T) {
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(authtest.NewMemStore(), &clock)
	ctx := context.Background()
	k, _, err := svc.CreateAPIKey(ctx, "service", KeyOptions{})
	if err != nil || k.Role != KeyRoleData || k.IsAdmin() {
		t.Errorf("default role = %q, %v", k.Role, err)
	}
	k, _, err = svc.CreateAPIKey(ctx, "tooling", KeyOptions{Role: KeyRoleAdmin})
	if err != nil || !k.IsAdmin() {
		t.Errorf("admin role = %q, %v", k.Role, err)
	}
	if _, _, err := svc.CreateAPIKey(ctx, "root", KeyOptions{Role: "root"}); err == nil || !strings.Contains(err.Error(), "must be data or admin") {
		t.Errorf("unknown role: %v", err)
	}
	if (APIKey{}).IsAdmin() {
		t.Error("a key stored without a role counts as admin")
	}
}
