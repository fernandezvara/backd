package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
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
