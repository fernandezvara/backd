package httpapi

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"maps"
	"net/http"
	"slices"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// userStatus is where a user stands: active, deactivated (disabled) or erased.
func userStatus(u auth.User) string {
	if !u.ErasedAt.IsZero() {
		return "erased"
	}
	if u.Disabled {
		return "deactivated"
	}
	return "active"
}

// ownedReport says what erasing the user would do, for the collections that
// declare a policy (the others are listed by name: an erase leaves them alone).
// It counts with the same queries the erase uses, and never returns content.
func ownedReport(ctx context.Context, reg *registry.Registry, store Store, realm string, u auth.User) (map[string]any, error) {
	rl := reg.Realms[realm]
	if rl == nil {
		return nil, fmt.Errorf("unknown realm %q", realm)
	}
	collections := []map[string]any{}
	without := []string{}
	if !u.ErasedAt.IsZero() {
		// Nothing left to count: the user is a tombstone.
		return map[string]any{"user": map[string]any{"id": u.ID, "status": "erased"}, "collections": collections, "without_policy": without}, nil
	}
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		db := rl.Databases[dbName]
		for _, name := range slices.Sorted(maps.Keys(db.Collections)) {
			c := db.Collections[name]
			p := c.Erasure
			if p == nil {
				without = append(without, dbName+"."+name)
				continue
			}
			eraser, ok := store.Repository(c).(storage.Eraser)
			if !ok {
				return nil, fmt.Errorf("the storage of %s.%s can't erase", dbName, name)
			}
			entry := map[string]any{"database": dbName, "collection": name, "action": nil, "owned": int64(0)}
			if p.Action != "" {
				entry["action"] = p.Action
				n, err := eraser.CountOwned(ctx, u.ID)
				if err != nil {
					return nil, err
				}
				entry["owned"] = n
			}
			if p.Action == registry.ErasureAnonymize {
				entry["remove"] = nonNil(slices.Clone(p.Remove))
				entry["replace"] = slices.Sorted(maps.Keys(p.Replace))
			}
			for key, fields := range map[string]map[string]string{"pull": p.Pull, "unset": p.Unset} {
				if len(fields) == 0 {
					continue
				}
				counts := map[string]int64{}
				for field, by := range fields {
					value := u.Email
					if by == registry.MatchID {
						value = u.ID
					}
					n, err := eraser.CountReferences(ctx, field, value, key == "pull")
					if err != nil {
						return nil, err
					}
					counts[field] = n
				}
				entry[key] = counts
			}
			collections = append(collections, entry)
		}
	}
	return map[string]any{
		"user":           map[string]any{"id": u.ID, "status": userStatus(u)},
		"collections":    collections,
		"without_policy": without,
	}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// owned handles GET /users/{id}/owned: what erasing this user would do.
func (a *adminAPI) owned(w http.ResponseWriter, r *http.Request) {
	u := adminUserOf(r)
	report, err := ownedReport(r.Context(), a.reg, a.fns.docs.store, chi.URLParam(r, "realm"), u)
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
