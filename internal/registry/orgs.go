package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/rules"
)

// Organizations (design/organizations.md): realm-owned groups of users, with roles
// declared in realm.yaml, that documents can belong to.

// OrgCaps are the things an organization role lets its holders manage in the organization:
// the same idea as the admin areas a realm role opens, scoped to one organization.
type OrgCaps uint8

const (
	CapOrg     OrgCaps = 1 << iota // rename, slug, require_mfa and deletion
	CapMembers                     // invite, remove members, revoke invitations
	CapRoles                       // grant or remove any role
	CapTeams                       // create, change and delete teams and their members

	// AllCaps is every capability.
	AllCaps = CapOrg | CapMembers | CapRoles | CapTeams
)

var capNames = []struct {
	name string
	cap  OrgCaps
}{{"org", CapOrg}, {"members", CapMembers}, {"roles", CapRoles}, {"teams", CapTeams}}

// Has reports whether c includes every capability in want.
func (c OrgCaps) Has(want OrgCaps) bool { return c&want == want }

// Any reports whether c holds anything: whether holding it makes someone a manager.
func (c OrgCaps) Any() bool { return c != 0 }

// Covers reports whether c can do everything b can: nobody hands out, or changes someone who
// holds, more than they have (the rule realm roles follow for admin areas).
func (c OrgCaps) Covers(b OrgCaps) bool { return c.Has(b) }

// Names lists the capabilities by name, in a fixed order.
func (c OrgCaps) Names() []string {
	var out []string
	for _, n := range capNames {
		if c.Has(n.cap) {
			out = append(out, n.name)
		}
	}
	return out
}

func capNamed(name string) (OrgCaps, bool) {
	for _, n := range capNames {
		if n.name == name {
			return n.cap, true
		}
	}
	return 0, false
}

// OrgRole is an organization role declared in realm.yaml.
type OrgRole struct {
	Description string
	// Manage is what holders may manage in the organization.
	Manage OrgCaps
	// RequireMFA makes the role count only for sessions with a second factor.
	RequireMFA bool
}

// TeamRole is a role held within one team of an organization, declared in realm.yaml's
// organizations.team_roles. Team roles are separate from organization roles: they grant
// nothing outside their team (design/team-access.md §3).
type TeamRole struct {
	Description string
	// Grants are the team roles its holders may grant, change and revoke in the same team.
	Grants []string
	// RequireMFA makes the role count only for sessions with a second factor.
	RequireMFA bool
}

// MaxTeamRolesPerMember is the most team role grants one member may hold in an organization.
const MaxTeamRolesPerMember = 50

// Defaults of the organizations section.
const (
	DefaultOrgCreateRule         = "user.email_verified"
	DefaultMaxCreatedPerUser     = 5
	DefaultMaxMembershipsPerUser = 100
	DefaultMaxTeamsPerOrg        = 100
	DefaultMaxTeamsPerMember     = 20
	DefaultMaxPendingInvitations = 500
	// MaxOrgClaimsBytes is the largest claims object of an organization.
	MaxOrgClaimsBytes = 1024

	MembersVisibleToMembers  = "members"
	MembersVisibleToManagers = "managers"

	// DefaultOrgInvitationTTL is how long an invitation lasts when the request doesn't say.
	DefaultOrgInvitationTTL = 7 * 24 * time.Hour
	// MaxOrgInvitationTTL is the longest an invitation may last.
	MaxOrgInvitationTTL = 30 * 24 * time.Hour
)

// Organization policies of a collection (collection.yaml's organization).
const (
	OrgNone     = "none"
	OrgOptional = "optional"
	OrgRequired = "required"

	// OrgDelete and OrgKeep are on_org_delete.action.
	OrgDelete = "delete"
	OrgKeep   = "keep"

	// InOrgKeep and InOrgApply are on_owner_delete.in_org.
	InOrgKeep  = "keep"
	InOrgApply = "apply"

	// TeamNone, TeamOptional and TeamRequired are collection.yaml's team.
	TeamNone     = "none"
	TeamOptional = "optional"
	TeamRequired = "required"
)

// OrgSettings is the realm's organizations section; nil in RealmSettings when the realm
// doesn't enable organizations.
type OrgSettings struct {
	Roles map[string]OrgRole
	// TeamRoles are the roles held within a team.
	TeamRoles map[string]TeamRole
	// CreateRule is the expression deciding who may create organizations (it sees `user`);
	// "false" leaves it to realm admins and functions.
	CreateRule   string
	CreatorRoles []string
	// DefaultRoles are held by every member without being granted.
	DefaultRoles          []string
	MaxCreatedPerUser     int
	MaxMembershipsPerUser int
	MaxTeamsPerOrg        int
	MaxTeamsPerMember     int
	MaxPendingInvitations int
	AllowRequireMFA       bool
	MembersVisibleTo      string
	// InvitationLink is where invitation links point: it holds {token} and may hold {org}.
	InvitationLink string
	create         *rules.Rule
}

// CreateRuleAllows evaluates the create rule for user.
func (o *OrgSettings) CreateRuleAllows(u *rules.User, now time.Time) (bool, error) {
	if o.create == nil {
		return false, nil
	}
	return o.create.Allow(rules.Values{User: u, Now: now})
}

// CapsOf is what the roles let their holder manage, added up. Roles the realm doesn't
// declare (any more) grant nothing.
func (o *OrgSettings) CapsOf(roles []string) OrgCaps {
	var out OrgCaps
	for _, r := range roles {
		out |= o.Roles[r].Manage
	}
	return out
}

// Declared reports whether role is declared.
func (o *OrgSettings) Declared(role string) bool {
	_, ok := o.Roles[role]
	return ok
}

// RoleNames returns the declared roles, sorted.
func (o *OrgSettings) RoleNames() []string { return slices.Sorted(maps.Keys(o.Roles)) }

// TeamRoleDeclared reports whether role is a declared team role.
func (o *OrgSettings) TeamRoleDeclared(role string) bool {
	_, ok := o.TeamRoles[role]
	return ok
}

// TeamRoleNames returns the declared team roles, sorted.
func (o *OrgSettings) TeamRoleNames() []string { return slices.Sorted(maps.Keys(o.TeamRoles)) }

// InvitationURL is the link for a token (and organization id).
func (o *OrgSettings) InvitationURL(token, org string) string {
	return strings.NewReplacer("{token}", token, "{org}", org).Replace(o.InvitationLink)
}

// manageDoc is a role's manage: one capability or a list of them.
type manageDoc struct{ Caps OrgCaps }

func (m *manageDoc) UnmarshalYAML(n *yaml.Node) error {
	var names []string
	switch n.Kind {
	case yaml.ScalarNode:
		names = []string{n.Value}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: manage lists capability names", c.Line)
			}
			names = append(names, c.Value)
		}
	default:
		return fmt.Errorf("line %d: manage must be a capability or a list of them (org, members, roles, teams)", n.Line)
	}
	for _, name := range names {
		c, ok := capNamed(name)
		if !ok {
			return fmt.Errorf("line %d: unknown capability %q (want org, members, roles, teams)", n.Line, name)
		}
		if m.Caps.Has(c) {
			return fmt.Errorf("line %d: capability %q is listed twice", n.Line, name)
		}
		m.Caps |= c
	}
	return nil
}

type orgsDoc struct {
	Enabled *bool `yaml:"enabled"`
	Roles   map[string]*struct {
		Description string    `yaml:"description"`
		Manage      manageDoc `yaml:"manage"`
		RequireMFA  bool      `yaml:"require_mfa"`
	} `yaml:"roles"`
	TeamRoles map[string]*struct {
		Description string   `yaml:"description"`
		Grants      []string `yaml:"grants"`
		RequireMFA  bool     `yaml:"require_mfa"`
	} `yaml:"team_roles"`
	Create                *string  `yaml:"create"`
	CreatorRoles          []string `yaml:"creator_roles"`
	DefaultRoles          []string `yaml:"default_roles"`
	MaxCreatedPerUser     *int     `yaml:"max_created_per_user"`
	MaxMembershipsPerUser *int     `yaml:"max_memberships_per_user"`
	MaxTeamsPerOrg        *int     `yaml:"max_teams_per_org"`
	MaxTeamsPerMember     *int     `yaml:"max_teams_per_member"`
	MaxPendingInvitations *int     `yaml:"max_pending_invitations"`
	AllowRequireMFA       *bool    `yaml:"allow_require_mfa"`
	MembersVisibleTo      string   `yaml:"members_visible_to"`
	InvitationLink        string   `yaml:"invitation_link"`
}

// apply reads the section into s. It runs after the realm's roles, redirects and sign-in
// settings, which it refers to.
func (d *orgsDoc) apply(s *RealmSettings) []error {
	if d.Enabled != nil && !*d.Enabled {
		return nil
	}
	if !s.AuthEnabled {
		return []error{errors.New("organizations: only apply when auth is enabled (organizations hold users)")}
	}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("organizations."+format, args...)) }
	o := &OrgSettings{
		Roles:                 map[string]OrgRole{},
		TeamRoles:             map[string]TeamRole{},
		CreateRule:            DefaultOrgCreateRule,
		CreatorRoles:          d.CreatorRoles,
		DefaultRoles:          d.DefaultRoles,
		MaxCreatedPerUser:     DefaultMaxCreatedPerUser,
		MaxMembershipsPerUser: DefaultMaxMembershipsPerUser,
		MaxTeamsPerOrg:        DefaultMaxTeamsPerOrg,
		MaxTeamsPerMember:     DefaultMaxTeamsPerMember,
		MaxPendingInvitations: DefaultMaxPendingInvitations,
		AllowRequireMFA:       true,
		MembersVisibleTo:      MembersVisibleToMembers,
		InvitationLink:        d.InvitationLink,
	}
	if len(o.CreatorRoles) == 0 {
		o.CreatorRoles = []string{"owner"}
	}
	for _, name := range slices.Sorted(maps.Keys(d.Roles)) {
		if msg := roleNameProblem(name); msg != "" {
			add("roles: invalid role name %q: %s", name, msg)
			continue
		}
		r := d.Roles[name]
		role := OrgRole{}
		if r != nil {
			role.Description, role.Manage, role.RequireMFA = r.Description, r.Manage.Caps, r.RequireMFA
		}
		o.Roles[name] = role
	}
	if len(o.Roles) == 0 {
		add("roles: declare at least one role (one with manage: org, for the people who run an organization)")
	} else if !slices.ContainsFunc(slices.Collect(maps.Values(o.Roles)), func(r OrgRole) bool { return r.Manage.Has(CapOrg) }) {
		add("roles: at least one role needs manage: org, or nobody could rename or delete an organization")
	}
	for _, name := range slices.Sorted(maps.Keys(d.TeamRoles)) {
		if msg := roleNameProblem(name); msg != "" {
			add("team_roles: invalid role name %q: %s", name, msg)
			continue
		}
		role := TeamRole{}
		if r := d.TeamRoles[name]; r != nil {
			role = TeamRole{Description: r.Description, Grants: r.Grants, RequireMFA: r.RequireMFA}
		}
		o.TeamRoles[name] = role
	}
	for _, name := range o.TeamRoleNames() {
		seen := map[string]bool{}
		for _, g := range o.TeamRoles[name].Grants {
			switch {
			case !o.TeamRoleDeclared(g):
				add("team_roles.%s.grants: %q is not a declared team role", name, g)
			case seen[g]:
				add("team_roles.%s.grants: %q is listed twice", name, g)
			}
			seen[g] = true
		}
	}
	holds := func(r string) bool { return o.Roles[r].Manage.Has(CapOrg) }
	for _, r := range o.CreatorRoles {
		if !o.Declared(r) {
			add("creator_roles: %q is not a declared role", r)
		}
	}
	if len(errs) == 0 && !slices.ContainsFunc(o.CreatorRoles, holds) {
		add("creator_roles: must include a role with manage: org, or the creator couldn't manage the organization")
	}
	for _, r := range o.DefaultRoles {
		switch {
		case !o.Declared(r):
			add("default_roles: %q is not a declared role", r)
		case o.Roles[r].Manage.Any():
			add("default_roles: %q has manage capabilities, which every member would hold", r)
		}
	}
	for _, c := range []struct {
		key string
		in  *int
		to  *int
	}{
		{"max_created_per_user", d.MaxCreatedPerUser, &o.MaxCreatedPerUser},
		{"max_memberships_per_user", d.MaxMembershipsPerUser, &o.MaxMembershipsPerUser},
		{"max_teams_per_org", d.MaxTeamsPerOrg, &o.MaxTeamsPerOrg},
		{"max_teams_per_member", d.MaxTeamsPerMember, &o.MaxTeamsPerMember},
		{"max_pending_invitations", d.MaxPendingInvitations, &o.MaxPendingInvitations},
	} {
		if c.in == nil {
			continue
		}
		if *c.in < 1 {
			add("%s: must be at least 1, got %d", c.key, *c.in)
			continue
		}
		*c.to = *c.in
	}
	if o.MaxMembershipsPerUser > 100 {
		add("max_memberships_per_user: must be at most 100, which keeps the lists the rules push down small")
	}
	if o.MaxTeamsPerMember > 20 {
		add("max_teams_per_member: must be at most 20")
	}
	if d.AllowRequireMFA != nil {
		o.AllowRequireMFA = *d.AllowRequireMFA
	}
	switch d.MembersVisibleTo {
	case "":
	case MembersVisibleToMembers, MembersVisibleToManagers:
		o.MembersVisibleTo = d.MembersVisibleTo
	default:
		add("members_visible_to: must be members or managers, got %q", d.MembersVisibleTo)
	}
	if d.Create != nil {
		o.CreateRule = strings.TrimSpace(*d.Create)
	}
	// Creating needs a session, so the rule may use `user` without a guard.
	rule, err := rules.CompileInvoke("user != nil && ("+o.CreateRule+")", slices.Sorted(maps.Keys(s.Roles)), nil)
	if err != nil {
		add("create: %s", strings.TrimPrefix(err.Error(), "invoke: "))
	}
	o.create = rule
	if o.InvitationLink != "" {
		probe := strings.NewReplacer("{token}", "x", "{org}", "x").Replace(o.InvitationLink)
		switch {
		case strings.Count(o.InvitationLink, "{token}") != 1:
			add("invitation_link: %q must contain {token} exactly once", o.InvitationLink)
		case !s.SignInRedirectAllowed(probe):
			add("invitation_link: %q must be an absolute URL within sign_in.allowed_redirects (or email.allowed_redirects when it has none)", o.InvitationLink)
		}
	}
	if len(errs) == 0 {
		s.Organizations = o
	}
	return errs
}

// roleNameProblem says what is wrong with a role name, or "" when it is fine. Realm roles
// and organization roles follow the same rules.
func roleNameProblem(name string) string {
	if !namePattern.MatchString(name) {
		return "must be lowercase letters and digits, optionally separated by single '-' or '_'"
	}
	return ""
}

// StaleRoles returns the roles in roles that the organizations section doesn't declare.
func (o *OrgSettings) StaleRoles(roles []string) []string {
	var out []string
	for _, r := range roles {
		if !o.Declared(r) {
			out = append(out, r)
		}
	}
	return out
}

// organizationDoc is the part of collection.yaml loadOrganization reads.
type organizationDoc struct {
	Organization *string `yaml:"organization"`
	OnOrgDelete  *struct {
		Action string `yaml:"action"`
	} `yaml:"on_org_delete"`
	Team         *string `yaml:"team"`
	OnTeamDelete *struct {
		Action string `yaml:"action"`
	} `yaml:"on_team_delete"`
	OnOwnerDelete *struct {
		InOrg  string `yaml:"in_org"`
		Action string `yaml:"action"`
	} `yaml:"on_owner_delete"`
}

// loadOrganization reads collection.yaml's organization policy into c. It runs before the
// rules, which may use the organization helpers of a collection that has one.
func loadOrganization(c *Collection, settings RealmSettings, path string) error {
	c.Organization = OrgNone
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc organizationDoc
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		// Another section's mistake is reported by the loader that reads it.
		if strings.Contains(err.Error(), "organization") || strings.Contains(err.Error(), "on_org_delete") || strings.Contains(err.Error(), "team") {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: "+format, append([]any{path}, args...)...)
	}
	if doc.Organization != nil {
		switch *doc.Organization {
		case OrgNone, OrgOptional, OrgRequired:
			c.Organization = *doc.Organization
		default:
			return fail("organization: must be required, optional or none, got %q", *doc.Organization)
		}
	}
	inOrg := ""
	if d := doc.OnOwnerDelete; d != nil && d.InOrg != "" {
		inOrg = d.InOrg
		switch inOrg {
		case InOrgKeep, InOrgApply:
		default:
			return fail("on_owner_delete.in_org: must be keep or apply, got %q", inOrg)
		}
		if d.Action == "" {
			return fail("on_owner_delete.in_org: needs an action (the in_org setting says whether organization documents follow it)")
		}
	}
	if c.Organization == OrgNone {
		if doc.OnOrgDelete != nil {
			return fail("on_org_delete: only applies to a collection with `organization: required` or `optional`")
		}
		if inOrg != "" {
			return fail("on_owner_delete.in_org: only applies to a collection with `organization: required` or `optional`")
		}
		return loadTeam(c, doc, fail)
	}
	if settings.Organizations == nil {
		return fail("organization: needs the realm to enable organizations (an `organizations:` section in realm.yaml)")
	}
	if c.CustomID != nil && c.CustomID.User {
		return fail("organization: can't be combined with `custom_id: user`, which would allow one document per user across every organization")
	}
	if doc.OnOrgDelete == nil {
		return fail("on_org_delete: a collection with `organization` must say what happens to its documents when the organization is deleted: `action: delete` or `action: keep`")
	}
	switch doc.OnOrgDelete.Action {
	case OrgDelete, OrgKeep:
		c.OnOrgDelete = doc.OnOrgDelete.Action
	default:
		return fail("on_org_delete.action: must be delete or keep, got %q", doc.OnOrgDelete.Action)
	}
	c.OwnerDeleteInOrg = InOrgKeep
	if inOrg != "" {
		c.OwnerDeleteInOrg = inOrg
	}
	return loadTeam(c, doc, fail)
}

// loadTeam reads `team` and `on_team_delete`: documents that belong to a team of their
// organization (design/team-access.md §4).
func loadTeam(c *Collection, doc organizationDoc, fail func(string, ...any) error) error {
	c.Team = TeamNone
	if doc.Team != nil {
		switch *doc.Team {
		case TeamNone, TeamOptional, TeamRequired:
			c.Team = *doc.Team
		default:
			return fail("team: must be required, optional or none, got %q", *doc.Team)
		}
	}
	if !c.InTeams() {
		if doc.OnTeamDelete != nil {
			return fail("on_team_delete: only applies to a collection with `team: required` or `optional`")
		}
		return nil
	}
	if !c.InOrganizations() {
		return fail("team: needs `organization: required` or `optional`: a team belongs to an organization")
	}
	if doc.OnTeamDelete == nil {
		return fail("on_team_delete: a collection with `team` must say what happens to its documents when the team is deleted: `action: delete` or `action: keep`")
	}
	switch doc.OnTeamDelete.Action {
	case OrgDelete, OrgKeep:
		c.OnTeamDelete = doc.OnTeamDelete.Action
	default:
		return fail("on_team_delete.action: must be delete or keep, got %q", doc.OnTeamDelete.Action)
	}
	return nil
}

// InTeams reports whether documents of the collection can belong to a team.
func (c *Collection) InTeams() bool {
	return c.Team == TeamRequired || c.Team == TeamOptional
}

// InOrganizations reports whether documents of the collection can belong to an organization.
func (c *Collection) InOrganizations() bool {
	return c.Organization == OrgRequired || c.Organization == OrgOptional
}

// orgRoles are the organization role names of the realm; nil when it has no organizations.
func orgRoles(s RealmSettings) []string {
	if s.Organizations == nil {
		return nil
	}
	return s.Organizations.RoleNames()
}

// teamRoles are the team role names of the realm; nil when it has no organizations.
func teamRoles(s RealmSettings) []string {
	if s.Organizations == nil {
		return nil
	}
	return s.Organizations.TeamRoleNames()
}

// addOrgIndex adds the index that serves the lists of one organization in the default order,
// which every query the caller's rules narrow to `_meta.org` uses.
func (c *Collection) addOrgIndex() {
	if !c.InOrganizations() {
		return
	}
	ix := Index{Keys: []IndexKey{{Field: MetaOrg}, {Field: "_meta.created_at"}, {Field: "id"}}}
	for _, have := range c.Indexes {
		if have.String() == ix.String() && !have.Unique {
			return
		}
	}
	c.Indexes = append(c.Indexes, ix)
}
