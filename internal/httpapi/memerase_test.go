package httpapi

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.Eraser = (*memRepo)(nil)

// pathGet, pathSet and pathUnset read and write a dotted path of a document.
func pathGet(doc map[string]any, path string) (any, bool) {
	cur := doc
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	v, ok := cur[parts[len(parts)-1]]
	return v, ok
}

func pathSet(doc map[string]any, path string, v any) {
	cur := doc
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

func pathUnset(doc map[string]any, path string) {
	cur := doc
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

func systemTouch(d storage.Document, now time.Time) {
	meta := d["_meta"].(map[string]any)
	meta["updated_at"] = now
	meta["updated_by"] = storage.ErasedBy
	meta["version"] = version(d) + 1
}

// matching returns up to limit documents, in id order, for which keep is true.
func (r *memRepo) matching(limit int, keep func(storage.Document) bool) []storage.Document {
	var out []storage.Document
	for _, id := range slices.Sorted(mapKeysDoc(r.coll())) {
		if d := r.coll()[id]; keep(d) {
			out = append(out, d)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

func mapKeysDoc(m map[string]storage.Document) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func ownedBy(owner string) func(storage.Document) bool {
	return func(d storage.Document) bool {
		v, _ := pathGet(d, "_meta.owner")
		return v == owner
	}
}

func holds(field, value string) func(storage.Document) bool {
	return func(d storage.Document) bool {
		v, ok := pathGet(d, field)
		if !ok {
			return false
		}
		if list, isList := v.([]any); isList {
			return slices.Contains(list, any(value))
		}
		return v == value
	}
}

func (r *memRepo) CountOwned(_ context.Context, owner string) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return int64(len(r.matching(0, ownedBy(owner)))), nil
}

func (r *memRepo) CountReferences(_ context.Context, field, value string, _ bool) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return int64(len(r.matching(0, holds(field, value)))), nil
}

func (r *memRepo) DeleteOwned(_ context.Context, owner string, fileFields []string, limit int) (int64, []storage.ErasedFile, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return 0, nil, r.s.fail
	}
	docs := r.matching(limit, ownedBy(owner))
	var files []storage.ErasedFile
	for _, d := range docs {
		files = append(files, memFiles(d, fileFields)...)
		delete(r.coll(), d["id"].(string))
	}
	return int64(len(docs)), files, nil
}

// memFiles lists the files a document holds in the fields.
func memFiles(d map[string]any, fields []string) []storage.ErasedFile {
	var out []storage.ErasedFile
	add := func(v any) {
		if m, ok := v.(map[string]any); ok {
			id, _ := m["id"].(string)
			size, _ := m["size"].(int64)
			out = append(out, storage.ErasedFile{ID: id, Size: size})
		}
	}
	for _, f := range fields {
		switch v := d[f].(type) {
		case []any:
			for _, e := range v {
				add(e)
			}
		default:
			add(v)
		}
	}
	return out
}

func (r *memRepo) AnonymizeOwned(_ context.Context, owner string, remove []string, replace map[string]any, fileFields []string, limit int, now time.Time) (int64, []storage.ErasedFile, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return 0, nil, r.s.fail
	}
	docs := r.matching(limit, ownedBy(owner))
	var files []storage.ErasedFile
	for _, d := range docs {
		files = append(files, memFiles(d, fileFields)...)
		for _, f := range remove {
			pathUnset(d, f)
		}
		for f, v := range replace {
			pathSet(d, f, v)
		}
		pathSet(d, "_meta.owner", nil)
		systemTouch(d, now)
	}
	return int64(len(docs)), files, nil
}

func (r *memRepo) PullReference(_ context.Context, field, value string, limit int, now time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return 0, r.s.fail
	}
	docs := r.matching(limit, holds(field, value))
	for _, d := range docs {
		v, _ := pathGet(d, field)
		var kept []any
		for _, e := range v.([]any) {
			if e != any(value) {
				kept = append(kept, e)
			}
		}
		pathSet(d, field, kept)
		systemTouch(d, now)
	}
	return int64(len(docs)), nil
}

func (r *memRepo) ClearReference(_ context.Context, field, value string, limit int, now time.Time) (int64, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.fail != nil {
		return 0, r.s.fail
	}
	docs := r.matching(limit, holds(field, value))
	for _, d := range docs {
		pathUnset(d, field)
		systemTouch(d, now)
	}
	return int64(len(docs)), nil
}
