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

// TeamRoleHistory is how long a grant stays visible after its window ended.
const TeamRoleHistory = 90 * 24 * time.Hour

// ErrTeamRoleNotHeld: the member doesn't hold the team role whose window a request changes.
var ErrTeamRoleNotHeld = errors.New("the member doesn't hold that role in the team")

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

// TeamRoleWindow is the period a team role grant counts in; a zero end is open.
type TeamRoleWindow struct{ From, Until time.Time }

// Valid checks the window: it ends after it starts.
func (w TeamRoleWindow) Valid() error {
	if !w.From.IsZero() && !w.Until.IsZero() && !w.Until.After(w.From) {
		return &OrgInvalidError{"until", "must be after from"}
	}
	return nil
}

// TeamRoleOp is what a request does to a team role grant.
type TeamRoleOp int

// The operations on a team role grant.
const (
	TeamRoleGrantOp  TeamRoleOp = iota // give it, for the window
	TeamRoleWindowOp                   // replace the window of a held grant
	TeamRoleRevokeOp                   // take it away
)

// canGrantTeamRole checks the caller's right to give, change or take away role in team: with
// manage: teams any team role in any team; otherwise a holder of a team role of that team whose
// `grants` lists it (design/team-access.md §5), in that team only.
func (s *Users) canGrantTeamRole(ctx context.Context, userID, orgID, teamID, role string) error {
	m, err := s.MembershipOf(ctx, userID, orgID)
	if err != nil {
		return err
	}
	if s.caps(m).Has(registry.CapTeams) {
		return nil
	}
	o := s.Settings.Organizations
	for _, held := range m.TeamRoles[teamID] {
		if slices.Contains(o.TeamRoles[held].Grants, role) {
			return nil
		}
	}
	return m.forbiddenToGrant(teamID, role)
}

// SetTeamRole gives, re-windows or revokes a team role of a member in a team. caller is the user
// making the request, who needs manage: teams or a team role of that team that grants role; ""
// stands for an administrator or a function, who may do it in any active organization.
func (s *Users) SetTeamRole(ctx context.Context, caller, orgID, teamID, target, role string, op TeamRoleOp, w TeamRoleWindow) (TeamMemberView, error) {
	if caller != "" {
		if err := s.canGrantTeamRole(ctx, caller, orgID, teamID, role); err != nil {
			return TeamMemberView{}, err
		}
	} else {
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
	}
	return s.setTeamRole(ctx, caller, orgID, teamID, target, role, op, w)
}

// setTeamRole applies op to the grant of role in team to target. The caller's right to do so was
// checked; by is "" for an administrator.
func (s *Users) setTeamRole(ctx context.Context, by, orgID, teamID, target, role string, op TeamRoleOp, w TeamRoleWindow) (TeamMemberView, error) {
	o := s.Settings.Organizations
	if !o.TeamRoleDeclared(role) {
		return TeamMemberView{}, &OrgInvalidError{"role", fmt.Sprintf("%q is not a team role of this realm", role)}
	}
	if err := w.Valid(); err != nil {
		return TeamMemberView{}, err
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
	held := slices.IndexFunc(tm.TeamRoles, func(g TeamRoleGrant) bool { return g.Team == teamID && g.Role == role })
	details := map[string]any{"team": teamID, "user": target, "role": role}
	var action string
	switch op {
	case TeamRoleGrantOp:
		if held >= 0 {
			return s.teamViewOf(ctx, tm, teamID) // already held: a window changes with PATCH
		}
		if len(tm.TeamRoles) >= registry.MaxTeamRolesPerMember {
			return TeamMemberView{}, ErrTeamRoleLimit
		}
		tm.TeamRoles = append(slices.Clone(tm.TeamRoles), TeamRoleGrant{Team: teamID, RoleGrant: RoleGrant{Role: role, From: w.From, Until: w.Until, GrantedBy: by, GrantedAt: s.now()}})
		action = AuditOrgTeamRoleGrant
	case TeamRoleWindowOp:
		if held < 0 {
			return TeamMemberView{}, ErrTeamRoleNotHeld
		}
		tm.TeamRoles = slices.Clone(tm.TeamRoles)
		tm.TeamRoles[held].From, tm.TeamRoles[held].Until = w.From, w.Until
		action = AuditOrgTeamRoleChange
	case TeamRoleRevokeOp:
		if held < 0 {
			return s.teamViewOf(ctx, tm, teamID)
		}
		tm.TeamRoles = slices.Delete(slices.Clone(tm.TeamRoles), held, held+1)
		action = AuditOrgTeamRoleRevoke
	}
	if op != TeamRoleRevokeOp {
		if !w.From.IsZero() {
			details["from"] = w.From.UTC().Format(time.RFC3339)
		}
		if !w.Until.IsZero() {
			details["until"] = w.Until.UTC().Format(time.RFC3339)
		}
	}
	if err := s.Store.PutOrgMember(ctx, tm); err != nil {
		return TeamMemberView{}, err
	}
	s.Audit(ctx, action, orgTarget(orgID), details)
	return s.teamViewOf(ctx, tm, teamID)
}

func (s *Users) teamViewOf(ctx context.Context, m OrgMember, teamID string) (TeamMemberView, error) {
	v, err := s.viewOf(ctx, m)
	if err != nil {
		return TeamMemberView{}, err
	}
	return TeamMemberView{MemberView: v, InTeam: slices.Contains(m.Teams, teamID), Roles: s.declaredTeamRoles(m, s.now())[teamID]}, nil
}

// SweepTeamRoles removes the team role grants that ended more than TeamRoleHistory ago. Running
// it in every worker is harmless.
func (s *Users) SweepTeamRoles(ctx context.Context) (int64, error) {
	if s.Settings.Organizations == nil {
		return 0, nil
	}
	return s.Store.PurgeExpiredTeamRoles(ctx, s.now().Add(-TeamRoleHistory))
}
