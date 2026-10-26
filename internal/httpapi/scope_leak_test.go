package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// A person who belongs to two organizations holds one session in each, and each reaches its own
// organization and nothing of the other: the surfaces that read memberships all go through
// Org.Counts(mfa, scope) (design/scoped-sessions.md §4.2).
func TestASessionNeverReachesAnotherOrganizationOfTheSamePerson(t *testing.T) {
	f := newRulesFixture(t)
	ctx := context.Background()
	raw := f.fixture // no shortcut that picks the organization from the path
	orgA := f.orgOf(t, f.ada, "Alpha")
	orgB := f.orgOf(t, f.bob, "Beta")
	if _, err := f.svc.AdminAddMember(ctx, orgB, f.adaID, []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	inA, inB := f.sessionIn(t, f.ada, orgA), f.sessionIn(t, f.ada, orgB)

	// Documents of both organizations, made by the same person from their two sessions.
	create := func(path, session, body string) string {
		t.Helper()
		rec, out := raw.doH(t, "POST", path, body, jsonHdr(session))
		if rec.Code != 201 {
			t.Fatalf("create %s: %d %v", path, rec.Code, out)
		}
		return out["id"].(string)
	}
	projects, shared, requests := "/v1/acme/app/projects", "/v1/acme/app/shared", "/v1/acme/app/requests"
	projA := create(projects, inA, `{"title": "a"}`)
	projB := create(projects, inB, `{"title": "b"}`)
	create(shared, inA, `{"title": "a"}`)
	create(shared, inB, `{"title": "b"}`)
	team := func(org, session, name string) string {
		t.Helper()
		rec, out := raw.doH(t, "POST", orgsPath+"/"+org+"/teams", fmt.Sprintf(`{"name": %q}`, name), jsonHdr(session))
		if rec.Code != 201 {
			t.Fatalf("team %s: %d %v", name, rec.Code, out)
		}
		id := out["id"].(string)
		if rec, _ := raw.doH(t, "PUT", orgsPath+"/"+org+"/teams/"+id+"/members/"+f.adaID, "", bearer(session)); rec.Code != 200 {
			t.Fatalf("join %s: %d", name, rec.Code)
		}
		return id
	}
	teamA, teamB := team(orgA, inA, "Sales"), team(orgB, inB, "Support")
	create(requests+"?team="+teamA, inA, `{"title": "ra"}`)
	create(requests+"?team="+teamB, inB, `{"title": "rb"}`)

	titles := func(path, session string) map[string]bool {
		t.Helper()
		rec, out := raw.doH(t, "GET", path, "", bearer(session))
		if rec.Code != 200 {
			t.Fatalf("list %s: %d %v", path, rec.Code, out)
		}
		got := map[string]bool{}
		for _, it := range out["items"].([]any) {
			got[it.(map[string]any)["title"].(string)] = true
		}
		return got
	}
	for _, c := range []struct {
		name, session string
		own, other    string
	}{{"alpha", inA, "a", "b"}, {"beta", inB, "b", "a"}} {
		// Lists, with and without a filter, of collections that require or allow an organization.
		for _, p := range []string{projects, shared} {
			if got := titles(p, c.session); got[c.other] {
				t.Errorf("%s session lists the other organization's documents at %s: %v", c.name, p, got)
			}
		}
		if got := titles(projects+`?where={"title":"`+c.other+`"}`, c.session); len(got) != 0 {
			t.Errorf("%s session filters its way to the other organization: %v", c.name, got)
		}
		if got := titles(projects, c.session); len(got) != 1 || !got[c.own] {
			t.Errorf("%s session lists its own: %v", c.name, got)
		}
		if got := titles(requests, c.session); len(got) != 1 || !got["r"+c.own] {
			t.Errorf("%s session lists requests: %v", c.name, got)
		}
		if rec, out := raw.doH(t, "GET", projects+"?count=true", "", bearer(c.session)); rec.Code != 200 || out["total"] != float64(1) {
			t.Errorf("%s session counts: %d %v", c.name, rec.Code, out)
		}
		// The other organization's team in an aggregate: only the session's own appears.
		rec, out := raw.doH(t, "GET", requests+"/_aggregate?group_by=_meta.team", "", bearer(c.session))
		if rec.Code != 200 || len(out["groups"].([]any)) != 1 {
			t.Errorf("%s session aggregates: %d %v", c.name, rec.Code, out)
		}
		// Asking for the other organization is refused, never answered with its data.
		for _, q := range []string{projects, requests + "/_aggregate?group_by=_meta.team"} {
			sep := "?"
			if strings.Contains(q, "?") {
				sep = "&"
			}
			other := orgB
			if c.session == inB {
				other = orgA
			}
			if rec, out := raw.doH(t, "GET", q+sep+"org="+other, "", bearer(c.session)); rec.Code != 400 {
				t.Errorf("%s session asks for the other organization at %s: %d %v", c.name, q, rec.Code, out)
			}
		}
	}
	// One document of the other organization, by id: it is not there.
	if rec, _ := raw.doH(t, "GET", projects+"/"+projB, "", bearer(inA)); rec.Code != 404 {
		t.Errorf("alpha reads beta's document: %d", rec.Code)
	}
	if rec, _ := raw.doH(t, "GET", projects+"/"+projA, "", bearer(inB)); rec.Code != 404 {
		t.Errorf("beta reads alpha's document: %d", rec.Code)
	}
	if rec, _ := raw.doH(t, "PATCH", projects+"/"+projB, `{"title": "x"}`, merge(inA)); rec.Code != 404 && rec.Code != 403 {
		t.Errorf("alpha writes beta's document: %d", rec.Code)
	}
	if rec, _ := raw.doH(t, "DELETE", projects+"/"+projB, "", bearer(inA)); rec.Code != 404 && rec.Code != 403 {
		t.Errorf("alpha deletes beta's document: %d", rec.Code)
	}
	// Creating lands in the session's organization.
	if rec, out := raw.doH(t, "POST", projects, `{"title": "c"}`, jsonHdr(inA)); rec.Code != 201 || out["_meta"].(map[string]any)["org"] != orgA {
		t.Errorf("create: %d %v", rec.Code, out)
	}

	// The organization routes: the other one is not a membership of this session; there is no
	// list of organizations.
	for _, p := range []string{"", "/members", "/teams", "/invitations", "/sso"} {
		if rec, out := raw.doH(t, "GET", orgsPath+"/"+orgB+p, "", bearer(inA)); rec.Code != 403 || errCode(out) != "not_org_member" {
			t.Errorf("alpha at beta%s: %d %v", p, rec.Code, out)
		}
	}
	if rec, _ := raw.doH(t, "GET", orgsPath, "", bearer(inA)); rec.Code != 404 && rec.Code != 405 {
		t.Errorf("GET /_orgs: %d", rec.Code)
	}
	// /me names one organization.
	if rec, out := raw.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(inA)); rec.Code != 200 {
		t.Errorf("me: %d %v", rec.Code, out)
	} else if _, many := out["orgs"]; many || out["org"] == nil {
		t.Errorf("me shows more than the session's organization: %v", out)
	}

	// Rules and functions read memberships through the same place.
	for _, scope := range []string{orgA, orgB} {
		mine, err := f.svc.RuleOrgs(ctx, f.adaID, true, scope)
		if err != nil || len(mine) != 1 || mine[scope] == nil {
			t.Errorf("RuleOrgs(%s) = %v, %v", scope, mine, err)
		}
		env := f.functionsUserEnvelope(t, f.adaID, scope)
		if orgs, _ := env["orgs"].(map[string]any); len(orgs) != 1 || orgs[scope] == nil {
			t.Errorf("ctx.user.orgs for %s = %v", scope, env["orgs"])
		}
	}
	if none, err := f.svc.RuleOrgs(ctx, f.adaID, true, ""); err != nil || len(none) != 0 {
		t.Errorf("a session scoped to nothing counts %v, %v", none, err)
	}
}

// Org.Counts is the one place that decides: only the session's own, active, MFA-satisfied
// organization counts.
func TestCountsIsTheOneChokePoint(t *testing.T) {
	for _, c := range []struct {
		name  string
		org   auth.Org
		mfa   bool
		scope string
		want  bool
	}{
		{"own and active", auth.Org{ID: "a", Status: auth.OrgActive}, false, "a", true},
		{"orphaned keeps access", auth.Org{ID: "a", Status: auth.OrgOrphaned}, false, "a", true},
		{"another organization", auth.Org{ID: "b", Status: auth.OrgActive}, true, "a", false},
		{"no scope", auth.Org{ID: "a", Status: auth.OrgActive}, true, "", false},
		{"deleted", auth.Org{ID: "a", Status: auth.OrgDeleted}, true, "a", false},
		{"mfa required, none used", auth.Org{ID: "a", Status: auth.OrgActive, RequireMFA: true}, false, "a", false},
		{"mfa required and used", auth.Org{ID: "a", Status: auth.OrgActive, RequireMFA: true}, true, "a", true},
	} {
		if got := c.org.Counts(c.mfa, c.scope); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The routes that need a session, and the administrators' ones, name an organization by its id:
// only the routes that run before there is a session take the slug.
func TestSSORoutesTakeTheIDAndNotTheSlug(t *testing.T) {
	f := newRulesFixture(t)
	raw := f.fixture
	org := f.orgOf(t, f.ada, "Slugged")
	inOrg := f.sessionIn(t, f.ada, org)
	if rec, out := raw.doH(t, "GET", orgsPath+"/"+org+"/sso", "", bearer(inOrg)); rec.Code != 200 {
		t.Fatalf("by id: %d %v", rec.Code, out)
	}
	if rec, out := raw.doH(t, "GET", orgsPath+"/slugged/sso", "", bearer(inOrg)); rec.Code != 403 || errCode(out) != "not_org_member" {
		t.Errorf("by slug: %d %v", rec.Code, out)
	}
	f.bobHolds(t, "staff")
	admin := bearer(f.bob)
	if rec, out := raw.doH(t, "GET", "/v1/acme/_admin/orgs/"+org+"/sso", "", admin); rec.Code != 200 {
		t.Fatalf("admin by id: %d %v", rec.Code, out)
	}
	if rec, out := raw.doH(t, "GET", "/v1/acme/_admin/orgs/slugged/sso", "", admin); rec.Code != 404 {
		t.Errorf("admin by slug: %d %v", rec.Code, out)
	}
}
