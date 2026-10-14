package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

// Organizations (design/organizations.md): groups of users owned by the realm, with roles the
// realm declares.

// Statuses of an organization.
const (
	OrgActive   = "active"
	OrgOrphaned = "orphaned" // its last manager was erased; members keep access
	OrgDeleted  = "deleted"
)

// Audit actions of organizations; the target is org:<id>, details never hold content.
const (
	AuditOrgCreate       = "org.create"        // details: by (self, admin), creator
	AuditOrgUpdate       = "org.update"        // details: fields
	AuditOrgMemberAdd    = "org.member_add"    // details: user, roles, by (invitation, admin)
	AuditOrgMemberLeave  = "org.member_leave"  // details: user
	AuditOrgMemberRemove = "org.member_remove" // details: user
	AuditOrgRoles        = "org.roles"         // details: user, added, removed
	AuditOrgClaims       = "org.claims"        // details: keys
	AuditOrgOwner        = "org.owner_assign"  // details: user
)

var (
	// ErrOrgsDisabled: the realm doesn't enable organizations.
	ErrOrgsDisabled = errors.New("organizations are not enabled in this realm")
	// ErrNotOrgMember covers a non-member and an unknown organization alike.
	ErrNotOrgMember = errors.New("not a member of that organization")
	// ErrOrgForbidden: the caller is a member without the capability the action needs.
	ErrOrgForbidden = errors.New("your roles in this organization don't allow that")
	// ErrOrgCreateNotAllowed: the realm's create rule refused the caller.
	ErrOrgCreateNotAllowed = errors.New("you are not allowed to create organizations")
	// ErrOrgLimit: the caller reached max_created_per_user.
	ErrOrgLimit = errors.New("the limit of organizations created by one user is reached")
	// ErrMembershipLimit: the user reached max_memberships_per_user.
	ErrMembershipLimit = errors.New("that user is a member of the most organizations a user may join")
	// ErrLastOrgManager: the last member who can manage the organization can't leave or lose that.
	ErrLastOrgManager = errors.New("the organization needs at least one member who can manage it")
	// ErrSlugTaken: another organization has that slug.
	ErrSlugTaken = errors.New("that slug is taken")
	// ErrOrgUserNotFound: the user an administrator named doesn't exist.
	ErrOrgUserNotFound = errors.New("user not found")
	// ErrOrgNotActive: the organization is deleted.
	ErrOrgNotActive = errors.New("the organization is not active")
)

// OrgInvalidError is a request the organization rules refuse, with the field to point at.
type OrgInvalidError struct{ Field, Reason string }

func (e *OrgInvalidError) Error() string { return e.Field + ": " + e.Reason }

// Org is an organization.
type Org struct {
	ID         string
	Name       string
	Slug       string // unique among organizations that aren't deleted; "" when none
	FormerSlug string // the slug a deleted organization had
	CreatedBy  string // user id; "" when created by a function
	Status     string
	RequireMFA bool
	Claims     map[string]any
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  time.Time
}

// OrgUpdate changes an organization; nil fields stay.
type OrgUpdate struct {
	Name *string
	Slug *string // "" removes it
	// RequireMFA turns the organization's second-factor requirement on or off.
	RequireMFA *bool
	Claims     map[string]any
	// ClaimsSet says Claims replaces the claims (an empty map clears them).
	ClaimsSet bool
	Status    string // "" leaves it
}

// RoleGrant is one organization role held by a member, with an optional window and who gave it.
type RoleGrant struct {
	Role      string
	From      time.Time // zero: from the start
	Until     time.Time // zero: no end
	GrantedBy string    // user id; "" for an administrator or function
	GrantedAt time.Time
}

// Active reports whether the grant counts at now.
func (g RoleGrant) Active(now time.Time) bool {
	return (g.From.IsZero() || !now.Before(g.From)) && (g.Until.IsZero() || now.Before(g.Until))
}

// OrgMember is a user's membership of one organization.
type OrgMember struct {
	Org       string
	User      string
	Roles     []RoleGrant // direct
	Teams     []string
	JoinedAt  time.Time
	InvitedBy string
}

// DirectRoles returns the names of the grants that count at now.
func (m OrgMember) DirectRoles(now time.Time) []string {
	var out []string
	for _, g := range m.Roles {
		if g.Active(now) && !slices.Contains(out, g.Role) {
			out = append(out, g.Role)
		}
	}
	return out
}

// EffectiveRole is a role a member holds and where it came from.
type EffectiveRole struct {
	Role string
	// Origin is "direct", "default" and the names of the teams that carry it.
	Origin []string
}

// Effective resolves the roles of a member: direct grants that count, the organization's
// default roles and the roles of the member's teams (teamRoles maps team names to their roles).
func (s *Users) effective(m OrgMember, now time.Time, teamRoles map[string][]string) []EffectiveRole {
	by := map[string]*EffectiveRole{}
	add := func(role, origin string) {
		e := by[role]
		if e == nil {
			e = &EffectiveRole{Role: role}
			by[role] = e
		}
		if !slices.Contains(e.Origin, origin) {
			e.Origin = append(e.Origin, origin)
		}
	}
	for _, r := range m.DirectRoles(now) {
		add(r, "direct")
	}
	for _, r := range s.Settings.Organizations.DefaultRoles {
		add(r, "default")
	}
	for _, team := range slices.Sorted(maps.Keys(teamRoles)) {
		for _, r := range teamRoles[team] {
			add(r, team)
		}
	}
	out := make([]EffectiveRole, 0, len(by))
	for _, r := range slices.Sorted(maps.Keys(by)) {
		out = append(out, *by[r])
	}
	return out
}

// Membership is what a user's request needs to know about one organization.
type Membership struct {
	Org    Org
	Member OrgMember
	Roles  []EffectiveRole
}

// RoleNames returns the effective role names.
func (m Membership) RoleNames() []string {
	out := make([]string, len(m.Roles))
	for i, r := range m.Roles {
		out[i] = r.Role
	}
	return out
}

func (s *Users) orgs() (*registry.OrgSettings, error) {
	if s.Settings.Organizations == nil {
		return nil, ErrOrgsDisabled
	}
	return s.Settings.Organizations, nil
}

// OrgsEnabled reports whether the realm has organizations.
func (s *Users) OrgsEnabled() bool { return s.Settings.Organizations != nil }

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

const (
	maxOrgNameLength = 100
	maxSlugLength    = 63
)

func validOrgName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return "", &OrgInvalidError{"name", "is required"}
	case n > maxOrgNameLength:
		return "", &OrgInvalidError{"name", fmt.Sprintf("must be at most %d characters", maxOrgNameLength)}
	}
	return name, nil
}

func validSlug(slug string) error {
	if slug == "" {
		return nil
	}
	if len(slug) > maxSlugLength || !slugPattern.MatchString(slug) {
		return &OrgInvalidError{"slug", fmt.Sprintf("must be lowercase letters and digits separated by single '-', at most %d characters", maxSlugLength)}
	}
	return nil
}

// ValidateClaims checks an organization's claims: a JSON object of at most 1 KiB.
func ValidateClaims(claims map[string]any) error {
	b, err := json.Marshal(claims)
	if err != nil {
		return &OrgInvalidError{"claims", "must be a JSON object"}
	}
	if len(b) > registry.MaxOrgClaimsBytes {
		return &OrgInvalidError{"claims", fmt.Sprintf("must be at most %d bytes", registry.MaxOrgClaimsBytes)}
	}
	return nil
}

// RulesUser is the user as rules see them.
func (p Principal) RulesUser() *rules.User {
	u := p.User
	return &rules.User{ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Roles: u.Roles, MFA: p.Session.MFA, Methods: append([]string{}, p.Session.Methods...), Consents: u.Consents}
}

// CreateOrg creates an organization with the caller as a member holding the creator roles.
func (s *Users) CreateOrg(ctx context.Context, p Principal, name, slug string) (Membership, error) {
	o, err := s.orgs()
	if err != nil {
		return Membership{}, err
	}
	ok, err := o.CreateRuleAllows(p.RulesUser(), s.now())
	if err != nil || !ok {
		return Membership{}, ErrOrgCreateNotAllowed
	}
	n, err := s.Store.CountOrgsCreatedBy(ctx, p.User.ID)
	if err != nil {
		return Membership{}, err
	}
	if n >= o.MaxCreatedPerUser {
		return Membership{}, ErrOrgLimit
	}
	return s.createOrg(ctx, o, name, slug, p.User.ID, "self")
}

// AdminCreateOrg creates an organization for a user, who holds the creator roles.
func (s *Users) AdminCreateOrg(ctx context.Context, name, slug, owner string) (Membership, error) {
	o, err := s.orgs()
	if err != nil {
		return Membership{}, err
	}
	if err := s.userExists(ctx, owner); err != nil {
		return Membership{}, err
	}
	return s.createOrg(ctx, o, name, slug, owner, "admin")
}

func (s *Users) createOrg(ctx context.Context, o *registry.OrgSettings, name, slug, creator, by string) (Membership, error) {
	name, err := validOrgName(name)
	if err != nil {
		return Membership{}, err
	}
	if err := validSlug(slug); err != nil {
		return Membership{}, err
	}
	if err := s.checkMembershipCap(ctx, o, creator); err != nil {
		return Membership{}, err
	}
	now := s.now()
	org := Org{ID: xid.New().String(), Name: name, Slug: slug, CreatedBy: creator, Status: OrgActive, CreatedAt: now, UpdatedAt: now}
	m := OrgMember{Org: org.ID, User: creator, JoinedAt: now}
	for _, r := range o.CreatorRoles {
		m.Roles = append(m.Roles, RoleGrant{Role: r, GrantedAt: now})
	}
	if err := s.Store.CreateOrg(ctx, org, m); err != nil {
		return Membership{}, err
	}
	s.Audit(ctx, AuditOrgCreate, orgTarget(org.ID), map[string]any{"by": by, "creator": creator})
	return s.membership(org, m), nil
}

func orgTarget(id string) string { return "org:" + id }

func (s *Users) userExists(ctx context.Context, id string) error {
	u, err := s.Store.UserByID(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && !u.ErasedAt.IsZero()) {
		return ErrOrgUserNotFound
	}
	return err
}

func (s *Users) checkMembershipCap(ctx context.Context, o *registry.OrgSettings, user string) error {
	n, err := s.Store.CountOrgMembershipsOf(ctx, user)
	if err != nil {
		return err
	}
	if n >= o.MaxMembershipsPerUser {
		return ErrMembershipLimit
	}
	return nil
}

func (s *Users) membership(org Org, m OrgMember) Membership {
	return Membership{Org: org, Member: m, Roles: s.effective(m, s.now(), nil)}
}

// MyOrgs lists the organizations a user belongs to, with their effective roles. Deleted
// organizations aren't listed.
func (s *Users) MyOrgs(ctx context.Context, userID string) ([]Membership, error) {
	o, err := s.orgs()
	if err != nil {
		return nil, err
	}
	members, err := s.Store.OrgMembershipsOf(ctx, userID, o.MaxMembershipsPerUser)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.Org
	}
	orgs, err := s.Store.OrgsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []Membership
	for _, m := range members {
		if org, ok := orgs[m.Org]; ok && org.Status != OrgDeleted {
			out = append(out, s.membership(org, m))
		}
	}
	slices.SortFunc(out, func(a, b Membership) int { return strings.Compare(a.Org.Name+a.Org.ID, b.Org.Name+b.Org.ID) })
	return out, nil
}

// MembershipOf returns the caller's membership of an organization, or ErrNotOrgMember for a
// non-member, an unknown organization and a deleted one alike.
func (s *Users) MembershipOf(ctx context.Context, userID, orgID string) (Membership, error) {
	if _, err := s.orgs(); err != nil {
		return Membership{}, err
	}
	m, err := s.Store.OrgMember(ctx, orgID, userID)
	if errors.Is(err, ErrNotFound) {
		return Membership{}, ErrNotOrgMember
	}
	if err != nil {
		return Membership{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if errors.Is(err, ErrNotFound) || (err == nil && org.Status == OrgDeleted) {
		return Membership{}, ErrNotOrgMember
	}
	if err != nil {
		return Membership{}, err
	}
	return s.membership(org, m), nil
}

// caps are what the caller's effective roles let them manage in the organization.
func (s *Users) caps(m Membership) registry.OrgCaps {
	return s.Settings.Organizations.CapsOf(m.RoleNames())
}

// UpdateOrg renames an organization or changes its slug; it needs manage: org.
func (s *Users) UpdateOrg(ctx context.Context, userID, orgID string, name, slug *string) (Membership, error) {
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return Membership{}, err
	}
	if !s.caps(m).Has(registry.CapOrg) {
		return Membership{}, ErrOrgForbidden
	}
	upd := OrgUpdate{}
	var fields []string
	if name != nil {
		n, err := validOrgName(*name)
		if err != nil {
			return Membership{}, err
		}
		upd.Name = &n
		fields = append(fields, "name")
	}
	if slug != nil {
		if err := validSlug(*slug); err != nil {
			return Membership{}, err
		}
		upd.Slug = slug
		fields = append(fields, "slug")
	}
	if len(fields) == 0 {
		return m, nil
	}
	org, err := s.Store.UpdateOrg(ctx, orgID, upd, s.now())
	if err != nil {
		return Membership{}, err
	}
	s.Audit(ctx, AuditOrgUpdate, orgTarget(orgID), map[string]any{"fields": fields})
	m.Org = org
	return m, nil
}

// MemberView is a member as the members list shows them.
type MemberView struct {
	Member    OrgMember
	Email     string // "" when the viewer may not see it
	Effective []EffectiveRole
	// Overlap lists roles held directly and through a team (or by default).
	Overlap []string
}

// Members lists an organization's members to a member, a page at a time sorted by user id.
func (s *Users) Members(ctx context.Context, userID, orgID, after string, limit int) ([]MemberView, bool, error) {
	viewer, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return nil, false, err
	}
	return s.memberViews(ctx, viewer, orgID, after, limit)
}

func (s *Users) memberViews(ctx context.Context, viewer Membership, orgID, after string, limit int) ([]MemberView, bool, error) {
	o := s.Settings.Organizations
	list, more, err := s.Store.ListOrgMembers(ctx, orgID, after, limit)
	if err != nil {
		return nil, false, err
	}
	// An administrator has no membership.
	showEmail := viewer.Member.User == "" || o.MembersVisibleTo == registry.MembersVisibleToMembers || s.caps(viewer).Any()
	now := s.now()
	out := make([]MemberView, len(list))
	for i, m := range list {
		v := MemberView{Member: m, Effective: s.effective(m, now, nil)}
		for _, e := range v.Effective {
			if slices.Contains(e.Origin, "direct") && len(e.Origin) > 1 {
				v.Overlap = append(v.Overlap, e.Role)
			}
		}
		if showEmail {
			if u, err := s.Store.UserByID(ctx, m.User); err == nil && u.ErasedAt.IsZero() {
				v.Email = u.Email
			}
		}
		out[i] = v
	}
	return out, more, nil
}

// orgCapsOfRoles are the capabilities of a set of role names.
func (s *Users) capsOfRoles(roles []string) registry.OrgCaps {
	return s.Settings.Organizations.CapsOf(roles)
}

// checkGrant applies the granting rules (design §5.1) for adding or removing roles: someone
// with manage: roles may change any role; someone with manage: members only roles that open
// nothing the caller lacks.
func (s *Users) checkGrant(callerCaps registry.OrgCaps, roles []string) error {
	o := s.Settings.Organizations
	for _, r := range roles {
		if !o.Declared(r) {
			return &OrgInvalidError{"roles", fmt.Sprintf("%q is not a role of this realm's organizations", r)}
		}
	}
	if callerCaps.Has(registry.CapRoles) {
		return nil
	}
	if !callerCaps.Has(registry.CapMembers) {
		return ErrOrgForbidden
	}
	if !callerCaps.Covers(s.capsOfRoles(roles)) {
		return ErrOrgForbidden
	}
	return nil
}

// UpdateMember replaces a member's direct roles with the given names.
func (s *Users) UpdateMember(ctx context.Context, userID, orgID, target string, roles []string) (MemberView, error) {
	caller, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return MemberView{}, err
	}
	return s.setRoles(ctx, s.caps(caller), userID, orgID, target, roles, true)
}

// AdminSetMemberRoles is UpdateMember for an administrator.
func (s *Users) AdminSetMemberRoles(ctx context.Context, orgID, target string, roles []string) (MemberView, error) {
	if _, err := s.orgs(); err != nil {
		return MemberView{}, err
	}
	return s.setRoles(ctx, registry.AllCaps, "", orgID, target, roles, false)
}

func (s *Users) setRoles(ctx context.Context, callerCaps registry.OrgCaps, by, orgID, target string, roles []string, strict bool) (MemberView, error) {
	if !callerCaps.Has(registry.CapRoles) && !callerCaps.Has(registry.CapMembers) {
		return MemberView{}, ErrOrgForbidden
	}
	tm, err := s.Store.OrgMember(ctx, orgID, target)
	if errors.Is(err, ErrNotFound) {
		return MemberView{}, ErrNotOrgMember
	}
	if err != nil {
		return MemberView{}, err
	}
	now := s.now()
	have := tm.DirectRoles(now)
	roles = uniqueStrings(roles)
	added, removed := diff(roles, have)
	// Someone holding more than the caller can't be changed, so a role can't be used to
	// take one over.
	targetCaps := s.capsOfRoles(tm.DirectRoles(now))
	if strict && !callerCaps.Covers(targetCaps) {
		return MemberView{}, ErrOrgForbidden
	}
	if err := s.checkGrant(callerCaps, append(slices.Clone(added), removed...)); err != nil {
		return MemberView{}, err
	}
	if len(added) == 0 && len(removed) == 0 {
		return s.viewOf(tm), nil
	}
	if !s.capsOfRoles(roles).Has(registry.CapOrg) && targetCaps.Has(registry.CapOrg) {
		if err := s.keepManager(ctx, orgID, target); err != nil {
			return MemberView{}, err
		}
	}
	var grants []RoleGrant
	for _, g := range tm.Roles {
		if g.Active(now) && slices.Contains(roles, g.Role) {
			grants = append(grants, g)
		} else if !g.Active(now) {
			grants = append(grants, g) // not in force now (a window): not touched here
		}
	}
	for _, r := range added {
		grants = append(grants, RoleGrant{Role: r, GrantedBy: by, GrantedAt: now})
	}
	tm.Roles = grants
	if err := s.Store.PutOrgMember(ctx, tm); err != nil {
		return MemberView{}, err
	}
	s.Audit(ctx, AuditOrgRoles, orgTarget(orgID), map[string]any{"user": target, "added": added, "removed": removed})
	return s.viewOf(tm), nil
}

func (s *Users) viewOf(m OrgMember) MemberView {
	return MemberView{Member: m, Effective: s.effective(m, s.now(), nil)}
}

// AdminAddMember adds a member directly, as an administrator (no invitation, audited).
func (s *Users) AdminAddMember(ctx context.Context, orgID, userID string, roles []string) (MemberView, error) {
	o, err := s.orgs()
	if err != nil {
		return MemberView{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return MemberView{}, err
	}
	if org.Status == OrgDeleted {
		return MemberView{}, ErrOrgNotActive
	}
	if err := s.userExists(ctx, userID); err != nil {
		return MemberView{}, err
	}
	roles = uniqueStrings(roles)
	for _, r := range roles {
		if !o.Declared(r) {
			return MemberView{}, &OrgInvalidError{"roles", fmt.Sprintf("%q is not a role of this realm's organizations", r)}
		}
	}
	return s.addMember(ctx, o, org, userID, roles, "", "admin")
}

// addMember creates the membership or adds roles to an existing one.
func (s *Users) addMember(ctx context.Context, o *registry.OrgSettings, org Org, userID string, roles []string, invitedBy, by string) (MemberView, error) {
	now := s.now()
	m, err := s.Store.OrgMember(ctx, org.ID, userID)
	switch {
	case errors.Is(err, ErrNotFound):
		if err := s.checkMembershipCap(ctx, o, userID); err != nil {
			return MemberView{}, err
		}
		m = OrgMember{Org: org.ID, User: userID, JoinedAt: now, InvitedBy: invitedBy}
	case err != nil:
		return MemberView{}, err
	}
	have := m.DirectRoles(now)
	for _, r := range roles {
		if !slices.Contains(have, r) {
			m.Roles = append(m.Roles, RoleGrant{Role: r, GrantedBy: invitedBy, GrantedAt: now})
		}
	}
	if err := s.Store.PutOrgMember(ctx, m); err != nil {
		return MemberView{}, err
	}
	s.Audit(ctx, AuditOrgMemberAdd, orgTarget(org.ID), map[string]any{"user": userID, "roles": roles, "by": by})
	if org.Status == OrgOrphaned && o.CapsOf(m.DirectRoles(now)).Has(registry.CapOrg) {
		if _, err := s.Store.UpdateOrg(ctx, org.ID, OrgUpdate{Status: OrgActive}, now); err != nil {
			return MemberView{}, err
		}
	}
	return s.viewOf(m), nil
}

// AdminAssignOwner gives a user the creator roles of an organization, for one left without a
// manager.
func (s *Users) AdminAssignOwner(ctx context.Context, orgID, userID string) (MemberView, error) {
	o, err := s.orgs()
	if err != nil {
		return MemberView{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return MemberView{}, err
	}
	if org.Status == OrgDeleted {
		return MemberView{}, ErrOrgNotActive
	}
	if err := s.userExists(ctx, userID); err != nil {
		return MemberView{}, err
	}
	v, err := s.addMember(ctx, o, org, userID, o.CreatorRoles, "", "admin")
	if err != nil {
		return MemberView{}, err
	}
	s.Audit(ctx, AuditOrgOwner, orgTarget(orgID), map[string]any{"user": userID})
	return v, nil
}

// RemoveMember removes a member; it needs manage: members, and the member can't hold more
// than the caller.
func (s *Users) RemoveMember(ctx context.Context, userID, orgID, target string) error {
	caller, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return err
	}
	cc := s.caps(caller)
	if !cc.Has(registry.CapMembers) {
		return ErrOrgForbidden
	}
	tm, err := s.Store.OrgMember(ctx, orgID, target)
	if errors.Is(err, ErrNotFound) {
		return ErrNotOrgMember
	}
	if err != nil {
		return err
	}
	if !cc.Covers(s.capsOfRoles(tm.DirectRoles(s.now()))) {
		return ErrOrgForbidden
	}
	return s.removeMember(ctx, orgID, tm, AuditOrgMemberRemove)
}

// AdminRemoveMember removes a member as an administrator.
func (s *Users) AdminRemoveMember(ctx context.Context, orgID, target string) error {
	if _, err := s.orgs(); err != nil {
		return err
	}
	tm, err := s.Store.OrgMember(ctx, orgID, target)
	if errors.Is(err, ErrNotFound) {
		return ErrNotOrgMember
	}
	if err != nil {
		return err
	}
	return s.removeMember(ctx, orgID, tm, AuditOrgMemberRemove)
}

// LeaveOrg removes the caller from an organization.
func (s *Users) LeaveOrg(ctx context.Context, userID, orgID string) error {
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return err
	}
	return s.removeMember(ctx, orgID, m.Member, AuditOrgMemberLeave)
}

func (s *Users) removeMember(ctx context.Context, orgID string, m OrgMember, action string) error {
	if s.capsOfRoles(m.DirectRoles(s.now())).Has(registry.CapOrg) {
		if err := s.keepManager(ctx, orgID, m.User); err != nil {
			return err
		}
	}
	if err := s.Store.DeleteOrgMember(ctx, orgID, m.User); err != nil {
		return err
	}
	s.Audit(ctx, action, orgTarget(orgID), map[string]any{"user": m.User})
	return nil
}

// keepManager returns ErrLastOrgManager when user is the only member who can manage the
// organization.
func (s *Users) keepManager(ctx context.Context, orgID, user string) error {
	o := s.Settings.Organizations
	var managers []string
	for name, r := range o.Roles {
		if r.Manage.Has(registry.CapOrg) {
			managers = append(managers, name)
		}
	}
	list, err := s.Store.OrgMembersWithRoles(ctx, orgID, managers)
	if err != nil {
		return err
	}
	now := s.now()
	for _, m := range list {
		if m.User != user && o.CapsOf(m.DirectRoles(now)).Has(registry.CapOrg) {
			return nil
		}
	}
	return ErrLastOrgManager
}

// AdminSetClaims replaces an organization's claims.
func (s *Users) AdminSetClaims(ctx context.Context, orgID string, claims map[string]any) (Org, error) {
	if _, err := s.orgs(); err != nil {
		return Org{}, err
	}
	if claims == nil {
		claims = map[string]any{}
	}
	if err := ValidateClaims(claims); err != nil {
		return Org{}, err
	}
	org, err := s.Store.UpdateOrg(ctx, orgID, OrgUpdate{Claims: claims, ClaimsSet: true}, s.now())
	if err != nil {
		return Org{}, err
	}
	s.Audit(ctx, AuditOrgClaims, orgTarget(orgID), map[string]any{"keys": slices.Sorted(maps.Keys(claims))})
	return org, nil
}

// AdminOrg returns an organization with its status, whatever it is.
func (s *Users) AdminOrg(ctx context.Context, orgID string) (Org, error) {
	if _, err := s.orgs(); err != nil {
		return Org{}, err
	}
	return s.Store.OrgByID(ctx, orgID)
}

// AdminOrgs lists organizations, optionally of one status, newest first.
func (s *Users) AdminOrgs(ctx context.Context, status string) ([]Org, error) {
	if _, err := s.orgs(); err != nil {
		return nil, err
	}
	return s.Store.ListOrgs(ctx, status)
}

// AdminMembers lists an organization's members, with emails.
func (s *Users) AdminMembers(ctx context.Context, orgID, after string, limit int) ([]MemberView, bool, error) {
	if _, err := s.orgs(); err != nil {
		return nil, false, err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return nil, false, err
	}
	return s.memberViews(ctx, Membership{}, orgID, after, limit)
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// diff returns what is in want and not in have (added), and in have and not in want (removed).
func diff(want, have []string) (added, removed []string) {
	for _, w := range want {
		if !slices.Contains(have, w) {
			added = append(added, w)
		}
	}
	for _, h := range have {
		if !slices.Contains(want, h) {
			removed = append(removed, h)
		}
	}
	return added, removed
}

// Counts reports whether memberships of the organization count for a session: it is active
// (or orphaned: its members keep access) and its require_mfa is met.
func (o Org) Counts(mfa bool) bool {
	return (o.Status == OrgActive || o.Status == OrgOrphaned) && (!o.RequireMFA || mfa)
}

// RuleOrgs returns the memberships of a user that count for a session, in the form access
// rules read them: the roles the user holds there (direct, default and through teams), the
// teams and the organization's claims. Loaded when a rule asks, never cached, bounded by
// max_memberships_per_user.
func (s *Users) RuleOrgs(ctx context.Context, userID string, mfa bool) (map[string]*rules.UserOrg, error) {
	out := map[string]*rules.UserOrg{}
	o := s.Settings.Organizations
	if o == nil {
		return out, nil
	}
	members, err := s.Store.OrgMembershipsOf(ctx, userID, o.MaxMembershipsPerUser)
	if err != nil || len(members) == 0 {
		return out, err
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.Org
	}
	orgs, err := s.Store.OrgsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, m := range members {
		org, ok := orgs[m.Org]
		if !ok || !org.Counts(mfa) {
			continue
		}
		eff := s.effective(m, now, nil)
		roles := make([]string, 0, len(eff))
		for _, e := range eff {
			if o.Declared(e.Role) { // a role the realm no longer declares grants nothing
				roles = append(roles, e.Role)
			}
		}
		out[m.Org] = &rules.UserOrg{Roles: roles, Teams: nonNilStrings(m.Teams), Claims: org.Claims}
	}
	return out, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// OrgActive reports whether an active (or orphaned) organization with the id exists.
func (s *Users) OrgActive(ctx context.Context, id string) bool {
	org, err := s.Store.OrgByID(ctx, id)
	return err == nil && (org.Status == OrgActive || org.Status == OrgOrphaned)
}
