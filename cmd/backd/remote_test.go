package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
)

// writeConfig writes files under a new CONFIG_DIR and returns it.
func writeConfig(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// cliEnv runs the CLI with an environment the test can change.
type cliEnv struct {
	t   *testing.T
	env map[string]string
}

func (c *cliEnv) run(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, func(k string) string { return c.env[k] }, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// expect runs the CLI and checks the exit code and that stdout or stderr
// contains want.
func (c *cliEnv) expect(wantCode int, want, stdin string, args ...string) string {
	c.t.Helper()
	code, out, errOut := c.run(stdin, args...)
	if code != wantCode || !strings.Contains(out+errOut, want) {
		c.t.Errorf("%v: code %d, stdout %q, stderr %q; want %d and %q", args, code, out, errOut, wantCode, want)
	}
	return out
}

// The user, apikey, login, logout and whoami commands work through the
// HTTP API; here against a server with an in-memory store.
func TestCLIThroughAPI(t *testing.T) {
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml": `password:
  min_length: 10
roles:
  ops:
    admin: true
    users: [ada@example.com]
  editor:
    users: [bob@example.com]
  writer:
    users:
      - email: cy@example.com
        login_networks: [10.0.0.0/8]
`,
		"acme/app/notes/schema.json": `{}`,
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	pw := "dev-p4ssw0rd!"
	if _, err := svc.Create(ctx, "ada@example.com", &pw); err != nil { // seeded into ops
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "eve@example.com", &pw); err != nil { // no admin role
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry: reg,
		Ready:    func(context.Context) error { return nil },
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	creds := filepath.Join(t.TempDir(), "backd", "credentials")
	c := &cliEnv{t: t, env: map[string]string{"BACKD_CREDENTIALS": creds}}

	// Nothing works before logging in.
	c.expect(1, "not logged in to realm \"acme\" on "+srv.URL, "", "user", "list", "--realm", "acme", "--url", srv.URL)
	c.expect(1, "invalid_credentials", "wrong password\n", "login", "--realm", "acme", "--url", srv.URL, "--email", "ada@example.com")
	c.expect(1, "--email is required", pw+"\n", "login", "--realm", "acme", "--url", srv.URL)
	c.expect(0, "logged in to realm acme on "+srv.URL+" as ada@example.com", pw+"\n", "login", "--realm", "acme", "--url", srv.URL, "--email", "Ada@Example.com")

	// The session is kept in a file only the user can read, and the server
	// is remembered.
	if fi, err := os.Stat(creds); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Dir(creds)); fi.Mode().Perm() != 0o700 {
		t.Errorf("credentials directory mode %v", fi.Mode().Perm())
	}
	var stored credentials
	data, _ := os.ReadFile(creds)
	if err := json.Unmarshal(data, &stored); err != nil || stored.DefaultURL != srv.URL || !strings.HasPrefix(stored.Sessions[srv.URL]["acme"].Token, "bds_") {
		t.Fatalf("stored credentials: %s", data)
	}
	c.expect(0, "ada@example.com (id ", "", "whoami", "--realm", "acme")
	c.expect(0, "roles: ops", "", "whoami", "--realm", "acme")

	// Users.
	c.expect(0, "created user bob@example.com (id ", "dev-p4ssw0rd!3\n", "user", "create", "--realm", "acme", "--email", "Bob@Example.com")
	c.expect(1, "email is already registered", pw+"\n", "user", "create", "--realm", "acme", "--email", "bob@example.com")
	c.expect(1, "password: must be at least 10 characters", "short\n", "user", "create", "--realm", "acme", "--email", "dan@example.com")
	c.expect(1, "no password on standard input", "", "user", "create", "--realm", "acme", "--email", "dan@example.com")
	c.expect(0, "has no password yet", "", "user", "create", "--realm", "acme", "--email", "dan@example.com", "--no-password")
	c.expect(0, "password set for dan@example.com", "dev-p4ssw0rd!4\n", "user", "set-password", "--realm", "acme", "--email", "dan@example.com")
	c.expect(1, "user not found: nobody@example.com", "x\n", "user", "set-password", "--realm", "acme", "--email", "nobody@example.com")
	c.expect(1, "this realm doesn't send email", "", "user", "change-email", "--realm", "acme", "--email", "bob@example.com", "--new-email", "bob.new@example.com")
	c.expect(0, "email of bob@example.com marked as verified", "", "user", "verify-email", "--realm", "acme", "--email", "bob@example.com")
	c.expect(0, "dan@example.com disabled", "", "user", "disable", "--realm", "acme", "--email", "dan@example.com")
	c.expect(0, "dan@example.com enabled", "", "user", "enable", "--realm", "acme", "--email", "dan@example.com")
	c.expect(1, "add --yes to confirm", "", "user", "delete", "--realm", "acme", "--email", "dan@example.com")
	c.expect(0, "deleted dan@example.com", "", "user", "delete", "--realm", "acme", "--email", "dan@example.com", "--yes")
	c.expect(1, "user not found", "", "user", "delete", "--realm", "acme", "--email", "dan@example.com", "--yes")
	out := c.expect(0, "EMAIL", "", "user", "list", "--realm", "acme")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "ada@example.com") || !strings.Contains(lines[1], "ops") ||
		!strings.HasPrefix(lines[2], "bob@example.com") || !strings.Contains(lines[2], " true ") {
		t.Errorf("user list:\n%s", out)
	}

	// Roles: without the server's config the CLI can't tell which
	// assignments realm.yaml makes, and says so; with it, it can.
	c.expect(0, "live only in the database", "", "user", "add-role", "--realm", "acme", "--email", "ada@example.com", "--role", "editor")
	c.expect(1, "is not declared in realm.yaml", "", "user", "add-role", "--realm", "acme", "--email", "bob@example.com", "--role", "root")
	c.env["CONFIG_DIR"] = root
	_, out, errOut := c.run("", "user", "remove-role", "--realm", "acme", "--email", "bob@example.com", "--role", "editor")
	if !strings.Contains(out, `role "editor" removed from bob@example.com`) || !strings.Contains(errOut, "will be assigned again") {
		t.Errorf("role remove of a seed: %q %q", out, errOut)
	}
	c.expect(0, "stored only in the database", "", "user", "add-role", "--realm", "acme", "--email", "bob@example.com", "--role", "writer")

	// Network restrictions: shown, set per list, lifted with "none".
	c.expect(0, "admin networks: any\n  login networks: any", "", "user", "networks", "--realm", "acme", "--email", "bob@example.com")
	c.expect(0, "login networks: 10.0.0.0/8, 192.0.2.7/32", "", "user", "networks", "--realm", "acme", "--email", "bob@example.com", "--login", "10.0.0.0/8,192.0.2.7")
	c.expect(0, "admin networks: 127.0.0.1/32\n  login networks: 10.0.0.0/8", "", "user", "networks", "--realm", "acme", "--email", "bob@example.com", "--admin=127.0.0.1")
	c.expect(0, "admin networks: any\n  login networks: 10.0.0.0/8", "", "user", "networks", "--realm", "acme", "--email", "bob@example.com", "--admin", "none")
	c.expect(2, "--login: want comma-separated", "", "user", "networks", "--realm", "acme", "--email", "bob@example.com", "--login", "office")
	c.expect(0, "created user cy@example.com", "", "user", "create", "--realm", "acme", "--email", "cy@example.com", "--no-password")
	c.expect(0, "sets networks for cy@example.com, so they will be restored", "", "user", "networks", "--realm", "acme", "--email", "cy@example.com", "--login", "none")
	delete(c.env, "CONFIG_DIR")

	// API keys.
	var key string
	code, key, errOut := c.run("", "apikey", "create", "--realm", "acme", "--name", "billing", "--expires", "30d")
	if code != 0 || !strings.HasPrefix(key, "bdk_") || !strings.Contains(errOut, "can't be shown again") || !strings.Contains(errOut, "It expires at") || !strings.Contains(errOut, "(role data)") {
		t.Errorf("apikey create: code %d, stdout %q, stderr %q", code, key, errOut)
	}
	c.expect(1, "409 conflict", "", "apikey", "create", "--realm", "acme", "--name", "billing")
	c.expect(1, "name: invalid API key name", "", "apikey", "create", "--realm", "acme", "--name", "Billing")
	out = c.expect(0, "billing", "", "apikey", "list", "--realm", "acme")
	if !strings.Contains(out, key[:10]+"…") || strings.Contains(out, strings.TrimSpace(key)) {
		t.Errorf("apikey list:\n%s", out)
	}
	c.expect(0, `API key "billing" revoked`, "", "apikey", "revoke", "--realm", "acme", "--name", "billing")
	c.expect(1, `no API key named "billing"`, "", "apikey", "revoke", "--realm", "acme", "--name", "billing")
	c.expect(0, "Warning: it never expires", "", "apikey", "create", "--realm", "acme", "--name", "forever")
	c.expect(0, "(role data)", "", "apikey", "create", "--realm", "acme", "--name", "pinned", "--networks", "10.0.0.0/8,192.0.2.7", "--expires", "1d")
	_, adminKey, _ := c.run("", "apikey", "create", "--realm", "acme", "--name", "tooling", "--role", "admin", "--expires", "1d")
	out = c.expect(0, "ROLE", "", "apikey", "list", "--realm", "acme")
	if !strings.Contains(out, "tooling  admin") || !strings.Contains(out, "10.0.0.0/8,192.0.2.7/32") {
		t.Errorf("apikey list without roles or networks:\n%s", out)
	}

	// The audit trail of all of the above.
	out = c.expect(0, "TIME", "", "audit", "--realm", "acme", "--action", "apikey.create", "--limit", "2")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 || !strings.Contains(lines[1], "key:tooling") || !strings.Contains(lines[1], "role=admin") {
		t.Errorf("audit --action --limit:\n%s", out)
	}
	out = c.expect(0, "role.remove", "", "audit", "--realm", "acme", "--user", "bob@example.com", "--since", "1h")
	if !strings.Contains(out, "role=editor") || !strings.Contains(out, "user.networks") || strings.Contains(out, "key:") && strings.Contains(out, "apikey.") {
		t.Errorf("audit --user:\n%s", out)
	}
	out = c.expect(0, `"action":"user.create"`, "", "audit", "--realm", "acme", "--action", "user.create", "--json")
	var rec auditRecord
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &rec); err != nil || rec.Target == nil || !strings.HasPrefix(*rec.Target, "user:") {
		t.Errorf("audit --json: %q %v", out, err)
	}
	c.expect(1, "user not found", "", "audit", "--realm", "acme", "--user", "gone@example.com")
	c.expect(2, "--since: want", "", "audit", "--realm", "acme", "--since", "yesterday")
	c.expect(2, "use --user or --target", "", "audit", "--realm", "acme", "--user", "a@x.io", "--target", "user:x")
	c.expect(2, "value 0 is less than minimum 1", "", "audit", "--realm", "acme", "--limit", "0")

	// BACKD_API_KEY takes over from the session; a data key can't
	// administer, and the error says what can.
	c.env["BACKD_API_KEY"] = strings.TrimSpace(adminKey)
	c.expect(0, "ada@example.com", "", "user", "list", "--realm", "acme")

	// Secrets.
	svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	svc.Cache = auth.NewSecretCache()
	c.expect(0, `secret "realm.SHARED" set`, "shared-value\n", "secret", "set", "--realm", "acme", "--name", "SHARED")
	c.expect(0, `secret "KEY" set`, "sk_live_1\n", "secret", "set", "--realm", "acme", "--database", "app", "--name", "KEY")
	c.expect(1, "the value must not be empty", "\n", "secret", "set", "--realm", "acme", "--name", "EMPTY")
	c.expect(1, "400 validation_error", "x\n", "secret", "set", "--realm", "acme", "--name", "bad-name")
	out = c.expect(0, "SCOPE", "", "secret", "list", "--realm", "acme")
	lines = strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "realm") || !strings.Contains(lines[1], "SHARED") ||
		!strings.HasPrefix(lines[2], "app") || !strings.Contains(lines[2], "KEY") {
		t.Errorf("secret list:\n%s", out)
	}
	c.expect(0, `secret "realm.SHARED" deleted`, "", "secret", "delete", "--realm", "acme", "--name", "SHARED")
	c.expect(1, `no secret named "realm.SHARED"`, "", "secret", "delete", "--realm", "acme", "--name", "SHARED")
	c.expect(0, `secret "KEY" deleted`, "", "secret", "delete", "--realm", "acme", "--database", "app", "--name", "KEY")

	_, dataKey, _ := c.run("", "apikey", "create", "--realm", "acme", "--name", "service", "--expires", "1d")
	c.env["BACKD_API_KEY"] = strings.TrimSpace(dataKey)
	c.expect(1, "admin commands need a user holding an admin role", "", "user", "list", "--realm", "acme")
	delete(c.env, "BACKD_API_KEY")

	// A user without an admin role can log in, but not administer.
	c.expect(0, "logged in", pw+"\n", "login", "--realm", "acme", "--email", "eve@example.com")
	c.expect(1, "403 forbidden", "", "user", "list", "--realm", "acme")
	c.expect(1, "realm not found or authentication disabled", pw+"\n", "login", "--realm", "nope", "--email", "eve@example.com")

	// Logging out ends the session on the server too; a session the server
	// no longer accepts is forgotten, with a hint to log in again.
	c.expect(0, "logged out of realm acme", "", "logout", "--realm", "acme")
	c.expect(1, "not logged in", "", "whoami", "--realm", "acme")
	c.expect(0, "logged in", pw+"\n", "login", "--realm", "acme", "--email", "ada@example.com")
	c.expect(0, "password set for ada@example.com", pw+"\n", "user", "set-password", "--realm", "acme", "--email", "ada@example.com") // revokes her sessions
	c.expect(1, "run `backd login --realm acme --url "+srv.URL+"`", "", "user", "list", "--realm", "acme")
	c.expect(0, "logged out", "", "logout", "--realm", "acme")
	c.expect(1, "not logged in", "", "user", "list", "--realm", "acme")
}

func TestLoginArguments(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string {
		return map[string]string{"HOME": home}[k]
	}
	for _, tt := range []struct {
		args     []string
		wantCode int
		want     string
	}{
		{[]string{"login", "help"}, 0, "Usage: backd login"},
		{[]string{"login"}, 2, "--realm string (required) -> Not provided"},
		{[]string{"login", "--realm", "a", "b"}, 2, "unexpected arguments: [b]"},
		{[]string{"logout", "--realm", "a", "--email", "x"}, 2, "flag provided but not defined: -email"},
		{[]string{"login", "--realm", "acme"}, 1, "no server given"},
		{[]string{"whoami", "--realm", "acme", "--url", "ftp://x"}, 1, "invalid server URL"},
		{[]string{"whoami", "--realm", "acme", "--url", "http://localhost:1"}, 1, "not logged in"},
		{[]string{"bootstrap", "help"}, 0, "Usage: backd bootstrap"},
		{[]string{"bootstrap", "--realm", "acme"}, 2, "--email"},
		{[]string{"bootstrap", "--realm", "acme", "--email", "a@x.io"}, 1, "CONFIG_DIR is required"},
	} {
		var stderr bytes.Buffer
		if code := run(tt.args, getenv, strings.NewReader(""), &stderr, &stderr); code != tt.wantCode || !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("%v: code %d, stderr %q; want %d and %q", tt.args, code, stderr.String(), tt.wantCode, tt.want)
		}
	}
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{"https://api.example.com", false},
		{"http://api.example.com", true},
		{"http://203.0.113.5:8080", true},
		{"http://localhost:8080", false},
		{"http://127.0.0.1:8080", false},
		{"http://[::1]:8080", false},
		{"http://backd:8080", false},
	} {
		if got := plainRemote(tt.url); got != tt.want {
			t.Errorf("plainRemote(%s) = %v", tt.url, got)
		}
	}
}
