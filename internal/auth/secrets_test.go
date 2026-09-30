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

const (
	testMasterKey  = "01234567890123456789012345678901"
	otherMasterKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func newSecretsUsers(t *testing.T, store *authtest.MemStore, realm string, clock *time.Time, cache *SecretCache) *Users {
	t.Helper()
	cipher, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	return &Users{Store: store, Realm: realm, Cipher: cipher, Cache: cache, Now: func() time.Time { return *clock }}
}

func TestSecretCipherRoundTrip(t *testing.T) {
	c, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := c.Seal("sk_live_topsecret")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Open(ct, nonce, c.KeyID)
	if err != nil || got != "sk_live_topsecret" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	other, err := NewSecretCipher([]byte(otherMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(ct, nonce, c.KeyID); err == nil {
		t.Error("a value sealed under one key opened under another")
	}
	if _, err := c.Open(ct, nonce, other.KeyID); err == nil {
		t.Error("Open accepted a mismatched key id")
	}
	if _, err := c.Open(ct, "not-base64!!", c.KeyID); err == nil {
		t.Error("Open accepted an invalid nonce")
	}

	if _, err := NewSecretCipher([]byte("short")); err == nil {
		t.Error("NewSecretCipher accepted a key shorter than 32 characters")
	}
}

func TestSecretCipherDeterministicKeyID(t *testing.T) {
	a, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID != b.KeyID {
		t.Errorf("the same master key gave different key ids: %s vs %s", a.KeyID, b.KeyID)
	}
	c, err := NewSecretCipher([]byte(otherMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID == c.KeyID {
		t.Error("different master keys gave the same key id")
	}
}

func TestSetListDeleteSecret(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "acme", &clock, nil)

	if err := svc.SetSecret(ctx, "app", "STRIPE_KEY", "sk_live_1", "user:u1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSecret(ctx, "", "SHARED", "shared-value", "user:u1"); err != nil {
		t.Fatal(err)
	}

	list, err := svc.ListSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSecrets = %+v", list)
	}
	// Realm scope ("") sorts first; neither entry exposes a value field
	// at all (SecretMeta has none), only metadata.
	if list[0].Database != "" || list[0].Name != "SHARED" || list[0].UpdatedBy != "user:u1" {
		t.Errorf("list[0] = %+v", list[0])
	}
	if list[1].Database != "app" || list[1].Name != "STRIPE_KEY" {
		t.Errorf("list[1] = %+v", list[1])
	}

	// Setting again (same scope+name) updates in place: one entry, and
	// CreatedAt doesn't move.
	later := clock.Add(time.Hour)
	svc2 := newSecretsUsers(t, store, "acme", &later, nil)
	if err := svc2.SetSecret(ctx, "app", "STRIPE_KEY", "sk_live_2", "user:u2"); err != nil {
		t.Fatal(err)
	}
	list, err = svc.ListSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("update created a new entry instead of replacing: %+v", list)
	}
	for _, m := range list {
		if m.Database == "app" {
			if !m.CreatedAt.Equal(clock) {
				t.Errorf("CreatedAt moved on update: %v", m.CreatedAt)
			}
			if !m.UpdatedAt.Equal(later) || m.UpdatedBy != "user:u2" {
				t.Errorf("UpdatedAt/UpdatedBy not refreshed: %+v", m)
			}
		}
	}

	if err := svc.SetSecret(ctx, "app", "not-valid", "x", "user:u1"); err == nil || !strings.Contains(err.Error(), "invalid secret name") {
		t.Errorf("invalid name accepted: %v", err)
	}

	if err := svc.DeleteSecret(ctx, "app", "STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSecret(ctx, "app", "STRIPE_KEY"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("delete of a missing secret: %v", err)
	}
}

func TestSetSecretRequiresCipher(t *testing.T) {
	ctx := context.Background()
	clock := time.Now()
	svc := &Users{Store: authtest.NewMemStore(), Now: func() time.Time { return clock }}
	if err := svc.SetSecret(ctx, "app", "KEY", "v", "user:u1"); !errors.Is(err, ErrSecretsNotConfigured) {
		t.Errorf("SetSecret without a cipher: %v", err)
	}
}

func TestResolveSecrets(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "acme", &clock, NewSecretCache())

	if err := svc.SetSecret(ctx, "app", "STRIPE_KEY", "sk_live_1", "user:u1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSecret(ctx, "", "SHARED", "shared-value", "user:u1"); err != nil {
		t.Fatal(err)
	}

	refs := []registry.SecretRef{{Name: "STRIPE_KEY"}, {Realm: true, Name: "SHARED"}, {Name: "MISSING"}}
	values, missing, err := svc.ResolveSecrets(ctx, refs, "app")
	if err != nil {
		t.Fatal(err)
	}
	if values["STRIPE_KEY"] != "sk_live_1" || values["realm.SHARED"] != "shared-value" {
		t.Errorf("values = %+v", values)
	}
	if len(missing) != 1 || missing[0] != "MISSING" {
		t.Errorf("missing = %v", missing)
	}

	// A same-named secret in a different database is a different value:
	// no fallback, no cross-database leak.
	if err := svc.SetSecret(ctx, "other", "STRIPE_KEY", "sk_live_other", "user:u1"); err != nil {
		t.Fatal(err)
	}
	values, _, err = svc.ResolveSecrets(ctx, []registry.SecretRef{{Name: "STRIPE_KEY"}}, "other")
	if err != nil {
		t.Fatal(err)
	}
	if values["STRIPE_KEY"] != "sk_live_other" {
		t.Errorf("cross-database value leaked: %+v", values)
	}
}

func TestResolveSecretsNoCipherIsAllMissing(t *testing.T) {
	ctx := context.Background()
	clock := time.Now()
	svc := &Users{Store: authtest.NewMemStore(), Now: func() time.Time { return clock }}
	_, missing, err := svc.ResolveSecrets(ctx, []registry.SecretRef{{Name: "A"}, {Realm: true, Name: "B"}}, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 {
		t.Errorf("missing = %v", missing)
	}
}

// TestResolveSecretsCaches proves values (and misses) are cached for a
// while: a change made straight through the store, bypassing Users
// (as a rotation script might), isn't seen until the cache entry
// expires — the ~60s bound F5 promises.
func TestResolveSecretsCaches(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cache := NewSecretCache()
	svc := newSecretsUsers(t, store, "acme", &clock, cache)

	if err := svc.SetSecret(ctx, "app", "KEY", "v1", "user:u1"); err != nil {
		t.Fatal(err)
	}
	refs := []registry.SecretRef{{Name: "KEY"}}
	values, _, err := svc.ResolveSecrets(ctx, refs, "app")
	if err != nil || values["KEY"] != "v1" {
		t.Fatalf("values = %+v, err = %v", values, err)
	}

	// Change the stored value directly (not through svc, so the cache
	// isn't invalidated) and re-encrypt it under the same cipher.
	ct, nonce, err := svc.Cipher.Seal("v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSecret(ctx, Secret{Database: "app", Name: "KEY", Ciphertext: ct, Nonce: nonce, KeyID: svc.Cipher.KeyID, CreatedAt: clock, UpdatedAt: clock, UpdatedBy: "script"}); err != nil {
		t.Fatal(err)
	}

	values, _, err = svc.ResolveSecrets(ctx, refs, "app")
	if err != nil || values["KEY"] != "v1" {
		t.Fatalf("expected the cached value before expiry: %+v, %v", values, err)
	}

	clock = clock.Add(61 * time.Second)
	values, _, err = svc.ResolveSecrets(ctx, refs, "app")
	if err != nil || values["KEY"] != "v2" {
		t.Fatalf("expected the fresh value after the cache expired: %+v, %v", values, err)
	}
}

// TestSecretCacheIsRealmScoped proves one shared SecretCache never
// serves one realm's value for another's same-named secret.
func TestSecretCacheIsRealmScoped(t *testing.T) {
	ctx := context.Background()
	storeA, storeB := authtest.NewMemStore(), authtest.NewMemStore()
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cache := NewSecretCache()
	a := newSecretsUsers(t, storeA, "realm-a", &clock, cache)
	b := newSecretsUsers(t, storeB, "realm-b", &clock, cache)

	if err := a.SetSecret(ctx, "app", "KEY", "value-a", "user:u1"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetSecret(ctx, "app", "KEY", "value-b", "user:u1"); err != nil {
		t.Fatal(err)
	}
	refs := []registry.SecretRef{{Name: "KEY"}}
	va, _, err := a.ResolveSecrets(ctx, refs, "app")
	if err != nil {
		t.Fatal(err)
	}
	vb, _, err := b.ResolveSecrets(ctx, refs, "app")
	if err != nil {
		t.Fatal(err)
	}
	if va["KEY"] != "value-a" || vb["KEY"] != "value-b" {
		t.Errorf("cross-realm leak: a=%+v b=%+v", va, vb)
	}
}
