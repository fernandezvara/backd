package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

func TestLogSecurityWarnings(t *testing.T) {
	var buf bytes.Buffer
	a := &app{
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
	if !strings.Contains(out, `"msg":"realm has no rules.yaml in any collection: only API keys can access its data","realm":"closed"`) {
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

// TestTemplatesServeSample checks that templated config starts and
// serves the blog sample, end to end through the CLI and the handler
// that `backd serve` runs.
func TestTemplatesServeSample(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	bin := os.Getenv("DENO")
	if bin == "" {
		bin = "deno"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("deno not found; skipping (runs in the dockerized test image) — the sample database now includes a sample function")
	}
	realm := "t" + xid.New().String()
	env := map[string]string{"CONFIG_DIR": t.TempDir(), "MONGO_URI": uri, "LOG_LEVEL": "error", "PASSWORD_HASH_CONCURRENCY": "1",
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials")}
	getenv := func(k string) string { return env[k] }
	t.Cleanup(func() {
		ctx := context.Background()
		if client, err := mongodb.Connect(ctx, uri); err == nil {
			for _, db := range []string{realm + "__blog", realm + "___system"} {
				_ = client.Database(db).Drop(ctx)
			}
			_ = client.Disconnect(ctx)
		}
	})
	stdin := ""
	cli := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run(args, getenv, strings.NewReader(stdin), &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr.String())
		}
		return stdout.String()
	}

	cli("template", "realm", "--realm", realm)
	cli("template", "database", "--realm", realm, "--database", "blog", "--sample")
	// Existing files are never overwritten.
	if out := cli("template", "realm", "--realm", realm); !strings.Contains(out, "left unchanged") {
		t.Errorf("second template run: %q", out)
	}
	// The sample database includes a sample function (roadmap F16): build
	// it before provisioning, the same as any real deployment would.
	reg, err := registry.Load(env["CONFIG_DIR"])
	if err != nil {
		t.Fatal(err)
	}
	if err := functions.Build(context.Background(), reg, functions.Options{Deno: bin}); err != nil {
		t.Fatal(err)
	}
	cli("provision")

	ctx := context.Background()
	a, err := setup(ctx, getenv, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if err := a.provisioner().Verify(ctx); err != nil {
		t.Fatalf("verify after provision: %v", err)
	}
	h := a.handler()
	// The template's admin role lets bootstrap create the first
	// administrator, who creates an API key through the admin API.
	srv := httptest.NewServer(h)
	defer srv.Close()
	stdin = "dev-p4ssw0rd!\n"
	cli("bootstrap", "--realm", realm, "--email", "ops@example.com")
	cli("login", "--realm", realm, "--url", srv.URL, "--email", "ops@example.com")
	stdin = ""
	key := strings.TrimSpace(cli("apikey", "create", "--realm", realm, "--name", "test"))
	do := func(method, body, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/"+realm+"/blog/posts", strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("POST", `{"title": "Hello", "published": true}`, key); rec.Code != http.StatusCreated {
		t.Fatalf("create post: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", `{"body": "no title"}`, key); rec.Code != http.StatusBadRequest {
		t.Errorf("post without title: %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "", key); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"Hello"`) {
		t.Errorf("list posts: %d %s", rec.Code, rec.Body)
	}
	// The sample rules: anonymous callers read published posts, but can't write.
	if rec := do("POST", `{"title": "draft", "published": false}`, key); rec.Code != http.StatusCreated {
		t.Fatalf("create draft: %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"Hello"`) || strings.Contains(rec.Body.String(), `"title":"draft"`) {
		t.Errorf("anonymous list: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", `{"title": "spam"}`, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create: %d", rec.Code)
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
	a := &app{
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
