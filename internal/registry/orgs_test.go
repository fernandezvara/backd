package registry

import (
	"slices"
	"strings"
	"testing"
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
