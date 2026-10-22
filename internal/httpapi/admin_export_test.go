package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/exportpkg"
	"github.com/fernandezvara/backd/internal/storage"
)

func TestAdminExport(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "export-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := bearer(adminKey)
	seedNotes(f, 1203, nil, map[int]bool{7: true})
	url := admin + "/export"

	// Not an administrator: nothing.
	if rec, _ := f.doH(t, "GET", url, "", nil); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound {
		t.Errorf("anonymous: %d", rec.Code)
	}
	// Parts need the lease of a plan.
	if rec, out := f.doH(t, "GET", url+"?part=app/notes.jsonl", "", key); rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "export_lease" {
		t.Errorf("a part without a lease: %d %v", rec.Code, out)
	}

	// The plan: the manifest as far as it is known, and the lease.
	rec, plan := f.doH(t, "GET", url+"?database=app", "", key)
	lease, _ := plan["lease"].(string)
	m, _ := plan["manifest"].(map[string]any)
	if rec.Code != http.StatusOK || lease == "" || m["realm"] != "acme" || m["format"] != float64(exportpkg.FormatVersion) || m["operator"] == "" {
		t.Fatalf("plan: %d %v", rec.Code, plan)
	}
	if flags, _ := m["flags"].(map[string]any); flags["partial"] != false || flags["auth"] != false || flags["anonymized"] != false {
		t.Errorf("flags: %v", m["flags"])
	}
	if enc, _ := m["encrypted_fields"].([]any); enc == nil || len(enc) != 0 {
		t.Errorf("encrypted_fields: %v", m["encrypted_fields"])
	}
	hdr := map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease}

	// One export at a time.
	if rec, out := f.doH(t, "GET", url, "", key); rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "export_running" {
		t.Errorf("a second plan: %d %v", rec.Code, out)
	}
	// A wrong lease is refused too.
	if rec, _ := f.doH(t, "GET", url+"?part=app/notes.jsonl", "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: "nope"}); rec.Code != http.StatusConflict {
		t.Errorf("a wrong lease: %d", rec.Code)
	}

	// The documents, in pages, every one once, the soft-deleted included, in id order.
	req := httptest.NewRequest("GET", url+"?part=app/notes.jsonl", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("part: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	var ids []string
	var trashed int
	err = exportpkg.ReadDocuments(bytes.NewReader(w.Body.Bytes()), func(_ int, doc map[string]any) error {
		ids = append(ids, doc["id"].(string))
		meta := doc["_meta"].(map[string]any)
		if meta["created_at"] == nil || meta["version"] != int64(1) {
			t.Errorf("_meta of %v: %v", doc["id"], meta)
		}
		if meta["deleted_at"] != nil {
			trashed++
		}
		return nil
	})
	if err != nil || len(ids) != 1203 || ids[0] != "n00001" || ids[1202] != "n01203" || trashed != 1 {
		t.Fatalf("documents: %v, %d, %d trashed", err, len(ids), trashed)
	}
	if strings.Contains(w.Body.String(), "_expanded") || !strings.Contains(w.Body.String(), `"$date"`) {
		t.Error("typed values must be Extended JSON")
	}

	for name, path := range map[string]string{
		"a database that doesn't exist":   "?part=ghost/notes.jsonl",
		"a collection that doesn't exist": "?part=app/ghost.jsonl",
		"not a collection part":           "?part=app/notes",
		"a file of a collection without":  "?file=app/notes/abc",
	} {
		if rec, _ := f.doH(t, "GET", url+path, "", hdr); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}

	// Finishing releases the realm and audits what was streamed.
	if rec, _ := f.doH(t, "DELETE", url+"?complete=true", "", hdr); rec.Code != http.StatusOK {
		t.Fatalf("finish: %d", rec.Code)
	}
	var started, done map[string]any
	entries, _, err := f.svc.AuditTrail(ctx, auth.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Action {
		case auth.AuditExport:
			started = e.Details
		case auth.AuditExportDone:
			done = e.Details
		}
	}
	if started == nil || started["database"] != "app" || done == nil || done["documents"] != int64(1203) || done["complete"] != true {
		t.Errorf("audit: %v / %v", started, done)
	}
	for _, d := range []map[string]any{started, done} {
		if b, _ := json.Marshal(d); strings.Contains(string(b), "a title") {
			t.Error("the audit record holds content")
		}
	}
	if rec, _ := f.doH(t, "GET", url+"?part=app/notes.jsonl", "", hdr); rec.Code != http.StatusConflict {
		t.Errorf("a released lease: %d", rec.Code)
	}

	// An interrupted export is resumed with its lease; a stranger can't take it over.
	_, plan = f.doH(t, "GET", url, "", key)
	old := plan["lease"].(string)
	if rec, _ := f.doH(t, "GET", url, "", key); rec.Code != http.StatusConflict {
		t.Errorf("a plan during an export: %d", rec.Code)
	}
	rec, plan = f.doH(t, "GET", url, "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: old})
	if rec.Code != http.StatusOK || plan["lease"] == old {
		t.Errorf("resume: %d %v", rec.Code, plan["lease"])
	}
	if rec, _ := f.doH(t, "GET", url+"?part=app/notes.jsonl", "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: old}); rec.Code != http.StatusConflict {
		t.Errorf("the replaced lease: %d", rec.Code)
	}

	f.doH(t, "DELETE", url, "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: plan["lease"].(string)})

	// A session needs a fresh proof of identity, and a role short of full admin access is refused.
	f.bobHolds(t, "viewer")
	if rec, _ := f.doH(t, "GET", url, "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a read-only administrator: %d", rec.Code)
	}
	f.bobHolds(t, "staff")
	rec, out := f.doH(t, "GET", url, "", bearer(f.bob))
	if rec.Code != http.StatusUnauthorized || out["error"].(map[string]any)["code"] != "reauth_required" {
		t.Errorf("a session without proof: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", url, "", f.withProof(t, f.bob)); rec.Code != http.StatusOK {
		t.Errorf("a session with proof: %d", rec.Code)
	}
}

func TestAdminExportExtract(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	_, adminKey, err := f.svc.CreateAPIKey(context.Background(), "extract-admin", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	key := bearer(adminKey)
	seedNotes(f, 1100, nil, nil)
	f.store.mu.Lock()
	for i := 1; i <= 1100; i += 2 {
		f.store.docs["acme__app.notes"][fmt.Sprintf("n%05d", i)]["published"] = false
	}
	f.store.mu.Unlock()
	url := admin + "/export"
	plan := func(query string, want int) map[string]any {
		t.Helper()
		rec, out := f.doH(t, "GET", url+"?"+query, "", key)
		if rec.Code != want {
			t.Fatalf("%s: %d %v", query, rec.Code, out)
		}
		if want == http.StatusOK {
			return out
		}
		return out
	}
	part := func(lease string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", url+"?part=app/notes.jsonl", nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set(exportHeader, lease)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		var docs []map[string]any
		if err := exportpkg.ReadDocuments(w.Body, func(_ int, d map[string]any) error { docs = append(docs, d); return nil }); err != nil {
			t.Fatal(err)
		}
		return docs
	}
	end := func(lease string) {
		f.doH(t, "DELETE", url, "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease})
	}

	// Bad options are refused before anything is taken.
	for name, q := range map[string]string{
		"a field that isn't there": "database=app&collection=notes&fields=title,ghost",
		"a where for a realm":      `where={"published":true}`,
		"a where for two":          `database=app&collection=notes&collection=archive&where={"published":true}`,
		"a where on a non-field":   `database=app&collection=notes&where={"ghost":1}`,
		"an unknown format":        "format=xml",
	} {
		_ = name
		plan(q, http.StatusBadRequest)
	}

	// Fields: id always, the rest as asked, and the package is partial.
	p := plan("database=app&collection=notes&fields=title,_meta.version", http.StatusOK)
	if p["manifest"].(map[string]any)["flags"].(map[string]any)["partial"] != true {
		t.Errorf("flags: %v", p["manifest"])
	}
	docs := part(p["lease"].(string))
	if len(docs) != 1100 || len(docs[0]) != 3 || docs[0]["title"] != "a title" || docs[0]["published"] != nil || docs[0]["_meta"].(map[string]any)["version"] != int64(1) || len(docs[0]["_meta"].(map[string]any)) != 1 {
		t.Fatalf("fields: %d %v", len(docs), docs[0])
	}
	end(p["lease"].(string))

	// Where: the matching documents only, in pages across the whole collection.
	p = plan(`database=app&collection=notes&where={"published":false}&fields=published`, http.StatusOK)
	docs = part(p["lease"].(string))
	if len(docs) != 550 || docs[0]["id"] != "n00001" || docs[549]["id"] != "n01099" {
		t.Fatalf("where: %d %v", len(docs), docs[len(docs)-1])
	}
	end(p["lease"].(string))

	// CSV extracts hold no files and are marked.
	p = plan("database=app&collection=notes&format=csv", http.StatusOK)
	flags := p["manifest"].(map[string]any)["flags"].(map[string]any)
	if flags["csv"] != true || flags["no_files"] != true || len(p["file_fields"].(map[string]any)) != 0 {
		t.Errorf("csv plan: %v", p)
	}
	end(p["lease"].(string))
}

// Authentication data leaves with the whole realm only, and what the realm sealed is sealed
// again under a one-time key the plan hands over once.
func TestAdminExportAuthData(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	var err error
	if f.svc.Cipher, err = auth.NewSecretCipher([]byte("01234567890123456789012345678901")); err != nil {
		t.Fatal(err)
	}
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "auth-export", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	mem := f.svc.Store.(*authtest.MemStore)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	sealedSecret, _ := f.svc.SealToken("JBSWY3DPEHPK3PXP")
	sealedDevice, _ := f.svc.SealToken("fcm-token")
	mem.SeedPortable("users", map[string]any{"id": "u1", "email": "ada@example.com", "email_verified": true, "roles": []any{}, "disabled": false, "created_at": at, "updated_at": at})
	mem.SeedPortable("identities", map[string]any{"id": "i1", "user_id": "u1", "provider": "password", "subject": "u1", "password_hash": "$argon2id$v=19$hash", "created_at": at, "updated_at": at})
	mem.SeedPortable("mfa", map[string]any{"id": "u1", "secret": sealedSecret, "enabled_at": at, "last_step": int64(3), "recovery_codes": []any{"keyedhash1", "keyedhash2"}})
	mem.SeedPortable("devices", map[string]any{"id": "d1", "user_id": "u1", "session_id": "s1", "platform": "ios", "token_hash": "h", "token": sealedDevice, "created_at": at, "last_seen": at})
	mem.SeedPortable("devices", map[string]any{"id": "d2", "user_id": "u1", "session_id": "s1", "platform": "ios", "token_hash": "h2", "token": "ffffffffffffffff.bm9uY2U=.Y3Q=", "created_at": at, "last_seen": at}) // sealed under another key
	url := admin + "/export"
	key := bearer(adminKey)

	for name, q := range map[string]string{
		"with a database": "include=auth&database=app",
		"with fields":     "include=auth&fields=title",
		"with csv":        "include=auth&format=csv",
		"another include": "include=sessions",
	} {
		_ = name
		if rec, _ := f.doH(t, "GET", url+"?"+q, "", key); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	rec, plan := f.doH(t, "GET", url+"?include=auth", "", key)
	ek, _ := plan["export_key"].(string)
	m, _ := plan["manifest"].(map[string]any)
	if rec.Code != http.StatusOK || len(ek) != 43 || m["auth_key_fingerprint"] == "" || m["flags"].(map[string]any)["auth"] != true || len(m["auth"].([]any)) != len(auth.PortableCollections) {
		t.Fatalf("plan: %d %v", rec.Code, plan)
	}
	cipher, err := auth.ExportKey(ek)
	if err != nil || cipher.KeyID != m["auth_key_fingerprint"] {
		t.Fatalf("the fingerprint is not the key's: %v", err)
	}
	lease := plan["lease"].(string)
	read := func(coll string) []map[string]any {
		req := httptest.NewRequest("GET", url+"?part=_system/auth/"+coll+".jsonl", nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set(exportHeader, lease)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		var docs []map[string]any
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", coll, w.Code, w.Body)
		}
		if err := exportpkg.ReadDocuments(w.Body, func(_ int, d map[string]any) error { docs = append(docs, d); return nil }); err != nil {
			t.Fatal(err)
		}
		return docs
	}
	if ids := read("identities"); len(ids) != 1 || ids[0]["password_hash"] != "$argon2id$v=19$hash" {
		t.Errorf("identities: %v", ids)
	}
	mfa := read("mfa")
	if len(mfa) != 1 || mfa[0]["recovery_codes"] == nil || len(mfa[0]["recovery_codes"].([]any)) != 0 {
		t.Fatalf("mfa: %v", mfa)
	}
	if plain, err := auth.OpenWith(cipher, mfa[0]["secret"].(string)); err != nil || plain != "JBSWY3DPEHPK3PXP" {
		t.Errorf("the secret under the export key: %q %v", plain, err)
	}
	if _, err := f.svc.OpenToken(mfa[0]["secret"].(string)); err == nil {
		t.Error("the secret is still sealed under the realm's key")
	}
	// A device whose token the realm can't open stays behind; the other loses its session.
	devs := read("devices")
	if len(devs) != 1 || devs[0]["id"] != "d1" || devs[0]["session_id"] != "" {
		t.Errorf("devices: %v", devs)
	}
	if rec, _ := f.doH(t, "GET", url+"?part=_system/auth/sessions.jsonl", "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease}); rec.Code != http.StatusNotFound {
		t.Errorf("sessions are not exportable: %d", rec.Code)
	}
	f.doH(t, "DELETE", url+"?complete=true", "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease})

	// Audited as its own event, with the key's fingerprint and never the key.
	entries, _, err := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditExportAuth, Limit: 5})
	if err != nil || len(entries) != 1 || entries[0].Details["key_fingerprint"] != cipher.KeyID {
		t.Fatalf("audit: %v %v", entries, err)
	}
	if strings.Contains(fmt.Sprint(entries[0].Details), ek) {
		t.Error("the audit record holds the export key")
	}
	// A plain export carries none of it.
	_, plan = f.doH(t, "GET", url, "", key)
	if plan["export_key"] != nil || plan["manifest"].(map[string]any)["flags"].(map[string]any)["auth"] != false || plan["manifest"].(map[string]any)["auth"] != nil {
		t.Errorf("a plain plan: %v", plan)
	}
}

// A staging copy keeps no original email and none of the fields an erase would remove.
func TestAdminExportAnonymized(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	_, adminKey, err := f.svc.CreateAPIKey(ctx, "anon-export", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	ada, err := f.svc.Find(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	f.store.mu.Lock()
	f.store.docs = map[string]map[string]storage.Document{"acme__app.orders": {
		"o1": {"id": "o1", "buyer": "Ada Lovelace", "phone": "+34 600 000 000", "members": []any{"ada@example.com", "stranger@elsewhere.com"}, "paid_by": ada.ID,
			"_meta": map[string]any{"version": int64(1), "created_at": at, "updated_at": at, "owner": ada.ID}},
	}}
	f.store.mu.Unlock()
	mem := f.svc.Store.(*authtest.MemStore)
	mem.SeedPortable("users", map[string]any{"id": ada.ID, "email": "ada@example.com", "pending_email": "new@example.com", "roles": []any{}, "created_at": at, "updated_at": at})
	mem.SeedPortable("identities",
		map[string]any{"id": "i1", "user_id": ada.ID, "provider": "password", "subject": ada.ID, "password_hash": "$argon2id$original", "created_at": at, "updated_at": at},
		map[string]any{"id": "i2", "user_id": ada.ID, "provider": "google", "subject": "google-sub-1234", "email": "ada@gmail.com", "created_at": at, "updated_at": at})
	mem.SeedPortable("mfa", map[string]any{"id": ada.ID, "secret": "sealed"})
	mem.SeedPortable("orgs", map[string]any{"id": "org1", "name": "Acme Clinic"})
	url := admin + "/export"
	post := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		return f.doH(t, "POST", url, body, jsonHdr(adminKey))
	}
	read := func(lease, part string) []map[string]any {
		req := httptest.NewRequest("GET", url+"?part="+part, nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set(exportHeader, lease)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", part, w.Code, w.Body)
		}
		var docs []map[string]any
		if err := exportpkg.ReadDocuments(w.Body, func(_ int, d map[string]any) error { docs = append(docs, d); return nil }); err != nil {
			t.Fatal(err)
		}
		return docs
	}

	// Options that make no sense, and rules the schema refuses, are refused before anything starts.
	for name, body := range map[string]string{
		"a password without users":    `{"anonymize": true, "staging_password": "dev-p4ssw0rd!"}`,
		"a config without anonymize":  `{"anonymize_config": "collections: {}"}`,
		"a field that isn't there":    `{"anonymize": true, "anonymize_config": "collections:\n  app/orders:\n    remove: [nothing]\n"}`,
		"a collection that isn't":     `{"anonymize": true, "anonymize_config": "collections:\n  app/ghost:\n    remove: [x]\n"}`,
		"files other than keep":       `{"anonymize": true, "anonymize_files": "all"}`,
		"a body that isn't JSON":      `nope`,
		"a staging password too weak": `{"anonymize": true, "staging_password": "x"}`,
	} {
		_ = name
		q := "?database=app"
		if strings.Contains(name, "too weak") {
			q = "?include=auth"
		}
		if rec, out := f.doH(t, "POST", url+q, body, jsonHdr(adminKey)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}

	rec, plan := f.doH(t, "POST", url+"?include=auth", `{"anonymize": true, "staging_password": "dev-p4ssw0rd!", "anonymize_config": "collections:\n  app/orders:\n    replace: {buyer: \"Override\"}\n"}`, jsonHdr(adminKey))
	m, _ := plan["manifest"].(map[string]any)
	if rec.Code != http.StatusOK || m["flags"].(map[string]any)["anonymized"] != true || m["flags"].(map[string]any)["no_files"] != true || plan["export_key"] != nil || len(m["auth"].([]any)) != len(auth.AnonymizedCollections()) {
		t.Fatalf("plan: %d %v", rec.Code, plan)
	}
	lease := plan["lease"].(string)

	orders := read(lease, "app/orders.jsonl")
	if len(orders) != 1 {
		t.Fatalf("orders: %v", orders)
	}
	o := orders[0]
	members := o["members"].([]any)
	if o["phone"] != nil || o["buyer"] != "Override" || members[0] != auth.AnonymousEmail(ada.ID) || !strings.HasSuffix(members[1].(string), "@example.invalid") || o["paid_by"] != ada.ID {
		t.Errorf("order: %v", o)
	}
	users := read(lease, "_system/auth/users.jsonl")
	if len(users) != 1 || users[0]["email"] != auth.AnonymousEmail(ada.ID) || users[0]["pending_email"] != nil {
		t.Errorf("users: %v", users)
	}
	ids := read(lease, "_system/auth/identities.jsonl")
	if len(ids) != 1 || ids[0]["provider"] != "password" {
		t.Fatalf("identities: %v", ids)
	}
	if ok, err := f.svc.Hasher.Verify(ctx, "dev-p4ssw0rd!", ids[0]["password_hash"].(string)); err != nil || !ok {
		t.Errorf("the staging password: %v %v", ok, err)
	}
	if got := read(lease, "_system/auth/orgs.jsonl"); len(got) != 1 || got[0]["name"] != "Acme Clinic" {
		t.Errorf("organizations are kept: %v", got)
	}
	// Nothing sealed leaves, nor a second factor.
	if rec, _ := f.doH(t, "GET", url+"?part=_system/auth/mfa.jsonl", "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease}); rec.Code != http.StatusNotFound {
		t.Errorf("mfa of an anonymized export: %d", rec.Code)
	}
	// And no original address is anywhere in what was streamed.
	all := fmt.Sprint(orders, users, ids)
	for _, secret := range []string{"ada@example.com", "ada@gmail.com", "new@example.com", "stranger@elsewhere.com", "+34 600", "google-sub-1234", "$argon2id$original"} {
		if strings.Contains(all, secret) {
			t.Errorf("%q survived the anonymization", secret)
		}
	}
	f.doH(t, "DELETE", url, "", map[string]string{"Authorization": "Bearer " + adminKey, exportHeader: lease})

	// Without --include auth and without an override file, the policy alone applies.
	rec, plan = post(`{"anonymize": true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %v", rec.Code, plan)
	}
	o = read(plan["lease"].(string), "app/orders.jsonl")[0]
	if o["buyer"] != "Erased customer" || o["phone"] != nil {
		t.Errorf("the policy alone: %v", o)
	}
}
