package rulestest

import (
	"fmt"
	"maps"
	"slices"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// fixtureParents are the documents the references of the collection point to, by reference
// name: what parent() reads in a rule.
type fixtureParents struct {
	key  map[string]string           // reference → the field of the parent its source matches
	docs map[string][]map[string]any // reference → live parents
}

var _ rules.Parents = fixtureParents{}

func (f fixtureParents) Get(ref string, key any) (map[string]any, error) {
	for _, d := range f.docs[ref] {
		if v, _ := docutil.GetPath(d, f.key[ref]); v == key {
			return d, nil
		}
	}
	return nil, nil
}

func (f fixtureParents) Keys(ref string, flt storage.Filter) ([]any, error) {
	var out []any
	for _, d := range f.docs[ref] {
		ok, err := storage.Match(d, flt)
		if err != nil {
			return nil, err
		}
		if v, has := docutil.GetPath(d, f.key[ref]); ok && has && v != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// loadParents builds the parents of the fixture. A parent that is deleted (_meta.deleted_at) is
// not there for the rules, as on the server.
func (r *runner) loadParents(def map[string]map[string]map[string]any) {
	r.parents = fixtureParents{key: map[string]string{}, docs: map[string][]map[string]any{}}
	for _, ref := range slices.Sorted(maps.Keys(def)) {
		rf := r.c.References[ref]
		if rf == nil {
			r.problem("parents.%s: %s has no reference by that name (declared: %s)", ref, r.c.Name, referenceNames(r))
			continue
		}
		r.parents.key[ref] = rf.Key
		for _, id := range slices.Sorted(maps.Keys(def[ref])) {
			fields := maps.Clone(def[ref][id])
			doc := map[string]any{"id": id}
			meta := map[string]any{}
			for k, v := range fields {
				switch k {
				case "id":
					if s, ok := v.(string); ok && s != "" {
						doc["id"] = s
					}
				case "_meta":
					if m, ok := v.(map[string]any); ok {
						meta = m
					}
				default:
					doc[k] = docutil.DeepCopy(v)
				}
			}
			if d, ok := meta["deleted_at"]; ok && d != nil {
				continue
			}
			doc["_meta"] = docutil.DeepCopy(meta)
			r.parents.docs[ref] = append(r.parents.docs[ref], doc)
		}
	}
}

func referenceNames(r *runner) string {
	names := slices.Sorted(maps.Keys(r.c.References))
	if len(names) == 0 {
		return "none"
	}
	return fmt.Sprint(names)
}
