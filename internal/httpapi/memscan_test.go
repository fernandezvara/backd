package httpapi

import (
	"context"
	"sort"

	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.Scanner = (*memRepo)(nil)

func (r *memRepo) ScanAfter(_ context.Context, afterID string, limit int) ([]storage.Document, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return nil, r.s.fail
	}
	var ids []string
	for id := range r.coll() {
		if id > afterID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]storage.Document, len(ids))
	for i, id := range ids {
		out[i] = clone(r.coll()[id]).(map[string]any)
	}
	return out, nil
}

func (r *memRepo) EstimatedCount(context.Context) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return int64(len(r.coll())), r.s.fail
}

var _ storage.SchemaMigrator = (*memRepo)(nil)

// stampOf is a stored document's schema_version, 1 when missing.
func stampOf(doc storage.Document) int {
	meta, _ := doc["_meta"].(map[string]any)
	return storedSchemaVersion(meta)
}

func (r *memRepo) CountBelow(_ context.Context, current int) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var n int64
	for _, d := range r.coll() {
		if stampOf(d) < current {
			n++
		}
	}
	return n, r.s.fail
}

func (r *memRepo) ScanBelow(_ context.Context, current int, afterID string, limit int) ([]storage.Document, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return nil, r.s.fail
	}
	var ids []string
	for id, d := range r.coll() {
		if id > afterID && stampOf(d) < current {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]storage.Document, len(ids))
	for i, id := range ids {
		out[i] = clone(r.coll()[id]).(map[string]any)
	}
	return out, nil
}
