package auth

import (
	"context"
	"errors"
	"slices"

	"github.com/fernandezvara/backd/internal/registry"
)

// Roles that require a second factor (design/team-access.md §8): `require_mfa: true` on an
// organization role or a team role makes it count only for sessions that used one. The
// membership loader is the only place that decides: a grant counts when its organization, its
// role and its window all count.

type sessionMFAKey struct{}

// WithSessionMFA records on ctx whether the request's session used a second factor, which the
// membership of the caller is judged by. A context that doesn't say counts as without one.
func WithSessionMFA(ctx context.Context, mfa bool) context.Context {
	return context.WithValue(ctx, sessionMFAKey{}, mfa)
}

func sessionMFA(ctx context.Context) bool {
	mfa, _ := ctx.Value(sessionMFAKey{}).(bool)
	return mfa
}

// WithheldRole is a role that doesn't count for a session without a second factor.
type WithheldRole struct {
	Role string
	// Team is the team of a team role; "" for an organization role.
	Team string
	// Caps are what an organization role would let its holder manage; Grants are the team roles
	// a team role would let its holder give.
	Caps   registry.OrgCaps
	Grants []string
}

// RoleRequiresMFAError: the action needs a role that requires a second factor and the session
// used none.
type RoleRequiresMFAError struct {
	Role string
	// Team is set for a team role.
	Team string
}

func (e *RoleRequiresMFAError) Error() string {
	return "the role " + e.Role + " requires a second factor: sign in again with one"
}

// IsRoleRequiresMFA reports whether err is a RoleRequiresMFAError, and which.
func IsRoleRequiresMFA(err error) (*RoleRequiresMFAError, bool) {
	var e *RoleRequiresMFAError
	return e, errors.As(err, &e)
}

// resolved is what a member holds for a session: the roles that count and those withheld.
type resolved struct {
	Roles     []EffectiveRole
	TeamRoles map[string][]string
	// TeamGrants are the team role grants that count, with their windows.
	TeamGrants []TeamRoleGrant
	Withheld   []WithheldRole
}

// resolve computes the roles of a member that count for a session with or without a second
// factor: direct, default and team-carried organization roles, then team roles, each within its
// window and unless it requires a second factor the session lacks.
func (s *Users) resolve(ctx context.Context, m OrgMember, mfa bool) (resolved, error) {
	o := s.Settings.Organizations
	now := s.now()
	carried, err := s.teamRolesOf(ctx, m.Org, m.Teams)
	if err != nil {
		return resolved{}, err
	}
	var out resolved
	for _, e := range s.effective(m, now, carried) {
		if role, declared := o.Roles[e.Role]; declared && role.RequireMFA && !mfa {
			out.Withheld = append(out.Withheld, WithheldRole{Role: e.Role, Caps: role.Manage})
			continue
		}
		out.Roles = append(out.Roles, e)
	}
	out.TeamRoles = map[string][]string{}
	for _, g := range m.TeamRoles {
		if !g.Active(now) || !o.TeamRoleDeclared(g.Role) || slices.Contains(out.TeamRoles[g.Team], g.Role) {
			continue
		}
		if def := o.TeamRoles[g.Role]; def.RequireMFA && !mfa {
			out.Withheld = append(out.Withheld, WithheldRole{Role: g.Role, Team: g.Team, Grants: def.Grants})
			continue
		}
		out.TeamRoles[g.Team] = append(out.TeamRoles[g.Team], g.Role)
		out.TeamGrants = append(out.TeamGrants, g)
	}
	return out, nil
}

// forbidden is the error for a caller who lacks the capability need: the one that says a role
// requiring a second factor would have allowed it, when that is why, and ErrOrgForbidden otherwise.
func (m Membership) forbidden(need registry.OrgCaps) error {
	for _, w := range m.Withheld {
		if w.Team == "" && w.Caps.Has(need) {
			return &RoleRequiresMFAError{Role: w.Role}
		}
	}
	return ErrOrgForbidden
}

// requireCaps returns what the caller's roles let them manage, or why they lack need.
func (s *Users) requireCaps(m Membership, need registry.OrgCaps) (registry.OrgCaps, error) {
	cc := s.caps(m)
	if !cc.Has(need) {
		return 0, m.forbidden(need)
	}
	return cc, nil
}

// forbiddenToGrant is the error for a caller who can't give role in team: a withheld team role
// that would have let them says it needs a second factor.
func (m Membership) forbiddenToGrant(team, role string) error {
	for _, w := range m.Withheld {
		if w.Team == team && slices.Contains(w.Grants, role) {
			return &RoleRequiresMFAError{Role: w.Role, Team: team}
		}
	}
	return m.forbidden(registry.CapTeams)
}

// mfaRolesOf lists the roles a member holds now that require a second factor, sorted.
func (s *Users) mfaRolesOf(v MemberView) []string {
	o := s.Settings.Organizations
	var out []string
	for _, e := range v.Effective {
		if o.Roles[e.Role].RequireMFA && !slices.Contains(out, e.Role) {
			out = append(out, e.Role)
		}
	}
	for _, roles := range s.declaredTeamRoles(v.Member, v.Now) {
		for _, r := range roles {
			if o.TeamRoles[r].RequireMFA && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	slices.Sort(out)
	return out
}
