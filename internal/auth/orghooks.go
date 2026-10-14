package auth

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Membership hooks (design/team-access.md §7): internal async functions called, through the job
// queue like account.on_signup, when people join, leave or change roles in an organization.
// The queue promises no order, so every event carries a `seq` that grows per organization and
// user; a handler that must not apply a stale event keeps the last one it saw.

// Origins of the jobs that call the hooks.
const (
	OrgMemberJoinedOrigin = "backd:org.member_joined"
	OrgMemberLeftOrigin   = "backd:org.member_left"
	OrgRolesChangedOrigin = "backd:org.roles_changed"
)

// Why a member left an organization (the `reason` of on_member_left).
const (
	LeftByThemselves = "left"
	LeftRemoved      = "removed"
	LeftErased       = "erased"
)

// defaultOrgHookTimeout is the lease of a hook's job when the function declares none.
const defaultOrgHookTimeout = 30 * time.Second

// HookTeamRole is a team role as hooks see it.
type HookTeamRole struct {
	Team  string `json:"team"`
	Role  string `json:"role"`
	From  string `json:"from,omitempty"`
	Until string `json:"until,omitempty"`
}

// MemberAccess is what a member holds in an organization, as hooks see it: direct roles,
// teams, team roles (all of them, with their windows) and the effective organization roles
// now, which change when a team's roles do.
type MemberAccess struct {
	Roles          []string       `json:"roles"`
	Teams          []string       `json:"teams"`
	TeamRoles      []HookTeamRole `json:"team_roles"`
	EffectiveRoles []string       `json:"effective_roles"`
}

// OrgMemberJoined is the input of on_member_joined.
type OrgMemberJoined struct {
	Event      string `json:"event"`
	Org        string `json:"org"`
	User       string `json:"user"`
	Seq        int64  `json:"seq"`
	Invitation string `json:"invitation,omitempty"`
	MemberAccess
}

// OrgMemberLeft is the input of on_member_left.
type OrgMemberLeft struct {
	Event  string `json:"event"`
	Org    string `json:"org"`
	User   string `json:"user"`
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
}

// OrgRolesChanged is the input of on_roles_changed.
type OrgRolesChanged struct {
	Event  string       `json:"event"`
	Org    string       `json:"org"`
	User   string       `json:"user"`
	Seq    int64        `json:"seq"`
	Before MemberAccess `json:"before"`
	After  MemberAccess `json:"after"`
}

func hookRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// accessOf describes what the member holds now. Team roles keep their windows: a grant that
// hasn't opened yet or has closed is part of what the member holds, and its boundaries are
// not events.
func (s *Users) accessOf(ctx context.Context, m OrgMember) MemberAccess {
	a := MemberAccess{Roles: []string{}, Teams: slices.Clone(nonNilStrings(m.Teams)), TeamRoles: []HookTeamRole{}, EffectiveRoles: []string{}}
	for _, g := range m.Roles {
		a.Roles = append(a.Roles, g.Role)
	}
	for _, g := range m.TeamRoles {
		a.TeamRoles = append(a.TeamRoles, HookTeamRole{Team: g.Team, Role: g.Role, From: hookRFC3339(g.From), Until: hookRFC3339(g.Until)})
	}
	if carried, err := s.teamRolesOf(ctx, m.Org, m.Teams); err == nil {
		for _, e := range s.effective(m, s.now(), carried) {
			a.EffectiveRoles = append(a.EffectiveRoles, e.Role)
		}
	}
	slices.Sort(a.Roles)
	slices.Sort(a.Teams)
	slices.SortFunc(a.TeamRoles, func(x, y HookTeamRole) int {
		return strings.Compare(x.Team+"\x00"+x.Role, y.Team+"\x00"+y.Role)
	})
	return a
}

// sameAccess reports whether two descriptions are the same.
func sameAccess(a, b MemberAccess) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// queueOrgHook queues the call of a hook for one organization and user. A failure is only
// logged: the change already happened and goes on, and the function is retried by the job
// mechanism (its `retry:`).
func (s *Users) queueOrgHook(ctx context.Context, hook registry.OrgHook, origin, org, user string, event func(seq int64) any) {
	if hook.Function == "" {
		return
	}
	seq, err := s.Store.NextOrgMemberSeq(ctx, org, user)
	if err == nil {
		var input []byte
		if input, err = json.Marshal(event(seq)); err == nil {
			db, fn, _ := strings.Cut(hook.Function, "/")
			timeout := hook.Timeout
			if timeout <= 0 {
				timeout = defaultOrgHookTimeout
			}
			_, err = s.EnqueueJob(ctx, Job{
				Database: db, Function: fn, Input: input, CallerActor: ActorSystem, Origin: origin,
				ActsAsFunction: true, TimeoutMS: timeout.Milliseconds(),
			})
		}
	}
	if err != nil && s.Log != nil {
		s.Log.Error("could not queue an organization hook", "origin", origin, "org", org, "user_id", user, "error", err)
	}
}

// hooks returns the realm's hooks, empty when it has no organizations.
func (s *Users) hooks() registry.OrgHooks {
	if o := s.Settings.Organizations; o != nil {
		return o.Hooks
	}
	return registry.OrgHooks{}
}

// putMember stores a membership and calls the hooks the change calls for: on_member_joined for
// a new membership (invitation says through which, "" for none), on_roles_changed when what
// the member holds changed. before is nil for a membership that didn't exist.
func (s *Users) putMember(ctx context.Context, before *OrgMember, after OrgMember, invitation string) error {
	if err := s.Store.PutOrgMember(ctx, after); err != nil {
		return err
	}
	s.memberChanged(ctx, before, after, invitation)
	return nil
}

// memberChanged calls the hooks for a membership that has just been stored.
func (s *Users) memberChanged(ctx context.Context, before *OrgMember, after OrgMember, invitation string) {
	h := s.hooks()
	if before == nil {
		if h.OnMemberJoined.Function == "" {
			return
		}
		access := s.accessOf(ctx, after)
		s.queueOrgHook(ctx, h.OnMemberJoined, OrgMemberJoinedOrigin, after.Org, after.User, func(seq int64) any {
			return OrgMemberJoined{Event: "member_joined", Org: after.Org, User: after.User, Seq: seq, Invitation: invitation, MemberAccess: access}
		})
		return
	}
	if h.OnRolesChanged.Function == "" {
		return
	}
	was, now := s.accessOf(ctx, *before), s.accessOf(ctx, after)
	if sameAccess(was, now) {
		return
	}
	s.queueOrgHook(ctx, h.OnRolesChanged, OrgRolesChangedOrigin, after.Org, after.User, func(seq int64) any {
		return OrgRolesChanged{Event: "roles_changed", Org: after.Org, User: after.User, Seq: seq, Before: was, After: now}
	})
}

// memberLeft calls on_member_left.
func (s *Users) memberLeft(ctx context.Context, org, user, reason string) {
	h := s.hooks().OnMemberLeft
	s.queueOrgHook(ctx, h, OrgMemberLeftOrigin, org, user, func(seq int64) any {
		return OrgMemberLeft{Event: "member_left", Org: org, User: user, Seq: seq, Reason: reason}
	})
}

// memberSnapshot is a member and what they held before a change to a team.
type memberSnapshot struct {
	user   string
	access MemberAccess
}

// snapshotTeam records what the members of a team hold, before the team's roles change or the
// team goes: what they hold through it changes with it. Nothing is read when the realm has no
// on_roles_changed hook.
func (s *Users) snapshotTeam(ctx context.Context, orgID, teamID string) []memberSnapshot {
	if s.hooks().OnRolesChanged.Function == "" {
		return nil
	}
	members, err := s.Store.OrgMembersInTeams(ctx, orgID, []string{teamID})
	if err != nil {
		if s.Log != nil {
			s.Log.Error("could not list a team's members for on_roles_changed", "org", orgID, "team", teamID, "error", err)
		}
		return nil
	}
	out := make([]memberSnapshot, len(members))
	for i, m := range members {
		out[i] = memberSnapshot{user: m.User, access: s.accessOf(ctx, m)}
	}
	return out
}

// teamChanged calls on_roles_changed for the members of a snapshot whose access differs now.
func (s *Users) teamChanged(ctx context.Context, orgID string, before []memberSnapshot) {
	h := s.hooks().OnRolesChanged
	for _, b := range before {
		m, err := s.Store.OrgMember(ctx, orgID, b.user)
		if err != nil {
			continue // gone: on_member_left says so
		}
		now := s.accessOf(ctx, m)
		if sameAccess(b.access, now) {
			continue
		}
		s.queueOrgHook(ctx, h, OrgRolesChangedOrigin, orgID, b.user, func(seq int64) any {
			return OrgRolesChanged{Event: "roles_changed", Org: orgID, User: b.user, Seq: seq, Before: b.access, After: now}
		})
	}
}
