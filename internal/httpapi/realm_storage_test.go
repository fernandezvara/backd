package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
	"github.com/fernandezvara/backd/internal/storage/storagetest"
)

func (f *rulesFixture) withStorage(t *testing.T, endpoint string) {
	t.Helper()
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Cache = auth.NewSecretCache()
	f.reg.Realms["acme"].Settings.Storage = &registry.StorageSettings{Provider: "minio", Endpoint: endpoint, PublicEndpoint: "http://localhost:9000", Region: "us-east-1", Bucket: "files", Prefix: "t",
		AccessKey: "STORAGE_ACCESS_KEY", SecretKey: "STORAGE_SECRET_KEY", Download: registry.DownloadProxy, PresignedTTL: registry.DefaultPresignedTTL, PendingTTL: registry.DefaultPendingTTL, HTTP: true}
}

func TestRealmStorageConnection(t *testing.T) {
	f := newRulesFixture(t)
	fake := storagetest.New(t, "files")
	ctx := context.Background()
	objects := newRealmObjects(f.reg, func(string) *auth.Users { return f.svc })
	var connects int
	objects.connect = func(c storage.ObjectsConfig) (*storage.Objects, error) {
		connects++
		return storage.NewObjects(c)
	}

	// No storage: the realm says so.
	f.reg.Realms["acme"].Settings.Storage = nil
	if _, err := objects.For(ctx, "acme"); !errors.Is(err, errNoStorage) {
		t.Fatalf("no storage: %v", err)
	}
	f.withStorage(t, fake.URL)
	// Storage, but its keys aren't set: unavailable, naming them.
	_, err := objects.For(ctx, "acme")
	if !errors.Is(err, errStorageUnavailable) || !strings.Contains(err.Error(), "realm.STORAGE_ACCESS_KEY") {
		t.Fatalf("missing keys: %v", err)
	}
	if missing, _ := objects.missingSecrets(ctx, "acme"); len(missing) != 2 {
		t.Errorf("missing: %v", missing)
	}
	// Without a secrets key on the instance they can't be read either.
	f.svc.Cipher = nil
	if _, err := objects.For(ctx, "acme"); !errors.Is(err, errStorageUnavailable) {
		t.Errorf("no cipher: %v", err)
	}
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))

	for _, n := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY"} {
		if err := f.svc.SetSecret(ctx, "", n, "v1-"+n, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	a, err := objects.For(ctx, "acme")
	if err != nil || a.Config().AccessKey != "v1-STORAGE_ACCESS_KEY" || a.Config().PublicEndpoint != "http://localhost:9000" {
		t.Fatalf("connection: %+v %v", a, err)
	}
	// The same keys: the same connection, not a new one.
	if b, _ := objects.For(ctx, "acme"); b != a || connects != 1 {
		t.Errorf("reconnected: %d", connects)
	}
	// A rotated key makes a new connection (once the secret cache lets go of the old one).
	f.svc.Cache = auth.NewSecretCache()
	if err := f.svc.SetSecret(ctx, "", "STORAGE_SECRET_KEY", "v2", "key:test"); err != nil {
		t.Fatal(err)
	}
	if c, err := objects.For(ctx, "acme"); err != nil || c == a || c.Config().SecretKey != "v2" || connects != 2 {
		t.Errorf("after a rotation: %v %d", err, connects)
	}
}

// The configuration view shows where the files go and the names of the keys,
// never their values.
func TestConfigShowsStorageWithoutKeys(t *testing.T) {
	f := newRulesFixture(t)
	f.withStorage(t, "http://minio:9000")
	_ = f.svc.SetSecret(context.Background(), "", "STORAGE_ACCESS_KEY", "AKIA-VALUE", "key:test")
	_, adminKey, _ := f.svc.CreateAPIKey(context.Background(), "cfg", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	rec, out := f.doH(t, "GET", admin+"/config", "", bearer(adminKey))
	st, _ := out["settings"].(map[string]any)["storage"].(map[string]any)
	if rec.Code != http.StatusOK || st["provider"] != "minio" || st["bucket"] != "files" || st["prefix"] != "t" || st["access_key"] != "secret:STORAGE_ACCESS_KEY" || st["public_endpoint"] != "http://localhost:9000" || st["download"] != "proxy" {
		t.Fatalf("config: %d %v", rec.Code, st)
	}
	if strings.Contains(rec.Body.String(), "AKIA-VALUE") {
		t.Error("a secret's value is in the configuration view")
	}
}

// Running the check is audited, and refuses what the admin areas refuse.
func TestStorageCheckIsAuditedAndNeedsConfigRead(t *testing.T) {
	f := newRulesFixture(t)
	fake := storagetest.New(t, "files")
	f.withStorage(t, fake.URL)
	ctx := context.Background()
	for _, n := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY"} {
		_ = f.svc.SetSecret(ctx, "", n, "v-"+n, "key:test")
	}
	_, adminKey, _ := f.svc.CreateAPIKey(ctx, "chk", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	rec, out := f.doH(t, "POST", admin+"/storage/check", "", bearer(adminKey))
	if rec.Code != http.StatusOK || out["ok"] != true || out["link_host"] == "" {
		t.Fatalf("check: %d %v", rec.Code, out)
	}
	if a := f.auditActions(t, "storage.check"); len(a) != 1 {
		t.Errorf("audit: %v", a)
	}
	// A level without the config area is refused.
	f.bobHolds(t, "keeper") // admin: [apikeys, secrets]
	if rec, _ := f.doH(t, "POST", admin+"/storage/check", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a level without config: %d", rec.Code)
	}
}
