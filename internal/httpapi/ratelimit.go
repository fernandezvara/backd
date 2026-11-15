package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/ratelimit"
	"github.com/fernandezvara/backd/internal/registry"
)

// The built-in request limiter (design/rate-limiting.md). Two stages:
//
//   - Before anyone is identified, a request with no credentials is limited by its client address:
//     the `auth` class for the sign-in routes under /_auth, `anonymous` for the rest. A request that
//     carries credentials skips this stage.
//   - Once the caller is known (where the data, admin and account routes identify them), it is
//     limited by principal: the user of a session or the API key, in the class of the route (read,
//     write, files, admin), or by the key's own limit when it has one. A credential that is refused
//     counts against the address, so a flood of guessed tokens is bounded too.
//
// The internal listener, health checks, preflights and anything outside /v1/ are not limited.

// requestLimiter is nil when the limiter is off, and every method then lets the request through.
type requestLimiter struct {
	l       *ratelimit.Limiter
	inst    registry.RateSettings
	reg     *registry.Registry
	metrics *metrics.Metrics
}

func newRequestLimiter(cfg Config) *requestLimiter {
	if !cfg.RateLimit.Enabled {
		return nil
	}
	inst := cfg.RateLimit
	if inst.Rates == nil {
		inst.Rates = registry.DefaultRates
	}
	return &requestLimiter{l: ratelimit.New(0, cfg.Now), inst: inst, reg: cfg.Registry, metrics: cfg.Metrics}
}

// realmPath splits /v1/{realm}/rest.
func realmPath(path string) (realm, rest string, ok bool) {
	p, found := strings.CutPrefix(path, "/v1/")
	if !found {
		return "", "", false
	}
	realm, rest, _ = strings.Cut(p, "/")
	return realm, rest, realm != ""
}

func hasCredentials(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || len(r.Cookies()) > 0
}

// addressStage is the middleware of the first stage.
func (rl *requestLimiter) addressStage(next http.Handler) http.Handler {
	if rl == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realm, rest, ok := realmPath(r.URL.Path)
		if !ok || r.Method == http.MethodOptions || hasCredentials(r) {
			next.ServeHTTP(w, r)
			return
		}
		class := registry.RateAnonymous
		if rest == "_auth" || strings.HasPrefix(rest, "_auth/") {
			class = registry.RateAuth
		}
		if !rl.take(w, r, realm, class, addressKey(r), rl.rate(realm, class)) {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), addressChargedKey{}, true)))
	})
}

// addressChargedKey marks a request the first stage already counted against its address.
type addressChargedKey struct{}

// principalStage limits a caller the route has identified. It answers 429 itself and reports false
// when the request is refused.
func (rl *requestLimiter) principalStage(w http.ResponseWriter, r *http.Request, realm string, caller auth.Caller) bool {
	if rl == nil {
		return true
	}
	if caller.Key != nil {
		if caller.Key.RateLimit != "" {
			rate, err := registry.ParseRate(caller.Key.RateLimit)
			if err == nil {
				return rl.take(w, r, realm, "key", "key:"+caller.Key.Name, rate)
			}
		}
		class := classOfRoute(r)
		return rl.take(w, r, realm, class, "key:"+caller.Key.Name, rl.rate(realm, class))
	}
	if caller.User != nil {
		class := classOfRoute(r)
		return rl.take(w, r, realm, class, "user:"+caller.User.User.ID, rl.rate(realm, class))
	}
	// Nobody was identified: a request that came in with a cookie or a header the realm ignores
	// still counts against its address, unless the first stage already did.
	if charged, _ := r.Context().Value(addressChargedKey{}).(bool); !charged {
		return rl.take(w, r, realm, registry.RateAnonymous, addressKey(r), rl.rate(realm, registry.RateAnonymous))
	}
	return true
}

// refusedCredential charges the address for a request whose credentials were refused, in the class
// the same request without credentials would have had. It reports false, having answered 429, when
// the address is over its limit.
func (rl *requestLimiter) refusedCredential(w http.ResponseWriter, r *http.Request, realm string) bool {
	if rl == nil {
		return true
	}
	class := registry.RateAnonymous
	if _, rest, ok := realmPath(r.URL.Path); ok && (rest == "_auth" || strings.HasPrefix(rest, "_auth/")) {
		class = registry.RateAuth
	}
	return rl.take(w, r, realm, class, addressKey(r), rl.rate(realm, class))
}

// classOfRoute is the class of an identified request.
func classOfRoute(r *http.Request) string {
	_, rest, _ := realmPath(r.URL.Path)
	switch {
	case rest == "_admin" || strings.HasPrefix(rest, "_admin/"):
		return registry.RateAdmin
	case strings.Contains(rest, "/_files/"):
		return registry.RateFiles
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		return registry.RateRead
	}
	return registry.RateWrite
}

func addressKey(r *http.Request) string {
	if k := auth.IPKey(clientIP(r)); k != "" {
		return k
	}
	return "ip:unknown"
}

// rate is the rate of class for a realm: the realm's own, else the instance's.
func (rl *requestLimiter) rate(realm, class string) ratelimit.Rate {
	var settings *registry.RealmSettings
	if rl.reg != nil {
		if rlm, ok := rl.reg.Realms[realm]; ok {
			settings = &rlm.Settings
		}
	}
	return rl.inst.RateFor(settings, class)
}

// take draws one request from the bucket and answers 429 when it is empty.
func (rl *requestLimiter) take(w http.ResponseWriter, r *http.Request, realm, class, key string, rate ratelimit.Rate) bool {
	if rate.Unlimited() {
		return true
	}
	d := rl.l.Allow(realm+"|"+class+"|"+key, rate)
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(d.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(d.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(d.Reset)))
	if d.Allowed {
		return true
	}
	rl.metrics.RateLimited(realm, "request_"+class)
	setLimited(r.Context(), class)
	retryAfter(w, d.RetryAfter)
	writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests,
		"too many requests ("+class+": "+registry.FormatRate(rate)+"): wait "+strconv.Itoa(ceilSeconds(d.RetryAfter))+"s and try again")
	return false
}

func ceilSeconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }

// realmOf is the realm of a request routed under /v1/{realm}.
func realmOf(r *http.Request) string {
	if v := chi.URLParam(r, "realm"); v != "" {
		return v
	}
	realm, _, _ := realmPath(r.URL.Path)
	return realm
}
