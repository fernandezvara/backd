package httpapi

import (
	"context"
	"testing"
)

func TestOrganizationTeams(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	projects := "/v1/acme/app/projects"
	org := f.orgOf(t, f.ada, "Teamwork")
	teams := orgsPath + "/" + org + "/teams"
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	// An owner creates a team that carries the editor role.
	rec, out := f.doH(t, "POST", teams, `{"name": "Editors", "roles": ["editor"]}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	team := out["id"].(string)
	// Names are unique per organization; roles must exist; members can't create.
	if rec, out := f.doH(t, "POST", teams, `{"name": "Editors"}`, jsonHdr(f.ada)); rec.Code != 409 || errCode(out) != "team_name_taken" {
		t.Errorf("duplicate: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", teams, `{"name": "Odd", "roles": ["wizard"]}`, jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("unknown role: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", teams, `{"name": "Sneaky"}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("a member creates: %d", rec.Code)
	}
	if rec, out := f.doH(t, "GET", teams, "", bearer(f.bob)); rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("a member lists: %d %v", rec.Code, out)
	}
	// Bob can't write projects yet; once in the team he can, and the list says why.
	if rec, _ := f.doH(t, "POST", projects+"?org="+org, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Fatalf("before the team: %d", rec.Code)
	}
	member := teams + "/" + team + "/members/" + f.bobID
	rec, out = f.doH(t, "PUT", member, "", bearer(f.ada))
	if rec.Code != 200 {
		t.Fatalf("add to the team: %d %v", rec.Code, out)
	}
	found := false
	for _, e := range out["effective_roles"].([]any) {
		m := e.(map[string]any)
		if m["role"] == "editor" {
			found = len(m["origin"].([]any)) == 1 && m["origin"].([]any)[0] == "Editors"
		}
	}
	if !found {
		t.Errorf("effective roles: %v", out["effective_roles"])
	}
	if rec, out := f.doH(t, "POST", projects+"?org="+org, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 201 {
		t.Errorf("after the team: %d %v", rec.Code, out)
	}
	// A role held both ways is marked.
	if _, err := f.svc.AdminSetMemberRoles(ctx, org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	_, out = f.doH(t, "GET", orgsPath+"/"+org+"/members", "", bearer(f.ada))
	var overlap any
	for _, it := range out["items"].([]any) {
		if m := it.(map[string]any); m["user"] == f.bobID {
			overlap = m["overlap"]
		}
	}
	if o, _ := overlap.([]any); len(o) != 1 || o[0] != "editor" {
		t.Errorf("overlap = %v", overlap)
	}
	// Taking him out of the team ends the team's roles at once, and so does deleting it.
	if rec, _ := f.doH(t, "DELETE", member, "", bearer(f.ada)); rec.Code != 200 {
		t.Errorf("remove: %d", rec.Code)
	}
	if _, err := f.svc.AdminSetMemberRoles(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	f.doH(t, "PUT", member, "", bearer(f.ada))
	if rec, _ := f.doH(t, "POST", projects+"?org="+org, `{"title": "y"}`, jsonHdr(f.bob)); rec.Code != 201 {
		t.Fatalf("back in the team: %d", rec.Code)
	}
	rec, out = f.doH(t, "PATCH", teams+"/"+team, `{"roles": []}`, jsonHdr(f.ada))
	if rec.Code != 200 || len(out["roles"].([]any)) != 0 {
		t.Errorf("patch: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", projects+"?org="+org, `{"title": "z"}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("after emptying the team: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", teams+"/"+team, "", bearer(f.ada)); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", teams+"/"+team, "", bearer(f.ada)); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
	_, out = f.doH(t, "GET", orgsPath+"/"+org+"/members", "", bearer(f.ada))
	for _, it := range out["items"].([]any) {
		if m := it.(map[string]any); m["user"] == f.bobID && len(m["teams"].([]any)) != 0 {
			t.Errorf("bob is still in the deleted team: %v", m)
		}
	}
}

func TestOrganizationTeamsGrantingAndManagers(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	org := f.orgOf(t, f.ada, "Rules")
	teams := orgsPath + "/" + org + "/teams"
	// bob is an admin: manage: members and teams, but he is not an owner.
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, []string{"admin"}); err != nil {
		t.Fatal(err)
	}
	// He may create a team of editors, but not one of owners.
	if rec, out := f.doH(t, "POST", teams, `{"name": "Editors", "roles": ["editor"]}`, jsonHdr(f.bob)); rec.Code != 201 {
		t.Errorf("editors: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", teams, `{"name": "Bosses", "roles": ["owner"]}`, jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("owners: %d %v", rec.Code, out)
	}
	// An owner makes the owners' team; bob can't put himself in it.
	rec, out := f.doH(t, "POST", teams, `{"name": "Bosses", "roles": ["owner"]}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("owner team: %d %v", rec.Code, out)
	}
	bosses := out["id"].(string)
	if rec, _ := f.doH(t, "PUT", teams+"/"+bosses+"/members/"+f.bobID, "", bearer(f.bob)); rec.Code != 403 {
		t.Errorf("bob joins the owners: %d", rec.Code)
	}
	// The last manager can't be taken out of the only owners' team they hold the role through.
	if _, err := f.svc.AdminSetMemberRoles(ctx, org, f.adaID, nil); err == nil {
		t.Fatal("the only owner lost the role")
	}
	f.doH(t, "PUT", teams+"/"+bosses+"/members/"+f.bobID, "", bearer(f.ada))
	if _, err := f.svc.AdminSetMemberRoles(ctx, org, f.adaID, nil); err != nil {
		t.Errorf("another owner exists through the team: %v", err)
	}
	// Now bob is the only manager, through the team: removing him or deleting it is refused.
	if rec, out := f.doH(t, "DELETE", teams+"/"+bosses+"/members/"+f.bobID, "", bearer(f.ada)); rec.Code != 403 && rec.Code != 409 {
		t.Errorf("remove the last manager from the team: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "DELETE", teams+"/"+bosses, "", bearer(f.bob)); rec.Code != 403 && rec.Code != 409 {
		t.Errorf("delete the owners' team: %d %v", rec.Code, out)
	}
	adm := "/v1/acme/_admin/orgs/" + org + "/teams"
	f.bobHolds(t, "staff")
	if rec, out := f.doH(t, "DELETE", adm+"/"+bosses, "", bearer(f.bob)); rec.Code != 409 || errCode(out) != "last_org_manager" {
		t.Errorf("admin deletes the last managers' team: %d %v", rec.Code, out)
	}
	// The caps.
	f.svc.Settings.Organizations.MaxTeamsPerOrg = 2
	if rec, out := f.doH(t, "POST", adm, `{"name": "Third"}`, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "team_limit_reached" {
		t.Errorf("cap: %d %v", rec.Code, out)
	}
}
