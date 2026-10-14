package authtest

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// Organizations of the in-memory store. They live in their own state so the rest of the
// store's constructor needn't know them.
type memOrgs struct {
	orgs    map[string]auth.Org
	members map[string]auth.OrgMember // org + "\x00" + user
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
