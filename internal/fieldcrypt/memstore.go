package fieldcrypt

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// MemStore is a Store in memory, for tests.
type MemStore struct {
	mu   sync.Mutex
	Keys map[string]Record
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{Keys: map[string]Record{}} }

func (m *MemStore) ActiveDataKey(_ context.Context, database, collection, path, kind string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best Record
	found := false
	for _, r := range m.Keys {
		if r.Database == database && r.Collection == collection && r.Path == path && r.Kind == kind && r.Status == StatusActive && (!found || r.Generation > best.Generation) {
			best, found = r, true
		}
	}
	return best, found, nil
}

func (m *MemStore) DataKeyByID(_ context.Context, id string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Keys[id]
	return r, ok, nil
}

func (m *MemStore) CreateDataKey(_ context.Context, r Record) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.Keys {
		if o.Database == r.Database && o.Collection == r.Collection && o.Path == r.Path && o.Kind == r.Kind && o.Generation == r.Generation {
			return false, nil
		}
	}
	m.Keys[r.ID] = r
	return true, nil
}

func (m *MemStore) DataKeysToRewrap(_ context.Context, key string, belowVersion int, afterID string, limit int) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.Keys {
		if r.Key == key && r.WrapVersion < belowVersion && r.ID > afterID {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Record) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) RewrapDataKey(_ context.Context, id string, wrapped []byte, from, to int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Keys[id]
	if !ok || r.WrapVersion != from {
		return false, nil
	}
	r.Wrapped, r.WrapVersion = wrapped, to
	m.Keys[id] = r
	return true, nil
}

func (m *MemStore) DataKeysByKey(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, r := range m.Keys {
		if r.Key == key && r.Status != StatusRetired {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) DataKeysOf(_ context.Context, database, collection, path string) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.Keys {
		if r.Database == database && r.Collection == collection && r.Path == path {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Record) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return a.Generation - b.Generation
	})
	return out, nil
}

func (m *MemStore) DataKeyPaths(_ context.Context, database, collection string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range m.Keys {
		if r.Database == database && r.Collection == collection && r.Kind == KindData {
			seen[r.Path] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out, nil
}

func (m *MemStore) SetDataKeyStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.Keys[id]
	if !ok {
		return nil
	}
	r.Status = status
	m.Keys[id] = r
	return nil
}
