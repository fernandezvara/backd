package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Team roles (design/team-access.md §3): roles held within one team of an organization, by
// members of the organization, whether or not they belong to the team. They use the grant shape
// of organization roles (RoleGrant) and never give organization or realm roles.

// Audit actions of team roles; the target is org:<id>.
const (
	AuditOrgTeamRoleGrant  = "org.team_role_grant"  // details: team, user, role, from, until
	AuditOrgTeamRoleChange = "org.team_role_change" // details: team, user, role, from, until
	AuditOrgTeamRoleRevoke = "org.team_role_revoke" // details: team, user, role
)

// ErrTeamRoleLimit: the member holds the most team role grants one may.
var ErrTeamRoleLimit = errors.New("that member holds the most team roles a member may")

// ActiveTeamRoles returns the team roles that count at now, by team id.
func (m OrgMember) ActiveTeamRoles(now time.Time) map[string][]string {
	out := map[string][]string{}
	for _, g := range m.TeamRoles {
		if g.Active(now) && !slices.Contains(out[g.Team], g.Role) {
			out[g.Team] = append(out[g.Team], g.Role)
		}
	}
	return out
}

// declaredTeamRoles returns the team roles of the member that count at now and that the realm
// declares: a role removed from realm.yaml grants nothing.
func (s *Users) declaredTeamRoles(m OrgMember, now time.Time) map[string][]string {
	out := map[string][]string{}
	for team, roles := range m.ActiveTeamRoles(now) {
		for _, r := range roles {
			if s.Settings.Organizations.TeamRoleDeclared(r) {
				out[team] = append(out[team], r)
			}
		}
	}
	return out
}

// TeamMemberView is a member or role holder of one team.
type TeamMemberView struct {
	MemberView
	// InTeam says the member belongs to the team (a holder may only hold a role in it).
	InTeam bool
	// Roles are the team roles the member holds in this team, in force now.
	Roles []string
}

// TeamMembers lists the members and role holders of a team to a member of the organization,
// honoring windows. With role, only the holders of that role in force now.
func (s *Users) TeamMembers(ctx context.Context, userID, orgID, teamID, role, after string, limit int) ([]TeamMemberView, bool, error) {
	viewer, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return nil, false, err
	}
	return s.teamMembers(ctx, viewer, orgID, teamID, role, after, limit)
}

// AdminTeamMembers is TeamMembers for an administrator.
func (s *Users) AdminTeamMembers(ctx context.Context, orgID, teamID, role, after string, limit int) ([]TeamMemberView, bool, error) {
	if _, err := s.orgs(); err != nil {
		return nil, false, err
	}
	if _, err := s.Store.OrgByID(ctx, orgID); err != nil {
		return nil, false, err
	}
	return s.teamMembers(ctx, Membership{}, orgID, teamID, role, after, limit)
}

func (s *Users) teamMembers(ctx context.Context, viewer Membership, orgID, teamID, role, after string, limit int) ([]TeamMemberView, bool, error) {
	o := s.Settings.Organizations
	if role != "" && !o.TeamRoleDeclared(role) {
		return nil, false, &OrgInvalidError{"role", fmt.Sprintf("%q is not a team role of this realm", role)}
	}
	if _, err := s.team(ctx, orgID, teamID); err != nil {
		return nil, false, err
	}
	now := s.now()
	list, more, err := s.Store.OrgTeamHolders(ctx, orgID, teamID, role, now, after, limit)
	if err != nil {
		return nil, false, err
	}
	views, err := s.viewsOf(ctx, viewer, orgID, list)
	if err != nil {
		return nil, false, err
	}
	out := make([]TeamMemberView, len(views))
	for i, v := range views {
		out[i] = TeamMemberView{MemberView: v, InTeam: slices.Contains(v.Member.Teams, teamID), Roles: s.declaredTeamRoles(v.Member, now)[teamID]}
	}
	return out, more, nil
}

// GrantTeamRole gives a member a team role in a team. It needs manage: teams.
func (s *Users) GrantTeamRole(ctx context.Context, userID, orgID, teamID, target, role string) (TeamMemberView, error) {
	_, _, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return TeamMemberView{}, err
	}
	return s.setTeamRole(ctx, userID, orgID, teamID, target, role, true)
}

// RevokeTeamRole takes a team role from a member. It needs manage: teams.
func (s *Users) RevokeTeamRole(ctx context.Context, userID, orgID, teamID, target, role string) (TeamMemberView, error) {
	_, _, err := s.teamManager(ctx, userID, orgID)
	if err != nil {
		return TeamMemberView{}, err
	}
	return s.setTeamRole(ctx, userID, orgID, teamID, target, role, false)
}

// AdminGrantTeamRole is GrantTeamRole for an administrator.
func (s *Users) AdminGrantTeamRole(ctx context.Context, orgID, teamID, target, role string) (TeamMemberView, error) {
	return s.adminSetTeamRole(ctx, orgID, teamID, target, role, true)
}

// AdminRevokeTeamRole is RevokeTeamRole for an administrator.
func (s *Users) AdminRevokeTeamRole(ctx context.Context, orgID, teamID, target, role string) (TeamMemberView, error) {
	return s.adminSetTeamRole(ctx, orgID, teamID, target, role, false)
}

func (s *Users) adminSetTeamRole(ctx context.Context, orgID, teamID, target, role string, add bool) (TeamMemberView, error) {
	if _, err := s.orgs(); err != nil {
		return TeamMemberView{}, err
	}
	org, err := s.Store.OrgByID(ctx, orgID)
	if err != nil {
		return TeamMemberView{}, err
	}
	if org.Status == OrgDeleted {
		return TeamMemberView{}, ErrOrgNotActive
	}
	return s.setTeamRole(ctx, "", orgID, teamID, target, role, add)
}

// setTeamRole adds or removes the grant of role in team to target. The caller's right to do so
// was checked by the caller of this function; by is "" for an administrator.
func (s *Users) setTeamRole(ctx context.Context, by, orgID, teamID, target, role string, add bool) (TeamMemberView, error) {
	o := s.Settings.Organizations
	if !o.TeamRoleDeclared(role) {
		return TeamMemberView{}, &OrgInvalidError{"role", fmt.Sprintf("%q is not a team role of this realm", role)}
	}
	if _, err := s.team(ctx, orgID, teamID); err != nil {
		return TeamMemberView{}, err
	}
	tm, err := s.Store.OrgMember(ctx, orgID, target)
	if errors.Is(err, ErrNotFound) {
		return TeamMemberView{}, ErrNotOrgMember
	}
	if err != nil {
		return TeamMemberView{}, err
	}
	now := s.now()
	held := slices.IndexFunc(tm.TeamRoles, func(g TeamRoleGrant) bool { return g.Team == teamID && g.Role == role })
	if add == (held >= 0) {
		return s.teamViewOf(ctx, tm, teamID) // already as asked
	}
	action := AuditOrgTeamRoleRevoke
	if add {
		if len(tm.TeamRoles) >= registry.MaxTeamRolesPerMember {
			return TeamMemberView{}, ErrTeamRoleLimit
		}
		tm.TeamRoles = append(slices.Clone(tm.TeamRoles), TeamRoleGrant{Team: teamID, RoleGrant: RoleGrant{Role: role, GrantedBy: by, GrantedAt: now}})
		action = AuditOrgTeamRoleGrant
	} else {
		tm.TeamRoles = slices.Delete(slices.Clone(tm.TeamRoles), held, held+1)
	}
	if err := s.Store.PutOrgMember(ctx, tm); err != nil {
		return TeamMemberView{}, err
	}
	s.Audit(ctx, action, orgTarget(orgID), map[string]any{"team": teamID, "user": target, "role": role})
	return s.teamViewOf(ctx, tm, teamID)
}

func (s *Users) teamViewOf(ctx context.Context, m OrgMember, teamID string) (TeamMemberView, error) {
	v, err := s.viewOf(ctx, m)
	if err != nil {
		return TeamMemberView{}, err
	}
	return TeamMemberView{MemberView: v, InTeam: slices.Contains(m.Teams, teamID), Roles: s.declaredTeamRoles(m, s.now())[teamID]}, nil
}
