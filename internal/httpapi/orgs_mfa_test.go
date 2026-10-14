package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// mfaSession gives a user a session that used a second factor, the way a sign-in with one
// would leave it.
func (f *rulesFixture) mfaSession(t *testing.T, userID string) string {
	t.Helper()
	token, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := f.svc.Clock()
	err = f.svc.Store.CreateSession(context.Background(), auth.Session{
		ID: xid.New().String(), UserID: userID, TokenHash: auth.HashToken(token), CreatedAt: now, LastUsedAt: now,
		ExpiresAt: now.Add(24 * time.Hour), Methods: []string{"password", "totp"}, MFA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestOrganizationRequiresMFA(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	id := f.orgOf(t, f.ada, "Strict")
	if _, err := f.svc.AdminAddMember(ctx, id, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "POST", projects+"?org="+id, `{"title": "before"}`, jsonHdr(f.ada)); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	adaMFA := f.mfaSession(t, f.adaID)
	// Turning it on: a fresh proof, and a second factor of the caller's own.
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+id, `{"require_mfa": true}`, jsonHdr(f.ada)); rec.Code != 401 {
		t.Errorf("without a proof: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+id, `{"require_mfa": true}`, f.withProof(t, f.ada)); rec.Code != 403 || errCode(out) != "org_requires_mfa" {
		t.Errorf("a session without a second factor turns it on: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+id, `{"require_mfa": true}`, f.withProof(t, adaMFA)); rec.Code != 200 || out["require_mfa"] != true || out["mfa_met"] != true {
		t.Fatalf("turn on: %d %v", rec.Code, out)
	}
	// A session without one doesn't count: the endpoints and the helpers refuse, except leaving.
	for _, c := range []struct{ method, path string }{
		{"GET", orgsPath + "/" + id}, {"GET", orgsPath + "/" + id + "/members"}, {"GET", orgsPath + "/" + id + "/teams"}, {"GET", orgsPath + "/" + id + "/invitations"},
	} {
		if rec, out := f.doH(t, c.method, c.path, "", bearer(f.bob)); rec.Code != 403 || errCode(out) != "org_requires_mfa" {
			t.Errorf("%s %s: %d %v", c.method, c.path, rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("documents without a second factor: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", projects+"?org="+id, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("create without a second factor: %d %v", rec.Code, out)
	}
	// The list marks it, so the app can ask for a second factor.
	rec, out := f.doH(t, "GET", orgsPath, "", bearer(f.bob))
	item := out["items"].([]any)[0].(map[string]any)
	if rec.Code != 200 || item["require_mfa"] != true || item["mfa_met"] != false {
		t.Errorf("list: %d %v", rec.Code, out)
	}
	// Non-members learn nothing: the same answer as for an unknown organization.
	if rec, out := f.doH(t, "GET", orgsPath+"/"+id, "", bearer(f.carl)); rec.Code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("a stranger: %d %v", rec.Code, out)
	}
	// With a second factor everything works, and managers see who has none.
	adaTok := adaMFA
	rec, out = f.doH(t, "GET", orgsPath+"/"+id+"/members", "", bearer(adaTok))
	if rec.Code != 200 {
		t.Fatalf("members: %d %v", rec.Code, out)
	}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if _, has := m["mfa"]; !has {
			t.Errorf("a manager sees no mfa field: %v", m)
		}
	}
	if rec, out := f.doH(t, "GET", projects+"?org="+id, "", bearer(adaTok)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("documents with a second factor: %d %v", rec.Code, out)
	}
	// Leaving stays possible without one.
	if rec, _ := f.doH(t, "POST", orgsPath+"/"+id+"/leave", "", bearer(f.bob)); rec.Code != 204 {
		t.Errorf("leave: %d", rec.Code)
	}
	// Turning it off, and the realm switch.
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+id, `{"require_mfa": false}`, f.withProof(t, adaMFA)); rec.Code != 200 || out["require_mfa"] != false {
		t.Errorf("turn off: %d %v", rec.Code, out)
	}
	f.svc.Settings.Organizations.AllowRequireMFA = false
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+id, `{"require_mfa": true}`, f.withProof(t, adaMFA)); rec.Code != 409 || errCode(out) != "require_mfa_not_allowed" {
		t.Errorf("not allowed: %d %v", rec.Code, out)
	}
	// An API key acting for a user has no second factor either.
	f.svc.Settings.Organizations.AllowRequireMFA = true
	if rec, _ := f.doH(t, "PATCH", orgsPath+"/"+id, `{"require_mfa": true}`, f.withProof(t, adaMFA)); rec.Code != 200 {
		t.Fatal("turn on again")
	}
	h := bearer(f.key)
	h["X-Backd-On-Behalf-Of"] = f.adaID
	if rec, out := f.doH(t, "GET", projects, "", h); rec.Code != 403 {
		t.Errorf("a key on behalf of a member: %d %v", rec.Code, out)
	}
}

func TestRolesThatRequireMFA(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	org := f.orgOf(t, f.ada, "Sensitive")
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	o := f.svc.Settings.Organizations
	o.Roles["editor"] = registry.OrgRole{RequireMFA: true}
	o.Roles["owner"] = registry.OrgRole{Manage: o.Roles["owner"].Manage, RequireMFA: true}
	o.TeamRoles["manager"] = registry.TeamRole{Grants: []string{"delegate"}, RequireMFA: true}
	bobMFA := f.mfaSession(t, f.bobID)
	adaMFA := f.mfaSession(t, f.adaID)

	// The role doesn't count without a second factor: the helpers are false, plain membership stays.
	if rec, out := f.doH(t, "POST", projects+"?org="+org, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("create without it: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", projects+"?org="+org, `{"title": "x"}`, jsonHdr(bobMFA)); rec.Code != 201 {
		t.Errorf("create with it: %d %v", rec.Code, out)
	}
	rec, out := f.doH(t, "GET", orgsPath+"/"+org, "", bearer(f.bob))
	if rec.Code != 200 || len(out["roles"].([]any)) != 1 || out["roles"].([]any)[0] != "member" {
		t.Errorf("a session without it keeps plain membership: %d %v", rec.Code, out["roles"])
	}
	if rec, out := f.doH(t, "GET", orgsPath+"/"+org+"/members", "", bearer(f.bob)); rec.Code != 200 {
		t.Errorf("members without it: %d %v", rec.Code, out)
	}

	// An action that needs the role says so, with the role.
	rec, out = f.doH(t, "PATCH", orgsPath+"/"+org, `{"name": "Renamed"}`, jsonHdr(f.ada))
	if rec.Code != 403 || errCode(out) != "org_role_requires_mfa" || !strings.Contains(fmt.Sprint(out), "owner") {
		t.Errorf("rename without it: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+org, `{"name": "Renamed"}`, jsonHdr(adaMFA)); rec.Code != 200 {
		t.Errorf("rename with it: %d %v", rec.Code, out)
	}
	// Someone who lacks the capability anyway is just forbidden.
	if rec, out := f.doH(t, "PATCH", orgsPath+"/"+org, `{"name": "Mine"}`, jsonHdr(bobMFA)); rec.Code != 403 || errCode(out) != "forbidden" {
		t.Errorf("a member renames: %d %v", rec.Code, out)
	}

	// A team role that requires it can't delegate without one.
	teams := orgsPath + "/" + org + "/teams"
	_, out = f.doH(t, "POST", teams, `{"name": "Sales"}`, jsonHdr(adaMFA))
	sales := out["id"].(string)
	if rec, out := f.doH(t, "POST", teams+"/"+sales+"/roles", fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID), jsonHdr(adaMFA)); rec.Code != 200 {
		t.Fatalf("grant manager: %d %v", rec.Code, out)
	}
	body := fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.adaID)
	rec, out = f.doH(t, "POST", teams+"/"+sales+"/roles", body, jsonHdr(f.bob))
	if rec.Code != 403 || errCode(out) != "org_role_requires_mfa" || !strings.Contains(fmt.Sprint(out), "manager") {
		t.Errorf("delegate without it: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", teams+"/"+sales+"/roles", body, jsonHdr(bobMFA)); rec.Code != 200 {
		t.Errorf("delegate with it: %d %v", rec.Code, out)
	}

	// Managers see who holds such roles and has no second factor yet.
	_, out = f.doH(t, "GET", orgsPath+"/"+org+"/members", "", bearer(adaMFA))
	seen := map[string]string{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		seen[m["user"].(string)] = fmt.Sprint(m["mfa"], m["mfa_roles"])
	}
	if seen[f.bobID] != "false [editor manager]" {
		t.Errorf("bob: %q (%v)", seen[f.bobID], seen)
	}
}
