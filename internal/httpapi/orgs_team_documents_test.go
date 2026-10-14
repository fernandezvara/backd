package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// patchHdr is a PATCH of a document made by the session.
func patchHdr(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/merge-patch+json"}
}

func TestTeamScopedDocuments(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	ctx := context.Background()
	requests := "/v1/acme/app/requests"
	org := f.orgOf(t, f.ada, "Departments")
	other := f.orgOf(t, f.ada, "Elsewhere")
	if _, err := f.svc.AdminAddMember(ctx, org, f.bobID, []string{"editor"}); err != nil {
		t.Fatal(err)
	}
	team := func(orgID, name string) string {
		t.Helper()
		rec, out := f.doH(t, "POST", orgsPath+"/"+orgID+"/teams", fmt.Sprintf(`{"name": %q}`, name), jsonHdr(f.ada))
		if rec.Code != 201 {
			t.Fatalf("team %s: %d %v", name, rec.Code, out)
		}
		return out["id"].(string)
	}
	sales, support, foreign := team(org, "Sales"), team(org, "Support"), team(other, "Sales")
	if rec, _ := f.doH(t, "PUT", orgsPath+"/"+org+"/teams/"+sales+"/members/"+f.bobID, "", bearer(f.ada)); rec.Code != 200 {
		t.Fatal("bob joins sales")
	}

	// Creating: the team is named, and the caller belongs to it (or holds a role in it).
	if rec, out := f.doH(t, "POST", requests+"?org="+org, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 400 || errCode(out) != "team_required" {
		t.Errorf("no team: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "POST", requests+"?team="+sales, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 400 {
		t.Errorf("?team= without ?org=: %d %v", rec.Code, out)
	}
	for name, team := range map[string]string{"a team bob isn't in": support, "an unknown team": "nosuch", "a team of another organization": foreign} {
		if rec, out := f.doH(t, "POST", requests+"?org="+org+"&team="+team, `{"title": "x"}`, jsonHdr(f.bob)); rec.Code != 403 || errCode(out) != "not_team_member" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	rec, out := f.doH(t, "POST", requests+"?org="+org+"&team="+sales, `{"title": "leave"}`, jsonHdr(f.bob))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	req := out["id"].(string)
	if meta := out["_meta"].(map[string]any); meta["team"] != sales || meta["org"] != org {
		t.Errorf("meta: %v", meta)
	}
	// A collection without teams refuses ?team=.
	if rec, out := f.doH(t, "POST", "/v1/acme/app/projects?org="+org+"&team="+sales, `{"title": "x"}`, jsonHdr(f.ada)); rec.Code != 400 || errCode(out) != "team_not_allowed" {
		t.Errorf("a collection without teams: %d %v", rec.Code, out)
	}

	// Reading: team members and managers of the team; an owner through the organization rule.
	ids := func(session string) []string {
		t.Helper()
		rec, out := f.doH(t, "GET", requests+"?org="+org, "", bearer(session))
		if rec.Code != 200 {
			t.Fatalf("list: %d %v", rec.Code, out)
		}
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["id"].(string))
		}
		return got
	}
	if got := ids(f.bob); len(got) != 1 || got[0] != req {
		t.Errorf("a member of the team: %v", got)
	}
	if got := ids(f.ada); len(got) != 1 {
		t.Errorf("an owner: %v", got)
	}
	// Somebody outside the organization sees nothing.
	if rec, _ := f.doH(t, "GET", requests+"?org="+org, "", bearer(f.carl)); rec.Code != 403 {
		t.Errorf("a stranger: %d", rec.Code)
	}

	// A team role is checked inside the query, within its window: bob manages Support from an
	// hour from now, without belonging to it.
	roles := orgsPath + "/" + org + "/teams/"
	if rec, _ := f.doH(t, "PUT", roles+support+"/members/"+f.adaID, "", bearer(f.ada)); rec.Code != 200 {
		t.Fatal("ada joins support")
	}
	rec, out = f.doH(t, "POST", requests+"?org="+org+"&team="+support, `{"title": "support request"}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("create in support: %d %v", rec.Code, out)
	}
	supportReq := out["id"].(string)
	from := f.clock.Add(time.Hour).UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"user": %q, "role": "manager", "from": %q}`, f.bobID, from)
	if rec, out := f.doH(t, "POST", roles+support+"/roles", body, jsonHdr(f.ada)); rec.Code != 200 {
		t.Fatalf("grant: %d %v", rec.Code, out)
	}
	if got := ids(f.bob); len(got) != 1 {
		t.Errorf("before the window: %v", got)
	}
	if rec, _ := f.doH(t, "PATCH", requests+"/"+supportReq, `{"title": "edited"}`, patchHdr(f.bob)); rec.Code != 404 && rec.Code != 403 {
		t.Errorf("update before the window: %d", rec.Code)
	}
	*f.clock = f.clock.Add(time.Hour)
	if got := ids(f.bob); len(got) != 2 {
		t.Errorf("inside the window: %v", got)
	}
	if rec, out := f.doH(t, "PATCH", requests+"/"+supportReq, `{"title": "edited"}`, patchHdr(f.bob)); rec.Code != 200 {
		t.Errorf("update as its manager: %d %v", rec.Code, out)
	}
	// ...and not in Sales, where he is a plain member.
	if rec, _ := f.doH(t, "PATCH", requests+"/"+req, `{"title": "edited"}`, patchHdr(f.bob)); rec.Code != 403 {
		t.Errorf("update as a plain member: %d", rec.Code)
	}
}

func TestTeamDocumentsAreImmutableAndMovedByAdministrators(t *testing.T) {
	f := newRulesFixture(t)
	f.bobHolds(t, "staff")
	memos := "/v1/acme/app/memos"
	org := f.orgOf(t, f.ada, "Moves")
	_, out := f.doH(t, "POST", orgsPath+"/"+org+"/teams", `{"name": "A"}`, jsonHdr(f.ada))
	a := out["id"].(string)
	_, out = f.doH(t, "POST", orgsPath+"/"+org+"/teams", `{"name": "B"}`, jsonHdr(f.ada))
	b := out["id"].(string)
	f.doH(t, "PUT", orgsPath+"/"+org+"/teams/"+a+"/members/"+f.adaID, "", bearer(f.ada))
	rec, out := f.doH(t, "POST", memos+"?org="+org+"&team="+a, `{"title": "m"}`, jsonHdr(f.ada))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	// PUT and PATCH can't move it.
	if rec, out := f.doH(t, "PATCH", memos+"/"+id, fmt.Sprintf(`{"_meta": {"team": %q}}`, b), patchHdr(f.ada)); rec.Code != 400 || errCode(out) != "immutable_field" {
		t.Errorf("patch: %d %v", rec.Code, out)
	}
	if rec, out := f.doH(t, "PUT", memos+"/"+id, fmt.Sprintf(`{"title": "m", "_meta": {"team": %q}}`, b), jsonHdr(f.ada)); rec.Code != 400 || errCode(out) != "immutable_field" {
		t.Errorf("put: %d %v", rec.Code, out)
	}
	// An administrator moves it with the transfer, naming the team.
	adm := "/v1/acme/_admin/data/app/memos/" + id + "/_transfer"
	if rec, out := f.doH(t, "POST", adm, fmt.Sprintf(`{"org": %q, "team": %q}`, org, b), jsonHdr(f.bob)); rec.Code != 200 || out["_meta"].(map[string]any)["team"] != b {
		t.Fatalf("transfer: %d %v", rec.Code, out)
	}
	// An unknown team is a 404, and null takes the document out of any team.
	if rec, _ := f.doH(t, "POST", adm, fmt.Sprintf(`{"org": %q, "team": "nosuch"}`, org), jsonHdr(f.bob)); rec.Code != 404 {
		t.Errorf("unknown team: %d", rec.Code)
	}
	if rec, out := f.doH(t, "POST", adm, fmt.Sprintf(`{"org": %q, "team": null}`, org), jsonHdr(f.bob)); rec.Code != 200 || out["_meta"].(map[string]any)["team"] != nil {
		t.Errorf("no team: %d %v", rec.Code, out)
	}
	// Moving to another organization leaves the team.
	other := f.orgOf(t, f.ada, "Other")
	if rec, _ := f.doH(t, "POST", adm, fmt.Sprintf(`{"org": %q, "team": %q}`, org, a), jsonHdr(f.bob)); rec.Code != 200 {
		t.Fatalf("back in a team: %d", rec.Code)
	}
	rec, out = f.doH(t, "POST", adm, fmt.Sprintf(`{"org": %q}`, other), jsonHdr(f.bob))
	if rec.Code != 200 || out["_meta"].(map[string]any)["team"] != nil || out["_meta"].(map[string]any)["org"] != other {
		t.Errorf("another organization: %d %v", rec.Code, out)
	}
	// The collection that requires a team can't lose it.
	reqs := "/v1/acme/_admin/data/app/requests/nosuch/_transfer"
	if rec, out := f.doH(t, "POST", reqs, fmt.Sprintf(`{"org": %q, "team": null}`, org), jsonHdr(f.bob)); rec.Code != 400 || errCode(out) != "team_required" {
		t.Errorf("required team: %d %v", rec.Code, out)
	}
}

func TestDeletingATeamAppliesOnTeamDelete(t *testing.T) {
	f := newRulesFixture(t)
	w := newTestWorker(t, f)
	w.reg = f.reg
	ctx := context.Background()
	requests, memos := "/v1/acme/app/requests", "/v1/acme/app/memos"
	org := f.orgOf(t, f.ada, "Cleanup")
	mk := func(name string) string {
		_, out := f.doH(t, "POST", orgsPath+"/"+org+"/teams", fmt.Sprintf(`{"name": %q}`, name), jsonHdr(f.ada))
		id := out["id"].(string)
		if rec, _ := f.doH(t, "PUT", orgsPath+"/"+org+"/teams/"+id+"/members/"+f.adaID, "", bearer(f.ada)); rec.Code != 200 {
			t.Fatal("join")
		}
		return id
	}
	doomed, safe := mk("Doomed"), mk("Safe")
	for path, body := range map[string]string{
		requests + "?org=" + org + "&team=" + doomed: `{"title": "goes"}`,
		requests + "?org=" + org + "&team=" + safe:   `{"title": "stays"}`,
		memos + "?org=" + org + "&team=" + doomed:    `{"title": "kept"}`,
	} {
		if rec, out := f.doH(t, "POST", path, body, jsonHdr(f.ada)); rec.Code != 201 {
			t.Fatalf("create %s: %d %v", path, rec.Code, out)
		}
	}
	// A team role goes with the team.
	if rec, out := f.doH(t, "POST", orgsPath+"/"+org+"/teams/"+doomed+"/roles", fmt.Sprintf(`{"user": %q, "role": "manager"}`, f.adaID), jsonHdr(f.ada)); rec.Code != 200 {
		t.Fatalf("role: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "DELETE", orgsPath+"/"+org+"/teams/"+doomed, "", bearer(f.ada)); rec.Code != 204 {
		t.Fatalf("delete team: %d", rec.Code)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("no cleanup job")
	}
	count := func(path string) int {
		t.Helper()
		_, out := f.doH(t, "GET", path+"?org="+org, "", bearer(f.ada))
		return len(out["items"].([]any))
	}
	if n := count(requests); n != 1 {
		t.Errorf("requests after the team went: %d (the other team's stays)", n)
	}
	// `keep` leaves the document with its old team id.
	rec, out := f.doH(t, "GET", memos+"?org="+org, "", bearer(f.ada))
	items := out["items"].([]any)
	if rec.Code != 200 || len(items) != 1 || items[0].(map[string]any)["_meta"].(map[string]any)["team"] != doomed {
		t.Errorf("kept: %d %v", rec.Code, out)
	}
	m, _ := f.svc.Store.OrgMember(ctx, org, f.adaID)
	for _, g := range m.TeamRoles {
		if g.Team == doomed {
			t.Errorf("a grant of the deleted team: %+v", g)
		}
	}
}
