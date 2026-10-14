package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func teamGrant(team, role string) auth.TeamRoleGrant {
	return auth.TeamRoleGrant{Team: team, RoleGrant: auth.RoleGrant{Role: role}}
}

func TestTeamRoles(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	org := f.orgOf(t, f.ada, "Departments")
	teams := orgsPath + "/" + org + "/teams"
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	rec, out := f.doH(t, "POST", teams, `{"name": "Sales"}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("team: %d %v", rec.Code, out)
	}
	sales := out["id"].(string)
	roles := teams + "/" + sales + "/roles"
	members := teams + "/" + sales + "/members"

	// A plain member can't grant, and can't see... the list is for members of the organization.
	if rec, out := f.doH(t, "POST", roles, fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID), jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("member grants: %d %v", rec.Code, out)
	}
	// An owner (manage: teams) grants a role to someone outside the team.
	rec, out = f.doH(t, "POST", roles, fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID), jsonHdr(f.ada))
	if rec.Code != 200 {
		t.Fatalf("grant: %d %v", rec.Code, out)
	}
	if out["in_team"] != false || fmt.Sprint(out["roles_in_team"]) != "[manager]" {
		t.Errorf("grant answer: %v", out)
	}
	// Again: nothing changes.
	if rec, _ := f.doH(t, "POST", roles, fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID), jsonHdr(f.ada)); rec.Code != 200 {
		t.Errorf("grant twice: %d", rec.Code)
	}
	// Roles must be team roles of the realm, and the user a member.
	if rec, _ := f.doH(t, "POST", roles, fmt.Sprintf(`{"user": %q, "role": "editor"}`, f.bobID), jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("an organization role as a team role: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", roles, `{"user": "nobody", "role": "manager"}`, jsonHdr(f.ada)); rec.Code != 403 && rec.Code != 404 {
		t.Errorf("a stranger: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", teams+"/nope/roles", fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID), jsonHdr(f.ada)); rec.Code != 404 {
		t.Errorf("unknown team: %d", rec.Code)
	}

	// The listing: the holder appears without being a member; ?role= narrows to holders.
	rec, out = f.doH(t, "GET", members+"?role=manager", "", bearer(f.bob))
	items, _ := out["items"].([]any)
	if rec.Code != 200 || len(items) != 1 || items[0].(map[string]any)["user"] != f.bobID {
		t.Fatalf("holders: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "GET", members+"?role=wizard", "", bearer(f.bob)); rec.Code != 400 {
		t.Errorf("unknown role filter: %d %v", rec.Code, out)
	}
	// ada joins the team: unfiltered shows both, the filtered one still shows bob only.
	f.doH(t, "PUT", members+"/"+f.adaID, "", bearer(f.ada))
	_, out = f.doH(t, "GET", members, "", bearer(f.bob))
	if n := len(out["items"].([]any)); n != 2 {
		t.Errorf("members and holders: %v", out)
	}
	_, out = f.doH(t, "GET", members+"?role=manager", "", bearer(f.ada))
	if n := len(out["items"].([]any)); n != 1 {
		t.Errorf("holders of manager: %v", out)
	}
	// A stranger to the organization learns nothing.
	if rec, _ := f.doH(t, "GET", members, "", bearer(f.carl)); rec.Code != 403 {
		t.Errorf("outsider: %d", rec.Code)
	}
	// What the member sees of themselves.
	_, out = f.doH(t, "GET", orgsPath+"/"+org, "", bearer(f.bob))
	if fmt.Sprint(out["team_roles"]) != "map["+sales+":[manager]]" {
		t.Errorf("own team roles: %v", out["team_roles"])
	}

	// Revoking, then the cap, then deleting the team takes the grants with it.
	if rec, out := f.doH(t, "DELETE", roles, fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID), jsonHdr(f.ada)); rec.Code != 200 || fmt.Sprint(out["roles_in_team"]) != "[]" {
		t.Errorf("revoke: %d %v", rec.Code, out)
	}
	f.doH(t, "POST", roles, fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.bobID), jsonHdr(f.ada))
	if rec, _ := f.doH(t, "DELETE", teams+"/"+sales, "", bearer(f.ada)); rec.Code != 204 {
		t.Fatalf("delete team: %d", rec.Code)
	}
	m, err := f.svc.Store.OrgMember(ctx, org, f.bobID)
	if err != nil || len(m.TeamRoles) != 0 {
		t.Errorf("grants of a deleted team: %v %v", m.TeamRoles, err)
	}
}

func TestTeamRolesAdminAndCap(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	org := f.orgOf(t, f.ada, "Capped")
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	f.bobHolds(t, "staff")
	adm := "/v1/acme/_admin/orgs/" + org + "/teams"
	rec, out := f.doH(t, "POST", adm, `{"name": "Ops"}`, jsonHdr(f.bob))
	if rec.Code != 201 {
		t.Fatalf("team: %d %v", rec.Code, out)
	}
	ops := out["id"].(string)
	body := fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.adaID)
	if rec, out := f.doH(t, "POST", adm+"/"+ops+"/roles", body, jsonHdr(f.bob)); rec.Code != 200 {
		t.Fatalf("admin grant: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "GET", adm+"/"+ops+"/members?role=delegate", "", bearer(f.bob))
	if rec.Code != 200 || len(out["items"].([]any)) != 1 {
		t.Errorf("admin list: %d %v", rec.Code, out)
	}
	// The cap of grants per member.
	m, _ := f.svc.Store.OrgMember(ctx, org, f.bobID)
	for i := 0; i < 49; i++ {
		m.TeamRoles = append(m.TeamRoles, teamGrant(fmt.Sprintf("t%d", i), "delegate"))
	}
	if err := f.svc.Store.PutOrgMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	body = fmt.Sprintf(`{"user": %q, "role": "delegate"}`, f.bobID)
	if rec, _ := f.doH(t, "POST", adm+"/"+ops+"/roles", body, jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("the 50th: %d", rec.Code)
	}
	body = fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.bobID)
	if rec, out := f.doH(t, "POST", adm+"/"+ops+"/roles", body, jsonHdr(f.bob)); rec.Code != 409 || errCode(out) != "team_limit_reached" {
		t.Errorf("the 51st: %d %v", rec.Code, out)
	}
}

func TestTeamRoleWindowsAndDelegation(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	f.svc.Now = func() time.Time { return *f.clock }
	org := f.orgOf(t, f.ada, "Windows")
	teams := orgsPath + "/" + org + "/teams"
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) string {
		t.Helper()
		rec, out := f.doH(t, "POST", teams, fmt.Sprintf(`{"name": %q}`, name), jsonHdr(f.ada))
		if rec.Code != 201 {
			t.Fatalf("team %s: %d %v", name, rec.Code, out)
		}
		return out["id"].(string)
	}
	sales, support := mk("Sales"), mk("Support")
	at := func(d time.Duration) string { return f.clock.Add(d).UTC().Format(time.RFC3339) }
	body := func(user, role string, from, until string) string {
		b := fmt.Sprintf(`{"user": %q, "role": %q`, user, role)
		if from != "" {
			b += fmt.Sprintf(`, "from": %q`, from)
		}
		if until != "" {
			b += fmt.Sprintf(`, "until": %q`, until)
		}
		return b + "}"
	}
	salesRoles := teams + "/" + sales + "/roles"

	// A window must end after it starts.
	if rec, out := f.doH(t, "POST", salesRoles, body(f.bobID, "manager", at(time.Hour), at(time.Hour)), jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("empty window: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", salesRoles, body(f.bobID, "manager", "tomorrow", ""), jsonHdr(f.ada)); rec.Code != 400 {
		t.Errorf("not a time: %d", rec.Code)
	}
	// Bob becomes manager of Sales in an hour, for an hour.
	rec, out := f.doH(t, "POST", salesRoles, body(f.bobID, "manager", at(time.Hour), at(2*time.Hour)), jsonHdr(f.ada))
	if rec.Code != 200 || fmt.Sprint(out["roles_in_team"]) != "[]" {
		t.Fatalf("grant with a window: %d %v", rec.Code, out)
	}
	// Not yet: he can't delegate, and he doesn't count as holder.
	if rec, _ := f.doH(t, "POST", salesRoles, body(f.adaID, "delegate", "", ""), jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("before the window: %d", rec.Code)
	}
	_, out = f.doH(t, "GET", teams+"/"+sales+"/members?role=manager", "", bearer(f.bob))
	if n := len(out["items"].([]any)); n != 0 {
		t.Errorf("holders before the window: %v", out)
	}

	// Inside the window he delegates, but only `delegate`, and only in Sales.
	*f.clock = f.clock.Add(90 * time.Minute)
	if rec, out := f.doH(t, "POST", salesRoles, body(f.adaID, "delegate", "", at(30*time.Minute)), jsonHdr(f.bob)); rec.Code != 200 || fmt.Sprint(out["roles_in_team"]) != "[delegate]" {
		t.Fatalf("delegate: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", salesRoles, body(f.adaID, "manager", "", ""), jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("a role not listed: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", teams+"/"+support+"/roles", body(f.adaID, "delegate", "", ""), jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("another team: %d", rec.Code)
	}
	// He changes the window, then revokes; patching what isn't held is a 404.
	if rec, out := f.doH(t, "PATCH", salesRoles, body(f.adaID, "delegate", "", at(10*time.Minute)), jsonHdr(f.bob)); rec.Code != 200 {
		t.Errorf("patch: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "PATCH", salesRoles, body(f.adaID, "manager", "", ""), jsonHdr(f.ada)); rec.Code != 404 {
		t.Errorf("patch what isn't held: %d", rec.Code)
	}
	// Ada holds the grant until the window ends, to the second.
	*f.clock = f.clock.Add(10*time.Minute - time.Second)
	_, out = f.doH(t, "GET", teams+"/"+sales+"/members?role=delegate", "", bearer(f.ada))
	if n := len(out["items"].([]any)); n != 1 {
		t.Errorf("one second before: %v", out)
	}
	*f.clock = f.clock.Add(time.Second)
	_, out = f.doH(t, "GET", teams+"/"+sales+"/members?role=delegate", "", bearer(f.ada))
	if n := len(out["items"].([]any)); n != 0 {
		t.Errorf("at the end: %v", out)
	}
	// The history keeps it, marked as not active.
	_, out = f.doH(t, "GET", orgsPath+"/"+org+"/members", "", bearer(f.ada))
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["user"] != f.adaID {
			continue
		}
		grants := m["team_roles"].([]any)
		if len(grants) != 1 || grants[0].(map[string]any)["active"] != false || grants[0].(map[string]any)["granted_by"] != f.bobID {
			t.Errorf("history: %v", grants)
		}
	}
	// Bob's own window closes: he can't delegate any more.
	*f.clock = f.clock.Add(time.Hour)
	if rec, _ := f.doH(t, "POST", salesRoles, body(f.adaID, "delegate", "", ""), jsonHdr(f.bob)); rec.Code != 403 {
		t.Errorf("after the window: %d", rec.Code)
	}
	// Grants that ended more than 90 days ago are cleaned up; recent ones stay.
	if n, err := f.svc.SweepTeamRoles(ctx); err != nil || n != 0 {
		t.Fatalf("sweep too early: %d %v", n, err)
	}
	*f.clock = f.clock.Add(auth.TeamRoleHistory)
	if n, err := f.svc.SweepTeamRoles(ctx); err != nil || n != 2 {
		t.Errorf("sweep: %d %v", n, err)
	}
}
