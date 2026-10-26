package authtest

import (
	"bytes"
	"context"
	"maps"
	"sort"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
)

var _ auth.PortableStore = (*MemStore)(nil)

// SeedPortable puts raw documents (with "id") into a portable collection, as stored.
func (m *MemStore) SeedPortable(collection string, docs ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.portable == nil {
		m.portable = map[string]map[string]map[string]any{}
	}
	if m.portable[collection] == nil {
		m.portable[collection] = map[string]map[string]any{}
	}
	for _, d := range docs {
		m.portable[collection][d["id"].(string)] = d
	}
}

// Portable returns the raw documents of a portable collection, by id.
func (m *MemStore) Portable(collection string) map[string]map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[string]any{}
	for id, d := range m.portable[collection] {
		out[id] = clonePortable(d)
	}
	return out
}

func clonePortable(d map[string]any) map[string]any {
	out := make(map[string]any, len(d))
	maps.Copy(out, d)
	return out
}

// ExportSystem implements auth.PortableStore.
func (m *MemStore) ExportSystem(_ context.Context, collection, afterID string, limit int) ([]map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.portable[collection] {
		if id > afterID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]map[string]any, len(ids))
	for i, id := range ids {
		out[i] = clonePortable(m.portable[collection][id])
	}
	return out, nil
}

// GetSystem implements auth.PortableStore.
func (m *MemStore) GetSystem(_ context.Context, collection, id string) (map[string]any, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.portable[collection][id]
	if !ok {
		return nil, false, nil
	}
	return clonePortable(d), true, nil
}

// ImportSystem implements auth.PortableStore (without unique indexes).
func (m *MemStore) ImportSystem(_ context.Context, collection string, docs []map[string]any, mode string, write bool) ([]auth.PortableResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.portable == nil {
		m.portable = map[string]map[string]map[string]any{}
	}
	if m.portable[collection] == nil {
		m.portable[collection] = map[string]map[string]any{}
	}
	out := make([]auth.PortableResult, len(docs))
	for i, d := range docs {
		id, _ := d["id"].(string)
		res := auth.PortableResult{ID: id}
		have, exists := m.portable[collection][id]
		switch {
		case !exists:
			res.Outcome = auth.PortableCreated
		default:
			x, _ := exportpkg.EncodeDocument(have)
			y, _ := exportpkg.EncodeDocument(d)
			switch {
			case bytes.Equal(x, y):
				res.Outcome = auth.PortableUnchanged
			case mode == "skip":
				res.Outcome = auth.PortableSkipped
			case mode == "replace":
				res.Outcome = auth.PortableReplaced
			default:
				res.Outcome, res.Reason = auth.PortableConflict, "a different document with this id exists in the target"
			}
		}
		if write && (res.Outcome == auth.PortableCreated || res.Outcome == auth.PortableReplaced) {
			m.portable[collection][id] = clonePortable(d)
		}
		out[i] = res
	}
	return out, nil
}
