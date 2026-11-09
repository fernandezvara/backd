package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"

	"github.com/fernandezvara/backd/internal/settings"
)

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		":8080":          false, // all interfaces
		"0.0.0.0:8080":   false,
		"10.0.0.5:8080":  false,
		"not-an-addr":    false,
	} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestServeRefusesDevOffLocalhost(t *testing.T) {
	a := &App{cfg: settings.Settings{Dev: true, HTTPAddr: "0.0.0.0:8080"}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	err := Serve(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "BACKD_DEV=true requires HTTP_ADDR bound to localhost") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkerRequiresExecutorURL(t *testing.T) {
	a := &App{cfg: settings.Settings{}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	if err := Worker(context.Background(), a); err == nil || !strings.Contains(err.Error(), "the worker role requires BACKD_EXECUTOR_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeWithWorkerRequiresExecutorURL(t *testing.T) {
	a := &App{cfg: settings.Settings{HTTPAddr: ":0"}, log: slog.New(slog.NewJSONHandler(io.Discard, nil)), withWorker: true}
	if err := Serve(context.Background(), a); err == nil || !strings.Contains(err.Error(), "--with-worker requires BACKD_EXECUTOR_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestStorageOriginsForTheAdminUI(t *testing.T) {
	reg := &registry.Registry{Realms: map[string]*registry.Realm{
		"a": {Name: "a", Settings: registry.RealmSettings{Storage: &registry.StorageSettings{Provider: "minio", Endpoint: "http://minio:9000", PublicEndpoint: "http://localhost:9000", Bucket: "b"}}},
		"b": {Name: "b", Settings: registry.RealmSettings{Storage: &registry.StorageSettings{Provider: "minio", Endpoint: "http://minio:9000", PublicEndpoint: "http://localhost:9000", Bucket: "b"}}},
		"c": {Name: "c", Settings: registry.RealmSettings{Storage: &registry.StorageSettings{Provider: "aws", Endpoint: "https://s3.eu-west-1.amazonaws.com", Bucket: "files"}}},
		"d": {Name: "d"},
	}}
	got := storageOrigins(reg)
	if len(got) != 2 || got[0] != "http://localhost:9000" || got[1] != "https://files.s3.eu-west-1.amazonaws.com" {
		t.Errorf("origins: %v", got)
	}
}

func TestUnprotectedAdminRoles(t *testing.T) {
	s := registry.RealmSettings{Roles: map[string]registry.Role{"owner": {Admin: registry.AllRights}, "staff": {}}}
	if got := unprotectedAdminRoles(s); len(got) != 1 || got[0] != "owner" {
		t.Errorf("none required: %v", got)
	}
	s.MFA.RequiredRoles = []string{"owner"}
	if got := unprotectedAdminRoles(s); len(got) != 0 {
		t.Errorf("owner required: %v", got)
	}
	s.MFA = registry.MFASettings{Off: true}
	if got := unprotectedAdminRoles(s); len(got) != 0 {
		t.Errorf("MFA off: %v", got)
	}
}

func TestStartupRefusesAFieldWithoutItsKey(t *testing.T) {
	ctx := context.Background()
	root := cmdtest.Write(t, map[string]string{
		"clinic/realm.yaml":                    "signup: open\n",
		"clinic/main/patients/schema.json":     `{"type":"object","properties":{"tax_id":{"type":"string"},"notes":{"type":"string"}}}`,
		"clinic/main/patients/collection.yaml": "fields:\n  tax_id:\n    encrypt: {key: TAX}\n  notes:\n    encrypt: {key: HEALTH}\n",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	store := authtest.NewMemStore()
	cipher, _ := auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc := &auth.Users{Store: store, Realm: "clinic", Cipher: cipher}
	users := func(string) *auth.Users { return svc }

	err = checkEncryptionKeys(ctx, reg, users)
	if err == nil || !strings.Contains(err.Error(), "main/patients.tax_id names the key TAX, which does not exist") || !strings.Contains(err.Error(), "HEALTH") || !strings.Contains(err.Error(), "--type key --generate --name HEALTH") {
		t.Fatalf("missing keys: %v", err)
	}
	if err := svc.SetSecret(ctx, "", "TAX", "a plain secret", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateKey(ctx, "HEALTH", "", "x"); err != nil {
		t.Fatal(err)
	}
	if err := checkEncryptionKeys(ctx, reg, users); err == nil || !strings.Contains(err.Error(), "TAX, which is a plain secret") || strings.Contains(err.Error(), "HEALTH") {
		t.Fatalf("a plain secret: %v", err)
	}
	svc.DeleteSecret(ctx, "", "TAX")
	if _, err := svc.CreateKey(ctx, "TAX", "", "x"); err != nil {
		t.Fatal(err)
	}
	if err := checkEncryptionKeys(ctx, reg, users); err != nil {
		t.Errorf("with both keys: %v", err)
	}
	// A realm without auth has nowhere to keep keys.
	if err := checkEncryptionKeys(ctx, reg, func(string) *auth.Users { return nil }); err == nil || !strings.Contains(err.Error(), "needs `auth` enabled") {
		t.Errorf("no auth: %v", err)
	}
}

func TestLogSecurityWarnings(t *testing.T) {
	var buf bytes.Buffer
	a := &App{
		log: slog.New(slog.NewJSONHandler(&buf, nil)),
		reg: &registry.Registry{Realms: map[string]*registry.Realm{
			"open":   {Name: "open", Settings: registry.RealmSettings{AuthEnabled: false}},
			"closed": {Name: "closed", Settings: registry.RealmSettings{AuthEnabled: true}},
			"ruled": {Name: "ruled", Settings: registry.RealmSettings{AuthEnabled: true}, Databases: map[string]*registry.Database{
				"app": {Collections: map[string]*registry.Collection{"posts": {Rules: &rules.Set{}}}},
			}},
		}},
	}
	a.logSecurityWarnings()
	out := buf.String()
	if !strings.Contains(out, `"level":"WARN","msg":"REALM HAS AUTH DISABLED`) || !strings.Contains(out, `"realm":"open"`) {
		t.Errorf("no warning for the auth-disabled realm: %s", out)
	}
	if !strings.Contains(out, `"msg":"realm has no rules in any collection: only API keys can access its data","realm":"closed"`) {
		t.Errorf("no warning for the auth-enabled realm without rules: %s", out)
	}
	if strings.Contains(out, `only API keys can access its data","realm":"ruled"`) {
		t.Errorf("warning for a realm with rules: %s", out)
	}
	if !strings.Contains(out, `"level":"INFO","msg":"admin API reachable from any network (no admin.allowed_networks in realm.yaml)","realm":"closed"`) ||
		strings.Contains(out, `admin.allowed_networks in realm.yaml)","realm":"open"`) {
		t.Errorf("admin network notice missing, or logged for a realm without auth: %s", out)
	}
	if strings.Contains(out, "not ready to be exposed") {
		t.Errorf("general warning still logged: %s", out)
	}
}

func TestLogAPIKeyExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	svc := &auth.Users{Store: authtest.NewMemStore(), Settings: registry.RealmSettings{AuthEnabled: true}}
	for name, ttl := range map[string]time.Duration{"forever": 0, "soon": 24 * time.Hour, "later": 60 * 24 * time.Hour, "gone": time.Millisecond} {
		if _, _, err := svc.CreateAPIKey(ctx, name, auth.KeyOptions{TTL: ttl}); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	a := &App{
		log: slog.New(slog.NewJSONHandler(&buf, nil)),
		reg: &registry.Registry{Realms: map[string]*registry.Realm{
			"r":    {Name: "r", Settings: registry.RealmSettings{AuthEnabled: true}},
			"open": {Name: "open"},
		}},
	}
	users := func(realm string) *auth.Users {
		if realm == "r" {
			return svc
		}
		return nil
	}
	if err := a.logAPIKeyExpiry(ctx, users, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`"level":"WARN","msg":"API keys that never expire: replace them with keys created with --expires","realm":"r","keys":["forever"]`,
		`"level":"WARN","msg":"API keys expire within 14 days: create replacements and roll them out","realm":"r","keys":["soon"]`,
		`"level":"INFO","msg":"expired API keys are refused but still listed: revoke them","realm":"r","keys":["gone"]`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "later") || strings.Contains(out, "bdk_") {
		t.Errorf("log mentions a healthy key or a key itself:\n%s", out)
	}
}

// checkFunctions: bundles must be built and fresh.
func TestCheckFunctions(t *testing.T) {
	dir := t.TempDir()
	fns := filepath.Join(dir, "shop", "app", registry.FunctionsDir)
	for p, c := range map[string]string{
		"shop/realm.yaml": "",
		"open/realm.yaml": "auth: disabled\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
	}
	os.MkdirAll(filepath.Join(fns, "hello"), 0o755)
	os.WriteFile(filepath.Join(fns, "hello", "function.yaml"), nil, 0o644)
	os.WriteFile(filepath.Join(fns, "hello", "index.js"), []byte("export default () => 1;\n"), 0o644)

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	load := func() *registry.Registry {
		reg, err := registry.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	if err := checkFunctions(load(), settings.Settings{}, log); err == nil || !strings.Contains(err.Error(), "functions are not built") {
		t.Errorf("unbuilt: %v", err)
	}
	// A build as `backd-server functions build` writes it.
	src, _ := registry.SourceHash(fns)
	bundle := []byte("export default () => 1;\n")
	sum := sha256.Sum256(bundle)
	os.MkdirAll(filepath.Join(fns, registry.BuildDir), 0o755)
	os.WriteFile(filepath.Join(fns, registry.BuildDir, "hello-x.js"), bundle, 0o644)
	m, _ := json.Marshal(registry.Manifest{Deno: registry.DenoVersion, Source: src, Functions: map[string]registry.ManifestBundle{"hello": {Bundle: "hello-x.js", SHA256: hex.EncodeToString(sum[:])}}})
	os.WriteFile(filepath.Join(fns, registry.BuildDir, registry.ManifestFile), m, 0o644)

	// Functions in realms with auth need no flag; the security review is done.
	if err := checkFunctions(load(), settings.Settings{}, log); err != nil {
		t.Errorf("built: %v", err)
	}
	// Realms without auth build the same way.
	os.Rename(filepath.Join(dir, "shop", "app"), filepath.Join(dir, "open", "app"))
	if err := checkFunctions(load(), settings.Settings{}, slog.New(slog.NewJSONHandler(io.Discard, nil))); err != nil {
		t.Errorf("auth disabled: %v", err)
	}
}

func TestWarnAnonymousFunctionsWithoutRateLimit(t *testing.T) {
	dir := t.TempDir()
	fns := filepath.Join(dir, "shop", "app", registry.FunctionsDir)
	os.MkdirAll(filepath.Join(dir, "shop"), 0o755)
	os.WriteFile(filepath.Join(dir, "shop", "realm.yaml"), nil, 0o644)
	for name, fnYAML := range map[string]string{
		"open":      "invoke: \"true\"\n", // anonymous allowed, no rate_limit: warn
		"limited":   "invoke: \"true\"\nrate_limit:\n  per: ip\n  limit: 10\n  window: 1m\n",
		"needsuser": "invoke: \"user != nil\"\n", // anonymous never allowed: no warning
		"keyonly":   "",                          // no invoke rule: only API keys, never anonymous
	} {
		d := filepath.Join(fns, name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "function.yaml"), []byte(fnYAML), 0o644)
		os.WriteFile(filepath.Join(d, "index.js"), nil, 0o644)
	}
	reg, err := registry.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	warnAnonymousFunctionsWithoutRateLimit(reg, slog.New(slog.NewJSONHandler(&logs, nil)))
	out := logs.String()
	if !strings.Contains(out, `"function":"shop/app/open"`) {
		t.Errorf("no warning for shop/app/open:\n%s", out)
	}
	for _, fn := range []string{"limited", "needsuser", "keyonly"} {
		if strings.Contains(out, `"function":"shop/app/`+fn+`"`) {
			t.Errorf("unexpected warning for shop/app/%s:\n%s", fn, out)
		}
	}
}
