package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage/storagetest"
)

// importLines makes the lines of a package part: n notes with generated ids, which the
// callers change to make them invalid.
func importLines(t *testing.T, n int, mod func(i int, doc map[string]any)) string {
	t.Helper()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := 1; i <= n; i++ {
		doc := map[string]any{
			"id":        fmt.Sprintf("%020d", 0), // replaced below
			"title":     fmt.Sprintf("note %d", i),
			"published": true,
			"_meta":     map[string]any{"version": int64(1), "created_at": at, "updated_at": at},
		}
		doc["id"] = xidFor(i)
		if mod != nil {
			mod(i, doc)
		}
		line, err := exportpkg.EncodeDocument(doc)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// xidFor makes a valid, stable xid for the i-th document.
func xidFor(i int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuv"
	// 20 chars of base32hex decoding to 12 bytes; the last char only carries 4 bits.
	id := []byte("9m4e2mr0ui3e8a215n4g")
	id[17] = alphabet[i%32]
	id[16] = alphabet[(i/32)%32]
	id[15] = alphabet[(i/1024)%32]
	return string(id)
}

func TestAdminImport(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "import-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	url := admin + "/import"
	hdr := func(lease string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease, "Content-Type": "application/x-ndjson"}
	}
	manifest := func(mod func(m *exportpkg.Manifest)) string {
		m := &exportpkg.Manifest{Format: 1, Realm: "acme", Databases: []exportpkg.Database{{Name: "app", Collections: []exportpkg.Collection{{Name: "notes", Part: "app/notes.jsonl"}}}}}
		if mod != nil {
			mod(m)
		}
		b, _ := m.Marshal()
		return string(b)
	}
	begin := func(m, opts string, want int) map[string]any {
		t.Helper()
		rec, out := f.doH(t, "POST", url, `{"manifest": `+m+opts+`}`, jsonHdr(adminKey))
		if rec.Code != want {
			t.Fatalf("begin %s: %d %v", opts, rec.Code, out)
		}
		return out
	}
	chunk := func(lease, phase, body string) map[string]any {
		t.Helper()
		rec, out := f.doH(t, "POST", url+"?phase="+phase+"&part=app/notes.jsonl", body, hdr(lease))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %v", phase, rec.Code, out)
		}
		return out
	}
	end := func(lease string) {
		f.doH(t, "DELETE", url+"?complete=true", "", hdr(lease))
	}
	stored := func() int {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		return len(f.store.docs["acme__app.notes"])
	}

	// A package that can't be read at all is refused before anything else.
	for name, m := range map[string]string{
		"another format":     manifest(func(m *exportpkg.Manifest) { m.Format = 9 }),
		"a partial package":  manifest(func(m *exportpkg.Manifest) { m.Flags.Partial = true }),
		"a CSV extract":      manifest(func(m *exportpkg.Manifest) { m.Flags.CSV = true }),
		"a missing database": manifest(func(m *exportpkg.Manifest) { m.Databases[0].Name = "ghost" }),
		"an odd part":        manifest(func(m *exportpkg.Manifest) { m.Databases[0].Collections[0].Part = "../notes.jsonl" }),
	} {
		_ = name
		begin(m, "", http.StatusUnprocessableEntity)
	}
	begin(manifest(nil), `, "mode": "merge"`, http.StatusBadRequest)
	if rec, _ := f.doH(t, "POST", url, `{"manifest": {}}`, jsonHdr(adminKey)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an empty manifest: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", url, `nope`, jsonHdr(adminKey)); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that isn't JSON: %d", rec.Code)
	}

	// Validation finds what the target would refuse, and writes nothing.
	b := begin(manifest(func(m *exportpkg.Manifest) { m.Fingerprint = "other" }), "", http.StatusOK)
	lease := b["lease"].(string)
	if w, _ := b["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "different configuration") {
		t.Errorf("warnings: %v", b["warnings"])
	}
	body := importLines(t, 40, func(i int, d map[string]any) {
		switch i {
		case 3:
			d["title"] = int64(7) // the schema says string
		case 5:
			d["id"] = "not-an-xid"
		case 7:
			delete(d, "_meta")
		case 9:
			d["_meta"].(map[string]any)["owner"] = "ghost-user"
		}
	})
	got := chunk(lease, "validate", body)
	counts := got["counts"].(map[string]any)
	if got["documents"] != float64(40) || counts["invalid"] != float64(3) || counts["created"] != float64(37) || got["missing_owners"] != float64(1) || stored() != 0 {
		t.Fatalf("validate: %v (stored %d)", got, stored())
	}
	problems := got["problems"].([]any)
	if len(problems) != 3 || problems[0].(map[string]any)["line"] != float64(3) || problems[0].(map[string]any)["problems"].([]any)[0].(map[string]any)["path"] != "title" {
		t.Errorf("problems: %v", problems)
	}
	// A dry run refuses to write.
	end(lease)
	b = begin(manifest(nil), `, "dry_run": true`, http.StatusOK)
	if rec, _ := f.doH(t, "POST", url+"?phase=write&part=app/notes.jsonl", body, hdr(b["lease"].(string))); rec.Code != http.StatusConflict {
		t.Errorf("a dry run wrote: %d", rec.Code)
	}
	end(b["lease"].(string))

	// Write: the valid documents go in with their ids and _meta, the invalid ones are left out.
	lease = begin(manifest(nil), `, "skip_invalid": true`, http.StatusOK)["lease"].(string)
	got = chunk(lease, "write", body)
	counts = got["counts"].(map[string]any)
	if counts["created"] != float64(37) || counts["invalid"] != float64(3) || stored() != 37 {
		t.Fatalf("write: %v (stored %d)", got, stored())
	}
	f.store.mu.Lock()
	first := f.store.docs["acme__app.notes"][xidFor(1)]
	f.store.mu.Unlock()
	if first["title"] != "note 1" || first["_meta"].(map[string]any)["created_at"] != time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) {
		t.Errorf("a stored document: %v", first)
	}
	// Running it again finds everything identical: that is how an interrupted import continues.
	got = chunk(lease, "write", body)
	counts = got["counts"].(map[string]any)
	if counts["unchanged"] != float64(37) || counts["created"] != nil || stored() != 37 {
		t.Errorf("again: %v", got)
	}
	end(lease)

	// Modes, with a document that differs from the one the target holds.
	changed := importLines(t, 2, func(i int, d map[string]any) { d["title"] = "changed" })
	for _, tc := range []struct {
		opts, outcome, title string
	}{
		{``, "conflict", "note 1"},
		{`, "mode": "skip"`, "skipped", "note 1"},
		{`, "mode": "replace"`, "replaced", "changed"},
	} {
		lease = begin(manifest(nil), tc.opts, http.StatusOK)["lease"].(string)
		got = chunk(lease, "validate", changed)
		if got["counts"].(map[string]any)[tc.outcome] != float64(2) {
			t.Errorf("validate %s: %v", tc.opts, got)
		}
		got = chunk(lease, "write", changed)
		f.store.mu.Lock()
		title := f.store.docs["acme__app.notes"][xidFor(1)]["title"]
		f.store.mu.Unlock()
		if got["counts"].(map[string]any)[tc.outcome] != float64(2) || title != tc.title {
			t.Errorf("write %s: %v, title %v", tc.opts, got, title)
		}
		end(lease)
	}

	// Audit: the start and the end, with counts and never content.
	entries, _, err := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditImportDone, Limit: 20})
	if err != nil || len(entries) == 0 {
		t.Fatalf("audit: %v %d", err, len(entries))
	}
	var sawDone bool
	for _, e := range entries {
		if e.Details["created"] == int64(37) {
			sawDone = true
		}
		if strings.Contains(fmt.Sprint(e.Details), "note 1") {
			t.Error("the audit record holds content")
		}
	}
	if !sawDone {
		t.Errorf("no import.done with the created count: %v", entries[0].Details)
	}
	// Parts and chunks need the import's own lease.
	if rec, _ := f.doH(t, "POST", url+"?phase=validate&part=app/notes.jsonl", body, hdr("nope")); rec.Code != http.StatusConflict {
		t.Errorf("a wrong lease: %d", rec.Code)
	}
}

func TestAdminImportFiles(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "import-files", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Cache = auth.NewSecretCache()
	fake := storagetest.New(t, "files")
	f.reg.Realms["acme"].Settings.Storage = &registry.StorageSettings{Provider: "minio", Endpoint: fake.URL, Region: "us-east-1", Bucket: "files", Prefix: "imp", AccessKey: "STORAGE_ACCESS_KEY", SecretKey: "STORAGE_SECRET_KEY", Download: registry.DownloadPresigned, PresignedTTL: registry.DefaultPresignedTTL, PendingTTL: registry.DefaultPendingTTL, HTTP: true}
	for _, name := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY"} {
		if err := f.svc.SetSecret(ctx, "", name, "value-"+name, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	url := admin + "/import"
	h := map[string]string{"Authorization": "Bearer " + adminKey, "Content-Type": "application/json"}
	m := &exportpkg.Manifest{Format: 1, Realm: "acme", Databases: []exportpkg.Database{{Name: "app", Collections: []exportpkg.Collection{{Name: "library", Part: "app/library.jsonl"}}}}}
	mb, _ := m.Marshal()
	rec, b := f.doH(t, "POST", url, `{"manifest": `+string(mb)+`}`, h)
	if rec.Code != http.StatusOK || b["file_fields"].(map[string]any)["app/library"] == nil {
		t.Fatalf("begin: %d %v", rec.Code, b)
	}
	lease := b["lease"].(string)
	lh := map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease}
	content := []byte("%PDF-1.4 the manual")
	sum := sha256.Sum256(content)
	fileID := "fl_0123456789abcdefghij"
	put := func(id, hash string, data []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", url+"?file=app/library/"+id, bytes.NewReader(data))
		for k, v := range lh {
			req.Header.Set(k, v)
		}
		req.Header.Set("X-Backd-Sha256", hash)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		return w
	}
	// A file arrives under the key the target's documents expect, and only if it matches its checksum.
	if w := put(fileID, strings.Repeat("0", 64), content); w.Code != http.StatusBadRequest || len(fake.Keys()) != 0 {
		t.Errorf("a wrong checksum: %d, keys %v", w.Code, fake.Keys())
	}
	if w := put(fileID, "short", content); w.Code != http.StatusBadRequest {
		t.Errorf("no checksum: %d", w.Code)
	}
	if w := put("../x", hex.EncodeToString(sum[:]), content); w.Code != http.StatusNotFound {
		t.Errorf("an unsafe id: %d", w.Code)
	}
	if w := put(fileID, hex.EncodeToString(sum[:]), content); w.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if got := fake.Object("imp/acme/app/library/" + fileID); !bytes.Equal(got, content) {
		t.Errorf("object: %q (keys %v)", got, fake.Keys())
	}
	// The document that references it is written, and the file counts in the usage.
	doc := importLines(t, 1, func(i int, d map[string]any) {
		d["manual"] = map[string]any{"id": fileID, "name": "manual.pdf", "size": int64(len(content)), "type": "application/pdf", "sha256": hex.EncodeToString(sum[:]), "uploaded_at": time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
		d["_meta"].(map[string]any)["owner"] = "u1"
	})
	rec, out := f.doH(t, "POST", url+"?phase=write&part=app/library.jsonl", doc, map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease})
	if rec.Code != http.StatusOK || out["counts"].(map[string]any)["created"] != float64(1) {
		t.Fatalf("write: %d %v", rec.Code, out)
	}
	// A file larger than the field allows is invalid for the target.
	big := importLines(t, 1, func(i int, d map[string]any) {
		d["manual"] = map[string]any{"id": fileID, "name": "manual.pdf", "size": int64(1 << 20), "type": "application/pdf", "sha256": hex.EncodeToString(sum[:]), "uploaded_at": time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	})
	_, out = f.doH(t, "POST", url+"?phase=validate&part=app/library.jsonl", big, map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease})
	if out["counts"].(map[string]any)["invalid"] != float64(1) {
		t.Errorf("a large file: %v", out)
	}
}

func TestAdminImportAuthData(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "import-auth", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	mem := f.svc.Store.(*authtest.MemStore)
	url := admin + "/import"
	jh := jsonHdr(adminKey)
	m := &exportpkg.Manifest{Format: 1, Realm: "acme", Flags: exportpkg.Flags{Auth: true}, AuthKey: "x",
		Databases: []exportpkg.Database{{Name: "app", Collections: []exportpkg.Collection{{Name: "notes", Part: "app/notes.jsonl"}}}}}
	for _, c := range auth.PortableCollections {
		m.Auth = append(m.Auth, exportpkg.Collection{Name: c, Part: exportpkg.AuthPart(c)})
	}
	mb, _ := m.Marshal()
	rec, b := f.doH(t, "POST", url, `{"manifest": `+string(mb)+`}`, jh)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: %d %v", rec.Code, b)
	}
	lease := b["lease"].(string)
	lh := map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease, "Content-Type": "application/x-ndjson"}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	line := func(d map[string]any) string {
		l, _ := exportpkg.EncodeDocument(d)
		return string(l) + "\n"
	}
	user := line(map[string]any{"id": "u1", "email": "ada@example.com", "roles": []any{}, "created_at": at})
	mfa := line(map[string]any{"id": "u1", "secret": map[string]any{"$plain": "JBSWY3DPEHPK3PXP"}, "enabled_at": at, "recovery_codes": []any{}})
	sealed := line(map[string]any{"id": "u2", "secret": "keyid.nonce.ct", "enabled_at": at})
	post := func(phase, part, body string) map[string]any {
		rec, out := f.doH(t, "POST", url+"?phase="+phase+"&part="+part, body, lh)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %v", phase, part, rec.Code, out)
		}
		return out
	}
	if out := post("validate", "_system/auth/users.jsonl", user); out["counts"].(map[string]any)["created"] != float64(1) || len(mem.Portable("users")) != 0 {
		t.Errorf("validate: %v", out)
	}
	if out := post("write", "_system/auth/users.jsonl", user); out["counts"].(map[string]any)["created"] != float64(1) || len(mem.Portable("users")) != 1 {
		t.Errorf("write: %v", out)
	}
	if out := post("write", "_system/auth/users.jsonl", user); out["counts"].(map[string]any)["unchanged"] != float64(1) {
		t.Errorf("again: %v", out)
	}
	// The secret arrives as plaintext and is stored sealed under this realm's key.
	post("write", "_system/auth/mfa.jsonl", mfa)
	stored := mem.Portable("mfa")["u1"]["secret"].(string)
	if plain, err := f.svc.OpenToken(stored); err != nil || plain != "JBSWY3DPEHPK3PXP" || stored == "JBSWY3DPEHPK3PXP" {
		t.Errorf("stored secret: %q %v", plain, err)
	}
	if out := post("write", "_system/auth/mfa.jsonl", mfa); out["counts"].(map[string]any)["unchanged"] != float64(1) {
		t.Errorf("mfa again: %v", out)
	}
	if out := post("validate", "_system/auth/mfa.jsonl", sealed); out["counts"].(map[string]any)["invalid"] != float64(1) {
		t.Errorf("a secret that arrives sealed: %v", out)
	}
	// The users of the package are owners that exist for its documents.
	doc := importLines(t, 1, func(i int, d map[string]any) { d["_meta"].(map[string]any)["owner"] = "u1" })
	if out := post("validate", "app/notes.jsonl", doc); out["missing_owners"] != float64(0) {
		t.Errorf("owner: %v", out)
	}
	// A part that isn't authentication data of this backd is refused.
	if rec, _ := f.doH(t, "POST", url+"?phase=validate&part=_system/auth/sessions.jsonl", user, lh); rec.Code != http.StatusNotFound {
		t.Errorf("sessions: %d", rec.Code)
	}
	bad := *m
	bad.Auth = []exportpkg.Collection{{Name: "sessions", Part: exportpkg.AuthPart("sessions")}}
	bb, _ := bad.Marshal()
	f.doH(t, "DELETE", url, "", lh)
	if rec, _ := f.doH(t, "POST", url, `{"manifest": `+string(bb)+`}`, jh); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown auth part: %d", rec.Code)
	}
}
