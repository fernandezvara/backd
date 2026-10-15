package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Teams (design/organizations.md §8): groups of members of an organization that carry
// organization roles. Teams don't own data; a member's effective roles are their direct roles
// plus the roles of their teams, loaded with the membership.

// Audit actions of teams; the target is org:<id>.
const (
	AuditOrgTeamCreate       = "org.team_create"        // details: team, name, roles
	AuditOrgTeamUpdate       = "org.team_update"        // details: team, name, roles
	AuditOrgTeamDelete       = "org.team_delete"        // details: team
	AuditOrgTeamMemberAdd    = "org.team_member_add"    // details: team, user
	AuditOrgTeamMemberRemove = "org.team_member_remove" // details: team, user
)

const maxTeamNameLength = 60

// checkTeamGrant applies the granting rules to the roles a team carries: with manage: roles any
// role may be given; otherwise only roles that open nothing the caller lacks. manage: teams is
// the gate of the operation itself and is checked by the caller.
func (s *Users) checkTeamGrant(callerCaps registry.OrgCaps, roles []string) error {
	o := s.Settings.Organizations
	for _, r := range roles {
		if !o.Declared(r) {
			return &OrgInvalidError{"roles", "\"" + r + "\" is not a role of this realm's organizations"}
		}
	}
	if callerCaps.Has(registry.CapRoles) || callerCaps.Covers(o.CapsOf(roles)) {
		return nil
	}
	return ErrOrgForbidden
}

func validTeamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		return "", &OrgInvalidError{"name", "is required"}
	case n > maxTeamNameLength:
		return "", &OrgInvalidError{"name", "must be at most 60 characters"}
	case strings.ContainsAny(name, "\n\r\t"):
		return "", &OrgInvalidError{"name", "can't have line breaks or tabs"}
	}
	return name, nil
}

// teamManager returns the caller's membership of the organization, which must let them manage
// teams.
func (s *Users) teamManager(ctx context.Context, userID, orgID string) (Membership, registry.OrgCaps, error) {
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return Membership{}, 0, err
	}
	cc, err := s.requireCaps(m, registry.CapTeams)
	if err != nil {
		return Membership{}, 0, err
	}
	return m, cc, nil
}

// OrgTeamExists reports whether the organization has a team with the id.
func (s *Users) OrgTeamExists(ctx context.Context, orgID, teamID string) bool {
	_, err := s.team(ctx, orgID, teamID)
	return err == nil
}

// OrgTeams lists an organization's teams to a member.
func (s *Users) OrgTeams(ctx context.Context, userID, orgID string) ([]OrgTeam, error) {
	if _, err := s.MembershipOf(ctx, userID, orgID); err != nil {
		return nil, err
	}
	return s.Store.ListOrgTeams(ctx, orgID)
}

// AdminOrgTeams lists an organization's teams.
func (s *Users) AdminOrgTeams(ctx context.Context, orgID string) ([]OrgTeam, error) {
	if _, err := s.orgs(); err != nil {
		return nil, err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return nil, err
	}
	return s.Store.ListOrgTeams(ctx, orgID)
}

// CreateOrgTeam makes a team. It needs manage: teams, and gives only roles the caller may.
func (s *Users) CreateOrgTeam(ctx context.Context, userID, orgID, name string, roles []string) (OrgTeam, error) {
	_, cc, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return OrgTeam{}, err
	}
	return s.createOrgTeam(ctx, cc, orgID, name, roles)
}

// AdminCreateOrgTeam is CreateOrgTeam for an administrator.
func (s *Users) AdminCreateOrgTeam(ctx context.Context, orgID, name string, roles []string) (OrgTeam, error) {
	if _, err := s.orgs(); err != nil {
		return OrgTeam{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return OrgTeam{}, err
	}
	if org.Status == OrgDeleted {
		return OrgTeam{}, ErrOrgNotActive
	}
	return s.createOrgTeam(ctx, registry.AllCaps, orgID, name, roles)
}

func (s *Users) createOrgTeam(ctx context.Context, cc registry.OrgCaps, orgID, name string, roles []string) (OrgTeam, error) {
	name, err := validTeamName(name)
	if err != nil {
		return OrgTeam{}, err
	}
	roles = uniqueStrings(roles)
	if err := s.checkTeamGrant(cc, roles); err != nil {
		return OrgTeam{}, err
	}
	existing, err := s.Store.ListOrgTeams(ctx, orgID)
	if err != nil {
		return OrgTeam{}, err
	}
	if len(existing) >= s.Settings.Organizations.MaxTeamsPerOrg {
		return OrgTeam{}, ErrTeamLimit
	}
	t := OrgTeam{ID: xid.New().String(), Org: orgID, Name: name, Roles: roles, CreatedAt: s.now()}
	if err := s.Store.CreateOrgTeam(ctx, t); err != nil {
		return OrgTeam{}, err
	}
	s.Audit(ctx, AuditOrgTeamCreate, orgTarget(orgID), map[string]any{"team": t.ID, "name": t.Name, "roles": t.Roles})
	return t, nil
}

func (s *Users) team(ctx context.Context, orgID, id string) (OrgTeam, error) {
	found, err := s.Store.OrgTeamsByIDs(ctx, orgID, []string{id})
	if err != nil {
		return OrgTeam{}, err
	}
	if len(found) == 0 {
		return OrgTeam{}, ErrNotFound
	}
	return found[0], nil
}

// UpdateOrgTeam renames a team or changes its roles; the roles it gains or loses follow the
// granting rules, and the organization keeps a manager.
func (s *Users) UpdateOrgTeam(ctx context.Context, userID, orgID, id string, name *string, roles []string, setRoles bool) (OrgTeam, error) {
	_, cc, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return OrgTeam{}, err
	}
	return s.updateOrgTeam(ctx, cc, orgID, id, name, roles, setRoles)
}

// AdminUpdateOrgTeam is UpdateOrgTeam for an administrator.
func (s *Users) AdminUpdateOrgTeam(ctx context.Context, orgID, id string, name *string, roles []string, setRoles bool) (OrgTeam, error) {
	if _, err := s.orgs(); err != nil {
		return OrgTeam{}, err
	}
	return s.updateOrgTeam(ctx, registry.AllCaps, orgID, id, name, roles, setRoles)
}

func (s *Users) updateOrgTeam(ctx context.Context, cc registry.OrgCaps, orgID, id string, name *string, roles []string, setRoles bool) (OrgTeam, error) {
	t, err := s.team(ctx, orgID, id)
	if err != nil {
		return OrgTeam{}, err
	}
	if name != nil {
		n, err := validTeamName(*name)
		if err != nil {
			return OrgTeam{}, err
		}
		t.Name = n
	}
	if setRoles {
		roles = uniqueStrings(roles)
		added, removed := diff(roles, t.Roles)
		if err := s.checkTeamGrant(cc, append(slices.Clone(added), removed...)); err != nil {
			return OrgTeam{}, err
		}
		if len(removed) > 0 && s.capsOfRoles(removed).Has(registry.CapOrg) {
			all, err := s.Store.ListOrgTeams(ctx, orgID)
			if err != nil {
				return OrgTeam{}, err
			}
			after := map[string][]string{}
			for _, x := range all {
				after[x.ID] = x.Roles
			}
			after[id] = roles
			if err := s.keepManagerWith(ctx, orgID, "", after); err != nil {
				return OrgTeam{}, err
			}
		}
		t.Roles = roles
	}
	var snaps []memberSnapshot
	if setRoles {
		snaps = s.snapshotTeam(ctx, orgID, id)
	}
	err = s.Store.Transact(ctx, func(ctx context.Context) error {
		if err := s.Store.UpdateOrgTeam(ctx, t); err != nil {
			return err
		}
		return s.teamChanged(ctx, orgID, snaps)
	})
	if err != nil {
		return OrgTeam{}, err
	}
	s.Audit(ctx, AuditOrgTeamUpdate, orgTarget(orgID), map[string]any{"team": t.ID, "name": t.Name, "roles": t.Roles})
	return t, nil
}

// DeleteOrgTeam removes a team from the organization and from every membership; the roles it
// carried go with it, and the organization keeps a manager.
func (s *Users) DeleteOrgTeam(ctx context.Context, userID, orgID, id string) error {
	_, cc, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return err
	}
	return s.deleteOrgTeam(ctx, cc, orgID, id)
}

// AdminDeleteOrgTeam is DeleteOrgTeam for an administrator.
func (s *Users) AdminDeleteOrgTeam(ctx context.Context, orgID, id string) error {
	if _, err := s.orgs(); err != nil {
		return err
	}
	return s.deleteOrgTeam(ctx, registry.AllCaps, orgID, id)
}

func (s *Users) deleteOrgTeam(ctx context.Context, cc registry.OrgCaps, orgID, id string) error {
	t, err := s.team(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.checkTeamGrant(cc, t.Roles); err != nil {
		return err
	}
	if s.capsOfRoles(t.Roles).Has(registry.CapOrg) {
		all, err := s.Store.ListOrgTeams(ctx, orgID)
		if err != nil {
			return err
		}
		after := map[string][]string{}
		for _, x := range all {
			if x.ID != id {
				after[x.ID] = x.Roles
			}
		}
		if err := s.keepManagerWith(ctx, orgID, "", after); err != nil {
			return err
		}
	}
	snaps := s.snapshotTeam(ctx, orgID, id)
	err = s.Store.Transact(ctx, func(ctx context.Context) error {
		if err := s.Store.DeleteOrgTeam(ctx, orgID, id); err != nil {
			return err
		}
		return s.teamChanged(ctx, orgID, snaps)
	})
	if err != nil {
		return err
	}
	// The collections' on_team_delete applies to its documents through a job.
	j, err := s.enqueueCleanup(ctx, TeamDeleteOrigin, "team-delete:"+id, OrgDeleteJob{Org: orgID, Team: id}, "")
	if err != nil {
		return err
	}
	s.Audit(ctx, AuditOrgTeamDelete, orgTarget(orgID), map[string]any{"team": id, "job_id": j.ID})
	return nil
}

// AddOrgTeamMember puts a member of the organization in a team; they gain its roles.
func (s *Users) AddOrgTeamMember(ctx context.Context, userID, orgID, id, target string) (MemberView, error) {
	_, cc, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return MemberView{}, err
	}
	return s.setTeamMembership(ctx, cc, orgID, id, target, true)
}

// RemoveOrgTeamMember takes a member out of a team.
func (s *Users) RemoveOrgTeamMember(ctx context.Context, userID, orgID, id, target string) (MemberView, error) {
	_, cc, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return MemberView{}, err
	}
	return s.setTeamMembership(ctx, cc, orgID, id, target, false)
}

// AdminSetOrgTeamMember adds or removes a member of a team as an administrator.
func (s *Users) AdminSetOrgTeamMember(ctx context.Context, orgID, id, target string, add bool) (MemberView, error) {
	if _, err := s.orgs(); err != nil {
		return MemberView{}, err
	}
	return s.setTeamMembership(ctx, registry.AllCaps, orgID, id, target, add)
}

func (s *Users) setTeamMembership(ctx context.Context, cc registry.OrgCaps, orgID, id, target string, add bool) (MemberView, error) {
	t, err := s.team(ctx, orgID, id)
	if err != nil {
		return MemberView{}, err
	}
	// Joining or leaving a team gives or takes its roles: the caller must be able to.
	if err := s.checkTeamGrant(cc, t.Roles); err != nil {
		return MemberView{}, err
	}
	m, err := s.Store.OrgMember(ctx, orgID, target)
	if errors.Is(err, ErrNotFound) {
		return MemberView{}, ErrNotOrgMember
	}
	if err != nil {
		return MemberView{}, err
	}
	before := m
	has := slices.Contains(m.Teams, id)
	switch {
	case add && has, !add && !has:
		return s.viewOf(ctx, m)
	case add:
		if len(m.Teams) >= s.Settings.Organizations.MaxTeamsPerMember {
			return MemberView{}, ErrMemberTeamsLimit
		}
		m.Teams = append(m.Teams, id)
	default:
		if s.capsOfRoles(t.Roles).Has(registry.CapOrg) {
			// The member may still be a manager some other way; keepManager looks at all of it.
			all, err := s.Store.ListOrgTeams(ctx, orgID)
			if err != nil {
				return MemberView{}, err
			}
			rest := map[string][]string{}
			for _, x := range all {
				rest[x.ID] = x.Roles
			}
			// The member without this team: they count as a manager only through what else they hold.
			held := m
			held.Teams = slices.DeleteFunc(slices.Clone(m.Teams), func(x string) bool { return x == id })
			if !s.isManager(held, rest) {
				if err := s.keepManagerWith(ctx, orgID, target, rest); err != nil {
					return MemberView{}, err
				}
			}
		}
		m.Teams = slices.DeleteFunc(slices.Clone(m.Teams), func(x string) bool { return x == id })
	}
	if err := s.putMember(ctx, &before, m, ""); err != nil {
		return MemberView{}, err
	}
	action := AuditOrgTeamMemberRemove
	if add {
		action = AuditOrgTeamMemberAdd
	}
	s.Audit(ctx, action, orgTarget(orgID), map[string]any{"team": id, "user": target})
	return s.viewOf(ctx, m)
}

// isManager reports whether the member holds a role with manage: org, directly or through the
// teams (team id → roles).
func (s *Users) isManager(m OrgMember, teamRoles map[string][]string) bool {
	eff := m.DirectRoles(s.now())
	for _, t := range m.Teams {
		eff = append(eff, teamRoles[t]...)
	}
	return s.capsOfRoles(eff).Has(registry.CapOrg)
}
