package e2e

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/server"
)

// On MongoDB: provisioning, `backd bootstrap`, then the CLI through the
// server's own handler, and the role seeds coming back at provisioning.
func TestRunUserLifecycle(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := cmdtest.Write(t, map[string]string{
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
	a, err := server.Setup(context.Background(), func(k string) string { return c.env[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c.expect(0, "logged in", "dev-p4ssw0rd!\n", "login", "--realm", realm, "--url", srv.URL, "--email", "ada@example.com")
	c.expect(0, "created user bob@example.com", "dev-p4ssw0rd!3\n", "user", "create", "--realm", realm, "--email", "bob@example.com")
	c.expect(0, `role "owner" assigned`, "", "user", "add-role", "--realm", realm, "--email", "bob@example.com", "--role", "owner")
	c.expect(0, `role "editor" removed`, "", "user", "remove-role", "--realm", realm, "--email", "ada@example.com", "--role", "editor")
	c.expect(0, "login networks: 10.0.0.0/8", "", "user", "networks", "--realm", realm, "--email", "bob@example.com", "--login", "10.0.0.0/8")
	// A key is made by a person who proves it is them again (the password, on standard input here).
	c.expect(1, "no password on standard input", "", "apikey", "create", "--realm", realm, "--name", "tooling", "--role", "admin", "--expires", "1d")
	key := c.expect(0, "bdk_", "dev-p4ssw0rd!\n", "apikey", "create", "--realm", realm, "--name", "tooling", "--role", "admin", "--expires", "1d")
	c.env["BACKD_API_KEY"] = strings.TrimSpace(key)
	c.expect(0, "tooling  admin", "", "apikey", "list", "--realm", realm)
	delete(c.env, "BACKD_API_KEY")
	c.expect(0, "logged out", "", "logout", "--realm", realm)

	// Break glass: an operator with the instance's database resets a second factor directly,
	// audited as the instance (not as an administrator), and it needs no session.
	users, err := a.UsersOf(context.Background(), realm)
	if err != nil {
		t.Fatal(err)
	}
	adaUser, err := users.Find(context.Background(), "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Store.PutMFA(context.Background(), auth.MFARecord{UserID: adaUser.ID, Secret: "k.n.c", EnabledAt: time.Now(), RecoveryCodes: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	c.expect(1, "add --yes to confirm", "", "user", "mfa-reset", "--local", "--realm", realm, "--email", "ada@example.com")
	c.expect(0, "second factor of ada@example.com removed", "", "user", "mfa-reset", "--local", "--yes", "--realm", realm, "--email", "ada@example.com")
	c.expect(1, "has no second factor", "", "user", "mfa-reset", "--local", "--yes", "--realm", realm, "--email", "ada@example.com")
	trail, _, err := users.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditMFAReset, Limit: 5})
	if err != nil || len(trail) != 1 || trail[0].Actor != auth.ActorInstance || trail[0].Target != "user:"+adaUser.ID {
		t.Errorf("audit: %+v %v", trail, err)
	}

	// The seed comes back with the next provision.
	c.expect(0, "", "", "provision")
	c.expect(0, "logged in", "dev-p4ssw0rd!\n", "login", "--realm", realm, "--url", srv.URL, "--email", "ada@example.com")
	out = c.expect(0, "EMAIL", "", "user", "list", "--realm", realm)
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "ada@example.com") && !strings.Contains(line, "editor") {
			t.Errorf("seed not reapplied: %q", line)
		}
		if strings.HasPrefix(line, "bob@example.com") && !strings.Contains(line, "owner") {
			t.Errorf("role missing: %q", line)
		}
	}
}
