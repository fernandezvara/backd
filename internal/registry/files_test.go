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
		{"direct over everything", "files:\n  a: {max_size: 5TiB, upload: direct}\n", "at most 4TiB"},
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
			"shop/realm.yaml":                     "auth: enabled\nstorage:\n  provider: minio\n  endpoint: https://minio.internal:9000\n  bucket: tests-backd\n  prefix: p\n  access_key: secret:STORAGE_ACCESS_KEY\n  secret_key: secret:STORAGE_SECRET_KEY\n",
			"shop/app/_functions/f/function.yaml": "secrets: [" + name + "]\n",
			"shop/app/_functions/f/index.ts":      "export default () => 1\n",
		}))
		if err == nil || !strings.Contains(err.Error(), "a secret backd keeps for itself") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Another secret of the same realm, or one with the same name in a database, is the function's own.
	_, err := Load(writeTree(t, map[string]string{
		"shop/realm.yaml":                     "auth: enabled\nstorage:\n  provider: minio\n  endpoint: https://minio.internal:9000\n  bucket: tests-backd\n  prefix: p\n  access_key: secret:STORAGE_ACCESS_KEY\n  secret_key: secret:STORAGE_SECRET_KEY\n",
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

func TestImageVersions(t *testing.T) {
	reg, err := filesTree(t, `
files:
  photo:
    max_size: 5MiB
    types: [image/*]
    max_pixels: 20000000
    versions:
      thumb: {max_width: 256, max_height: 256, fit: cover, format: jpeg, quality: 80}
      preview: {max_width: 1280, upscale: true, writable: true}
      mark: {writable: true}
      sm_2x: {max_height: 100, format: png}
`, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "app", "notes")
	f := c.Files["photo"]
	if f.MaxPixels != 20_000_000 || len(f.Versions) != 4 {
		t.Fatalf("photo: %+v", f)
	}
	var names []string
	for _, v := range f.Versions {
		names = append(names, v.Name)
	}
	if strings.Join(names, " ") != "mark preview sm_2x thumb" {
		t.Errorf("versions are in name order: %v", names)
	}
	thumb, _ := f.Version("thumb")
	if !thumb.HasParams() || thumb.Writable || thumb.Params.MaxWidth != 256 || thumb.Params.Fit != "cover" || thumb.Params.Format != "jpeg" || thumb.Params.Quality != 80 {
		t.Errorf("thumb: %+v", thumb)
	}
	if prev, _ := f.Version("preview"); !prev.HasParams() || !prev.Writable || !prev.Params.Upscale {
		t.Errorf("preview: %+v", prev)
	}
	if mark, _ := f.Version("mark"); mark.HasParams() || !mark.Writable {
		t.Errorf("mark: %+v", mark)
	}
	if _, ok := f.Version("nope"); ok {
		t.Error("an undeclared version was found")
	}

	// The details a document holds are valid with them; a status the engine doesn't have is not.
	details := func(versions any) map[string]any {
		return map[string]any{"title": "t", "photo": map[string]any{"id": "fl_" + strings.Repeat("a", 20), "name": "a.png", "size": int64(3), "type": "image/png",
			"sha256": strings.Repeat("0", 64), "uploaded_at": "2026-10-06T10:00:00Z", "width": int64(20), "height": int64(10), "versions": versions}}
	}
	ok := map[string]any{
		"thumb":   map[string]any{"status": "ready", "id": "fv_" + strings.Repeat("b", 20), "size": int64(10), "type": "image/jpeg", "width": int64(256), "height": int64(128), "params": map[string]any{"max_width": int64(256)}, "fingerprint": "ab", "generated_at": "2026-10-06T10:00:00Z"},
		"preview": map[string]any{"status": "pending"},
		"mark":    map[string]any{"status": "empty"},
		"sm_2x":   map[string]any{"status": "failed", "reason": "too_large"},
	}
	if err := c.Schema.Validate(details(ok)); err != nil {
		t.Errorf("valid version states: %v", err)
	}
	for name, v := range map[string]any{
		"unknown status": map[string]any{"thumb": map[string]any{"status": "done"}},
		"no status":      map[string]any{"thumb": map[string]any{"reason": "x"}},
		"extra key":      map[string]any{"thumb": map[string]any{"status": "pending", "evil": true}},
		"not an object":  "pending",
	} {
		if err := c.Schema.Validate(details(v)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, path := range []string{"photo.width", "photo.height", "photo.versions"} {
		if !c.IsKnownField(path) {
			t.Errorf("%s isn't a known field", path)
		}
	}
}

func TestImageVersionErrors(t *testing.T) {
	field := func(body string) string { return "files:\n  a:\n    max_size: 1MB\n" + body }
	cases := []struct{ name, yaml, want string }{
		{"nothing to make", field("    versions:\n      v: {}\n"), "versions.v: needs max_width or max_height"},
		{"only format", field("    versions:\n      v: {format: jpeg}\n"), "needs max_width or max_height"},
		{"parameters without a box", field("    versions:\n      v: {writable: true, fit: cover}\n"), "need max_width or max_height"},
		{"zero width", field("    versions:\n      v: {max_width: 0}\n"), "max_width: must be between 1 and 16384"},
		{"huge height", field("    versions:\n      v: {max_height: 99999}\n"), "max_height: must be between 1 and 16384"},
		{"bad fit", field("    versions:\n      v: {max_width: 5, fit: zoom}\n"), "fit: must be contain, cover or stretch"},
		{"cover with one side", field("    versions:\n      v: {max_width: 5, fit: cover}\n"), "fit: cover needs both"},
		{"webp", field("    versions:\n      v: {max_width: 5, format: webp}\n"), "format: webp can't be written"},
		{"gif", field("    versions:\n      v: {max_width: 5, format: gif}\n"), "format: must be jpeg or png"},
		{"quality high", field("    versions:\n      v: {max_width: 5, quality: 101}\n"), "quality: must be between 1 and 100"},
		{"quality zero", field("    versions:\n      v: {max_width: 5, quality: 0}\n"), "quality: must be between 1 and 100"},
		{"quality for png", field("    versions:\n      v: {max_width: 5, format: png, quality: 50}\n"), "quality: only applies to format jpeg"},
		{"name", field("    versions:\n      Thumb: {max_width: 5}\n"), "the name must be lower-case"},
		{"name with a slash", field("    versions:\n      \"a/b\": {max_width: 5}\n"), "the name must be lower-case"},
		{"unknown key", field("    versions:\n      v: {max_width: 5, sharpen: 3}\n"), "sharpen"},
		{"max_pixels", field("    max_pixels: 0\n"), "max_pixels: must be a positive number"},
	}
	for _, c := range cases {
		_, err := filesTree(t, c.yaml, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

func TestMaxPixelsMayOnlyLowerTheInstanceLimit(t *testing.T) {
	reg, err := filesTree(t, "files:\n  a: {max_size: 1MB, max_pixels: 5000000}\n  b: {max_size: 1MB}\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.CheckImageLimits(40_000_000); err != nil {
		t.Errorf("a lower field limit: %v", err)
	}
	if err := reg.CheckImageLimits(5_000_000); err != nil {
		t.Errorf("an equal field limit: %v", err)
	}
	err = reg.CheckImageLimits(1_000_000)
	if err == nil || !strings.Contains(err.Error(), "shop/app/notes: files.a: max_pixels: 5000000 is above this instance's limit of 1000000 (BACKD_IMAGE_MAX_PIXELS)") {
		t.Errorf("a higher field limit: %v", err)
	}
}

// Above one signed PUT a direct upload is multipart, which a provider must support.
func TestDirectUploadsAboveOnePut(t *testing.T) {
	reg, err := filesTree(t, "files:\n  a: {max_size: 1TiB, upload: direct}\n", nil)
	if err != nil {
		t.Fatalf("minio takes multipart uploads: %v", err)
	}
	c, _ := reg.Collection("shop", "app", "notes")
	if f := c.Files["a"]; f == nil || f.MaxSize != 1<<40 || f.Upload != UploadDirect {
		t.Errorf("field: %+v", f)
	}
	r2 := "roles:\n  staff: {}\nstorage:\n  provider: r2\n  endpoint: https://be24f8b836589be5638e95d6579afa46.r2.cloudflarestorage.com\n  bucket: tests-backd\n  prefix: p\n  access_key: secret:K\n  secret_key: secret:S\n"
	_, err = filesTree(t, "files:\n  a: {max_size: 6GiB, upload: direct}\n", map[string]string{"shop/realm.yaml": r2})
	if err == nil || !strings.Contains(err.Error(), "at most 5GiB, with r2") {
		t.Errorf("r2 and 6GiB: %v", err)
	}
	if _, err = filesTree(t, "files:\n  a: {max_size: 5GiB, upload: direct}\n", map[string]string{"shop/realm.yaml": r2}); err != nil {
		t.Errorf("r2 and 5GiB: %v", err)
	}
}
