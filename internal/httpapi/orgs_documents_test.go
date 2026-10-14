package httpapi

import (
	"context"
	"github.com/fernandezvara/backd/internal/auth"
	"net/http"
	"strings"
	"testing"
)

// Documents in organizations (design/organizations.md §6).
func TestDocumentsInOrganizations(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	orgA := f.orgOf(t, f.ada, "Alpha")
	orgB := f.orgOf(t, f.bob, "Beta")

	// Creating: the organization is a parameter, and the caller must belong to it.
	rec, out := f.doH(t, "POST", projects+"?org="+orgA, `{"title": "One"}`, jsonHdr(f.ada))
	if rec.Code != 201 || out["_meta"].(map[string]any)["org"] != orgA {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	for name, c := range map[string]struct {
		path, who string
		code      int
		errCode   string
	}{
		"no organization":   {projects, f.ada, 400, "org_required"},
		"empty":             {projects + "?org=", f.ada, 400, "org_required"},
		"not a member":      {projects + "?org=" + orgA, f.bob, 403, "not_org_member"},
		"unknown":           {projects + "?org=nosuchorg", f.ada, 403, "not_org_member"},
		"no organizations":  {"/v1/acme/app/posts?org=" + orgA, f.ada, 400, "org_not_allowed"},
		"a key, no user":    {projects + "?org=" + orgA, f.key, 403, "not_org_member"},
		"another one's org": {projects + "?org=" + orgB, f.ada, 403, "not_org_member"},
	} {
		rec, out := f.doH(t, "POST", c.path, `{"title": "x"}`, jsonHdr(c.who))
		if rec.Code != c.code || errCode(out) != c.errCode {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// Members see the organization's documents; others don't, whatever they ask for.
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.ada)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("ada lists: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("bob lists: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", projects+"/"+id, "", bearer(f.bob)); rec.Code != 404 {
		t.Errorf("bob reads it: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", projects+"?org="+orgA, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("bob asks for it: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", projects+"?org="+orgA+"&where="+`{"title":"Nope"}`, "", bearer(f.ada)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("shorthand with where: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", projects+"?org="+orgA+"&count=true", "", bearer(f.ada)); rec.Code != 200 || out["total"] != float64(1) {
		t.Errorf("count: %d %v", rec.Code, out)
	}
	// Roles decide the writes: an editor writes, a plain member only reads.
	if _, err := f.svc.AdminAddMember(ctx, orgA, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "GET", projects+"/"+id, "", bearer(f.bob)); rec.Code != 200 {
		t.Errorf("a new member reads it: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PATCH", projects+"/"+id, `{"title": "Edited"}`, merge(f.bob)); rec.Code != 200 {
		t.Errorf("editor patches: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", projects+"/"+id, "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("editor deletes: %d", rec.Code)
	}
	if _, err := f.svc.AdminSetMemberRoles(ctx, orgA, f.bobID, []string{}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.doH(t, "POST", projects+"?org="+orgA, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("member creates: %d", rec.Code)
	}
	// The organization can't change through an update.
	for name, c := range map[string]struct{ method, body string }{
		"patch to another": {"PATCH", `{"_meta": {"org": "` + orgB + `"}}`},
		"patch to none":    {"PATCH", `{"_meta": {"org": null}}`},
		"put to another":   {"PUT", `{"title": "x", "_meta": {"org": "` + orgB + `"}}`},
	} {
		hdr := jsonHdr(f.ada)
		if c.method == "PATCH" {
			hdr = merge(f.ada)
		}
		rec, out := f.doH(t, c.method, projects+"/"+id, c.body, hdr)
		if rec.Code != 400 || errCode(out) != "immutable_field" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// Sending the document back as it came is fine, and keeps it in its organization.
	body := `{"title": "Back", "_meta": {"org": "` + orgA + `"}}`
	if rec, out := f.doH(t, "PUT", projects+"/"+id, body, jsonHdr(f.ada)); rec.Code != 200 || out["_meta"].(map[string]any)["org"] != orgA {
		t.Errorf("put back: %d %v", rec.Code, out)
	}
	// Leaving ends access at once.
	if _, err := f.svc.AdminAddMember(ctx, orgA, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.bob)); len(out["items"].([]any)) != 1 {
		t.Fatalf("bob sees it: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", orgsPath+"/"+orgA+"/leave", "", bearer(f.bob)); rec.Code != 204 {
		t.Fatalf("leave: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 0 {
		t.Errorf("after leaving: %d %v", rec.Code, out)
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
	if rec, _ := f.doH(t, "POST", projects+"?org="+org, `{"title": "x"}`, jsonHdr(f.ada)); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	on := true
	if _, err := f.svc.Store.UpdateOrg(ctx, org, authOrgUpdateMFA(&on), f.svc.Clock()); err != nil {
		t.Fatal(err)
	}
	// A session without a second factor doesn't count as a member there.
	// (the read rule can't be true for anyone who is nowhere a member that counts: 403)
	if rec, out := f.doH(t, "GET", projects, "", bearer(f.ada)); rec.Code != 403 {
		t.Errorf("list without MFA: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", projects+"?org="+org, `{"title": "y"}`, jsonHdr(f.ada)); rec.Code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("create without MFA: %d %v", rec.Code, out)
	}
}

func TestOrganizationClaimsInRules(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	plans := "/v1/acme/app/plans"
	org := f.orgOf(t, f.ada, "Plans")
	if rec, _ := f.doH(t, "POST", plans+"?org="+org, `{"title": "x"}`, jsonHdr(f.ada)); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	list := func() int {
		rec, out := f.doH(t, "GET", plans, "", bearer(f.ada))
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
	// A batch takes the organization from the request.
	body := `{"operations": [{"op": "create", "collection": "projects", "document": {"title": "a"}}, {"op": "create", "collection": "projects", "document": {"title": "b"}}]}`
	rec, out = f.doH(t, "POST", "/v1/acme/app/_batch?org="+org, body, jsonHdr(f.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %v", rec.Code, out)
	}
	for _, res := range out["results"].([]any) {
		if res.(map[string]any)["_meta"].(map[string]any)["org"] != org {
			t.Errorf("batch result: %v", res)
		}
	}
	if rec, out := f.doH(t, "POST", "/v1/acme/app/_batch", body, jsonHdr(f.ada)); rec.Code != 400 || errCode(out) != "org_required" {
		t.Errorf("batch without org: %d %v", rec.Code, out)
	}
	if !strings.Contains(rec.Body.String(), "org") {
		t.Error("body")
	}
}

func authOrgUpdateMFA(on *bool) auth.OrgUpdate { return auth.OrgUpdate{RequireMFA: on} }
