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

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
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
	if rec, _ := f.doH(t, "DELETE", url+"?complete=true", "", hdr); rec.Code != http.StatusNoContent {
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
