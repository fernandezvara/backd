package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// On MongoDB: provision, set secrets directly (bypassing the API, as an
// operator restoring from a backup might), then `backd secret rotate-key`
// re-encrypts them under a new master key in place.
func TestSecretRotateKey(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":            "signup: open\n",
		realm + "/app/notes/schema.json": `{}`,
	})
	const oldKey = "01234567890123456789012345678901"
	const newKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ctx := context.Background()
	t.Cleanup(func() {
		client, err := mongodb.Connect(ctx, uri)
		if err != nil {
			return
		}
		defer client.Disconnect(ctx)
		for _, db := range []string{realm + "__app", realm + "___system"} {
			_ = client.Database(db).Drop(ctx)
		}
	})

	getenv := func(k string) string {
		m := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "BACKD_SECRETS_KEY": oldKey}
		return m[k]
	}
	if code := run([]string{"provision"}, getenv, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("provision failed")
	}

	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	oldCipher, err := auth.NewSecretCipher([]byte(oldKey))
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{Store: mongodb.NewAuthStore(client, realm), Realm: realm, Cipher: oldCipher}
	if err := svc.SetSecret(ctx, "app", "KEY", "sk_live_1", "test"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSecret(ctx, "", "SHARED", "shared-value", "test"); err != nil {
		t.Fatal(err)
	}

	c := &cliEnv{t: t, env: map[string]string{
		"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error",
		"BACKD_SECRETS_KEY": oldKey, "BACKD_SECRETS_NEW_KEY": newKey,
	}}
	c.expect(0, realm+": 2 secret(s) rotated", "", "secret", "rotate-key")

	// Rotating again is a no-op: everything is already under the new key.
	code, out, _ := c.run("", "secret", "rotate-key")
	if code != 0 || strings.Contains(out, "rotated") {
		t.Errorf("second rotate-key: code %d, stdout %q", code, out)
	}

	// The values read correctly under the new key, and are unreachable
	// under the old one (proving it actually re-encrypted, not just
	// relabeled).
	newCipher, err := auth.NewSecretCipher([]byte(newKey))
	if err != nil {
		t.Fatal(err)
	}
	refs := []registry.SecretRef{{Name: "KEY"}, {Realm: true, Name: "SHARED"}}
	svcNew := &auth.Users{Store: mongodb.NewAuthStore(client, realm), Realm: realm, Cipher: newCipher}
	values, missing, err := svcNew.ResolveSecrets(ctx, refs, "app")
	if err != nil || len(missing) != 0 || values["KEY"] != "sk_live_1" || values["realm.SHARED"] != "shared-value" {
		t.Fatalf("after rotation, under the new key: values=%+v missing=%v err=%v", values, missing, err)
	}
	svcOld := &auth.Users{Store: mongodb.NewAuthStore(client, realm), Realm: realm, Cipher: oldCipher}
	if _, _, err := svcOld.ResolveSecrets(ctx, refs, "app"); err == nil {
		t.Fatal("a secret rotated to the new key still opened under the old one")
	}

	delete(c.env, "BACKD_SECRETS_NEW_KEY")
	c.expect(1, "BACKD_SECRETS_NEW_KEY", "", "secret", "rotate-key")
}
