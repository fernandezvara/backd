package httpapi

import (
	"net/http"
	"strings"
	"sync"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/typegen"
)

// Headers of the stale-types warning for development: a client built against generated types
// sends the fingerprint of its realm.json; the answer says whether the config has moved on.
const (
	typesHeader      = "X-Backd-Types"
	typesStaleHeader = "X-Backd-Types-Stale"
)

// typesStale answers X-Backd-Types-Stale: true to a request that carries an X-Backd-Types
// fingerprint other than the one of the types this config generates for the realm. Nothing is
// ever refused: clients that send no header are unaffected, and production builds send none.
func typesStale(reg *registry.Registry) func(http.Handler) http.Handler {
	var mu sync.Mutex
	current := map[string]string{} // realm → fingerprint of its generated types
	expected := func(realm string) string {
		mu.Lock()
		defer mu.Unlock()
		if fp, ok := current[realm]; ok {
			return fp
		}
		fp := ""
		if out, err := typegen.Generate(reg, typegen.Options{Realm: realm}); err == nil {
			fp = out.Info.Fingerprint
		}
		current[realm] = fp
		return fp
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get(typesHeader); got != "" {
				if realm, ok := realmOfPath(r.URL.Path); ok {
					if want := expected(realm); want != "" && want != got {
						w.Header().Set(typesStaleHeader, "true")
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// realmOfPath returns the realm of a /v1/{realm}/… path.
func realmOfPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/v1/")
	if !ok {
		return "", false
	}
	realm, _, _ := strings.Cut(rest, "/")
	return realm, realm != ""
}
