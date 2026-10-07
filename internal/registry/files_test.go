package registry

import (
	"strings"
	"testing"
)

const filesRealm = "roles:\n  staff: {}\n" + storageBase

func filesTree(t *testing.T, collectionYAML string, extra map[string]string) (*Registry, error) {
	t.Helper()
	files := map[string]string{
		"shop/realm.yaml":                filesRealm,
		"shop/app/notes/schema.json":     `{"type": "object", "properties": {"title": {"type": "string"}}, "required": ["title"]}`,
		"shop/app/notes/collection.yaml": collectionYAML,
	}
	for k, v := range extra {
		files[k] = v
	}
	return Load(writeTree(t, files))
}

func TestFileFieldsAreInjectedIntoTheSchema(t *testing.T) {
	reg, err := filesTree(t, `
files:
  avatar:
    max_size: 2MiB
    types: [image/png, image/*, IMAGE/JPEG]
  receipts:
    multiple: true
    max_files: 3
    max_size: 10MB
    download: proxy
    presigned_ttl: 1h
    cache: 30m
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "app", "notes")
	av, rc := c.Files["avatar"], c.Files["receipts"]
	if av == nil || av.Multiple || av.MaxSize != 2<<20 || av.Upload != UploadProxy || len(av.Types) != 3 || av.Types[2] != "image/jpeg" {
		t.Errorf("avatar: %+v", av)
	}
	if rc == nil || !rc.Multiple || rc.MaxFiles != 3 || rc.MaxSize != 10_000_000 || rc.Download != DownloadProxy || rc.Cache.Minutes() != 30 || rc.PresignedTTL.Hours() != 1 {
		t.Errorf("receipts: %+v", rc)
	}
	if !av.Allows("image/png") || !av.Allows("image/webp; charset=x") || av.Allows("application/pdf") || !(&FileField{}).Allows("anything/x") {
		t.Error("Allows")
	}

	// A family never admits what runs in a browser: it has to be named.
	imgs := &FileField{Types: []string{"image/*", "text/*"}}
	named := &FileField{Types: []string{"image/svg+xml", "text/html"}}
	for _, ct := range []string{"image/svg+xml", "text/html", "text/html; charset=utf-8", "application/xhtml+xml"} {
		if imgs.Allows(ct) {
			t.Errorf("image/* and text/* admit %s", ct)
		}
	}
	if !imgs.Allows("image/png") || !imgs.Allows("text/plain") || !named.Allows("image/svg+xml") || !named.Allows("text/html") || !(&FileField{}).Allows("text/html") {
		t.Error("what is named or unrestricted is allowed")
	}

	ok := map[string]any{"title": "t", "avatar": map[string]any{"id": "fl_" + strings.Repeat("a", 20), "name": "a.png", "size": int64(3), "type": "image/png", "sha256": strings.Repeat("0", 64), "uploaded_at": "2026-10-06T10:00:00Z"}}
	if err := c.Schema.Validate(ok); err != nil {
		t.Errorf("a document with a file: %v", err)
	}
	bad := map[string]any{"title": "t", "avatar": map[string]any{"id": "x", "name": "a.png"}}
	if err := c.Schema.Validate(bad); err == nil {
		t.Error("incomplete file details were accepted")
	}
	four := make([]any, 4)
	for i := range four {
		four[i] = ok["avatar"]
	}
	if err := c.Schema.Validate(map[string]any{"title": "t", "receipts": four}); err == nil {
		t.Error("more files than max_files were accepted")
	}
	// The author's own schema is untouched, and rules and queries know the new fields.
	if _, in := c.RawSchema["properties"].(map[string]any)["title"]; !in {
		t.Error("the author's properties were lost")
	}
	for _, path := range []string{"avatar", "avatar.size", "avatar.type", "receipts", "receipts.name"} {
		if !c.IsKnownField(path) {
			t.Errorf("%s isn't a known field", path)
		}
	}
}

func TestFileFieldErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"no max_size", "files:\n  a: {}\n", "files.a: max_size: is required"},
		{"bad size", "files:\n  a: {max_size: lots}\n", "files.a: max_size: invalid size"},
		{"bad name", "files:\n  Avatar: {max_size: 1MB}\n", "the name must be lower-case"},
		{"system name", "files:\n  _meta: {max_size: 1MB}\n", "the name must be lower-case"},
		{"in schema.json", "files:\n  title: {max_size: 1MB}\n", "is also declared in schema.json"},
		{"max_files alone", "files:\n  a: {max_size: 1MB, max_files: 3}\n", "only applies to a field with `multiple: true`"},
		{"max_files range", "files:\n  a: {max_size: 1MB, multiple: true, max_files: 0}\n", "max_files: must be between 1 and 1000"},
		{"type", "files:\n  a: {max_size: 1MB, types: [png]}\n", "must be a content type"},
		{"upload mode", "files:\n  a: {max_size: 1MB, upload: ftp}\n", "upload: must be proxy or direct"},
		{"direct over one PUT", "files:\n  a: {max_size: 6GiB, upload: direct}\n", "at most 5GiB"},
		{"download", "files:\n  a: {max_size: 1MB, download: cdn}\n", "download: must be presigned or proxy"},
		{"ttl", "files:\n  a: {max_size: 1MB, presigned_ttl: 8d}\n", "presigned_ttl: must be between"},
		{"cache without proxy", "files:\n  a: {max_size: 1MB, cache: 1h}\n", "cache: only applies to proxied downloads"},
		{"unknown key", "files:\n  a: {max_size: 1MB, colour: red}\n", "colour"},
	}
	for _, c := range cases {
		_, err := filesTree(t, c.yaml, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
	// A realm without storage can't have file fields.
	_, err := Load(writeTree(t, map[string]string{
		"shop/realm.yaml":                "roles:\n  staff: {}\n",
		"shop/app/notes/schema.json":     `{}`,
		"shop/app/notes/collection.yaml": "files:\n  a: {max_size: 1MB}\n",
	}))
	if err == nil || !strings.Contains(err.Error(), "needs `storage:`") {
		t.Errorf("no storage: %v", err)
	}
	// Other sections of collection.yaml still work next to files.
	if _, err := filesTree(t, "soft_delete: true\nfiles:\n  a: {max_size: 1MB}\n", nil); err != nil {
		t.Errorf("soft_delete next to files: %v", err)
	}
}

// A function can't read what backd uses to reach the bucket or to sign links.
func TestFunctionsCantDeclareTheStorageSecrets(t *testing.T) {
	for _, name := range []string{"realm.STORAGE_SECRET_KEY", "realm.STORAGE_ACCESS_KEY", "realm.BACKD_FILES_LINK_KEY"} {
		_, err := Load(writeTree(t, map[string]string{
			"shop/realm.yaml":                     "auth: enabled\nstorage:\n  provider: minio\n  endpoint: https://minio.internal:9000\n  bucket: b\n  prefix: p\n  access_key: secret:STORAGE_ACCESS_KEY\n  secret_key: secret:STORAGE_SECRET_KEY\n",
			"shop/app/_functions/f/function.yaml": "secrets: [" + name + "]\n",
			"shop/app/_functions/f/index.ts":      "export default () => 1\n",
		}))
		if err == nil || !strings.Contains(err.Error(), "a secret backd keeps for itself") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Another secret of the same realm, or one with the same name in a database, is the function's own.
	_, err := Load(writeTree(t, map[string]string{
		"shop/realm.yaml":                     "auth: enabled\nstorage:\n  provider: minio\n  endpoint: https://minio.internal:9000\n  bucket: b\n  prefix: p\n  access_key: secret:STORAGE_ACCESS_KEY\n  secret_key: secret:STORAGE_SECRET_KEY\n",
		"shop/app/_functions/f/function.yaml": "secrets: [realm.SHARED, STORAGE_SECRET_KEY]\n",
		"shop/app/_functions/f/index.ts":      "export default () => 1\n",
	}))
	if err != nil && strings.Contains(err.Error(), "keeps for itself") {
		t.Errorf("an unrelated secret was refused: %v", err)
	}
}

func TestStorageQuotaSettings(t *testing.T) {
	load := func(quota string) (*Registry, error) {
		return Load(writeTree(t, map[string]string{
			"shop/realm.yaml": "auth: enabled\nstorage:\n  provider: minio\n  endpoint: https://minio.internal:9000\n  bucket: files\n  prefix: p\n  access_key: secret:A\n  secret_key: secret:B\n" + quota,
		}))
	}
	reg, err := load("  quota:\n    realm: 50GiB\n    user: 2GiB\n")
	if err != nil {
		t.Fatal(err)
	}
	if st := reg.Realms["shop"].Settings.Storage; st.QuotaRealm != 50<<30 || st.QuotaUser != 2<<30 {
		t.Errorf("quota: %+v", st)
	}
	if reg, err = load("  quota:\n    user: 1MiB\n"); err != nil || reg.Realms["shop"].Settings.Storage.QuotaRealm != 0 {
		t.Errorf("a user quota alone: %v", err)
	}
	for quota, want := range map[string]string{
		"  quota:\n    realm: lots\n":                 "quota.realm:",
		"  quota:\n    user: 0B\n":                    "at least 1B",
		"  quota:\n    realm: 1GiB\n    user: 2GiB\n": "can't be larger than the realm's",
		"  quota:\n    files: 5\n":                    "files",
	} {
		if _, err := load(quota); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", quota, err)
		}
	}
}
