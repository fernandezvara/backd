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
