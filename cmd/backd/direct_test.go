package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/mongodb"
)

// notesPackage writes a package of two notes by hand.
func notesPackage(t *testing.T) (dir string, lines string) {
	t.Helper()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var b bytes.Buffer
	for _, id := range []string{"9m4e2mr0ui3e8a20014g", "9m4e2mr0ui3e8a20024g"} {
		line, err := exportpkg.EncodeDocument(map[string]any{
			"id": id, "title": "note " + id[len(id)-3:],
			"_meta": map[string]any{"version": int64(1), "created_at": at, "updated_at": at},
		})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "notes.jsonl"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	h := exportpkg.NewHasher()
	h.Write(b.Bytes())
	m := &exportpkg.Manifest{Format: 1, Realm: "src", Checksums: map[string]string{"app/notes.jsonl": h.Sum()},
		Databases: []exportpkg.Database{{Name: "app", Collections: []exportpkg.Collection{{Name: "notes", Documents: 2, Part: "app/notes.jsonl"}}}}}
	data, _ := m.Marshal()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, b.String()
}

// Offline, straight on MongoDB: import a package, export it back, and the refusals around it.
func TestExportImportDirect(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":            "signup: open\n",
		realm + "/app/notes/schema.json": `{"type": "object", "properties": {"title": {"type": "string"}}, "required": ["title"]}`,
	})
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, db := range []string{realm + "__app", realm + "___system"} {
			_ = client.Database(db).Drop(ctx)
		}
		_ = client.Disconnect(ctx)
	})
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error"}
	if code := run([]string{"provision"}, func(k string) string { return env[k] }, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("provision failed")
	}
	c := &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "HOME": t.TempDir()}}
	pkg, lines := notesPackage(t)

	// The flags that belong to the direct mode are refused without it.
	c.expect(2, "--mongo-uri needs --direct", "", "export", "--realm", realm, "--out", filepath.Join(t.TempDir(), "x"), "--mongo-uri", "mongodb://x")
	c.expect(2, "--force needs --direct", "", "import", "--realm", realm, "--from", pkg, "--force")
	c.expect(1, "has no realm", "", "import", "--realm", "ghost", "--from", pkg, "--direct")

	// A dry run reads and writes nothing.
	c.expect(0, "dry run: nothing was written", "", "import", "--realm", realm, "--from", pkg, "--direct", "--dry-run")
	out0 := filepath.Join(t.TempDir(), "empty")
	c.expect(0, "exported 0 documents", "", "export", "--realm", realm, "--out", out0, "--direct")

	// A running instance of the realm stops the import, unless it is forced.
	if err := mongodb.Heartbeat(ctx, client, "inst-"+realm, []string{realm}, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.expect(1, "served by 1 running backd", "", "import", "--realm", realm, "--from", pkg, "--direct")
	if _, err := os.Stat(filepath.Join(out0, "app", "notes.jsonl")); err != nil {
		t.Fatal(err)
	}
	mongodb.ReleaseInstance(ctx, client, "inst-"+realm)
	// A silent one doesn't count.
	if err := mongodb.Heartbeat(ctx, client, "old-"+realm, []string{realm}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	defer mongodb.ReleaseInstance(ctx, client, "old-"+realm)
	c.expect(0, "imported 2 documents", "", "import", "--realm", realm, "--from", pkg, "--direct")

	// Straight back out: the same documents, in the same form.
	out := filepath.Join(t.TempDir(), "back")
	c.expect(0, "exported 2 documents", "", "export", "--realm", realm, "--out", out, "--direct")
	got, err := os.ReadFile(filepath.Join(out, "app", "notes.jsonl"))
	if err != nil || string(got) != lines {
		t.Errorf("exported:\n%s\nwant:\n%s (%v)", got, lines, err)
	}
	// Again: nothing to do.
	c.expect(0, "2 unchanged", "", "import", "--realm", realm, "--from", pkg, "--direct")

	// The records say it was offline, and who.
	records, _, err := mongodb.NewAuthStore(client, realm).ListAudit(ctx, auth.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, r := range records {
		if r.Action == auth.AuditImport || r.Action == auth.AuditImportDone || r.Action == auth.AuditExport || r.Action == auth.AuditExportDone {
			seen++
			if r.Details["offline"] != true || r.Actor != auth.ActorOffline {
				t.Errorf("%s: actor %q details %v", r.Action, r.Actor, r.Details)
			}
		}
	}
	if seen < 6 {
		t.Errorf("audit records of the offline work: %d", seen)
	}
}

// Authentication data offline: what the realm sealed needs its master key, and without it is
// said to be left behind.
func TestExportAuthDirectNeedsTheMasterKey(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{realm + "/realm.yaml": "signup: open\n", realm + "/app/notes/schema.json": `{}`})
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, db := range []string{realm + "__app", realm + "___system"} {
			_ = client.Database(db).Drop(ctx)
		}
		_ = client.Disconnect(ctx)
	})
	const master = "0123456789012345678901234567890123"
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "HOME": t.TempDir(), "MASTER": master}
	if code := run([]string{"provision"}, func(k string) string { return env[k] }, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("provision failed")
	}
	cipher, _ := auth.NewSecretCipher([]byte(master))
	svc := &auth.Users{Store: mongodb.NewAuthStore(client, realm), Realm: realm, Cipher: cipher, Hasher: auth.NewHasher(1, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}), Settings: auth.Users{}.Settings}
	svc.Settings.PasswordMinLength = 12
	pw := "dev-p4ssw0rd!"
	u, err := svc.Create(ctx, "ada@example.com", &pw)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := svc.SealToken("JBSWY3DPEHPK3PXP")
	if err := svc.Store.(*mongodb.AuthStore).PutMFA(ctx, auth.MFARecord{UserID: u.ID, Secret: sealed, EnabledAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	c := &cliEnv{t: t, env: env}

	// With the master key: the secret is sealed again under the export key, which opens it.
	dir := filepath.Join(t.TempDir(), "pkg")
	keyFile := filepath.Join(t.TempDir(), "export.key")
	c.expect(0, "exported", "", "export", "--realm", realm, "--out", dir, "--direct", "--include", "auth", "--master-key", "env:MASTER", "--auth-key-out", keyFile)
	keyText, _ := os.ReadFile(keyFile)
	ek, err := auth.ExportKey(strings.TrimSpace(string(keyText)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "_system", "auth", "mfa.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := exportpkg.DecodeDocument(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := auth.OpenWith(ek, doc["secret"].(string)); err != nil || plain != "JBSWY3DPEHPK3PXP" {
		t.Errorf("secret: %q %v", plain, err)
	}
	if m, _ := os.ReadFile(filepath.Join(dir, "_system", "auth", "users.jsonl")); !strings.Contains(string(m), "ada@example.com") {
		t.Errorf("users: %s", m)
	}

	// Without it the second factor stays behind, and the command says so.
	dir2 := filepath.Join(t.TempDir(), "pkg2")
	c.expect(0, "second factors or push devices were left out", "", "export", "--realm", realm, "--out", dir2, "--direct", "--include", "auth")
	if raw, _ := os.ReadFile(filepath.Join(dir2, "_system", "auth", "mfa.jsonl")); len(raw) != 0 {
		t.Errorf("mfa without the master key: %s", raw)
	}
}
