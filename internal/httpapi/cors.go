package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/registry"
)

// CORS: browsers may call a realm's routes from the origins listed in its
// realm.yaml. Credentials travel in the Authorization header; only a realm
// that turns on session cookies (sessions.cookie) also allows credentials,
// and only to the origins it lists (never to a wildcard).
const (
	corsMethods = "GET, POST, PUT, PATCH, DELETE"
	// X-Backd-On-Behalf-Of is left out on purpose: only server-side API
	// keys may use it.
	corsAllowHeaders  = "Authorization, Content-Type, If-Match, X-Request-ID"
	corsExposeHeaders = "ETag, Location, Retry-After, WWW-Authenticate, X-Request-ID"
	corsMaxAge        = "600"
)

// cors answers preflight requests and adds CORS headers to responses of
// /v1/{realm}/… routes whose realm allows the request's Origin.
func cors(reg *registry.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Answers under /v1/ depend on the Origin whether or not the
			// request has one, so caches must key them on it.
			if strings.HasPrefix(r.URL.Path, "/v1/") {
				addVary(w.Header(), "Origin")
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			allowed := corsAllowed(reg, r.URL.Path, origin)
			if allowed != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", allowed)
				h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
				if corsCredentials(reg, r.URL.Path, allowed) {
					h.Set("Access-Control-Allow-Credentials", "true") // a realm with session cookies, for its listed origins only
				}
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				// Preflight: never reaches the routes. Without an allowed
				// origin the browser sees no CORS headers and blocks.
				if allowed != "" {
					h := w.Header()
					addVary(h, "Access-Control-Request-Method", "Access-Control-Request-Headers")
					h.Set("Access-Control-Allow-Methods", corsMethods)
					h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
					h.Set("Access-Control-Max-Age", corsMaxAge)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// corsAllowed returns the Access-Control-Allow-Origin value for origin on
// path, or "" if the path's realm doesn't allow it.
func corsAllowed(reg *registry.Registry, path, origin string) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) < 2 || parts[0] != "v1" {
		return ""
	}
	rl, ok := reg.Realms[parts[1]]
	if !ok {
		return ""
	}
	origins := rl.Settings.CORSOrigins
	switch {
	case slices.Contains(origins, "*"):
		return "*"
	case slices.Contains(origins, origin):
		return origin
	}
	return ""
}
