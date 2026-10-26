package httpapi

import (
	"context"
	"github.com/fernandezvara/backd/internal/auth"
	"net/http"
	"strings"
	"testing"
)

// Documents in organizations (design/organizations.md §6, design/scoped-sessions.md §4.2): a
// session works in the organization it belongs to and does not say which.
func TestDocumentsInOrganizations(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	orgA := f.orgOf(t, f.ada, "Alpha")
	orgB := f.orgOf(t, f.bob, "Beta")
	adaA := f.sessionIn(t, f.ada, orgA)
	bobB := f.sessionIn(t, f.bob, orgB)

	// Creating: the document belongs to the organization of the session.
	rec, out := f.doH(t, "POST", projects, `{"title": "One"}`, jsonHdr(adaA))
	if rec.Code != 201 || out["_meta"].(map[string]any)["org"] != orgA {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	for name, c := range map[string]struct {
		path, who string
		code      int
		errCode   string
	}{
		"?org= is not accepted":    {projects + "?org=" + orgA, adaA, 400, "validation_error"},
		"empty":                    {projects + "?org=", adaA, 400, "validation_error"},
		"no organizations":         {"/v1/acme/app/posts?org=" + orgA, adaA, 400, "org_not_allowed"},
		"a key, no user":           {projects, f.key, 400, "org_required"},
		"another one's org, asked": {projects + "?org=" + orgB, adaA, 400, "validation_error"},
	} {
		rec, out := f.fixture.doH(t, "POST", c.path, `{"title": "x"}`, jsonHdr(c.who))
		if rec.Code != c.code || errCode(out) != c.errCode {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// A person can't open a session in an organization they don't belong to: there is no way to
	// say "create in Alpha" as Bob.
	if _, _, err := f.svc.OpenOrgSession(ctx, mustPrincipal(t, f, f.bob), orgA); err == nil {
		t.Error("bob got a session in Alpha")
	}
	// Each session sees its organization's documents and nothing else, whatever it asks for.
	if rec, out := f.doH(t, "GET", projects, "", bearer(adaA)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("ada lists: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(bobB)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("bob lists: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", projects+"/"+id, "", bearer(bobB)); rec.Code != 404 {
		t.Errorf("bob reads it: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", projects+"?where="+`{"title":"Nope"}`, "", bearer(adaA)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("where: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", projects+"?count=true", "", bearer(adaA)); rec.Code != 200 || out["total"] != float64(1) {
		t.Errorf("count: %d %v", rec.Code, out)
	}
	if rec, out := f.fixture.doH(t, "GET", projects+"?org="+orgA, "", bearer(bobB)); rec.Code != 400 {
		t.Errorf("a list asking for the organization: %d %v", rec.Code, out)
	}
	// Roles decide the writes: an editor writes, a plain member only reads.
	if _, err := f.svc.AdminAddMember(ctx, orgA, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	bobA := f.sessionIn(t, f.bob, orgA)
	if rec, out := f.doH(t, "GET", projects+"/"+id, "", bearer(bobA)); rec.Code != 200 {
		t.Errorf("a new member reads it: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", projects+"/"+id, `{"title": "Edited"}`, merge(bobA)); rec.Code != 200 {
		t.Errorf("editor patches: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", projects+"/"+id, "", bearer(bobA)); rec.Code != 403 {
		t.Errorf("editor deletes: %d", rec.Code)
	}
	if _, err := f.svc.AdminSetMemberRoles(ctx, orgA, f.bobID, []string{}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "POST", projects, `{"title": "x"}`, jsonHdr(bobA)); rec.Code != 403 {
		t.Errorf("member creates: %d", rec.Code)
	}
	// The organization can't change through an update.
	for name, c := range map[string]struct{ method, body string }{
		"patch to another": {"PATCH", `{"_meta": {"org": "` + orgB + `"}}`},
		"patch to none":    {"PATCH", `{"_meta": {"org": null}}`},
		"put to another":   {"PUT", `{"title": "x", "_meta": {"org": "` + orgB + `"}}`},
	} {
		hdr := jsonHdr(adaA)
		if c.method == "PATCH" {
			hdr = merge(adaA)
		}
		rec, out := f.doH(t, c.method, projects+"/"+id, c.body, hdr)
		if rec.Code != 400 || errCode(out) != "immutable_field" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// Sending the document back as it came is fine, and keeps it in its organization.
	body := `{"title": "Back", "_meta": {"org": "` + orgA + `"}}`
	if rec, out := f.doH(t, "PUT", projects+"/"+id, body, jsonHdr(adaA)); rec.Code != 200 || out["_meta"].(map[string]any)["org"] != orgA {
		t.Errorf("put back: %d %v", rec.Code, out)
	}
	// Leaving ends access at once, and the session that left is ended.
	if _, err := f.svc.AdminAddMember(ctx, orgA, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(bobA)); len(out["items"].([]any)) != 1 {
		t.Fatalf("bob sees it: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", orgsPath+"/"+orgA+"/leave", "", bearer(bobA)); rec.Code != 204 {
		t.Fatalf("leave: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", projects, "", bearer(bobA)); rec.Code != 401 {
		t.Errorf("after leaving, the session of that organization: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("his other sessions are untouched: %d %v", rec.Code, out)
	}
}

func merge(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/merge-patch+json"}
}

func TestOrganizationsCountOnlyWhenActiveAndMFAMet(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	org := f.orgOf(t, f.ada, "Strict")
	adaS := f.sessionIn(t, f.ada, org)
	if rec, _ := f.doH(t, "POST", projects, `{"title": "x"}`, jsonHdr(adaS)); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	on := true
	if _, err := f.svc.Store.UpdateOrg(ctx, org, authOrgUpdateMFA(&on), f.svc.Clock()); err != nil {
		t.Fatal(err)
	}
	// A session without a second factor doesn't count as a member there.
	// (the read rule can't be true for anyone who is nowhere a member that counts: 403)
	if rec, out := f.doH(t, "GET", projects, "", bearer(adaS)); rec.Code != 403 {
		t.Errorf("list without MFA: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", projects, `{"title": "y"}`, jsonHdr(adaS)); rec.Code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("create without MFA: %d %v", rec.Code, out)
	}
}

func TestOrganizationClaimsInRules(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	plans := "/v1/acme/app/plans"
	org := f.orgOf(t, f.ada, "Plans")
	adaP := f.sessionIn(t, f.ada, org)
	if rec, _ := f.doH(t, "POST", plans, `{"title": "x"}`, jsonHdr(adaP)); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	list := func() int {
		rec, out := f.doH(t, "GET", plans, "", bearer(adaP))
		if rec.Code != 200 {
			return 0 // the plan doesn't qualify for anyone: the rule is false
		}
		return len(out["items"].([]any))
	}
	if n := list(); n != 0 {
		t.Errorf("free plan sees %d", n)
	}
	if _, err := f.svc.AdminSetClaims(ctx, org, map[string]any{"plan": "pro"}); err != nil {
		t.Fatal(err)
	}
	if n := list(); n != 1 {
		t.Errorf("pro plan sees %d", n)
	}
}

func TestOrganizationDocumentsAdminAndBatch(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	org := f.orgOf(t, f.ada, "Admin")
	// An administrator's data route may create in any active organization.
	rec, out := f.doH(t, "POST", "/v1/acme/_admin/data/app/projects?org="+org, `{"title": "by staff"}`, jsonHdr(f.bob))
	if rec.Code != 201 || out["_meta"].(map[string]any)["org"] != org {
		t.Errorf("admin create: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", "/v1/acme/_admin/data/app/projects?org=nosuch", `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 404 {
		t.Errorf("admin unknown org: %d", rec.Code)
	}
	// A batch works in the organization of the session.
	body := `{"operations": [{"op": "create", "collection": "projects", "document": {"title": "a"}}, {"op": "create", "collection": "projects", "document": {"title": "b"}}]}`
	adaO := f.sessionIn(t, f.ada, org)
	rec, out = f.doH(t, "POST", "/v1/acme/app/_batch", body, jsonHdr(adaO))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %v", rec.Code, out)
	}
	for _, res := range out["results"].([]any) {
		if res.(map[string]any)["_meta"].(map[string]any)["org"] != org {
			t.Errorf("batch result: %v", res)
		}
	}
	if rec, out := f.fixture.doH(t, "POST", "/v1/acme/app/_batch?org="+org, body, jsonHdr(adaO)); rec.Code != 400 {
		t.Errorf("batch asking for the organization: %d %v", rec.Code, out)
	}
	if !strings.Contains(rec.Body.String(), "org") {
		t.Error("body")
	}
}

func authOrgUpdateMFA(on *bool) auth.OrgUpdate { return auth.OrgUpdate{RequireMFA: on} }

// A key acting on behalf of a user is bound to one organization when it is made: it counts that
// one and no other; one that isn't bound counts none.
func TestKeysOnBehalfAreBoundToAnOrganization(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	orgA := f.orgOf(t, f.ada, "Alpha")
	orgB := f.orgOf(t, f.ada, "Beta")
	_, keyA, err := f.svc.CreateAPIKey(ctx, "alpha", auth.KeyOptions{Org: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	_, unbound, err := f.svc.CreateAPIKey(ctx, "unbound", auth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, org := range map[string]string{"nobody": "nosuch", "an id of nothing": "dbf8cvd1spovkllg0000"} {
		if _, _, err := f.svc.CreateAPIKey(ctx, "bad"+strings.ReplaceAll(name, " ", ""), auth.KeyOptions{Org: org}); err == nil {
			t.Errorf("a key bound to %s", name)
		}
	}
	if _, _, err := f.svc.CreateAPIKey(ctx, "admin-in-org", auth.KeyOptions{Role: auth.KeyRoleAdmin, Org: "alpha"}); err == nil {
		t.Error("an admin key bound to an organization")
	}
	onBehalf := func(key string) map[string]string {
		h := jsonHdr(key)
		h[onBehalfHeader] = f.adaID
		return h
	}
	rec, out := f.doH(t, "POST", projects, `{"title": "by key"}`, onBehalf(keyA))
	if rec.Code != 201 || out["_meta"].(map[string]any)["org"] != orgA {
		t.Fatalf("bound key creates: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", projects, "", onBehalf(keyA)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("bound key lists: %d %v", rec.Code, out)
	}
	// The same person in Beta sees none of it; the key can't reach Beta.
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.sessionIn(t, f.ada, orgB))); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("beta: %d %v", rec.Code, out)
	}
	if rec, out := f.fixture.doH(t, "GET", projects+"?org="+orgB, "", onBehalf(keyA)); rec.Code != 400 {
		t.Errorf("the key asks for another organization: %d %v", rec.Code, out)
	}
	// A key that isn't bound counts no organization.
	if rec, out := f.doH(t, "POST", projects, `{"title": "x"}`, onBehalf(unbound)); rec.Code == 201 {
		t.Errorf("an unbound key creates: %d %v", rec.Code, out)
	}
}
