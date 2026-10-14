package mongodb

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

// The organization helpers are pushed down as conditions on _meta.org, which has to list
// exactly what the rule allows in memory (design/organizations.md §6.2).

const orgsDiffSchema = `{
  "type": "object",
  "properties": {
    "status":   {"type": "string"},
    "audience": {"type": "string"}
  }
}`

var orgsDiffRules = []string{
	`inOrg(document._meta.org)`,
	`!inOrg(document._meta.org)`,
	`hasOrgRole(document._meta.org, 'editor')`,
	`hasOrgRole(document._meta.org, 'owner') || document.status == 'public'`,
	`hasOrgRole(document._meta.org, document.audience)`,
	`hasOrgRole(document._meta.org, document.audience) && document.status != 'draft'`,
	`orgClaims(document._meta.org).plan == 'pro'`,
	`orgClaims(document._meta.org).plan != 'pro'`,
	`orgClaims(document._meta.org).seats >= 3`,
	`orgClaims(document._meta.org).limits.items < 10`,
	`orgClaims(document._meta.org).plan in ['pro', 'team']`,
	`'a' in orgClaims(document._meta.org).features`,
	`orgClaims(document._meta.org).beta == true`,
	`inOrg(document._meta.org) && document.status == 'a'`,
	`user != nil && (document._meta.owner == user.id || hasOrgRole(document._meta.org, 'viewer'))`,
}

func orgsDiffCallers(now time.Time) map[string]rules.Values {
	pro := map[string]any{"plan": "pro", "seats": int64(5), "limits": map[string]any{"items": int64(20)}, "features": []any{"a", "b"}, "beta": true}
	free := map[string]any{"plan": "free", "seats": int64(2), "limits": map[string]any{"items": int64(5)}, "features": []any{"c"}}
	team := map[string]any{"plan": "team", "seats": 3.0}
	mk := func(id string, orgs map[string]*rules.UserOrg) rules.Values {
		u := &rules.User{ID: id, Orgs: orgs}
		return rules.Values{User: u, Orgs: orgs, Now: now}
	}
	return map[string]rules.Values{
		"anonymous": {Now: now},
		"nobody":    mk("u0", map[string]*rules.UserOrg{}),
		"u1":        mk("u1", map[string]*rules.UserOrg{"o1": {Roles: []string{"owner", "member"}, Claims: pro}, "o2": {Roles: []string{"editor"}, Claims: free}}),
		"u2":        mk("u2", map[string]*rules.UserOrg{"o3": {Roles: []string{"viewer", "member"}, Claims: team}}),
		"u3":        mk("u3", map[string]*rules.UserOrg{"o1": {Roles: []string{"member"}, Claims: pro}, "o3": {Roles: []string{"editor", "viewer"}, Claims: team}, "o4": {Roles: []string{"owner"}}}),
		"u4":        mk("u4", map[string]*rules.UserOrg{"o2": {Roles: []string{"viewer"}, Claims: free}}),
	}
}

func TestOrgRuleFiltersMatchRules(t *testing.T) {
	c, repo := provisionOneWith(t, orgsDiffSchema, "organization: required\non_org_delete:\n  action: keep\n")
	rng := rand.New(rand.NewPCG(11, 5))
	pick := func(vs ...any) any { return vs[rng.IntN(len(vs))] }
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 240 {
		doc := map[string]any{
			"id":       fmt.Sprintf("d%04d", i),
			"status":   pick("a", "b", "draft", "public"),
			"audience": pick("owner", "editor", "viewer", "member", "nobody"),
			"_meta": map[string]any{
				"created_at": now, "updated_at": now, "version": int64(1), "created_by": "key:test", "updated_by": "key:test",
				"owner": pick("u1", "u2", "u3", nil),
				"org":   pick("o1", "o2", "o3", "o4", "o5"),
			},
		}
		if err := repo.Create(context.Background(), doc); err != nil {
			t.Fatal(err)
		}
	}
	docs := all(t, repo, nil)
	schema := rules.Schema{
		DocumentField: c.IsKnownField, DataField: func(string) bool { return false },
		ScalarField: c.ScalarField, ArrayField: c.ArrayField, Roles: diffRoles,
		OrgRoles: []string{"owner", "editor", "viewer", "member"}, InOrgs: true,
	}
	callers := orgsDiffCallers(now)
	for _, src := range orgsDiffRules {
		var doc map[string]string
		if err := yaml.Unmarshal([]byte("read: \""+escape(src)+"\"\n"), &doc); err != nil {
			t.Fatal(err)
		}
		set, errs := rules.Parse(doc, schema)
		if len(errs) > 0 {
			t.Fatalf("%s: %v", src, errs)
		}
		r := set.For(rules.Read)
		mismatches, _ := compareCallers(t, repo, r, nil, src, docs, callers, rng)
		for _, m := range mismatches {
			t.Error(m)
		}
	}
	// The registry knows the system field and indexed it.
	rc, _ := registryCollection(c)
	if !rc.IsKnownField(registry.MetaOrg) {
		t.Error("_meta.org is not a known field")
	}
}

func escape(s string) string {
	out := ""
	for _, r := range s {
		if r == '"' || r == '\\' {
			out += "\\"
		}
		out += string(r)
	}
	return out
}

func registryCollection(c *registry.Collection) (*registry.Collection, bool) { return c, c != nil }
