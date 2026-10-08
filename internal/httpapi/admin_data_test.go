package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

const adminDataBase = admin + "/data/app"

func (f *rulesFixture) auditActions(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	_, out := f.doH(t, "GET", admin+"/audit?limit=100", "", bearer(f.key))
	var got []map[string]any
	for _, it := range out["items"].([]any) {
		rec := it.(map[string]any)
		if strings.HasPrefix(rec["action"].(string), prefix) {
			got = append(got, rec)
		}
	}
	return got
}

// The admin data route serves the data routes' operations past the rules, for
// administrators who hold the data area; writes are audited, never their content.
func TestAdminDataRoute(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	boss := jsonHdr(f.bob)

	// A draft of ada's: the rules hide it from everyone else.
	rec, out := f.doH(t, "POST", posts, `{"title":"ada's draft","published":false}`, jsonHdr(f.ada))
	if rec.Code != http.StatusCreated {
		t.Fatalf("ada creates: %d %v", rec.Code, out)
	}
	draft := out["id"].(string)
	if rec, _ := f.doH(t, "GET", posts+"/"+draft, "", bearer(f.bob)); rec.Code != http.StatusNotFound {
		t.Fatalf("the rules should hide ada's draft from bob: %d", rec.Code)
	}

	// The admin route reads it anyway, with the same shape and ETag.
	rec, out = f.doH(t, "GET", adminDataBase+"/posts/"+draft, "", boss)
	if rec.Code != http.StatusOK || out["title"] != "ada's draft" || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("admin reads a draft: %d %v %v", rec.Code, out, rec.Header())
	}
	_, list := f.doH(t, "GET", adminDataBase+"/posts?where="+`{"published":false}`, "", boss)
	if items, _ := list["items"].([]any); len(items) != 1 {
		t.Errorf("admin lists drafts: %v", list)
	}

	// Writes: the schema and If-Match still apply; the document has no owner
	// and records the administrator.
	if rec, _ := f.doH(t, "POST", adminDataBase+"/posts", `{"published":true}`, boss); rec.Code != http.StatusBadRequest {
		t.Errorf("schema still enforced: %d", rec.Code)
	}
	rec, out = f.doH(t, "POST", adminDataBase+"/posts", `{"title":"from the admin","published":true}`, boss)
	meta, _ := out["_meta"].(map[string]any)
	if rec.Code != http.StatusCreated || meta["created_by"] != "user:"+f.bobID || meta["owner"] != nil || !strings.HasPrefix(rec.Header().Get("Location"), adminDataBase+"/posts/") {
		t.Fatalf("admin creates: %d %v %v", rec.Code, out, rec.Header())
	}
	created := out["id"].(string)
	patchHdr := map[string]string{"Authorization": "Bearer " + f.bob, "Content-Type": "application/merge-patch+json", "If-Match": `"9"`}
	if rec, _ := f.doH(t, "PATCH", adminDataBase+"/posts/"+created, `{"title":"x"}`, patchHdr); rec.Code != http.StatusPreconditionFailed {
		t.Errorf("a stale If-Match: %d", rec.Code)
	}
	patchHdr["If-Match"] = `"1"`
	rec, out = f.doH(t, "PATCH", adminDataBase+"/posts/"+draft, `{"title":"fixed by an admin"}`, patchHdr)
	meta, _ = out["_meta"].(map[string]any)
	if rec.Code != http.StatusOK || out["title"] != "fixed by an admin" || meta["updated_by"] != "user:"+f.bobID || meta["owner"] != f.adaID {
		t.Errorf("admin patches ada's draft (the owner stays ada): %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PUT", adminDataBase+"/posts/"+created, `{"title":"replaced","published":true}`, boss); rec.Code != http.StatusOK {
		t.Errorf("admin replaces: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", adminDataBase+"/posts/"+created, "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Errorf("admin deletes: %d", rec.Code)
	}

	// A batch, across collections, in one transaction.
	rec, out = f.doH(t, "POST", adminDataBase+"/_batch", `{"operations":[
	  {"op":"create","collection":"posts","document":{"title":"b1"}},
	  {"op":"patch","collection":"posts","id":"`+draft+`","patch":{"status":"reviewed"}}]}`, boss)
	if rec.Code != http.StatusOK || len(out["results"].([]any)) != 2 {
		t.Errorf("admin batch: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", adminDataBase+"/nope/x", "", boss); rec.Code != http.StatusNotFound {
		t.Errorf("unknown collection: %d", rec.Code)
	}

	// Audit: who, which document, never the content; reads are not audited.
	acts := f.auditActions(t, "data.")
	counts := map[string]int{}
	for _, a := range acts {
		counts[a["action"].(string)]++
		if a["actor"] != "user:"+f.bobID {
			t.Errorf("actor of %v", a)
		}
		if strings.Contains(strings.ToLower(toJSON(t, a)), "from the admin") || strings.Contains(toJSON(t, a), "fixed by an admin") {
			t.Errorf("an audit record holds document content: %v", a)
		}
	}
	if counts["data.create"] != 2 || counts["data.update"] != 3 || counts["data.delete"] != 1 || len(acts) != 6 {
		t.Errorf("audit counts: %v (want 2 creates, 3 updates, 1 delete, nothing for reads)", counts)
	}
	found := false
	for _, a := range acts {
		d, _ := a["details"].(map[string]any)
		if a["target"] == "doc:app/posts/"+draft && d["collection"] == "posts" && d["database"] == "app" && d["id"] == draft {
			found = true
		}
	}
	if !found {
		t.Errorf("no record for %s: %v", draft, acts)
	}

	// The normal route is unchanged: the same administrator is bound by rules there.
	if rec, _ := f.doH(t, "GET", posts+"/"+draft, "", bearer(f.bob)); rec.Code != http.StatusNotFound {
		t.Errorf("the rules still apply on the data route: %d", rec.Code)
	}
}

// A collection that soft-deletes: the administrator sees its trash, restores
// and purges without the restore and purge rules, and each is audited.
func TestAdminDataRouteSoftDelete(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	boss := jsonHdr(f.bob)
	rec, out := f.doH(t, "POST", adminDataBase+"/bin", `{"title":"t"}`, boss)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	if rec, _ := f.doH(t, "DELETE", adminDataBase+"/bin/"+id, "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Fatalf("soft delete: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", adminDataBase+"/bin/"+id, "", boss); rec.Code != http.StatusNotFound {
		t.Errorf("a deleted document is out of every read: %d", rec.Code)
	}
	if _, out := f.doH(t, "GET", adminDataBase+"/bin?deleted=only", "", boss); len(out["items"].([]any)) != 1 {
		t.Errorf("the trash: %v", out)
	}
	if rec, out := f.doH(t, "POST", adminDataBase+"/bin/"+id+"/restore", "", boss); rec.Code != http.StatusOK {
		t.Errorf("restore: %d %v", rec.Code, out)
	}
	f.doH(t, "DELETE", adminDataBase+"/bin/"+id, "", bearer(f.bob))
	if rec, _ := f.doH(t, "DELETE", adminDataBase+"/bin/"+id+"?purge=true", "", bearer(f.bob)); rec.Code != http.StatusNoContent {
		t.Errorf("purge: %d", rec.Code)
	}
	got := map[string]int{}
	for _, a := range f.auditActions(t, "data.") {
		got[a["action"].(string)]++
	}
	if got["data.create"] != 1 || got["data.delete"] != 2 || got["data.restore"] != 1 || got["data.purge"] != 1 {
		t.Errorf("audit: %v", got)
	}
}

// Who gets in: full administrators and the data area; read-only ones only to
// read and only when realm.yaml grants it; an admin key is full.
func TestAdminDataRouteLevels(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "support") // users, invitations: no data
	if rec, out := f.doH(t, "GET", adminDataBase+"/posts", "", bearer(f.bob)); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "data") {
		t.Errorf("a role without the data area: %d %v", rec.Code, out)
	}
	f.bobHolds(t, "viewer")
	if rec, _ := f.doH(t, "GET", adminDataBase+"/posts", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("read-only without read_access.data: %d", rec.Code)
	}
	f.svc.Settings.ReadAccess.Data = true
	if rec, out := f.doH(t, "GET", adminDataBase+"/posts", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("read-only with read_access.data: %d %v", rec.Code, out)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/posts", `{"title":"x"}`}, {"DELETE", "/posts/x", ""}, {"POST", "/_batch", `{"operations":[{"op":"delete","collection":"posts","id":"x"}]}`},
	} {
		rec, _ := f.doH(t, c.method, adminDataBase+c.path, c.body, jsonHdr(f.bob))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "can read") {
			t.Errorf("read-only %s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	// An admin key is full; its writes record the key.
	rec, out := f.doH(t, "POST", adminDataBase+"/posts", `{"title":"by key"}`, jsonHdr(f.key))
	meta, _ := out["_meta"].(map[string]any)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(meta["created_by"].(string), "key:") {
		t.Errorf("admin key creates: %d %v", rec.Code, out)
	}
	// A plain user and nobody are no administrators.
	if rec, _ := f.doH(t, "GET", adminDataBase+"/posts", "", bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("a plain user: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", adminDataBase+"/posts", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
}
