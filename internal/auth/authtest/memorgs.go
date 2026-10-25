package authtest

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// Organizations of the in-memory store. They live in their own state so the rest of the
// store's constructor needn't know them.
type memOrgs struct {
	orgs    map[string]auth.Org
	members map[string]auth.OrgMember // org + "\x00" + user
	teams   map[string]auth.OrgTeam
	invites map[string]auth.OrgInvitation
	seq     map[string]int64 // org + "\x00" + user → the last membership event number
	tpls    map[string]auth.OrgTemplate
	conns   map[string]auth.OrgConnection // org + "\x00" + provider
}

func (m *MemStore) org() *memOrgs {
	if m.orgState == nil {
		m.orgState = &memOrgs{orgs: map[string]auth.Org{}, members: map[string]auth.OrgMember{}}
	}
	return m.orgState
}

func memberKey(org, user string) string { return org + "\x00" + user }

func (o *memOrgs) slugTaken(slug, except string) bool {
	if slug == "" {
		return false
	}
	for id, x := range o.orgs {
		if id != except && x.Slug == slug && x.Status != auth.OrgDeleted {
			return true
		}
	}
	return false
}

func (m *MemStore) CreateOrg(_ context.Context, org auth.Org, first auth.OrgMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.slugTaken(org.Slug, "") {
		return auth.ErrSlugTaken
	}
	o.orgs[org.ID] = org
	o.members[memberKey(first.Org, first.User)] = first
	return nil
}

func (m *MemStore) OrgByID(_ context.Context, id string) (auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	org, ok := m.org().orgs[id]
	if !ok {
		return auth.Org{}, auth.ErrNotFound
	}
	return org, nil
}

func (m *MemStore) OrgBySlug(_ context.Context, slug string) (auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.org().orgs {
		if slug != "" && o.Slug == slug && o.Status != auth.OrgDeleted {
			return o, nil
		}
	}
	return auth.Org{}, auth.ErrNotFound
}

func (m *MemStore) OrgsByIDs(_ context.Context, ids []string) (map[string]auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]auth.Org{}
	for _, id := range ids {
		if org, ok := m.org().orgs[id]; ok {
			out[id] = org
		}
	}
	return out, nil
}

func (m *MemStore) UpdateOrg(_ context.Context, id string, upd auth.OrgUpdate, now time.Time) (auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	org, ok := o.orgs[id]
	if !ok {
		return auth.Org{}, auth.ErrNotFound
	}
	if upd.Name != nil {
		org.Name = *upd.Name
	}
	if upd.Slug != nil {
		if o.slugTaken(*upd.Slug, id) {
			return auth.Org{}, auth.ErrSlugTaken
		}
		org.Slug = *upd.Slug
	}
	if upd.RequireMFA != nil {
		org.RequireMFA = *upd.RequireMFA
	}
	if upd.SessionMaxAge != nil {
		org.SessionMaxAge = *upd.SessionMaxAge
	}
	if upd.ClaimsSet {
		org.Claims = upd.Claims
	}
	if upd.Status != "" {
		org.Status = upd.Status
	}
	org.UpdatedAt = now
	o.orgs[id] = org
	return org, nil
}

func (m *MemStore) ForgetUserInOrgs(_ context.Context, userID, email string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	var orgs []string
	for k, mem := range o.members {
		if mem.User == userID {
			orgs = append(orgs, mem.Org)
			delete(o.members, k)
		}
	}
	for k, inv := range o.invites {
		switch {
		case inv.User == userID || (email != "" && inv.Email == email):
			delete(o.invites, k)
		case inv.CreatedBy == userID:
			inv.CreatedBy = ""
			o.invites[k] = inv
		}
	}
	for k, org := range o.orgs {
		if org.CreatedBy == userID {
			org.CreatedBy = ""
			o.orgs[k] = org
		}
	}
	return orgs, nil
}

func (m *MemStore) ArchiveOrg(_ context.Context, id string, now time.Time) (auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	org, ok := o.orgs[id]
	if !ok {
		return auth.Org{}, auth.ErrNotFound
	}
	if org.Status == auth.OrgDeleted {
		return auth.Org{}, auth.ErrOrgNotActive
	}
	if org.Slug != "" {
		org.FormerSlug = org.Slug
	}
	org.Slug, org.Status, org.DeletedAt, org.UpdatedAt = "", auth.OrgDeleted, now, now
	o.orgs[id] = org
	for k, mem := range o.members {
		if mem.Org == id {
			delete(o.members, k)
		}
	}
	for k, t := range o.teams {
		if t.Org == id {
			delete(o.teams, k)
		}
	}
	for k, inv := range o.invites {
		if inv.Org == id {
			delete(o.invites, k)
		}
	}
	return org, nil
}

func (m *MemStore) ReactivateOrg(_ context.Context, id, slug string, now time.Time) (auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	org, ok := o.orgs[id]
	if !ok {
		return auth.Org{}, auth.ErrNotFound
	}
	if org.Status != auth.OrgDeleted {
		return auth.Org{}, auth.ErrOrgNotDeleted
	}
	if slug != "" {
		if o.slugTaken(slug, id) {
			return auth.Org{}, auth.ErrSlugTaken
		}
		org.Slug = slug
	}
	org.Status, org.DeletedAt, org.UpdatedAt = auth.OrgActive, time.Time{}, now
	o.orgs[id] = org
	return org, nil
}

func (m *MemStore) ListOrgs(_ context.Context, status string) ([]auth.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Org
	for _, org := range m.org().orgs {
		if status == "" || org.Status == status {
			out = append(out, org)
		}
	}
	slices.SortFunc(out, func(a, b auth.Org) int { return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID)) })
	return out, nil
}

func (m *MemStore) CountOrgMembers(_ context.Context, orgs []string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, mem := range m.org().members {
		if slices.Contains(orgs, mem.Org) {
			out[mem.Org]++
		}
	}
	return out, nil
}

func (m *MemStore) CountOrgsCreatedBy(_ context.Context, userID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, org := range m.org().orgs {
		if org.CreatedBy == userID {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) OrgMember(_ context.Context, org, user string) (auth.OrgMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.org().members[memberKey(org, user)]
	if !ok {
		return auth.OrgMember{}, auth.ErrNotFound
	}
	return mem, nil
}

func (m *MemStore) ListOrgMembers(_ context.Context, org, after string, limit int) ([]auth.OrgMember, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []auth.OrgMember
	for _, mem := range m.org().members {
		if mem.Org == org && mem.User > after {
			all = append(all, mem)
		}
	}
	slices.SortFunc(all, func(a, b auth.OrgMember) int { return cmp.Compare(a.User, b.User) })
	if limit > 0 && len(all) > limit {
		return all[:limit], true, nil
	}
	return all, false, nil
}

func tplKey(org, kind, locale string) string { return org + "\x00" + kind + "\x00" + locale }

func (m *MemStore) OrgTemplate(_ context.Context, org, kind, locale string) (auth.OrgTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.org().tpls[tplKey(org, kind, locale)]
	if !ok {
		return auth.OrgTemplate{}, auth.ErrNotFound
	}
	return t, nil
}

func (m *MemStore) ListOrgTemplates(_ context.Context, org string) ([]auth.OrgTemplate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgTemplate
	for _, t := range m.org().tpls {
		if t.Org == org {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgTemplate) int { return cmp.Compare(a.Kind+"\x00"+a.Locale, b.Kind+"\x00"+b.Locale) })
	return out, nil
}

func (m *MemStore) PutOrgTemplate(_ context.Context, t auth.OrgTemplate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.tpls == nil {
		o.tpls = map[string]auth.OrgTemplate{}
	}
	o.tpls[tplKey(t.Org, t.Kind, t.Locale)] = t
	return nil
}

func (m *MemStore) DeleteOrgTemplate(_ context.Context, org, kind, locale string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if _, ok := o.tpls[tplKey(org, kind, locale)]; !ok {
		return auth.ErrNotFound
	}
	delete(o.tpls, tplKey(org, kind, locale))
	return nil
}

func (m *MemStore) NextOrgMemberSeq(_ context.Context, org, user string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.seq == nil {
		o.seq = map[string]int64{}
	}
	o.seq[memberKey(org, user)]++
	return o.seq[memberKey(org, user)], nil
}

func (m *MemStore) PurgeExpiredTeamRoles(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, mem := range m.org().members {
		kept := slices.DeleteFunc(slices.Clone(mem.TeamRoles), func(g auth.TeamRoleGrant) bool { return !g.Until.IsZero() && g.Until.Before(before) })
		if len(kept) != len(mem.TeamRoles) {
			n += int64(len(mem.TeamRoles) - len(kept))
			mem.TeamRoles = kept
			m.org().members[k] = mem
		}
	}
	return n, nil
}

func (m *MemStore) OrgTeamHolders(_ context.Context, org, team, role string, now time.Time, after string, limit int) ([]auth.OrgMember, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []auth.OrgMember
	for _, mem := range m.org().members {
		if mem.Org != org || mem.User <= after {
			continue
		}
		holds := slices.ContainsFunc(mem.TeamRoles, func(g auth.TeamRoleGrant) bool {
			return g.Team == team && (role == "" || g.Role == role) && g.Active(now)
		})
		if holds || (role == "" && slices.Contains(mem.Teams, team)) {
			all = append(all, mem)
		}
	}
	slices.SortFunc(all, func(a, b auth.OrgMember) int { return cmp.Compare(a.User, b.User) })
	if limit > 0 && len(all) > limit {
		return all[:limit], true, nil
	}
	return all, false, nil
}

func (m *MemStore) OrgMembersWithRoles(_ context.Context, org string, roles []string) ([]auth.OrgMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgMember
	for _, mem := range m.org().members {
		if mem.Org != org {
			continue
		}
		for _, g := range mem.Roles {
			if slices.Contains(roles, g.Role) {
				out = append(out, mem)
				break
			}
		}
	}
	return out, nil
}

func (m *MemStore) OrgMembershipsOf(_ context.Context, user string, limit int) ([]auth.OrgMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgMember
	for _, mem := range m.org().members {
		if mem.User == user {
			out = append(out, mem)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgMember) int { return cmp.Compare(a.Org, b.Org) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) CountOrgMembershipsOf(ctx context.Context, user string) (int, error) {
	list, err := m.OrgMembershipsOf(ctx, user, 0)
	return len(list), err
}

func (m *MemStore) PutOrgMember(_ context.Context, mem auth.OrgMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.org().members[memberKey(mem.Org, mem.User)] = mem
	return nil
}

func (m *MemStore) DeleteOrgMember(_ context.Context, org, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memberKey(org, user)
	if _, ok := m.org().members[k]; !ok {
		return auth.ErrNotFound
	}
	delete(m.org().members, k)
	return nil
}

func (m *MemStore) CreateOrgTeam(_ context.Context, t auth.OrgTeam) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.teams == nil {
		o.teams = map[string]auth.OrgTeam{}
	}
	for _, x := range o.teams {
		if x.Org == t.Org && x.Name == t.Name {
			return auth.ErrTeamNameTaken
		}
	}
	o.teams[t.ID] = t
	return nil
}

func (m *MemStore) UpdateOrgTeam(_ context.Context, t auth.OrgTeam) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	cur, ok := o.teams[t.ID]
	if !ok || cur.Org != t.Org {
		return auth.ErrNotFound
	}
	for id, x := range o.teams {
		if id != t.ID && x.Org == t.Org && x.Name == t.Name {
			return auth.ErrTeamNameTaken
		}
	}
	cur.Name, cur.Roles = t.Name, t.Roles
	o.teams[t.ID] = cur
	return nil
}

func (m *MemStore) DeleteOrgTeam(_ context.Context, org, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if t, ok := o.teams[id]; !ok || t.Org != org {
		return auth.ErrNotFound
	}
	delete(o.teams, id)
	for k, mem := range o.members {
		if mem.Org == org {
			mem.Teams = slices.DeleteFunc(slices.Clone(mem.Teams), func(t string) bool { return t == id })
			mem.TeamRoles = slices.DeleteFunc(slices.Clone(mem.TeamRoles), func(g auth.TeamRoleGrant) bool { return g.Team == id })
			o.members[k] = mem
		}
	}
	return nil
}

func (m *MemStore) ListOrgTeams(_ context.Context, org string) ([]auth.OrgTeam, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgTeam
	for _, t := range m.org().teams {
		if t.Org == org {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgTeam) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *MemStore) OrgMembersInTeams(_ context.Context, org string, teams []string) ([]auth.OrgMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgMember
	for _, mem := range m.org().members {
		if mem.Org != org {
			continue
		}
		for _, t := range mem.Teams {
			if slices.Contains(teams, t) {
				out = append(out, mem)
				break
			}
		}
	}
	return out, nil
}

// PutOrgTeam stores a team, for tests: teams are managed through the service.
func (m *MemStore) PutOrgTeam(t auth.OrgTeam) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.teams == nil {
		o.teams = map[string]auth.OrgTeam{}
	}
	o.teams[t.ID] = t
}

func (m *MemStore) OrgTeamsByIDs(_ context.Context, org string, ids []string) ([]auth.OrgTeam, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgTeam
	for _, id := range ids {
		if t, ok := m.org().teams[id]; ok && t.Org == org {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *MemStore) CreateOrgInvitation(_ context.Context, inv auth.OrgInvitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.invites == nil {
		o.invites = map[string]auth.OrgInvitation{}
	}
	o.invites[inv.ID] = inv
	return nil
}

func (m *MemStore) OrgInvitationByHash(_ context.Context, hash string) (auth.OrgInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inv := range m.org().invites {
		if inv.TokenHash == hash {
			return inv, nil
		}
	}
	return auth.OrgInvitation{}, auth.ErrNotFound
}

func (m *MemStore) OrgInvitationByID(_ context.Context, org, id string) (auth.OrgInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv, ok := m.org().invites[id]; ok && inv.Org == org {
		return inv, nil
	}
	return auth.OrgInvitation{}, auth.ErrNotFound
}

func (m *MemStore) OrgInvitationsAddressedTo(_ context.Context, userID, email string) ([]auth.OrgInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgInvitation
	for _, inv := range m.org().invites {
		if (inv.Binding == auth.BindUser && userID != "" && inv.User == userID) || (inv.Binding == auth.BindEmail && email != "" && inv.Email == email) {
			out = append(out, inv)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgInvitation) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (m *MemStore) ListOrgInvitations(_ context.Context, org string) ([]auth.OrgInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgInvitation
	for _, inv := range m.org().invites {
		if inv.Org == org {
			out = append(out, inv)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgInvitation) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (m *MemStore) CountPendingOrgInvitations(_ context.Context, org string, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, inv := range m.org().invites {
		if inv.Org == org && inv.Status(now) == "pending" {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) UseOrgInvitation(_ context.Context, id string, now time.Time) (auth.OrgInvitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.org().invites[id]
	if !ok || inv.Status(now) != "pending" {
		return auth.OrgInvitation{}, auth.ErrNotFound
	}
	inv.Uses++
	m.org().invites[id] = inv
	return inv, nil
}

func (m *MemStore) RevokeOrgInvitation(_ context.Context, org, id string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.org().invites[id]
	if !ok || inv.Org != org {
		return auth.ErrNotFound
	}
	inv.RevokedAt, inv.SealedToken = now, ""
	m.org().invites[id] = inv
	return nil
}

func (m *MemStore) ReplaceOrgInvitationToken(_ context.Context, org, id, tokenHash, sealed string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.org().invites[id]
	if !ok || inv.Org != org {
		return auth.ErrNotFound
	}
	inv.TokenHash, inv.SealedToken = tokenHash, sealed
	m.org().invites[id] = inv
	return nil
}

func (m *MemStore) DeleteOrgInvitation(_ context.Context, org, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv, ok := m.org().invites[id]; ok && inv.Org == org {
		delete(m.org().invites, id)
	}
	return nil
}

func connKey(org, provider string) string { return org + "\x00" + provider }

func (m *MemStore) PutOrgConnection(_ context.Context, c auth.OrgConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.conns == nil {
		o.conns = map[string]auth.OrgConnection{}
	}
	if have, ok := o.conns[connKey(c.Org, c.Provider)]; ok {
		// What a replace keeps, as the MongoDB store does.
		c.Blocked, c.CreatedBy, c.CreatedAt = have.Blocked, have.CreatedBy, have.CreatedAt
	}
	o.conns[connKey(c.Org, c.Provider)] = c
	return nil
}

func (m *MemStore) OrgConnection(_ context.Context, org, provider string) (auth.OrgConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.org().conns[connKey(org, provider)]
	if !ok {
		return auth.OrgConnection{}, auth.ErrNotFound
	}
	return c, nil
}

func (m *MemStore) OrgConnections(_ context.Context, org string) ([]auth.OrgConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgConnection
	for _, c := range m.org().conns {
		if c.Org == org {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgConnection) int { return strings.Compare(a.Provider, b.Provider) })
	return out, nil
}

func (m *MemStore) DeleteOrgConnection(_ context.Context, org, provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if _, ok := o.conns[connKey(org, provider)]; !ok {
		return auth.ErrNotFound
	}
	delete(o.conns, connKey(org, provider))
	return nil
}

// SeedConnection stores a connection as it is, Blocked included, which PutOrgConnection never
// changes on an existing one.
func (m *MemStore) SeedConnection(c auth.OrgConnection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if o.conns == nil {
		o.conns = map[string]auth.OrgConnection{}
	}
	o.conns[connKey(c.Org, c.Provider)] = c
}

func (m *MemStore) BlockOrgMember(_ context.Context, org, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.org().conns {
		if c.Org == org && !slices.Contains(c.Blocked, user) {
			c.Blocked = append(slices.Clone(c.Blocked), user)
			if len(c.Blocked) > auth.MaxConnectionBlocked {
				c.Blocked = c.Blocked[len(c.Blocked)-auth.MaxConnectionBlocked:]
			}
			m.org().conns[k] = c
		}
	}
	return nil
}

func (m *MemStore) UnblockOrgMember(_ context.Context, org, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.org().conns {
		if c.Org == org {
			c.Blocked = slices.DeleteFunc(slices.Clone(c.Blocked), func(u string) bool { return u == user })
			m.org().conns[k] = c
		}
	}
	return nil
}
