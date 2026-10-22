package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestAGeneratedKeyIsShownOnceAndFingerprinted(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2126, 10, 22, 12, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "clinic", &clock, nil)

	k, err := svc.CreateKey(ctx, "HEALTH", "", "key:ops")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := DecodeKey(k.Value)
	if err != nil || len(raw) != KeyLength {
		t.Fatalf("value %q: %v", k.Value, err)
	}
	if k.Fingerprint != KeyFingerprint(raw) || len(k.Fingerprint) != 8 {
		t.Errorf("fingerprint %q for %q", k.Fingerprint, k.Value)
	}
	other, _ := svc.CreateKey(ctx, "TAX", "", "key:ops")
	if other.Value == k.Value || other.Fingerprint == k.Fingerprint {
		t.Error("two generated keys are equal")
	}

	// Listings never carry the value, only the fingerprint.
	list, err := svc.ListSecrets(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[0].Name != "HEALTH" || list[0].Type != SecretTypeKey || list[0].Fingerprint != k.Fingerprint {
		t.Errorf("listed: %+v", list[0])
	}
	// Stored sealed: the store never holds the key text.
	sec, _ := store.SecretByScope(ctx, "", "HEALTH")
	if strings.Contains(sec.Ciphertext, k.Value) || sec.Fingerprint != k.Fingerprint {
		t.Errorf("stored: %+v", sec)
	}
	if plain, err := svc.Cipher.Open(sec.Ciphertext, sec.Nonce, sec.KeyID); err != nil || plain != k.Value {
		t.Errorf("opened: %q %v", plain, err)
	}
}

func TestAnImportedKeyKeepsItsValueAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2126, 10, 22, 12, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "clinic", &clock, nil)

	raw := []byte("0123456789abcdef0123456789abcdef")
	text := base64.RawURLEncoding.EncodeToString(raw)
	k, err := svc.CreateKey(ctx, "HEALTH", text, "key:ops")
	if err != nil || k.Value != "" || k.Fingerprint != KeyFingerprint(raw) {
		t.Fatalf("import: %+v %v", k, err)
	}
	// Padded text is the same key.
	if _, err := svc.CreateKey(ctx, "COPY", text+"=", "key:ops"); err != nil {
		t.Errorf("padded: %v", err)
	}
	for _, bad := range []string{"short", base64.RawURLEncoding.EncodeToString(raw[:16]), "!!!notbase64!!!"} {
		if _, err := svc.CreateKey(ctx, "BAD", bad, "key:ops"); !errors.Is(err, ErrKeyValue) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := svc.CreateKey(ctx, "HEALTH", "", "key:ops"); !errors.Is(err, ErrSecretExists) {
		t.Errorf("a second HEALTH: %v", err)
	}
	if err := svc.SetSecret(ctx, "", "PLAIN", "v", "key:ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateKey(ctx, "PLAIN", "", "key:ops"); !errors.Is(err, ErrSecretExists) {
		t.Errorf("a key over a secret: %v", err)
	}
	if _, err := svc.CreateKey(ctx, "lower", "", "key:ops"); err == nil {
		t.Error("an invalid name")
	}
	// A plain write can't replace a key's value.
	if err := svc.SetSecret(ctx, "", "HEALTH", "x", "key:ops"); !errors.Is(err, ErrSecretIsKey) {
		t.Errorf("set over a key: %v", err)
	}
	noCipher := &Users{Store: store, Realm: "clinic"}
	if _, err := noCipher.CreateKey(ctx, "NEW", "", "x"); !errors.Is(err, ErrSecretsNotConfigured) {
		t.Errorf("without a master key: %v", err)
	}
}

func TestAKeyIsNeverHandedToFunctionsOrProviders(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2126, 10, 22, 12, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "clinic", &clock, NewSecretCache())
	if _, err := svc.CreateKey(ctx, "HEALTH", "", "key:ops"); err != nil {
		t.Fatal(err)
	}
	values, missing, err := svc.ResolveSecrets(ctx, []registry.SecretRef{{Realm: true, Name: "HEALTH"}}, "app")
	if err != nil || len(values) != 0 || len(missing) != 1 {
		t.Errorf("resolved: %v %v %v", values, missing, err)
	}
}

func TestAKeyInUseCantBeDeleted(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2126, 10, 22, 12, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "clinic", &clock, nil)
	svc.KeyUses = func(name string) []string {
		if name == "HEALTH" {
			return []string{"main/patients.notes"}
		}
		return nil
	}
	for _, n := range []string{"HEALTH", "FREE"} {
		if _, err := svc.CreateKey(ctx, n, "", "key:ops"); err != nil {
			t.Fatal(err)
		}
	}
	var inUse *KeyInUseError
	if err := svc.DeleteKey(ctx, "HEALTH"); !errors.As(err, &inUse) || inUse.Uses[0] != "main/patients.notes" {
		t.Errorf("delete in use: %v", err)
	}
	if err := svc.DeleteSecret(ctx, "", "HEALTH"); !errors.As(err, &inUse) {
		t.Errorf("delete in use as a secret: %v", err)
	}
	if err := svc.DeleteKey(ctx, "FREE"); err != nil {
		t.Errorf("delete a free key: %v", err)
	}
	if err := svc.DeleteKey(ctx, "FREE"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("twice: %v", err)
	}
	svc.SetSecret(ctx, "", "PLAIN", "v", "x")
	if err := svc.DeleteKey(ctx, "PLAIN"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("a plain secret is not a key: %v", err)
	}
}
