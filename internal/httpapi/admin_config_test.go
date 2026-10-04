package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminConfigView(t *testing.T) {
	f := newRulesFixture(t)
	rec, out := f.doH(t, "GET", admin+"/config", "", bearer(f.key))
	if rec.Code != http.StatusOK || out["realm"] != "acme" {
		t.Fatalf("config: %d %v", rec.Code, out)
	}
	if out["file"] != "acme/realm.yaml" {
		t.Errorf("realm file: %v", out["file"])
	}
	settings := out["settings"].(map[string]any)
	if settings["auth"] != "enabled" || settings["signup"] != "open" {
		t.Errorf("settings: %v", settings)
	}
	roles := settings["roles"].(map[string]any)
	if roles["staff"].(map[string]any)["admin"] != "all" || roles["viewer"].(map[string]any)["admin"] != "read" ||
		roles["support"].(map[string]any)["admin"] != "users, invitations" {
		t.Errorf("roles: %v", roles)
	}
	if t2 := settings["login_throttle"].(map[string]any); t2["account_threshold"] != float64(5) {
		t.Errorf("defaults are shown: %v", t2)
	}
	app := out["databases"].(map[string]any)["app"].(map[string]any)
	var posts, bin map[string]any
	for _, c := range app["collections"].([]any) {
		switch c.(map[string]any)["name"] {
		case "posts":
			posts = c.(map[string]any)
		case "bin":
			bin = c.(map[string]any)
		}
	}
	if posts == nil || posts["schema_file"] != "acme/app/posts/schema.json" || posts["schema"] == nil || posts["rules_file"] != "acme/app/posts/rules.yaml" {
		t.Fatalf("posts: %v", posts)
	}
	if r := posts["rules"].(map[string]any)["read"].(map[string]any); !strings.Contains(r["expression"].(string), "published") {
		t.Errorf("a rule shows its expression: %v", r)
	}
	if pol := bin["policy"].(map[string]any)["soft_delete"].(map[string]any); pol["retention"] != "168h0m0s" || bin["policy_file"] != "acme/app/bin/collection.yaml" {
		t.Errorf("soft delete policy: %v %v", bin["policy"], bin["policy_file"])
	}
	var keyed map[string]any
	for _, fn := range app["functions"].([]any) {
		if fn.(map[string]any)["name"] == "keyed" {
			keyed = fn.(map[string]any)
		}
	}
	if keyed == nil || keyed["secrets"].([]any)[0] != "KEY" || keyed["file"] != "acme/app/_functions/keyed/function.yaml" {
		t.Errorf("a function names its secrets and its file: %v", keyed)
	}
	if _, ok := out["templates"].(map[string]any)["email"].(map[string]any)["order-shipped"]; !ok {
		t.Errorf("email templates: %v", out["templates"])
	}
	if w := out["warnings"].([]any); len(w) == 0 {
		t.Errorf("a realm with open sign-up and no admin networks has warnings: %v", w)
	}
	if strings.Contains(rec.Body.String(), "password_hash") {
		t.Error("the config view holds nothing secret")
	}
	// Seeded emails are personal data: only for whoever may read users.
	if _, has := roles["staff"].(map[string]any)["seeded_emails"]; !has {
		t.Errorf("a full admin sees seeded emails: %v", roles["staff"])
	}
	f.bobHolds(t, "viewer")
	_, out = f.doH(t, "GET", admin+"/config", "", bearer(f.bob))
	if _, has := out["settings"].(map[string]any)["roles"].(map[string]any)["staff"].(map[string]any)["seeded_emails"]; has {
		t.Errorf("a read-only admin without read_access.users doesn't see seeded emails")
	}
	// The config area: read-only reads it; an area list needs `config`; changes don't exist.
	f.bobHolds(t, "support")
	if rec, _ := f.doH(t, "GET", admin+"/config", "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("a role without the config area: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", admin+"/config", `{}`, jsonHdr(f.key)); rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
		t.Errorf("nothing changes the config through the API: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", admin+"/config", "", bearer(f.ada)); rec.Code != http.StatusForbidden {
		t.Errorf("a plain user: %d", rec.Code)
	}
}

func TestAdminUserSearch(t *testing.T) {
	f := newRulesFixture(t)
	emails := func(q string) string {
		t.Helper()
		rec, out := f.doH(t, "GET", admin+"/users"+q, "", bearer(f.key))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %v", q, rec.Code, out)
		}
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["email"].(string))
		}
		return strings.Join(got, ",")
	}
	if got := emails("?q=ada"); got != "ada@example.com" {
		t.Errorf("q=ada: %q", got)
	}
	if got := emails("?q=ADA@Example"); got != "ada@example.com" {
		t.Errorf("a search ignores case: %q", got)
	}
	if got := emails("?q=%40example.com"); !strings.Contains(got, "ada@example.com") || !strings.Contains(got, "bob@example.com") {
		t.Errorf("a domain: %q", got)
	}
	if got := emails("?q=nobody-has-this"); got != "" {
		t.Errorf("no match: %q", got)
	}
	if got := emails("?q=.*"); got != "" { // not a pattern
		t.Errorf("a search is text, not a regular expression: %q", got)
	}
	// Paging follows the search.
	_, first := f.doH(t, "GET", admin+"/users?q=%40example.com&limit=1", "", bearer(f.key))
	cursor, _ := first["next_cursor"].(string)
	if first["has_more"] != true || cursor == "" {
		t.Fatalf("first page of a search: %v", first)
	}
	_, next := f.doH(t, "GET", admin+"/users?q=%40example.com&limit=1&after="+cursor, "", bearer(f.key))
	if len(next["items"].([]any)) != 1 || next["items"].([]any)[0].(map[string]any)["email"] == first["items"].([]any)[0].(map[string]any)["email"] {
		t.Errorf("the next page of a search: %v", next)
	}
	for _, q := range []string{"?q=", "?q=ada&email=ada@example.com"} {
		if rec, out := f.doH(t, "GET", admin+"/users"+q, "", bearer(f.key)); rec.Code != http.StatusBadRequest || errCode(out) != "invalid_query" {
			t.Errorf("%s: %d %v", q, rec.Code, out)
		}
	}
}

func TestAdminUserSessions(t *testing.T) {
	f := newRulesFixture(t)
	rec, out := f.doH(t, "GET", admin+"/users/"+f.adaID+"/sessions", "", bearer(f.key))
	items, _ := out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) == 0 {
		t.Fatalf("sessions: %d %v", rec.Code, out)
	}
	sess := items[0].(map[string]any)
	for _, k := range []string{"id", "created_at", "last_used_at", "expires_at"} {
		if sess[k] == nil {
			t.Errorf("a session shows %s: %v", k, sess)
		}
	}
	if strings.Contains(rec.Body.String(), f.ada) {
		t.Error("the sessions list must not hold a token")
	}
	// A read-only administrator reads them (with read_access.users) and ends none.
	f.svc.Settings.ReadAccess.Users = true
	f.bobHolds(t, "viewer")
	if rec, _ := f.doH(t, "GET", admin+"/users/"+f.adaID+"/sessions", "", bearer(f.bob)); rec.Code != http.StatusOK {
		t.Errorf("viewer lists sessions: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/users/"+f.adaID+"/sessions/"+sess["id"].(string), "", bearer(f.bob)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer revokes a session: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/users/"+f.adaID+"/sessions/nope", "", bearer(f.key)); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown session: %d", rec.Code)
	}
	// Ending a session ends it: the token stops working, and it is audited.
	if rec, _ := f.doH(t, "GET", posts, "", bearer(f.ada)); rec.Code != http.StatusOK {
		t.Fatalf("ada's session works before: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/users/"+f.adaID+"/sessions/"+sess["id"].(string), "", bearer(f.key)); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", posts, "", bearer(f.ada)); rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked session still works: %d", rec.Code)
	}
	if acts := f.auditActions(t, "session."); len(acts) != 1 || acts[0]["target"] != "user:"+f.adaID {
		t.Errorf("audit: %v", acts)
	}
}
