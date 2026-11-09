package httpapi

import (
	"net/http"
	"strings"

	"github.com/fernandezvara/backd/internal/buildinfo"
)

// The backd CLI says which version it is in its User-Agent (backd/<version>). The server never
// says which version it runs (an unauthenticated version string tells an attacker which known
// vulnerabilities to try); it only tells an authenticated administrator's CLI whether it is newer
// or older, and keeps a development CLI away from a released server.

// versionSkewHeader answers, on authenticated admin responses, how the server's version compares
// with the CLI's: newer or older. Not exposed through CORS: browsers have no use for it.
const versionSkewHeader = "X-Backd-Version-Skew"

// cliVersion is the version a backd CLI says it is, from a User-Agent whose first product token is
// exactly backd ("backd/0.9.7"); ok is false for any other caller (browsers, the other clients),
// which are never compared or refused.
func cliVersion(r *http.Request) (version string, ok bool) {
	product, _, _ := strings.Cut(r.Header.Get("User-Agent"), " ")
	name, version, found := strings.Cut(product, "/")
	if !found || name != "backd" || version == "" {
		return "", false
	}
	return version, true
}

// devServer reports whether this server accepts a development CLI: it is a development build
// itself, or runs with BACKD_DEV=true.
func devServer(cfg Config) bool {
	return cfg.Dev || cfg.Version == "" || cfg.Version == buildinfo.Dev
}

// refuseDevClients answers 403 dev_client_refused to a development CLI (backd/dev) on every
// route of a released server outside dev mode, before authentication. It guards against a
// mistake (an untested build on production data), not against anyone: a User-Agent is easy to
// change, and the refusal only tells the caller that this server is not in development.
func refuseDevClients(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if devServer(cfg) {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if v, ok := cliVersion(r); ok && v == buildinfo.Dev {
				writeError(w, r, http.StatusForbidden, codeDevClientRefused, "this CLI is a development build: use a released backd against a released server")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// versionSkew adds X-Backd-Version-Skew to the admin answers of a CLI whose version differs from
// the server's by semver precedence. It runs after the caller is authenticated, so an anonymous
// probe learns nothing; a development side on either end is not compared.
func versionSkew(serverVersion string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if v, ok := cliVersion(r); ok {
				if c, ok := buildinfo.Compare(serverVersion, v); ok && c != 0 {
					skew := "newer"
					if c < 0 {
						skew = "older"
					}
					w.Header().Set(versionSkewHeader, skew)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
