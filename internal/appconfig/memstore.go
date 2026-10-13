package appconfig

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"
)

// MemStore is a Store in memory, for tests and for embedding in other test stores.
// Its zero value is ready to use.
type MemStore struct {
	mu      sync.Mutex
	state   State
	entries map[int64]Entry
}

func (m *MemStore) AppConfigState(_ context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state
	st.Values = cloneValues(st.Values)
	return st, nil
}

func (m *MemStore) SaveAppConfig(_ context.Context, expected int64, next State, e Entry, pruneTo int64, pruneBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Version != expected {
		return ErrConflict
	}
	m.state = next
	m.state.Values = cloneValues(next.Values)
	if m.entries == nil {
		m.entries = map[int64]Entry{}
	}
	e.Values = cloneValues(e.Values)
	m.entries[e.Version] = e
	for v, old := range m.entries {
		if v <= pruneTo && old.At.Before(pruneBefore) {
			delete(m.entries, v)
		}
	}
	return nil
}

func (m *MemStore) AppConfigHistory(_ context.Context, limit int) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, v := range slices.Backward(slices.Sorted(maps.Keys(m.entries))) {
		if len(out) == limit {
			break
		}
		out = append(out, m.entries[v])
	}
	return out, nil
}

func (m *MemStore) AppConfigEntry(_ context.Context, version int64) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[version]
	if !ok {
		return Entry{}, ErrNoVersion
	}
	e.Values = cloneValues(e.Values)
	return e, nil
}

func cloneValues(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	return normalize(v).(map[string]any)
}
