package mongodb

import (
	"context"
	"fmt"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// blindFilter rewrites a filter tree so that a condition on a searchable encrypted field becomes
// one on its blind index (design/field-encryption.md §6.1). Only equality and `in` can be
// answered; anything else on an encrypted field, or a comparison with null, fails with
// storage.ErrFieldEncrypted rather than being run against ciphertext. The tree is copied.
func (r *Repository) blindFilter(ctx context.Context, f storage.Filter) (storage.Filter, error) {
	if f == nil || r.c == nil || len(r.c.Encrypted) == 0 {
		return f, nil
	}
	switch t := f.(type) {
	case storage.Const:
		return t, nil
	case storage.Not:
		inner, err := r.blindFilter(ctx, t.Filter)
		return storage.Not{Filter: inner}, err
	case storage.And:
		out := make(storage.And, len(t))
		for i, p := range t {
			var err error
			if out[i], err = r.blindFilter(ctx, p); err != nil {
				return nil, err
			}
		}
		return out, nil
	case storage.Or:
		out := make(storage.Or, len(t))
		for i, p := range t {
			var err error
			if out[i], err = r.blindFilter(ctx, p); err != nil {
				return nil, err
			}
		}
		return out, nil
	case storage.Condition:
		if !r.c.IsEncrypted(t.Field) {
			return t, nil
		}
		return r.blindCondition(ctx, t)
	}
	return nil, fmt.Errorf("unknown filter %T", f)
}

func (r *Repository) blindCondition(ctx context.Context, c storage.Condition) (storage.Filter, error) {
	if !r.c.IsSearchable(c.Field) || (c.Op != storage.OpEq && c.Op != storage.OpIn) {
		return nil, fmt.Errorf("%w: %s", storage.ErrFieldEncrypted, c.Field)
	}
	if r.ring == nil {
		return nil, fmt.Errorf("%w: this instance has no field encryption for the realm", storage.ErrFieldEncrypted)
	}
	hash := func(v any) ([]byte, error) {
		if v == nil {
			// A missing field and a null can't be told apart through a blind index.
			return nil, fmt.Errorf("%w: %s can't be searched for null", storage.ErrFieldEncrypted, c.Field)
		}
		return r.ring.Blind(ctx, r.c, c.Field, v)
	}
	field := registry.BlindField(c.Field)
	if c.Op == storage.OpEq {
		h, err := hash(c.Value)
		if err != nil {
			return nil, err
		}
		return storage.Condition{Field: field, Op: storage.OpEq, Value: h}, nil
	}
	values, ok := c.Value.([]any)
	if !ok {
		return nil, fmt.Errorf("$in on %s: want a list", c.Field)
	}
	hashes := make([]any, len(values))
	for i, v := range values {
		h, err := hash(v)
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}
	return storage.Condition{Field: field, Op: storage.OpIn, Value: hashes}, nil
}
