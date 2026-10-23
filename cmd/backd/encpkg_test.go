package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	clinicSchema = `{"type":"object","properties":{"name":{"type":"string"},"notes":{"type":"string"},"tax_id":{"type":"string"}}}`
	clinicConfig = "fields:\n  notes:\n    encrypt: {key: HEALTH}\n  tax_id:\n    encrypt: {key: TAX, search: equality}\n"
	masterKey    = "0123456789012345678901234567890123"
)

// clinicRealm provisions a realm with encrypted fields on MongoDB and returns a way to write and
// read its documents the way the server does (sealing on write, opening on read).
type clinicRealm struct {
	name string
	root string
	svc  *auth.Users
	repo storage.Repository
	ring *fieldcrypt.Ring
	keys map[string]string
}

func newClinic(t *testing.T, uri string, keys map[string]string) *clinicRealm {
	t.Helper()
	ctx := context.Background()
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":                    "signup: open\n",
		realm + "/main/patients/schema.json":     clinicSchema,
		realm + "/main/patients/collection.yaml": clinicConfig,
	})
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, db := range []string{realm + "__main", realm + "___system"} {
			_ = client.Database(db).Drop(ctx)
		}
		_ = client.Disconnect(ctx)
	})
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error"}
	if code := run([]string{"provision"}, func(k string) string { return env[k] }, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("provision failed")
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := auth.NewSecretCipher([]byte(masterKey))
	svc := &auth.Users{Store: mongodb.NewAuthStore(client, realm), Realm: realm, Cipher: cipher, Settings: reg.Realms[realm].Settings}
	svc.KeyUses = reg.Realms[realm].EncryptionKeyUses
	made := map[string]string{}
	for _, name := range []string{"HEALTH", "TAX"} {
		k, err := svc.CreateKey(ctx, name, keys[name], "test")
		if err != nil {
			t.Fatal(err)
		}
		made[name] = k.Value
		if keys[name] != "" {
			made[name] = keys[name]
		}
	}
	ring := &fieldcrypt.Ring{Realm: realm, Store: svc.Store.(fieldcrypt.Store), Keys: svc.KeyMaterial}
	c, _ := reg.Collection(realm, "main", "patients")
	if err := ring.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	store := &mongodb.Store{Client: client, Crypt: func(string) *fieldcrypt.Ring { return ring }}
	return &clinicRealm{name: realm, root: root, svc: svc, repo: store.Repository(c), ring: ring, keys: made}
}

func (r *clinicRealm) add(t *testing.T, name, notes, tax string) string {
	t.Helper()
	id := xid.New().String()
	now := time.Now().UTC().Truncate(time.Millisecond)
	doc := map[string]any{"id": id, "name": name, "notes": notes, "tax_id": tax,
		"_meta": map[string]any{"created_at": now, "updated_at": now, "version": int64(1), "owner": nil, "created_by": "key:test", "updated_by": "key:test"}}
	if err := r.repo.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *clinicRealm) env(t *testing.T) *cliEnv {
	return &cliEnv{t: t, env: map[string]string{"CONFIG_DIR": r.root, "MONGO_URI": os.Getenv("MONGO_TEST_URI"), "LOG_LEVEL": "error", "HOME": t.TempDir(), "BACKD_SECRETS_KEY": masterKey}}
}

func (r *clinicRealm) read(t *testing.T) map[string]storage.Document {
	t.Helper()
	page, err := r.repo.List(context.Background(), storage.Query{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]storage.Document{}
	for _, d := range page.Items {
		out[d["name"].(string)] = d
	}
	return out
}

func TestEncryptedFieldsTravelAsCiphertextAndOpenWithTheirKeys(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	ctx := context.Background()
	src := newClinic(t, uri, nil)
	src.add(t, "Ana", "allergic to nuts", "B1234")
	src.add(t, "Ben", "no allergies", "B5678")
	c := src.env(t)

	// An export carries ciphertext and the wrapped keys, and never the master key or a plaintext.
	pkg := filepath.Join(t.TempDir(), "pkg")
	c.expect(0, "exported 2 documents", "", "export", "--realm", src.name, "--out", pkg, "--direct")
	docs, _ := os.ReadFile(filepath.Join(pkg, "main", "patients.jsonl"))
	for _, secret := range []string{"allergic", "B1234", "no allergies"} {
		if strings.Contains(string(docs), secret) {
			t.Errorf("the package holds %q in the clear", secret)
		}
	}
	if strings.Count(string(docs), `"subType":"06"`) != 4 {
		t.Errorf("encrypted values: %s", docs)
	}
	keysFile, _ := os.ReadFile(filepath.Join(pkg, "_system", "data_keys.jsonl"))
	if lines := strings.Count(strings.TrimSpace(string(keysFile)), "\n") + 1; lines != 3 { // data: notes, tax_id; index: tax_id
		t.Errorf("data keys in the package: %d\n%s", lines, keysFile)
	}
	if strings.Contains(string(keysFile), src.keys["HEALTH"]) || strings.Contains(string(keysFile), masterKey) {
		t.Error("a key leaked into the package")
	}
	data, _ := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	m, err := exportpkg.ParseManifest(data)
	if err != nil || len(m.Encrypted) != 2 || m.Encrypted[0].Form != exportpkg.FormCiphertext || m.Encrypted[0].Fingerprint == "" || m.Encrypted[0].Database != "main" {
		t.Fatalf("manifest: %+v %v", m.Encrypted, err)
	}

	// An anonymized copy for staging keeps encrypted fields as ciphertext.
	anon := filepath.Join(t.TempDir(), "anon")
	c.expect(0, "exported 2 documents", "", "export", "--realm", src.name, "--out", anon, "--direct", "--anonymize")
	if g, _ := os.ReadFile(filepath.Join(anon, "main", "patients.jsonl")); strings.Contains(string(g), "allergic") || strings.Count(string(g), `"subType":"06"`) != 4 {
		t.Errorf("an anonymized package: %s", g)
	}

	// A reporting extract carries ciphertext too (base64 in a CSV cell), and opens with a key.
	csvDir := filepath.Join(t.TempDir(), "csv")
	c.expect(0, "exported 2 documents", "", "export", "--realm", src.name, "--out", csvDir, "--direct", "--format", "csv", "--field", "notes=key:"+src.keys["HEALTH"])
	if g, _ := os.ReadFile(filepath.Join(csvDir, "main", "patients.csv")); !strings.Contains(string(g), "allergic to nuts") || strings.Contains(string(g), "B1234") {
		t.Errorf("a CSV extract: %s", g)
	}

	// With a key, that field opens in the command; the other stays ciphertext, and the manifest says so.
	part := filepath.Join(t.TempDir(), "part")
	c.expect(0, "still encrypted", "", "export", "--realm", src.name, "--out", part, "--direct", "--field", "notes=key:"+src.keys["HEALTH"])
	got, _ := os.ReadFile(filepath.Join(part, "main", "patients.jsonl"))
	if !strings.Contains(string(got), "allergic to nuts") || strings.Contains(string(got), "B1234") || strings.Count(string(got), `"subType":"06"`) != 2 {
		t.Errorf("a partly opened package: %s", got)
	}
	data, _ = os.ReadFile(filepath.Join(part, "manifest.json"))
	pm, _ := exportpkg.ParseManifest(data)
	forms := map[string]string{}
	for _, e := range pm.Encrypted {
		forms[e.Path] = e.Form
	}
	if forms["notes"] != "plaintext" || forms["tax_id"] != "ciphertext" {
		t.Errorf("forms = %v", forms)
	}
	c.expect(0, "dry run", "", "import", "--realm", src.name, "--from", part, "--direct", "--dry-run", "--mode", "skip") // checksums hold

	// A wrong key is reported by field and key name; a key for nothing is refused.
	other, _ := auth.NewKey()
	c.expect(2, "not that key", "", "export", "--realm", src.name, "--out", filepath.Join(t.TempDir(), "w"), "--direct", "--field", "notes=key:"+b64(other))
	c.expect(2, "matches no encrypted field", "", "export", "--realm", src.name, "--out", filepath.Join(t.TempDir(), "n"), "--direct", "--field", "nothing=key:"+src.keys["HEALTH"])

	// Offline: no server, no database, no master key.
	plain := filepath.Join(t.TempDir(), "plain")
	offline := &cliEnv{t: t, env: map[string]string{"HOME": t.TempDir()}}
	offline.expect(0, "in plaintext", "", "export-decrypt", "--from", pkg, "--out", plain, "--field", "notes=key:"+src.keys["HEALTH"]+";main/patients.tax_id=key:"+src.keys["TAX"])
	full, _ := os.ReadFile(filepath.Join(plain, "main", "patients.jsonl"))
	for _, want := range []string{"allergic to nuts", "no allergies", "B1234", "B5678"} {
		if !strings.Contains(string(full), want) {
			t.Errorf("the opened package lacks %q", want)
		}
	}
	offline.expect(1, "not empty", "", "export-decrypt", "--from", pkg, "--out", plain, "--field", "notes=key:"+src.keys["HEALTH"])

	// Import into another realm that holds the same notes key and its own tax key: the notes are opened
	// by the target itself, the tax ids with the source key given here; both are sealed under the target's keys.
	dst := newClinic(t, uri, map[string]string{"HEALTH": src.keys["HEALTH"]})
	dc := dst.env(t)
	dc.expect(1, "tax_id", "", "import", "--realm", dst.name, "--from", pkg, "--direct", "--dry-run")
	dc.expect(0, "dry run: nothing was written", "", "import", "--realm", dst.name, "--from", pkg, "--direct", "--dry-run", "--field", "tax_id=key:"+src.keys["TAX"])
	if len(dst.read(t)) != 0 {
		t.Fatal("a dry run wrote")
	}
	dc.expect(0, "imported 2 documents", "", "import", "--realm", dst.name, "--from", pkg, "--direct", "--field", "tax_id=key:"+src.keys["TAX"])
	back := dst.read(t)
	if back["Ana"]["notes"] != "allergic to nuts" || back["Ana"]["tax_id"] != "B1234" || back["Ben"]["tax_id"] != "B5678" {
		t.Errorf("imported: %v", back)
	}
	// The same documents again: nothing to do, though every sealed value differs.
	dc.expect(0, "2 unchanged", "", "import", "--realm", dst.name, "--from", pkg, "--direct", "--field", "tax_id=key:"+src.keys["TAX"])
	// Sealed under the target's keys, with the blind index built: a search finds them.
	page, err := dst.repo.List(ctx, storage.Query{Filter: storage.Condition{Field: "tax_id", Op: storage.OpEq, Value: "B5678"}, Limit: 5})
	if err != nil || len(page.Items) != 1 || page.Items[0]["name"] != "Ben" {
		t.Errorf("search in the target: %v %v", page.Items, err)
	}

	// A target with neither key refuses, naming the fields.
	empty := newClinic(t, uri, nil)
	empty.env(t).expect(1, "notes", "", "import", "--realm", empty.name, "--from", pkg, "--direct")

	// Recovery: the instance lost its secrets; saving the keys under the same names makes the data readable again.
	if err := dst.svc.Store.DeleteSecret(ctx, "", "HEALTH"); err != nil {
		t.Fatal(err)
	}
	dst.ring.Reset()
	if _, err := dst.repo.Get(ctx, firstID(t, dst)); err == nil {
		t.Fatal("a value opened without its key")
	}
	// Offline, with no running backd, which would refuse to start without the key anyway.
	dc.expect(0, "imported, fingerprint HEALTH:", "", "secret", "create", "--realm", dst.name, "--type", "key", "--name", "HEALTH", "--value", "key:"+src.keys["HEALTH"], "--direct")
	dc.expect(0, "exists already", "", "secret", "create", "--realm", dst.name, "--type", "key", "--name", "HEALTH", "--generate", "--skip-existing", "--direct")
	dc.expect(1, "409", "", "secret", "create", "--realm", dst.name, "--type", "key", "--name", "HEALTH", "--generate", "--direct")
	if out := dc.expect(0, "FINGERPRINT", "", "secret", "list", "--realm", dst.name, "--direct"); !strings.Contains(out, "HEALTH") || strings.Contains(out, src.keys["HEALTH"]) {
		t.Errorf("secret list --direct: %s", out)
	}
	dst.ring.Reset()
	if d, err := dst.repo.Get(ctx, firstID(t, dst)); err != nil || d["notes"] == nil {
		t.Errorf("after restoring the key: %v %v", d, err)
	}
}

func firstID(t *testing.T, r *clinicRealm) string {
	t.Helper()
	page, err := r.repo.List(storage.WithRawEncrypted(context.Background()), storage.Query{Limit: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	return page.Items[0]["id"].(string)
}

func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }
