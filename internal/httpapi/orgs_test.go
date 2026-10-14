package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

const orgsPath = "/v1/acme/_orgs"

// orgOf creates an organization as a user and returns its id.
func (f *rulesFixture) orgOf(t *testing.T, token, name string) string {
	t.Helper()
	rec, out := f.doH(t, "POST", orgsPath, `{"name": "`+name+`"}`, jsonHdr(token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", name, rec.Code, out)
	}
	return out["id"].(string)
}

func TestOrganizationsLifecycle(t *testing.T) {
	f := newRulesFixture(t)
	// Creating: the caller becomes an owner, and holds the default role too.
	rec, out := f.doH(t, "POST", orgsPath, `{"name": "Acme Corp", "slug": "acme-corp"}`, jsonHdr(f.ada))
	if rec.Code != 201 || out["slug"] != "acme-corp" || out["status"] != "active" {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	roles, _ := out["roles"].([]any)
	if len(roles) != 2 || roles[0] != "member" || roles[1] != "owner" {
		t.Errorf("roles = %v", roles)
	}
	// An unverified user may not create (the default create rule), nor can a key.
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "No"}`, jsonHdr(f.carl)); rec.Code != 403 {
		t.Errorf("unverified: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", orgsPath, `{"name": "No"}`, jsonHdr(f.key)); rec.Code != 401 {
		t.Errorf("api key: %d", rec.Code)
	}
	// The slug is unique; names and slugs are validated.
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "Other", "slug": "acme-corp"}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "slug_taken" {
		t.Errorf("slug taken: %d %v", rec.Code, out)
	}
	for name, body := range map[string]string{"no name": `{}`, "blank": `{"name": "  "}`, "bad slug": `{"name": "x", "slug": "Not A Slug"}`, "unknown field": `{"name": "x", "owner": "u"}`} {
		if rec, out := f.doH(t, "POST", orgsPath, body, jsonHdr(f.bob)); rec.Code != 400 {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// The cap per creator: three organizations.
	f.orgOf(t, f.ada, "Two")
	f.orgOf(t, f.ada, "Three")
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "Four"}`, jsonHdr(f.ada)); rec.Code != 409 || errCode(out) != "org_limit_reached" {
		t.Errorf("cap: %d %v", rec.Code, out)
	}
	// Listing shows the caller's organizations only.
	rec, out = f.doH(t, "GET", orgsPath, "", bearer(f.ada))
	if items, _ := out["items"].([]any); rec.Code != 200 || len(items) != 3 {
		t.Errorf("list: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", orgsPath, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("bob's list: %d %v", rec.Code, out)
	}
	// A non-member and an unknown organization answer the same.
	for _, org := range []string{id, "nosuchorganization"} {
		rec, out := f.doH(t, "GET", orgsPath+"/"+org, "", bearer(f.bob))
		if rec.Code != 403 || errCode(out) != "not_org_member" {
			t.Errorf("%s: %d %v", org, rec.Code, out)
		}
	}
	// Members read the organization; managers rename it.
	if rec, out := f.doH(t, "GET", orgsPath+"/"+id, "", bearer(f.ada)); rec.Code != 200 || out["name"] != "Acme Corp" {
		t.Errorf("get: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+id, `{"name": "Acme Inc", "slug": "acme-inc"}`, jsonHdr(f.ada)); rec.Code != 200 || out["name"] != "Acme Inc" || out["slug"] != "acme-inc" {
		t.Errorf("rename: %d %v", rec.Code, out)
	}
}

func TestOrganizationMembers(t *testing.T) {
	f := newRulesFixture(t)
	id := f.orgOf(t, f.ada, "Team")
	admin := f.svc // the service, to add members the way an administrator does
	ctx := t.Context()
	if _, err := admin.AdminAddMember(ctx, id, f.bobID, []string{"admin"}); err != nil {
		t.Fatal(err)
	}
	// The members list shows emails (members_visible_to: members) and where roles come from.
	rec, out := f.doH(t, "GET", orgsPath+"/"+id+"/members", "", bearer(f.bob))
	items, _ := out["items"].([]any)
	if rec.Code != 200 || len(items) != 2 || !strings.Contains(rec.Body.String(), "ada@example.com") {
		t.Fatalf("members: %d %s", rec.Code, rec.Body)
	}
	// An admin (manage: members, teams) grants editor, which opens nothing, but not owner.
	target := orgsPath + "/" + id + "/members/" + f.adaID
	bobTarget := orgsPath + "/" + id + "/members/" + f.bobID
	if rec, out := f.doH(t, "PATCH", bobTarget, `{"roles": ["admin", "editor"]}`, jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("grant editor: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", bobTarget, `{"roles": ["admin", "owner"]}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("grant owner: %d %v", rec.Code, out)
	}
	// An admin can't change or remove an owner, who holds more than they do.
	if rec, _ := f.doH(t, "PATCH", target, `{"roles": ["editor"]}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("change owner: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", target, "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("remove owner: %d", rec.Code)
	}
	// An unknown role is a validation error.
	if rec, out := f.doH(t, "PATCH", bobTarget, `{"roles": ["wizard"]}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("unknown role: %d %v", rec.Code, out)
	}
	// An owner may make another owner (manage: roles), and then leave.
	if rec, out := f.doH(t, "PATCH", bobTarget, `{"roles": ["owner"]}`, jsonHdr(f.ada)); rec.Code != 200 {
		t.Errorf("make owner: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", orgsPath+"/"+id+"/leave", "", bearer(f.ada)); rec.Code != 204 {
		t.Errorf("leave: %d", rec.Code)
	}
	// Bob is now the last manager: he can't leave, be demoted, or be removed.
	if rec, out := f.doH(t, "POST", orgsPath+"/"+id+"/leave", "", bearer(f.bob)); rec.Code != 409 || errCode(out) != "last_org_manager" {
		t.Errorf("last manager leaves: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", bobTarget, `{"roles": ["editor"]}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "last_org_manager" {
		t.Errorf("last manager demoted: %d %v", rec.Code, out)
	}
	// Ada lost access at once.
	if rec, out := f.doH(t, "GET", orgsPath+"/"+id, "", bearer(f.ada)); rec.Code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("after leaving: %d %v", rec.Code, out)
	}
}

func TestOrganizationsAdminAPI(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "support") // the users area
	adm := "/v1/acme/_admin/orgs"
	rec, out := f.doH(t, "POST", adm, `{"name": "Made by staff", "owner": "`+f.adaID+`"}`, jsonHdr(f.bob))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	// The owner is a member; staff adds another one directly, and sets claims.
	if rec, out := f.doH(t, "POST", adm+"/"+id+"/members", `{"user": "`+f.bobID+`", "roles": ["editor"]}`, jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("add member: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PUT", adm+"/"+id+"/claims", `{"plan": "pro", "seats": 5}`, jsonHdr(f.bob)); rec.Code != 200 || out["claims"].(map[string]any)["plan"] != "pro" {
		t.Errorf("claims: %d %v", rec.Code, out)
	}
	big := `{"x": "` + strings.Repeat("a", 1100) + `"}`
	if rec, _ := f.doH(t, "PUT", adm+"/"+id+"/claims", big, jsonHdr(f.bob)); rec.Code != 400 {
		t.Errorf("big claims: %d", rec.Code)
	}
	// Members of the organization see the claims, read-only.
	if rec, out := f.doH(t, "GET", orgsPath+"/"+id, "", bearer(f.bob)); rec.Code != 200 || out["claims"].(map[string]any)["plan"] != "pro" {
		t.Errorf("member reads claims: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "GET", adm+"?status=active", "", bearer(f.bob))
	if items, _ := out["items"].([]any); rec.Code != 200 || len(items) != 1 || items[0].(map[string]any)["members"] != float64(2) {
		t.Errorf("list: %d %v", rec.Code, out)
	}
	// Searching by name or slug.
	for q, want := range map[string]int{"staff": 1, "MADE": 1, "nothing": 0} {
		rec, out = f.doH(t, "GET", adm+"?q="+q, "", bearer(f.bob))
		if items, _ := out["items"].([]any); rec.Code != 200 || len(items) != want {
			t.Errorf("search %q: %d %v", q, rec.Code, out)
		}
	}
	// Roles of another area don't open it.
	f.bobHolds(t, "auditor")
	if rec, _ := f.doH(t, "GET", adm, "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("auditor: %d", rec.Code)
	}
	// The admin API refuses the last manager's removal too, and assigns a new owner.
	f.bobHolds(t, "support")
	if rec, out := f.doH(t, "DELETE", adm+"/"+id+"/members/"+f.adaID, "", bearer(f.bob)); rec.Code != 409 || errCode(out) != "last_org_manager" {
		t.Errorf("remove last: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", adm+"/"+id+"/owner", `{"user": "`+f.bobID+`"}`, jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("assign owner: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", adm+"/"+id+"/members/"+f.adaID, "", bearer(f.bob)); rec.Code != 204 {
		t.Errorf("remove: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", adm+"/nosuch", "", bearer(f.bob)); rec.Code != 404 {
		t.Errorf("unknown: %d %v", rec.Code, out)
	}
}
