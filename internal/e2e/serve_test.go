package e2e

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/cli"
	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/server"
)

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
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"),
		"BACKD_SECRETS_KEY": "local-dev-secrets-key-not-a-secret-0123456789abc"}
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
	runCLI := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := cli.Run(args, getenv, strings.NewReader(stdin), &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr.String())
		}
		return stdout.String()
	}

	runCLI("template", "realm", "--realm", realm)
	runCLI("template", "database", "--realm", realm, "--database", "blog", "--sample")
	// Existing files are never overwritten.
	if out := runCLI("template", "realm", "--realm", realm); !strings.Contains(out, "left unchanged") {
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
	runCLI("provision")

	ctx := context.Background()
	a, err := server.Setup(ctx, getenv, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Provisioner().Verify(ctx); err != nil {
		t.Fatalf("verify after provision: %v", err)
	}
	h := a.Handler()
	// The template's admin role lets bootstrap create the first
	// administrator, who creates an API key through the admin API.
	srv := httptest.NewServer(h)
	defer srv.Close()
	stdin = "dev-p4ssw0rd!\n"
	runCLI("bootstrap", "--realm", realm, "--email", "ops@example.com")
	// The template requires a second factor of its admin role: the first login sets it up, with
	// the code of the secret it shows.
	var loginOut, loginErr cmdtest.LockedBuffer
	secretRe := regexp.MustCompile(`secret:\s+([A-Z2-7]+)`)
	var secret string
	enrol := &cmdtest.Script{Steps: []func() string{
		func() string { return "dev-p4ssw0rd!\n" },
		func() string {
			m := secretRe.FindStringSubmatch(loginErr.String())
			if m == nil {
				t.Errorf("the login showed no secret: %q", loginErr.String())
				return "000000\n"
			}
			secret = m[1]
			c, _ := totp.GenerateCode(secret, time.Now())
			return c + "\n"
		},
	}}
	if code := cli.Run([]string{"login", "--realm", realm, "--url", srv.URL, "--email", "ops@example.com"}, getenv, enrol, &loginOut, &loginErr); code != 0 {
		t.Fatalf("login: exit %d: %s%s", code, loginOut.String(), loginErr.String())
	}
	// A key is made by a person who proves it is them again: the code of the next time step.
	next, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	stdin = next + "\n"
	key := strings.TrimSpace(runCLI("apikey", "create", "--realm", realm, "--name", "test"))
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
