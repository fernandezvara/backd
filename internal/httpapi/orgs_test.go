package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const orgsPath = "/v1/acme/_orgs"

// orgOf creates an organization as a user and returns its id.
func (f *rulesFixture) orgOf(t *testing.T, token, name string) string {
	t.Helper()
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	rec, out := f.doH(t, "POST", orgsPath, `{"name": "`+name+`", "slug": "`+slug+`"}`, jsonHdr(token))
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
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "No", "slug": "no"}`, jsonHdr(f.carl)); rec.Code != 403 {
		t.Errorf("unverified: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", orgsPath, `{"name": "No"}`, jsonHdr(f.key)); rec.Code != 401 {
		t.Errorf("api key: %d", rec.Code)
	}
	// The slug is unique; names and slugs are validated.
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "Other", "slug": "acme-corp"}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "slug_taken" {
		t.Errorf("slug taken: %d %v", rec.Code, out)
	}
	for name, body := range map[string]string{"no name": `{}`, "blank": `{"name": "  "}`, "bad slug": `{"name": "x", "slug": "Not A Slug"}`, "unknown field": `{"name": "x", "slug": "x", "owner": "u"}`} {
		if rec, out := f.doH(t, "POST", orgsPath, body, jsonHdr(f.bob)); rec.Code != 400 {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// The cap per creator: three organizations, the one everybody has included.
	f.orgOf(t, f.ada, "Two")
	if rec, out := f.doH(t, "POST", orgsPath, `{"name": "Four", "slug": "four"}`, jsonHdr(f.ada)); rec.Code != 409 || errCode(out) != "org_limit_reached" {
		t.Errorf("cap: %d %v", rec.Code, out)
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
	rec, out := f.doH(t, "POST", adm, `{"name": "Made by staff", "slug": "made-by-staff", "owner": "`+f.adaID+`"}`, jsonHdr(f.bob))
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
	rec, out = f.doH(t, "GET", adm+"?status=active&q=staff", "", bearer(f.bob))
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

// previewInvite is what an invitation to the organization offers, asked by someone not signed in.
func (f *rulesFixture) previewInvite(t *testing.T, org, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token})
	return f.doH(t, "POST", "/v1/acme/_auth/orgs/"+org+"/invitations/preview", string(body), jsonHdr(""))
}

// acceptInvite signs the person whose session is who in to the organization with the invitation,
// as joining by invitation goes. It answers the sign-in's refusal, or, on success, their membership.
func (f *rulesFixture) acceptInvite(t *testing.T, org, who, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	p, err := f.svc.Authenticate(context.Background(), who)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"email": p.User.Email, "password": "dev-p4ssw0rd!", "invitation": token})
	rec, out := f.fixture.doH(t, "POST", "/v1/acme/_auth/orgs/"+org+"/login", string(body), jsonHdr(""))
	if rec.Code != http.StatusOK {
		return rec, out
	}
	return f.fixture.doH(t, "GET", orgsPath+"/"+org, "", bearer(out["token"].(string)))
}

// An organization may shorten the lifetime of its sessions: the session opened in it ends sooner,
// the others do not.
func TestOrganizationSessionMaxAge(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.orgOf(t, f.ada, "Short")
	path := orgsPath + "/" + id
	for name, c := range map[string]struct {
		body string
		code int
	}{
		"a minute":       {`{"session_max_age": 60}`, 400},
		"negative":       {`{"session_max_age": -1}`, 400},
		"over the realm": {`{"session_max_age": 99999999}`, 400},
		"text":           {`{"session_max_age": "2h"}`, 400},
	} {
		if rec, out := f.doH(t, "PATCH", path, c.body, f.withProof(t, f.ada)); rec.Code != c.code {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "PATCH", path, `{"session_max_age": 7200}`, jsonHdr(f.ada)); rec.Code != 401 {
		t.Errorf("without a proof: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", path, `{"session_max_age": 7200}`, f.withProof(t, f.bob)); rec.Code == 200 {
		t.Errorf("a stranger sets it: %d %v", rec.Code, out)
	}
	rec, out := f.doH(t, "PATCH", path, `{"session_max_age": 7200}`, f.withProof(t, f.ada))
	if rec.Code != 200 || out["session_max_age"] != float64(7200) {
		t.Fatalf("set: %d %v", rec.Code, out)
	}
	// A session opened now ends in two hours at the latest; one of the home organization does not.
	short := f.sessionIn(t, f.ada, id)
	p, err := f.svc.Authenticate(context.Background(), short)
	if err != nil {
		t.Fatal(err)
	}
	if p.Session.MaxLifetime != 2*time.Hour || p.Session.ExpiresAt.After(p.Session.CreatedAt.Add(2*time.Hour)) {
		t.Errorf("short session: %+v", p.Session)
	}
	if h, err := f.svc.Authenticate(context.Background(), f.ada); err != nil || h.Session.MaxLifetime != 0 {
		t.Errorf("the home session changed: %+v %v", h.Session, err)
	}
	*f.clock = f.clock.Add(3 * time.Hour)
	if _, err := f.svc.Authenticate(context.Background(), short); err == nil {
		t.Error("the session outlived the organization's limit")
	}
	f.proofs = nil // (the proofs are old now)
	// 0 goes back to the realm's.
	rec, out = f.doH(t, "PATCH", path, `{"session_max_age": 0}`, f.withProof(t, f.ada))
	if rec.Code != 200 || out["session_max_age"] != nil {
		t.Errorf("clear: %d %v", rec.Code, out)
	}
}

// A session without an organization, in a realm that has them, is refused and the person signs in
// again; an administrator's reaches the admin API alone.
func TestUnscopedSessionsAreRefused(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	p, token, err := f.svc.Signup(ctx, "late@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = p
	// The realm's own sign-in and sign-up are closed to the people of the realm.
	if rec, out := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(token)); rec.Code != 401 || errCode(out) != "session_unscoped" {
		t.Errorf("an unscoped session: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", "/v1/acme/app/projects", "", bearer(token)); rec.Code != 401 || errCode(out) != "session_unscoped" {
		t.Errorf("an unscoped session reads documents: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "late@example.com", "password": "dev-p4ssw0rd!"}`, jsonHdr("")); rec.Code != 401 || errCode(out) != "invalid_credentials" {
		t.Errorf("the realm's login for a person: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", "/v1/acme/_auth/signup", `{"email": "new@example.com", "password": "dev-p4ssw0rd!"}`, jsonHdr("")); rec.Code != 404 {
		t.Errorf("the realm's sign-up: %d %v", rec.Code, out)
	}
	// An administrator signs in at the realm and has no organization.
	f.bobHolds(t, "staff")
	rec, out := f.doH(t, "POST", "/v1/acme/_auth/login", `{"email": "bob@example.com", "password": "dev-p4ssw0rd!"}`, jsonHdr(""))
	if rec.Code != 200 || out["token"] == nil {
		t.Fatalf("an administrator's login: %d %v", rec.Code, out)
	}
	op := out["token"].(string)
	if rec, out := f.doH(t, "GET", "/v1/acme/_admin/users?limit=1", "", bearer(op)); rec.Code != 200 {
		t.Errorf("the admin API: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", "/v1/acme/app/projects", "", bearer(op)); rec.Code != 403 {
		t.Errorf("an operator counts no organization: %d %v", rec.Code, out)
	}
}
