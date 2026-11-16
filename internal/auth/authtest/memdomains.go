package authtest

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// Verified domains (design/sso.md §10): claims by organization and domain. At most one
// organization holds a domain as verified, as the MongoDB store's partial unique index says.

func (m *MemStore) PutOrgDomain(_ context.Context, d auth.OrgDomain) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if d.State == auth.DomainVerified {
		for _, other := range o.domains {
			if other.Domain == d.Domain && other.Org != d.Org && other.State == auth.DomainVerified {
				return auth.ErrDomainTaken
			}
		}
	}
	if o.domains == nil {
		o.domains = map[string]auth.OrgDomain{}
	}
	o.domains[d.ID] = d
	return nil
}

func (m *MemStore) OrgDomain(_ context.Context, org, domain string) (auth.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.org().domains[org+":"+domain]
	if !ok {
		return auth.OrgDomain{}, auth.ErrNotFound
	}
	return d, nil
}

func (m *MemStore) OrgDomains(_ context.Context, org string) ([]auth.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgDomain
	for _, d := range m.org().domains {
		if d.Org == org {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgDomain) int { return strings.Compare(a.Domain, b.Domain) })
	return out, nil
}

func (m *MemStore) VerifiedOrgDomain(_ context.Context, domain string) (auth.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.org().domains {
		if d.Domain == domain && d.State == auth.DomainVerified {
			return d, nil
		}
	}
	return auth.OrgDomain{}, auth.ErrNotFound
}

func (m *MemStore) DueOrgDomains(_ context.Context, now time.Time, limit int) ([]auth.OrgDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.OrgDomain
	for _, d := range m.org().domains {
		if !d.NextCheckAt.IsZero() && !d.NextCheckAt.After(now) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b auth.OrgDomain) int { return a.NextCheckAt.Compare(b.NextCheckAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) DeleteOrgDomain(_ context.Context, org, domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.org()
	if _, ok := o.domains[org+":"+domain]; !ok {
		return auth.ErrNotFound
	}
	delete(o.domains, org+":"+domain)
	return nil
}

func (m *MemStore) DeleteOrgDomains(_ context.Context, org string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, d := range m.org().domains {
		if d.Org == org {
			delete(m.org().domains, k)
			n++
		}
	}
	return n, nil
}
