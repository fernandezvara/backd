package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/mongodb"
)

func TestRunUserArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"no command", []string{"user"}, 0, "Usage: backd user"},
		{"help", []string{"user", "help"}, 0, "Usage: backd user"},
		{"unknown command", []string{"user", "rename", "--realm", "r", "--email", "a@x.io"}, 2, `unknown command "rename"`},
		{"missing email", []string{"user", "create", "--realm", "r"}, 2, "--email string (required) -> Not provided"},
		{"extra argument", []string{"user", "list", "--realm", "r", "x"}, 2, "unexpected arguments: [x]"},
		{"unknown flag", []string{"user", "create", "--realm", "r", "--email", "a@x.io", "--admin"}, 2, "flag provided but not defined: -admin"},
		{"flag of another command", []string{"user", "list", "--realm", "r", "--yes"}, 2, "flag provided but not defined: -yes"},
		{"no server", []string{"user", "list", "--realm", "r"}, 1, "no server given"},
		{"url without value", []string{"user", "list", "--realm", "r", "--url"}, 2, "flag needs an argument: -url"},
		{"bad url", []string{"user", "list", "--realm", "r", "--url", "backd.example.com"}, 1, "invalid server URL"},
		{"networks flag on create", []string{"user", "create", "--realm", "r", "--email", "a@x.io", "--login", "10.0.0.0/8"}, 2, "flag provided but not defined: -login"},
		{"old role syntax", []string{"user", "role", "add"}, 2, `unknown command "role"`},
		{"role without role", []string{"user", "add-role", "--realm", "r", "--email", "a@x.io"}, 2, "--role string (required) -> Not provided"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			home := t.TempDir()
			getenv := func(k string) string {
				if k == "HOME" {
					return home
				}
				return ""
			}
			code := run(tt.args, getenv, strings.NewReader(""), &stderr, &stderr)
			if code != tt.wantCode || !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("code %d, stderr %q; want %d and %q", code, stderr.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}

// On MongoDB: provisioning, `backd bootstrap`, then the CLI through the
// server's own handler, and the role seeds coming back at provisioning.
func TestRunUserLifecycle(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":            "password:\n  min_length: 10\nroles:\n  ops:\n    admin: true\n  owner:\n    admin: true\n  editor:\n    users: [ada@example.com]\n",
		realm + "/app/notes/schema.json": `{}`,
		"plain/realm.yaml":               "roles:\n  editor: {}\n",
		"plain/app/notes/schema.json":    `{}`,
		"open/realm.yaml":                "auth: disabled\n",
		"open/app/notes/schema.json":     `{}`,
	})
	c := &cliEnv{t: t, env: map[string]string{
		"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "PASSWORD_HASH_CONCURRENCY": "1",
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"),
	}}
	t.Cleanup(func() {
		ctx := context.Background()
		client, err := mongodb.Connect(ctx, uri)
		if err != nil {
			return
		}
		defer client.Disconnect(ctx)
		for _, db := range []string{realm + "__app", realm + "___system", "plain__app", "plain___system", "open__app"} {
			_ = client.Database(db).Drop(ctx)
		}
	})

	// Before provisioning, bootstrap refuses to write.
	c.expect(1, "is not provisioned (run `backd provision`)", "", "bootstrap", "--realm", realm, "--email", "ada@example.com", "--role", "ops")
	// At warn level, provisioning reports realms nobody can administer.
	c.env["LOG_LEVEL"] = "warn"
	for _, want := range []string{"realm declares no admin role", "create the first administrator with `backd bootstrap`"} {
		c.expect(0, want, "", "provision")
	}
	c.env["LOG_LEVEL"] = "error"

	c.expect(1, `realm "nope" is not configured`, "", "bootstrap", "--realm", "nope", "--email", "a@example.com")
	c.expect(1, `realm "open" has auth disabled`, "", "bootstrap", "--realm", "open", "--email", "a@example.com")
	c.expect(1, "declares no admin role", "", "bootstrap", "--realm", "plain", "--email", "a@example.com")
	c.expect(1, "several admin roles (ops, owner): pick one with --role", "", "bootstrap", "--realm", realm, "--email", "ada@example.com")
	c.expect(1, `"editor" isn't an admin role`, "", "bootstrap", "--realm", realm, "--email", "ada@example.com", "--role", "editor")
	c.expect(1, "password must be at least 10 characters", "short\n", "bootstrap", "--realm", realm, "--email", "ada@example.com", "--role", "ops")
	out := c.expect(0, `created ada@example.com (id `, "dev-p4ssw0rd!\n", "bootstrap", "--realm", realm, "--email", "Ada@Example.com", "--role", "ops")
	if !strings.Contains(out, `admin role "ops"`) {
		t.Errorf("bootstrap: %q", out)
	}
	c.expect(1, "already has an administrator (ada@example.com)", "dev-p4ssw0rd!\n", "bootstrap", "--realm", realm, "--email", "bob@example.com", "--role", "ops")

	// The administrator manages the realm through the API.
	a, err := setup(context.Background(), func(k string) string { return c.env[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	c.expect(0, "logged in", "dev-p4ssw0rd!\n", "login", "--realm", realm, "--url", srv.URL, "--email", "ada@example.com")
	c.expect(0, "created user bob@example.com", "dev-p4ssw0rd!3\n", "user", "create", "--realm", realm, "--email", "bob@example.com")
	c.expect(0, `role "owner" assigned`, "", "user", "add-role", "--realm", realm, "--email", "bob@example.com", "--role", "owner")
	c.expect(0, `role "editor" removed`, "", "user", "remove-role", "--realm", realm, "--email", "ada@example.com", "--role", "editor")
	c.expect(0, "login networks: 10.0.0.0/8", "", "user", "networks", "--realm", realm, "--email", "bob@example.com", "--login", "10.0.0.0/8")
	key := c.expect(0, "bdk_", "", "apikey", "create", "--realm", realm, "--name", "tooling", "--role", "admin", "--expires", "1d")
	c.env["BACKD_API_KEY"] = strings.TrimSpace(key)
	c.expect(0, "tooling  admin", "", "apikey", "list", "--realm", realm)
	delete(c.env, "BACKD_API_KEY")
	c.expect(0, "logged out", "", "logout", "--realm", realm)

	// The seed comes back with the next provision.
	c.expect(0, "", "", "provision")
	c.expect(0, "logged in", "dev-p4ssw0rd!\n", "login", "--realm", realm, "--url", srv.URL, "--email", "ada@example.com")
	out = c.expect(0, "EMAIL", "", "user", "list", "--realm", realm)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ada@example.com") && !strings.Contains(line, "editor") {
			t.Errorf("seed not reapplied: %q", line)
		}
		if strings.HasPrefix(line, "bob@example.com") && !strings.Contains(line, "owner") {
			t.Errorf("role missing: %q", line)
		}
	}
}

func TestRunAPIKeyArguments(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"no command", []string{"apikey"}, 0, "Usage: backd apikey"},
		{"help", []string{"apikey", "help"}, 0, "Usage: backd apikey"},
		{"unknown command", []string{"apikey", "rotate", "--realm", "r", "k"}, 2, `unknown command "rotate"`},
		{"missing name", []string{"apikey", "create", "--realm", "r"}, 2, "--name string (required) -> Not provided"},
		{"expires without value", []string{"apikey", "create", "--realm", "r", "--name", "k", "--expires"}, 2, "flag needs an argument: -expires"},
		{"role without value", []string{"apikey", "create", "--realm", "r", "--name", "k", "--role"}, 2, "flag needs an argument: -role"},
		{"bad role", []string{"apikey", "create", "--realm", "r", "--name", "k", "--role", "root"}, 2, "not one of"},
		{"role on list", []string{"apikey", "list", "--realm", "r", "--role", "admin"}, 2, "flag provided but not defined: -role"},
		{"bad networks", []string{"apikey", "create", "--realm", "r", "--name", "k", "--networks", "10.0.0.0/8,office"}, 2, "--networks: want comma-separated"},
		{"bad expires", []string{"apikey", "create", "--realm", "r", "--name", "k", "--expires", "soon"}, 2, "want a positive duration"},
		{"expires on list", []string{"apikey", "list", "--realm", "r", "--expires", "1d"}, 2, "flag provided but not defined: -expires"},
		{"no server", []string{"apikey", "list", "--realm", "r"}, 1, "no server given"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			home := t.TempDir()
			getenv := func(k string) string {
				if k == "HOME" {
					return home
				}
				return ""
			}
			code := run(tt.args, getenv, strings.NewReader(""), &stderr, &stderr)
			if code != tt.wantCode || !strings.Contains(stderr.String(), tt.wantErr) {
				t.Errorf("code %d, stderr %q; want %d and %q", code, stderr.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}
