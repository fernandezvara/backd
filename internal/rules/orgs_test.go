package rules

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

func orgSchema() Schema {
	s := testSchema
	s.DocumentField = func(p string) bool {
		return testSchema.DocumentField(p) || p == "_meta.org" || p == "_meta.team" || p == "audience"
	}
	s.ScalarField = func(p string) bool {
		return testSchema.ScalarField(p) || p == "_meta.org" || p == "_meta.team" || p == "audience"
	}
	s.DataField = func(p string) bool { return testSchema.DataField(p) || p == "_meta.org" || p == "_meta.team" }
	s.OrgRoles = []string{"owner", "editor", "viewer"}
	s.InOrgs = true
	s.TeamRoles = []string{"manager", "delegate"}
	s.InTeams = true
	return s
}

func orgRule(t *testing.T, op Op, src string) (*Rule, []error) {
	t.Helper()
	set, errs := Parse(map[string]string{string(op): src}, orgSchema())
	if len(errs) > 0 {
		return nil, errs
	}
	return set.For(op), nil
}

func TestOrgHelpersFilter(t *testing.T) {
	orgs := map[string]*UserOrg{
		"o1": {Roles: []string{"owner", "editor"}, Claims: map[string]any{"plan": "pro", "seats": int64(5)}},
		"o2": {Roles: []string{"viewer"}, Claims: map[string]any{"plan": "free", "seats": int64(1)}},
	}
	in := func(ids ...any) storage.Filter {
		return storage.Condition{Field: "_meta.org", Op: storage.OpIn, Value: ids}
	}
	for name, tc := range map[string]struct {
		src  string
		orgs map[string]*UserOrg
		want storage.Filter
	}{
		"member":                   {`inOrg(document._meta.org)`, orgs, in("o1", "o2")},
		"nobody":                   {`inOrg(document._meta.org)`, nil, storage.Const(false)},
		"role":                     {`hasOrgRole(document._meta.org, 'editor')`, orgs, in("o1")},
		"role nobody holds":        {`hasOrgRole(document._meta.org, 'viewer') && hasOrgRole(document._meta.org, 'owner')`, orgs, storage.And{in("o2"), in("o1")}},
		"claim equals":             {`orgClaims(document._meta.org).plan == 'pro'`, orgs, in("o1")},
		"claim differs":            {`orgClaims(document._meta.org).plan != 'pro'`, orgs, in("o2")},
		"claim number":             {`orgClaims(document._meta.org).seats >= 2`, orgs, in("o1")},
		"claim flipped":            {`2 <= orgClaims(document._meta.org).seats`, orgs, in("o1")},
		"claim in list":            {`orgClaims(document._meta.org).plan in ['free', 'team']`, orgs, in("o2")},
		"not a member":             {`!inOrg(document._meta.org)`, orgs, storage.Not{Filter: in("o1", "o2")}},
		"constant parts are exact": {`user != nil && inOrg(document._meta.org)`, orgs, in("o1", "o2")},
	} {
		t.Run(name, func(t *testing.T) {
			r, errs := orgRule(t, Read, tc.src)
			if errs != nil {
				t.Fatal(errs)
			}
			got, err := r.Filter(Values{User: &User{ID: "u1"}, Orgs: tc.orgs, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
	// A role named by the document pairs each organization with each role held there.
	r, _ := orgRule(t, Read, `hasOrgRole(document._meta.org, document.audience)`)
	got, err := r.Filter(Values{Orgs: orgs, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	or, ok := got.(storage.Or)
	if !ok || len(or) != 3 {
		t.Fatalf("pairs: %#v", got)
	}
}

func TestOrgHelpersInMemory(t *testing.T) {
	orgs := map[string]*UserOrg{"o1": {Roles: []string{"owner"}, Claims: map[string]any{"plan": "pro"}}}
	vals := func(org any) Values {
		return Values{User: &User{ID: "u1", Orgs: orgs}, Orgs: orgs, Data: map[string]any{"_meta": map[string]any{"org": org}}, Now: time.Now()}
	}
	for name, tc := range map[string]struct {
		src  string
		org  any
		want bool
	}{
		"member":                  {`user != nil && inOrg(data._meta.org)`, "o1", true},
		"other organization":      {`inOrg(data._meta.org)`, "o2", false},
		"no organization":         {`inOrg(data._meta.org)`, nil, false},
		"role":                    {`hasOrgRole(data._meta.org, 'owner')`, "o1", true},
		"role lacking":            {`hasOrgRole(data._meta.org, 'editor')`, "o1", false},
		"claim":                   {`orgClaims(data._meta.org).plan == 'pro'`, "o1", true},
		"claim of a stranger":     {`orgClaims(data._meta.org).plan != 'pro'`, "o2", false},
		"missing claim":           {`orgClaims(data._meta.org).seats > 1`, "o1", false},
		"user.orgs":               {`user != nil && 'owner' in user.orgs[data._meta.org].roles`, "o1", true},
		"user.orgs of a stranger": {`user != nil && len(user.orgs) == 1`, "o2", true},
	} {
		t.Run(name, func(t *testing.T) {
			r, errs := orgRule(t, Create, tc.src)
			if errs != nil {
				t.Fatal(errs)
			}
			// An evaluation error (an ordering comparison with a missing claim) is a denial.
			got, _ := r.Allow(vals(tc.org))
			if got != tc.want {
				t.Errorf("got %v", got)
			}
		})
	}
}

func TestOrgHelpersChecks(t *testing.T) {
	for name, tc := range map[string]struct {
		op   Op
		src  string
		want string
	}{
		"read with another expression": {Read, `inOrg(document.status)`, "call it with document._meta.org"},
		"undeclared role":              {Read, `hasOrgRole(document._meta.org, 'wizard')`, `organization role "wizard" is not declared`},
		"role from a non-scalar":       {Read, `hasOrgRole(document._meta.org, document.members)`, "single-valued field"},
		"bare claim":                   {Read, `orgClaims(document._meta.org).beta`, "to compare it with a value"},
		"claim against the document":   {Read, `orgClaims(document._meta.org).seats > document.status`, "doesn't depend on the document"},
		"unknown field":                {Read, `inOrg(document._meta.nope)`, "_meta.nope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, errs := orgRule(t, tc.op, tc.src)
			var all []string
			for _, e := range errs {
				all = append(all, e.Error())
			}
			if len(all) == 0 || !strings.Contains(strings.Join(all, "\n"), tc.want) {
				t.Errorf("errors = %v, want %q", all, tc.want)
			}
		})
	}
	// A realm without organizations refuses the helpers.
	s := orgSchema()
	s.OrgRoles = nil
	_, errs := Parse(map[string]string{"read": "inOrg(document._meta.org)"}, s)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "doesn't enable organizations") {
		t.Errorf("without organizations: %v", errs)
	}
	// Create and invoke rules take any expression.
	for _, src := range []string{`user != nil && hasOrgRole(data.title, 'owner')`, `user != nil && len(user.orgs) > 0`} {
		if _, errs := orgRule(t, Create, src); errs != nil {
			t.Errorf("%s: %v", src, errs)
		}
	}
	r, errs := orgRule(t, Create, `user != nil && hasOrgRole(data._meta.org, 'owner')`)
	if errs != nil || !r.UsesOrgs() {
		t.Errorf("UsesOrgs: %v %v", errs, r)
	}
	plain, _ := orgRule(t, Create, `user != nil`)
	if plain.UsesOrgs() {
		t.Error("a rule without helpers uses orgs")
	}
}

func TestTeamHelpersFilter(t *testing.T) {
	// u1 belongs to sales and supports, manages sales until the window closed support's role,
	// and holds a role in billing without being a member.
	orgs := map[string]*UserOrg{
		"o1": {Roles: []string{"member"}, Teams: []string{"sales", "support"}, TeamRoles: map[string][]string{"sales": {"manager"}, "billing": {"delegate"}}},
		"o2": {Roles: []string{"member"}, Teams: []string{"ops"}},
	}
	in := func(ids ...any) storage.Filter {
		return storage.Condition{Field: "_meta.team", Op: storage.OpIn, Value: ids}
	}
	orgIn := func(ids ...any) storage.Filter {
		return storage.Condition{Field: "_meta.org", Op: storage.OpIn, Value: ids}
	}
	for name, tc := range map[string]struct {
		src  string
		orgs map[string]*UserOrg
		want storage.Filter
	}{
		"teams I belong to":        {`inTeam(document._meta.team)`, orgs, in("ops", "sales", "support")},
		"a team role":              {`hasTeamRole(document._meta.team, 'manager')`, orgs, in("sales")},
		"held without membership":  {`hasTeamRole(document._meta.team, 'delegate')`, orgs, in("billing")},
		"a role nobody holds":      {`hasTeamRole(document._meta.team, 'delegate') && hasTeamRole(document._meta.team, 'manager')`, orgs, storage.And{in("billing"), in("sales")}},
		"nobody":                   {`inTeam(document._meta.team)`, nil, storage.Const(false)},
		"with an organization one": {`inOrg(document._meta.org) && hasTeamRole(document._meta.team, 'manager')`, orgs, storage.And{orgIn("o1", "o2"), in("sales")}},
		"either":                   {`hasOrgRole(document._meta.org, 'owner') || inTeam(document._meta.team)`, orgs, storage.Or{storage.Const(false), in("ops", "sales", "support")}},
	} {
		t.Run(name, func(t *testing.T) {
			r, errs := orgRule(t, Read, tc.src)
			if errs != nil {
				t.Fatal(errs)
			}
			got, err := r.Filter(Values{User: &User{ID: "u1"}, Orgs: tc.orgs, Now: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(storage.Simplify(got), storage.Simplify(tc.want)) {
				t.Errorf("got %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestTeamHelpersInMemoryAndChecks(t *testing.T) {
	orgs := map[string]*UserOrg{"o1": {Teams: []string{"sales"}, TeamRoles: map[string][]string{"sales": {"manager"}, "billing": {"delegate"}}}}
	vals := func(team any) Values {
		return Values{User: &User{ID: "u1", Orgs: orgs}, Orgs: orgs, Data: map[string]any{"_meta": map[string]any{"team": team}}, Now: time.Now()}
	}
	for name, tc := range map[string]struct {
		src  string
		team any
		want bool
	}{
		"member":             {`inTeam(data._meta.team)`, "sales", true},
		"not a member":       {`inTeam(data._meta.team)`, "support", false},
		"role there":         {`hasTeamRole(data._meta.team, 'manager')`, "sales", true},
		"role elsewhere":     {`hasTeamRole(data._meta.team, 'manager')`, "billing", false},
		"role without being": {`hasTeamRole(data._meta.team, 'delegate')`, "billing", true},
		"no team":            {`inTeam(data._meta.team)`, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, errs := orgRule(t, Create, tc.src)
			if errs != nil {
				t.Fatal(errs)
			}
			if got, _ := r.Allow(vals(tc.team)); got != tc.want {
				t.Errorf("got %v", got)
			}
		})
	}
	for name, tc := range map[string]struct{ src, want string }{
		"another field":      {`inTeam(document.status)`, "call it with document._meta.team"},
		"undeclared role":    {`hasTeamRole(document._meta.team, 'wizard')`, `team role "wizard" is not declared`},
		"role from the data": {`hasTeamRole(document._meta.team, document.audience)`, "string literal"},
	} {
		t.Run(name, func(t *testing.T) {
			_, errs := orgRule(t, Read, tc.src)
			var all []string
			for _, e := range errs {
				all = append(all, e.Error())
			}
			if !strings.Contains(strings.Join(all, "\n"), tc.want) {
				t.Errorf("errors = %v, want %q", all, tc.want)
			}
		})
	}
	// A collection without teams refuses the helpers.
	s := orgSchema()
	s.InTeams = false
	if _, errs := Parse(map[string]string{"read": "inTeam(document._meta.team)"}, s); len(errs) == 0 || !strings.Contains(errs[0].Error(), "has no teams") {
		t.Errorf("without teams: %v", errs)
	}
}
