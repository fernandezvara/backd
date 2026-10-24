package authtest

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func cloneExport(e auth.UserExport) auth.UserExport {
	e.Counts = maps.Clone(e.Counts)
	return e
}

func (m *MemStore) CreateUserExport(_ context.Context, e auth.UserExport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.userExports == nil {
		m.userExports = map[string]auth.UserExport{}
	}
	m.userExports[e.ID] = cloneExport(e)
	return nil
}

func (m *MemStore) UserExportByID(_ context.Context, id string) (auth.UserExport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.userExports[id]
	if !ok {
		return auth.UserExport{}, auth.ErrNotFound
	}
	return cloneExport(e), nil
}

func (m *MemStore) ReplaceUserExport(_ context.Context, e auth.UserExport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.userExports[e.ID]; !ok {
		return auth.ErrNotFound
	}
	m.userExports[e.ID] = cloneExport(e)
	return nil
}

func (m *MemStore) ListUserExports(_ context.Context, userID string, limit int) ([]auth.UserExport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.UserExport
	for _, e := range m.userExports {
		if e.UserID == userID {
			out = append(out, cloneExport(e))
		}
	}
	slices.SortFunc(out, func(a, b auth.UserExport) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListExpiredUserExports(_ context.Context, now time.Time, limit int) ([]auth.UserExport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.UserExport
	for _, e := range m.userExports {
		if e.Status == auth.ExportReady && !e.ExpiresAt.After(now) {
			out = append(out, cloneExport(e))
		}
	}
	slices.SortFunc(out, func(a, b auth.UserExport) int {
		return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), cmp.Compare(a.ID, b.ID))
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
