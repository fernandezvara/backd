package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// hookEvents returns, oldest first, the inputs of the hook jobs of an origin.
func hookEvents(t *testing.T, f *rulesFixture, origin string) []map[string]any {
	t.Helper()
	jobs, _, err := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for i := len(jobs) - 1; i >= 0; i-- {
		var in map[string]any
		if err := json.Unmarshal(jobs[i].Input, &in); err != nil {
			t.Fatal(err)
		}
		out = append(out, in)
	}
	return out
}

func TestMembershipHooks(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	f.svc.Settings.Organizations.Hooks = registry.OrgHooks{
		OnMemberJoined: registry.OrgHook{Function: "app/cleanup"},
		OnMemberLeft:   registry.OrgHook{Function: "app/cleanup"},
		OnRolesChanged: registry.OrgHook{Function: "app/cleanup"},
	}
	org := f.orgOf(t, f.ada, "Hooks")
	teams := orgsPath + "/" + org + "/teams"

	// The creator joins, with the creator roles.
	joined := hookEvents(t, f, auth.OrgMemberJoinedOrigin)
	if len(joined) != 1 || joined[0]["user"] != f.adaID || joined[0]["org"] != org || joined[0]["seq"] != float64(1) {
		t.Fatalf("creator joined: %v", joined)
	}
	// An invitation accepted names the invitation, the roles and the teams.
	_, out := f.doH(t, "POST", teams, `{"name": "Sales", "roles": ["editor"]}`, jsonHdr(f.ada))
	sales := out["id"].(string)
	rec, out := f.doH(t, "POST", orgsPath+"/"+org+"/invitations", fmt.Sprintf(`{"binding": "user", "user": %q, "roles": ["member"], "teams": [%q]}`, f.bobID, sales), jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("invite: %d %v", rec.Code, out)
	}
	if rec, out := f.acceptInvite(t, org, f.bob, out["token"].(string)); rec.Code != 200 {
		t.Fatalf("accept: %d %v", rec.Code, out)
	}
	joined = hookEvents(t, f, auth.OrgMemberJoinedOrigin)
	if len(joined) != 2 || joined[1]["user"] != f.bobID || joined[1]["invitation"] == nil || joined[1]["seq"] != float64(1) {
		t.Fatalf("bob joined: %v", joined)
	}
	if teams := joined[1]["teams"].([]any); len(teams) != 1 || teams[0] != sales {
		t.Errorf("teams of the join: %v", joined[1]["teams"])
	}
	// Changes to roles, teams and team roles are events with before and after; nothing changed, no event.
	changes := func() []map[string]any { return hookEvents(t, f, auth.OrgRolesChangedOrigin) }
	if n := len(changes()); n != 0 {
		t.Fatalf("events before any change: %d", n)
	}
	if _, err := f.svc.AdminSetMemberRoles(ctx, org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AdminSetMemberRoles(ctx, org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	ev := changes()
	if len(ev) != 1 || ev[0]["seq"] != float64(2) {
		t.Fatalf("a role change (and a no-op): %v", ev)
	}
	if before := ev[0]["before"].(map[string]any)["roles"].([]any); len(before) != 1 || before[0] != "member" {
		t.Errorf("before: %v", before)
	}
	if after := ev[0]["after"].(map[string]any)["roles"].([]any); len(after) != 1 || after[0] != "editor" {
		t.Errorf("after: %v", after)
	}
	if rec, _ := f.doH(t, "POST", teams+"/"+sales+"/roles", fmt.Sprintf(`{"user": %q, "role": "manager", "until": "2099-01-01T00:00:00Z"}`, f.bobID), jsonHdr(f.ada)); rec.Code != 200 {
		t.Fatal("team role")
	}
	ev = changes()
	if len(ev) != 2 || len(ev[1]["after"].(map[string]any)["team_roles"].([]any)) != 1 || ev[1]["seq"] != float64(3) {
		t.Fatalf("a team role: %v", ev)
	}
	// A team that carries roles changes what its members hold: one event per member.
	if rec, out := f.doH(t, "PATCH", teams+"/"+sales, `{"roles": ["admin"]}`, jsonHdr(f.ada)); rec.Code != 200 {
		t.Fatalf("team roles: %d %v", rec.Code, out)
	}
	ev = changes()
	last := ev[len(ev)-1]
	if len(ev) != 3 || last["user"] != f.bobID || fmt.Sprint(last["before"].(map[string]any)["effective_roles"]) == fmt.Sprint(last["after"].(map[string]any)["effective_roles"]) {
		t.Fatalf("team roles change: %v", ev)
	}
	// Leaving, removal and erasure are on_member_left with their reasons; joining again goes on counting.
	if rec, _ := f.doH(t, "POST", orgsPath+"/"+org+"/leave", "", bearer(f.bob)); rec.Code != 204 {
		t.Fatal("leave")
	}
	left := hookEvents(t, f, auth.OrgMemberLeftOrigin)
	if len(left) != 1 || left[0]["reason"] != "left" || left[0]["user"] != f.bobID || left[0]["seq"] != float64(5) {
		t.Fatalf("left: %v", left)
	}
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AdminRemoveMember(ctx, org, f.bobID); err != nil {
		t.Fatal(err)
	}
	left = hookEvents(t, f, auth.OrgMemberLeftOrigin)
	if len(left) != 2 || left[1]["reason"] != "removed" || left[1]["seq"] != float64(7) {
		t.Fatalf("removed: %v", left)
	}
	// Erasing a user is a departure too, with its own reason.
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Erase(ctx, f.bobID, "req"); err != nil {
		t.Fatal(err)
	}
	// Bob's own team goes with him too: two departures, one of them from this organization.
	left = hookEvents(t, f, auth.OrgMemberLeftOrigin)
	var erased int
	for _, e := range left[2:] {
		if e["reason"] == "erased" && e["org"] == org {
			erased++
		}
	}
	if len(left) != 4 || erased != 1 {
		t.Fatalf("erased: %v", left)
	}
	// Without the hooks nothing is queued.
	f.svc.Settings.Organizations.Hooks = registry.OrgHooks{}
	before := len(hookEvents(t, f, auth.OrgMemberJoinedOrigin))
	if _, err := f.svc.AdminAddMember(ctx, org, f.adaID, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(hookEvents(t, f, auth.OrgMemberJoinedOrigin)); n != before {
		t.Errorf("an unset hook queued a job: %d", n)
	}
}
