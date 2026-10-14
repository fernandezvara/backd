package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// orgsCall sends a request to the internal listener as a function would.
func (f *rulesFixture) orgsCall(t *testing.T, method, path, body string, claims auth.CallbackClaims) (int, map[string]any) {
	t.Helper()
	claims.Realm, claims.Expires = "acme", f.clock.Add(time.Minute)
	if claims.Function == "" {
		claims.Function = "app/billing"
	}
	req := httptest.NewRequest(method, "/v1/acme/_fn/orgs"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+auth.SignCallback(f.callbackKey, claims))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.internal.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestFunctionsManageOrganizations(t *testing.T) {
	f := newRulesFixture(t)
	admin := auth.CallbackClaims{Admin: true}
	// Only a function with admin access: "payment received, create the organization".
	if code, _ := f.orgsCall(t, "GET", "", "", auth.CallbackClaims{}); code != 403 {
		t.Errorf("without admin access: %d", code)
	}
	code, out := f.orgsCall(t, "POST", "", `{"name": "Paid Co", "owner": "`+f.adaID+`"}`, admin)
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["id"].(string)
	if code, out := f.orgsCall(t, "PUT", "/"+id+"/claims", `{"plan": "pro", "seats": 10}`, admin); code != 200 || out["claims"].(map[string]any)["plan"] != "pro" {
		t.Errorf("claims: %d %v", code, out)
	}
	if code, out := f.orgsCall(t, "POST", "/"+id+"/members", `{"user": "`+f.bobID+`", "roles": ["editor"]}`, admin); code != 200 {
		t.Errorf("add member: %d %v", code, out)
	}
	code, out = f.orgsCall(t, "POST", "/"+id+"/teams", `{"name": "Ops", "roles": ["editor"]}`, admin)
	if code != 201 {
		t.Fatalf("team: %d %v", code, out)
	}
	if code, out := f.orgsCall(t, "POST", "/"+id+"/invitations", `{"binding": "email", "email": "new@example.com", "roles": ["editor"]}`, admin); code != 201 || out["token"] == nil {
		t.Errorf("invitation: %d %v", code, out)
	}
	if code, out := f.orgsCall(t, "GET", "?status=active", "", admin); code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("list: %d %v", code, out)
	}
	if code, _ := f.orgsCall(t, "DELETE", "/"+id+"/members/"+f.adaID, "", admin); code != 409 {
		t.Errorf("the last manager: %d", code)
	}
	// The function is the actor in the audit trail.
	trail, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditOrgClaims})
	if len(trail) != 1 || trail[0].Actor != "func:acme/app/billing" {
		t.Errorf("audit: %+v", trail)
	}
	// ctx.user.orgs: the caller's memberships, with what counts.
	env := f.functionsUserEnvelope(t, f.bobID)
	orgs, _ := env["orgs"].(map[string]any)
	m, _ := orgs[id].(map[string]any)
	if m == nil || m["counts"] != true || m["name"] != "Paid Co" || m["claims"].(map[string]any)["plan"] != "pro" {
		t.Errorf("ctx.user.orgs = %v", env["orgs"])
	}
	roles := m["roles"].([]any)
	if len(roles) != 2 || roles[0] != "editor" || roles[1] != "member" {
		t.Errorf("roles = %v", roles)
	}
}

// functionsUserEnvelope is ctx.user for a user, as the runner receives it.
func (f *rulesFixture) functionsUserEnvelope(t *testing.T, userID string) map[string]any {
	t.Helper()
	u, err := f.svc.Store.UserByID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	c := auth.Caller{User: &auth.Principal{User: u}}
	fns := &functions{docs: &documents{reg: f.reg, users: func(string) *auth.Users { return f.svc }}}
	var env map[string]any
	if err := json.Unmarshal(userEnvelope(c, fns.userOrgsEnvelope(context.Background(), "acme", c)), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

// ctx.db creates in an organization with the same checks as HTTP: the caller is the user the
// function acts for, and the admin token (ctx.admin.db) may use any active organization.
func TestFunctionsCreateInOrganizations(t *testing.T) {
	f := newRulesFixture(t)
	org := f.orgOf(t, f.ada, "Callbacks")
	post := func(claims auth.CallbackClaims, query string) (int, map[string]any) {
		claims.Realm, claims.Expires, claims.Function = "acme", f.clock.Add(time.Minute), "app/billing"
		req := httptest.NewRequest("POST", "/v1/acme/app/projects"+query, strings.NewReader(`{"title": "from a function"}`))
		req.Header.Set("Authorization", "Bearer "+auth.SignCallback(f.callbackKey, claims))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.internal.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := post(auth.CallbackClaims{UserID: f.adaID}, "?org="+org); code != 201 || out["_meta"].(map[string]any)["org"] != org {
		t.Errorf("as a member: %d %v", code, out)
	}
	if code, out := post(auth.CallbackClaims{UserID: f.bobID}, "?org="+org); code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("as a stranger: %d %v", code, out)
	}
	if code, out := post(auth.CallbackClaims{UserID: f.adaID}, ""); code != 400 || errCode(out) != "org_required" {
		t.Errorf("without one: %d %v", code, out)
	}
	if code, out := post(auth.CallbackClaims{Admin: true}, "?org="+org); code != 201 {
		t.Errorf("with admin access: %d %v", code, out)
	}
}

// ctx.admin.orgs.teams grants and lists team roles, ctx.user.orgs shows the ones that count, and
// ctx.db creates in a team with the same checks as HTTP.
func TestFunctionsTeamRoles(t *testing.T) {
	f := newRulesFixture(t)
	*f.clock = f.clock.Truncate(time.Second) // the API takes whole seconds
	f.svc.Now = func() time.Time { return *f.clock }
	admin := auth.CallbackClaims{Admin: true}
	org := f.orgOf(t, f.ada, "Departments")
	if _, err := f.svc.AdminAddMember(context.Background(), org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	code, out := f.orgsCall(t, "POST", "/"+org+"/teams", `{"name": "Sales"}`, admin)
	if code != 201 {
		t.Fatalf("team: %d %v", code, out)
	}
	sales := out["id"].(string)
	roles := "/" + org + "/teams/" + sales + "/roles"
	// Not yet open: the grant is stored, and ctx.user.orgs leaves it out.
	from := f.clock.Add(time.Hour).UTC().Format(time.RFC3339)
	const millis = "2006-01-02T15:04:05.000Z" // how the API shows times
	fromShown := f.clock.Add(time.Hour).UTC().Format(millis)
	if code, out := f.orgsCall(t, "POST", roles, `{"user": "`+f.bobID+`", "role": "manager", "from": "`+from+`"}`, admin); code != 200 {
		t.Fatalf("grant: %d %v", code, out)
	}
	teamRolesOf := func() []any {
		t.Helper()
		env := f.functionsUserEnvelope(t, f.bobID)
		return env["orgs"].(map[string]any)[org].(map[string]any)["team_roles"].([]any)
	}
	if got := teamRolesOf(); len(got) != 0 {
		t.Errorf("before the window: %v", got)
	}
	*f.clock = f.clock.Add(time.Hour)
	got := teamRolesOf()
	if len(got) != 1 || got[0].(map[string]any)["role"] != "manager" || got[0].(map[string]any)["team"] != sales || got[0].(map[string]any)["from"] != fromShown {
		t.Errorf("inside the window: %v", got)
	}
	// Listing, re-windowing and revoking.
	if code, out := f.orgsCall(t, "GET", "/"+org+"/teams/"+sales+"/members?role=manager", "", admin); code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("members: %d %v", code, out)
	}
	until := f.clock.Add(time.Minute).UTC().Format(time.RFC3339)
	untilShown := f.clock.Add(time.Minute).UTC().Format(millis)
	if code, out := f.orgsCall(t, "PATCH", roles, `{"user": "`+f.bobID+`", "role": "manager", "until": "`+until+`"}`, admin); code != 200 {
		t.Errorf("window: %d %v", code, out)
	}
	if got := teamRolesOf(); len(got) != 1 || got[0].(map[string]any)["until"] != untilShown || got[0].(map[string]any)["from"] != nil {
		t.Errorf("after re-windowing: %v", got)
	}
	if code, out := f.orgsCall(t, "DELETE", roles, `{"user": "`+f.bobID+`", "role": "manager"}`, admin); code != 200 {
		t.Errorf("revoke: %d %v", code, out)
	}
	if got := teamRolesOf(); len(got) != 0 {
		t.Errorf("after revoking: %v", got)
	}
	// Not a function with admin access: nothing.
	if code, _ := f.orgsCall(t, "POST", roles, `{"user": "`+f.bobID+`", "role": "manager"}`, auth.CallbackClaims{}); code != 403 {
		t.Errorf("without admin access: %d", code)
	}
}
