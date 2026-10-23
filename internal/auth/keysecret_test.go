package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
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

func newRotationUsers(t *testing.T) (*Users, *authtest.MemStore, *fieldcrypt.MemStore, *time.Time) {
	t.Helper()
	clock := time.Date(2126, 10, 22, 12, 0, 0, 0, time.UTC)
	store := authtest.NewMemStore()
	svc := newSecretsUsers(t, store, "clinic", &clock, nil)
	return svc, store, nil, &clock
}

func TestRotatingAKeyRewrapsItsDataKeysAndKeepsThemReadable(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newRotationUsers(t)
	store := fieldcrypt.NewMemStore()
	svc.Store = &keyedStore{MemStore: svc.Store.(*authtest.MemStore), Store: store}
	k, err := svc.CreateKey(ctx, "HEALTH", "", "ops")
	if err != nil {
		t.Fatal(err)
	}
	ring := &fieldcrypt.Ring{Realm: "clinic", Store: store, Keys: svc.KeyMaterial}
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{{Path: "notes", Key: "HEALTH"}, {Path: "history", Key: "HEALTH"}}}
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	doc, err := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "notes": "n", "history": "h"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.RotateKey(ctx, "HEALTH", "", false, "ops")
	if err != nil || res.Rewrapped != 2 || res.Value == "" || res.Value == k.Value || res.Fingerprint == k.Fingerprint || res.Previous != k.Fingerprint || res.Version != 2 {
		t.Fatalf("rotate: %+v %v", res, err)
	}
	// Every data key now carries the new version, and the previous value is gone.
	for _, r := range store.Keys {
		if r.WrapVersion != 2 {
			t.Errorf("a data key is still wrapped by version %d", r.WrapVersion)
		}
	}
	sec, _ := svc.Store.SecretByScope(ctx, "", "HEALTH")
	if sec.PrevCiphertext != "" || sec.Version != 2 || sec.Fingerprint != res.Fingerprint {
		t.Errorf("secret after: %+v", sec)
	}
	// A new process opens the old values with the new key alone.
	fresh := &fieldcrypt.Ring{Realm: "clinic", Store: store, Keys: svc.KeyMaterial}
	if err := fresh.Decrypt(ctx, c, doc); err != nil || doc["notes"] != "n" || doc["history"] != "h" {
		t.Errorf("after: %v %v", doc, err)
	}
	// The old copy of the key no longer opens anything.
	oldRaw, _ := DecodeKey(k.Value)
	for _, r := range store.Keys {
		id, _ := hexDecode(r.ID)
		if _, err := fieldcrypt.Unwrap(oldRaw, r.Wrapped, id, r.Field("clinic"), r.Kind); err == nil {
			t.Error("the old key still opens a data key")
		}
	}
	// A key being used can be rotated to a value the owner brings.
	raw := []byte("0123456789abcdef0123456789abcdef")
	own := base64.RawURLEncoding.EncodeToString(raw)
	if res, err := svc.RotateKey(ctx, "HEALTH", own, false, "ops"); err != nil || res.Value != "" || res.Fingerprint != KeyFingerprint(raw) || res.Version != 3 {
		t.Errorf("own value: %+v %v", res, err)
	}
	if _, err := svc.RotateKey(ctx, "HEALTH", "short", false, "ops"); !errors.Is(err, ErrKeyValue) {
		t.Errorf("bad value: %v", err)
	}
	if _, err := svc.RotateKey(ctx, "NOPE", "", false, "ops"); !errors.Is(err, ErrNoKey) {
		t.Errorf("missing: %v", err)
	}
	if _, err := svc.RotateKey(ctx, "HEALTH", "", true, "ops"); !errors.Is(err, ErrNoRotation) {
		t.Errorf("resume with nothing pending: %v", err)
	}
	// A key with data keys under it can't be deleted, even once no field names it.
	var inUse *KeyInUseError
	if err := svc.DeleteKey(ctx, "HEALTH"); !errors.As(err, &inUse) || inUse.DataKeys != 2 {
		t.Errorf("delete: %v", err)
	}
}

func TestAnInterruptedRotationKeepsBothValuesUntilItIsResumed(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newRotationUsers(t)
	store := &failingRewrap{MemStore: fieldcrypt.NewMemStore()}
	svc.Store = &keyedStore{MemStore: svc.Store.(*authtest.MemStore), Store: store}
	if _, err := svc.CreateKey(ctx, "HEALTH", "", "ops"); err != nil {
		t.Fatal(err)
	}
	ring := &fieldcrypt.Ring{Realm: "clinic", Store: store, Keys: svc.KeyMaterial}
	c := &registry.Collection{Realm: "clinic", Database: "main", Name: "patients", Encrypted: []registry.EncryptedField{{Path: "notes", Key: "HEALTH"}, {Path: "history", Key: "HEALTH"}}}
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	doc, _ := ring.Encrypt(ctx, c, map[string]any{"id": "p1", "notes": "n", "history": "h"})

	store.failAfter = 1
	if _, err := svc.RotateKey(ctx, "HEALTH", "", false, "ops"); err == nil {
		t.Fatal("the rotation should have stopped")
	}
	// Half the data keys are on each version: everything still opens.
	fresh := &fieldcrypt.Ring{Realm: "clinic", Store: store, Keys: svc.KeyMaterial}
	if err := fresh.Decrypt(ctx, c, doc); err != nil || doc["notes"] != "n" || doc["history"] != "h" {
		t.Fatalf("mid rotation: %v %v", doc, err)
	}
	// Starting another is refused; resuming finishes it.
	if _, err := svc.RotateKey(ctx, "HEALTH", "", false, "ops"); !errors.Is(err, ErrRotationPending) {
		t.Errorf("another rotation: %v", err)
	}
	store.failAfter = 0
	res, err := svc.RotateKey(ctx, "HEALTH", "", true, "ops")
	if err != nil || !res.Resumed || res.Rewrapped != 1 || res.Value != "" {
		t.Fatalf("resume: %+v %v", res, err)
	}
	sec, _ := svc.Store.SecretByScope(ctx, "", "HEALTH")
	if sec.PrevCiphertext != "" {
		t.Error("the previous value was kept after the rotation finished")
	}
}

// keyedStore gives a MemStore the data keys of a fieldcrypt store.
type keyedStore struct {
	*authtest.MemStore
	fieldcrypt.Store
}

// failingRewrap fails the rewrap of every data key after the first failAfter ones succeeded.
type failingRewrap struct {
	*fieldcrypt.MemStore
	failAfter, done int
}

func (f *failingRewrap) RewrapDataKey(ctx context.Context, id string, wrapped []byte, from, to int) (bool, error) {
	if f.failAfter > 0 && f.done >= f.failAfter {
		return false, errors.New("disk on fire")
	}
	f.done++
	return f.MemStore.RewrapDataKey(ctx, id, wrapped, from, to)
}

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }
