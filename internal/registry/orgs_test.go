package registry

import (
	"slices"
	"strings"
	"testing"
	"time"
)

const orgsRealm = `signup: open
sign_in:
  allowed_redirects: ["https://app.acme.example"]
organizations:
  roles:
    owner:  { manage: [org, members, roles, teams] }
    admin:  { manage: [members, teams] }
    editor: {}
    member: {}
  default_roles: [member]
`

func TestOrganizationSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte(orgsRealm))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	o := s.Organizations
	if o == nil {
		t.Fatal("Organizations is nil")
	}
	if o.MaxCreatedPerUser != 5 || o.MaxMembershipsPerUser != 100 || o.MaxTeamsPerOrg != 100 || o.MaxTeamsPerMember != 20 || o.MaxPendingInvitations != 500 || !o.AllowRequireMFA || o.MembersVisibleTo != "members" {
		t.Errorf("defaults: %+v", o)
	}
	if got := strings.Join(o.CreatorRoles, ","); got != "owner" {
		t.Errorf("creator roles = %q", got)
	}
	if !o.Roles["owner"].Manage.Has(CapOrg|CapRoles) || o.Roles["admin"].Manage.Has(CapOrg) || o.Roles["editor"].Manage.Any() {
		t.Errorf("roles: %+v", o.Roles)
	}
	if !o.CapsOf([]string{"admin", "gone"}).Covers(CapMembers) || o.CapsOf([]string{"admin"}).Covers(CapRoles) {
		t.Error("CapsOf/Covers")
	}
	if got := o.StaleRoles([]string{"owner", "gone"}); len(got) != 1 || got[0] != "gone" {
		t.Errorf("stale = %v", got)
	}
}

func TestOrganizationSettingsRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"no roles":             {"organizations: {}\n", "declare at least one role"},
		"nobody manages":       {"organizations:\n  roles:\n    owner: { manage: [members] }\n", "at least one role needs manage: org"},
		"unknown capability":   {"organizations:\n  roles:\n    owner: { manage: [everything] }\n", "unknown capability"},
		"creator not declared": {"organizations:\n  roles:\n    boss: { manage: org }\n", `creator_roles: "owner" is not a declared role`},
		"creator can't manage": {"organizations:\n  roles:\n    boss: { manage: org }\n    owner: {}\n", "creator_roles: must include a role with manage: org"},
		"default with manage":  {"organizations:\n  roles:\n    owner: { manage: org }\n  default_roles: [owner]\n", "has manage capabilities"},
		"bad rule":             {"organizations:\n  create: user.nothing.\n  roles:\n    owner: { manage: org }\n", "organizations.create"},
		"too many memberships": {"organizations:\n  max_memberships_per_user: 500\n  roles:\n    owner: { manage: org }\n", "at most 100"},
		"link without token":   {"sign_in:\n  allowed_redirects: [\"https://app.acme.example\"]\norganizations:\n  invitation_link: https://app.acme.example/join\n  roles:\n    owner: { manage: org }\n", "{token} exactly once"},
		"link outside":         {"sign_in:\n  allowed_redirects: [\"https://app.acme.example\"]\norganizations:\n  invitation_link: https://evil.example/join?t={token}\n  roles:\n    owner: { manage: org }\n", "within sign_in.allowed_redirects"},
		"auth disabled":        {"auth: disabled\norganizations:\n  roles:\n    owner: { manage: org }\n", "only apply when auth is enabled"},
		"grants unknown":       {"organizations:\n  roles:\n    owner: { manage: org }\n  team_roles:\n    manager: { grants: [boss] }\n", `grants: "boss" is not a declared team role`},
		"grants twice":         {"organizations:\n  roles:\n    owner: { manage: org }\n  team_roles:\n    manager: { grants: [delegate, delegate] }\n    delegate: {}\n", "listed twice"},
		"bad team role name":   {"organizations:\n  roles:\n    owner: { manage: org }\n  team_roles:\n    Manager: {}\n", "invalid role name"},
		"unknown team key":     {"organizations:\n  roles:\n    owner: { manage: org }\n  team_roles:\n    manager: { manage: org }\n", "manage"},
		"bad visible":          {"organizations:\n  members_visible_to: everyone\n  roles:\n    owner: { manage: org }\n", "members or managers"},
	} {
		t.Run(name, func(t *testing.T) {
			_, errs := parseRealmSettings([]byte(tc.yaml))
			var all []string
			for _, e := range errs {
				all = append(all, e.Error())
			}
			if !strings.Contains(strings.Join(all, "\n"), tc.want) {
				t.Errorf("errors = %v, want one containing %q", all, tc.want)
			}
		})
	}
}

func TestOrganizationDisabled(t *testing.T) {
	s, errs := parseRealmSettings([]byte("organizations:\n  enabled: false\n"))
	if len(errs) > 0 || s.Organizations != nil {
		t.Errorf("errs=%v org=%v", errs, s.Organizations)
	}
}

func TestCollectionOrganizationPolicy(t *testing.T) {
	ok := "organization: required\non_org_delete:\n  action: delete\n"
	for name, tc := range map[string]struct {
		realm, yaml string
		org, onDel  string
		inOrg       string
	}{
		"none by default": {orgsRealm, "", OrgNone, "", ""},
		"required":        {orgsRealm, ok, OrgRequired, OrgDelete, InOrgKeep},
		"optional keep":   {orgsRealm, "organization: optional\non_org_delete:\n  action: keep\n", OrgOptional, OrgKeep, InOrgKeep},
		"in_org apply":    {orgsRealm, ok + "on_owner_delete:\n  action: delete\n  in_org: apply\n", OrgRequired, OrgDelete, InOrgApply},
	} {
		t.Run(name, func(t *testing.T) {
			reg, err := softTree(t, tc.realm, tc.yaml, "")
			if err != nil {
				t.Fatal(err)
			}
			c, _ := reg.Collection("shop", "main", "orders")
			if c.Organization != tc.org || c.OnOrgDelete != tc.onDel || c.OwnerDeleteInOrg != tc.inOrg {
				t.Errorf("got %q %q %q", c.Organization, c.OnOrgDelete, c.OwnerDeleteInOrg)
			}
			if tc.org != OrgNone && !c.InOrganizations() {
				t.Error("InOrganizations")
			}
			has := slices.ContainsFunc(c.Indexes, func(ix Index) bool { return ix.String() == "_meta.org,_meta.created_at,id" })
			if has != (tc.org != OrgNone) {
				t.Errorf("organization index present = %v for %s", has, tc.org)
			}
			if c.IsKnownField(MetaOrg) != (tc.org != OrgNone) {
				t.Errorf("_meta.org known = %v for %s", c.IsKnownField(MetaOrg), tc.org)
			}
		})
	}
	for name, tc := range map[string]struct{ realm, yaml, want string }{
		"no on_org_delete":      {orgsRealm, "organization: required\n", "on_org_delete"},
		"bad value":             {orgsRealm, "organization: sometimes\n", "must be required, optional or none"},
		"realm without orgs":    {"signup: open\n", ok, "enable organizations"},
		"on_org_delete alone":   {orgsRealm, "on_org_delete:\n  action: keep\n", "only applies to a collection"},
		"bad action":            {orgsRealm, "organization: required\non_org_delete:\n  action: purge\n", "delete or keep"},
		"in_org without action": {orgsRealm, ok + "on_owner_delete:\n  in_org: apply\n", "needs an action"},
		"custom id user":        {orgsRealm, ok + "custom_id: user\n", "custom_id: user"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := softTree(t, tc.realm, tc.yaml, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestRolesThatRequireMFA(t *testing.T) {
	s, errs := parseRealmSettings([]byte("organizations:\n  roles:\n    owner: { manage: org }\n    hr: { manage: members, require_mfa: true }\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	o := s.Organizations
	if o.Roles["owner"].RequireMFA || !o.Roles["hr"].RequireMFA {
		t.Errorf("roles: %+v", o.Roles)
	}
}

func TestTeamRoleSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte("organizations:\n  roles:\n    owner: { manage: org }\n  team_roles:\n    manager:\n      description: Runs the team\n      grants: [delegate]\n      require_mfa: true\n    delegate: {}\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	o := s.Organizations
	if got := o.TeamRoleNames(); len(got) != 2 || got[0] != "delegate" || got[1] != "manager" {
		t.Errorf("names: %v", got)
	}
	m := o.TeamRoles["manager"]
	if !m.RequireMFA || len(m.Grants) != 1 || m.Grants[0] != "delegate" || m.Description != "Runs the team" {
		t.Errorf("manager: %+v", m)
	}
	if !o.TeamRoleDeclared("delegate") || o.TeamRoleDeclared("owner") {
		t.Error("team roles and organization roles are separate")
	}
}

func TestCollectionTeamPolicy(t *testing.T) {
	realm := orgsRealm + "  team_roles:\n    manager: {}\n"
	ok := "organization: required\non_org_delete:\n  action: delete\n"
	reg, err := softTree(t, realm, ok+"team: required\non_team_delete:\n  action: keep\n", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection("shop", "main", "orders")
	if c.Team != TeamRequired || c.OnTeamDelete != OrgKeep || !c.InTeams() || !c.IsKnownField(MetaTeam) {
		t.Errorf("team: %q %q", c.Team, c.OnTeamDelete)
	}
	// The lists of a team have an index next to the organization's.
	for _, want := range []string{"_meta.org,_meta.created_at,id", "_meta.org,_meta.team,_meta.created_at,id"} {
		if !slices.ContainsFunc(c.Indexes, func(ix Index) bool { return ix.String() == want }) {
			t.Errorf("index %s is missing: %v", want, c.Indexes)
		}
	}
	reg, err = softTree(t, realm, ok+"team: optional\non_team_delete:\n  action: delete\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := reg.Collection("shop", "main", "orders"); c.Team != TeamOptional || c.OnTeamDelete != OrgDelete {
		t.Errorf("optional: %q %q", c.Team, c.OnTeamDelete)
	}
	plain, err := softTree(t, realm, ok, "")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := plain.Collection("shop", "main", "orders"); c.InTeams() || c.IsKnownField(MetaTeam) {
		t.Error("a collection without team has teams")
	}
	for name, tc := range map[string]struct{ yaml, want string }{
		"no on_team_delete":     {ok + "team: required\n", "on_team_delete"},
		"bad value":             {ok + "team: sometimes\n", "must be required, optional or none"},
		"without organization":  {"team: required\non_team_delete:\n  action: keep\n", "needs `organization"},
		"on_team_delete alone":  {ok + "on_team_delete:\n  action: keep\n", "only applies to a collection with `team"},
		"bad action":            {ok + "team: required\non_team_delete:\n  action: purge\n", "delete or keep"},
		"rule without the team": {ok + "rules:\n  read: inTeam(document._meta.team)\n", "has no teams"},
		"role not declared":     {ok + "team: required\non_team_delete:\n  action: keep\nrules:\n  read: hasTeamRole(document._meta.team, 'boss')\n", `team role "boss"`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := softTree(t, realm, tc.yaml, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	// The rules compile with the helpers when the collection has teams.
	if _, err := softTree(t, realm, ok+"team: required\non_team_delete:\n  action: keep\nrules:\n  read: inTeam(document._meta.team) || hasTeamRole(document._meta.team, 'manager')\n", ""); err != nil {
		t.Errorf("rules: %v", err)
	}
}

func TestOrganizationHooksSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte("organizations:\n  roles:\n    owner: { manage: org }\n  hooks:\n    on_member_joined: core/joined\n    on_roles_changed: core/changed\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	h := s.Organizations.Hooks
	if h.OnMemberJoined.Function != "core/joined" || h.OnRolesChanged.Function != "core/changed" || h.OnMemberLeft.Function != "" {
		t.Errorf("hooks: %+v", h)
	}
	_, errs = parseRealmSettings([]byte("organizations:\n  roles:\n    owner: { manage: org }\n  hooks:\n    on_member_left: nonsense\n"))
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "hooks.on_member_left: must be <database>/<function>") {
		t.Errorf("a bad reference: %v", errs)
	}
}

func TestOrganizationHooksNameBackgroundFunctions(t *testing.T) {
	tree := func(fn string) string {
		return writeTree(t, map[string]string{
			"shop/realm.yaml":                         "signup: open\norganizations:\n  roles:\n    owner: { manage: org }\n  hooks:\n    on_member_joined: app/hello\n",
			"shop/app/_functions/deno.json":           `{}`,
			"shop/app/_functions/hello/function.yaml": fn,
			"shop/app/_functions/hello/index.ts":      "export default () => 1;\n",
		})
	}
	reg, err := Load(tree("internal: true\nmode: async\ntimeout: 20s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if h := reg.Realms["shop"].Settings.Organizations.Hooks.OnMemberJoined; h.Function != "app/hello" || h.Timeout != 20*time.Second {
		t.Errorf("hook: %+v", h)
	}
	for name, tc := range map[string]struct{ fn, want string }{
		"not internal": {"mode: async\n", "organizations.hooks.on_member_joined: app/hello must have `internal: true`"},
		"not async":    {"internal: true\n", "organizations.hooks.on_member_joined: app/hello must have `mode: async`"},
	} {
		if _, err := Load(tree(tc.fn)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestTemplateDataHooksAreInternalSyncFunctions(t *testing.T) {
	tree := func(realm, fn string) string {
		return writeTree(t, map[string]string{
			"shop/realm.yaml":                         realm,
			"shop/app/_functions/deno.json":           `{}`,
			"shop/app/_functions/hello/function.yaml": fn,
			"shop/app/_functions/hello/index.ts":      "export default () => 1;\n",
			"shop/app/_functions/send/function.yaml":  "internal: true\nmode: async\n",
			"shop/app/_functions/send/index.ts":       "export default () => 1;\n",
		})
	}
	email := "signup: open\nemail:\n  function: app/send\n  from: Shop <no-reply@shop.example>\n  public_url: https://api.shop.example\n  data_hook: app/hello\n"
	reg, err := Load(tree(email, "internal: true\n"))
	if err != nil {
		// The templates are generated for a realm that sends email: this tree has none.
		if !strings.Contains(err.Error(), "email") || strings.Contains(err.Error(), "data_hook") {
			t.Fatalf("a sync internal function is a valid hook: %v", err)
		}
	} else if reg.Realms["shop"].Settings.Email.DataHook != "app/hello" {
		t.Errorf("hook: %q", reg.Realms["shop"].Settings.Email.DataHook)
	}
	for name, tc := range map[string]struct{ realm, fn, want string }{
		"email: not a function": {strings.Replace(email, "app/hello", "app/nothing", 1), "internal: true\n", "email.data_hook"},
		"email: not internal":   {email, "mode: sync\n", "email.data_hook: app/hello must have `internal: true`"},
		"email: async":          {email, "internal: true\nmode: async\n", "email.data_hook: app/hello must have `mode: sync`"},
		"email: not a path":     {strings.Replace(email, "app/hello", "hello", 1), "internal: true\n", "email.data_hook: must be <database>/<function>"},
		"pages: not internal":   {"signup: open\npages:\n  data_hook: app/hello\n", "mode: sync\n", "pages.data_hook: app/hello must have `internal: true`"},
		"pages: async":          {"signup: open\npages:\n  data_hook: app/hello\n", "internal: true\nmode: async\n", "pages.data_hook: app/hello must have `mode: sync`"},
	} {
		if _, err := Load(tree(tc.realm, tc.fn)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestCustomizableEmailKinds(t *testing.T) {
	s, errs := parseRealmSettings([]byte("signup: open\nemail:\n  function: app/send\n  from: Shop <no-reply@shop.example>\n  public_url: https://api.shop.example\n  customizable: [reset-password, org-invitation]\n  overrides:\n    permission: manage_members\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	e := s.Email
	if len(e.Customizable) != 2 || e.Customizable[1] != "org-invitation" || e.OverridePermission != CapMembers {
		t.Errorf("email: %+v", e)
	}
	s, _ = parseRealmSettings([]byte("signup: open\nemail:\n  function: app/send\n  from: Shop <no-reply@shop.example>\n  public_url: https://api.shop.example\n"))
	if s.Email.OverridePermission != CapOrg || len(s.Email.Customizable) != 0 {
		t.Errorf("defaults: %+v", s.Email)
	}
	for name, yaml := range map[string]string{
		"listed twice":   "customizable: [reset-password, reset-password]\n",
		"an empty kind":  "customizable: [\"\"]\n",
		"bad permission": "overrides:\n  permission: everyone\n",
	} {
		_, errs := parseRealmSettings([]byte("signup: open\nemail:\n  function: app/send\n  from: Shop <no-reply@shop.example>\n  public_url: https://api.shop.example\n  " + strings.ReplaceAll(strings.TrimSpace(yaml), "\n", "\n  ") + "\n"))
		if len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	// The kinds need organizations, which own the overrides.
	tree := writeTree(t, map[string]string{
		"shop/realm.yaml":                        "signup: open\nemail:\n  function: app/send\n  from: Shop <no-reply@shop.example>\n  public_url: https://api.shop.example\n  customizable: [reset-password]\n",
		"shop/app/_functions/deno.json":          `{}`,
		"shop/app/_functions/send/function.yaml": "internal: true\nmode: async\n",
		"shop/app/_functions/send/index.ts":      "export default () => 1;\n",
	})
	if _, err := Load(tree); err == nil || !strings.Contains(err.Error(), "email.customizable: needs the realm to enable organizations") {
		t.Errorf("without organizations: %v", err)
	}
}
