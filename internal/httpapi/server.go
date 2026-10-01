// Package httpapi is backd's HTTP layer: routing, middleware, error
// envelope, the generic document handlers and the /_auth endpoints.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Config holds the handler dependencies.
type Config struct {
	Log      *slog.Logger
	Registry *registry.Registry
	Store    Store
	// Ready reports whether backd can serve requests (e.g. MongoDB reachable).
	Ready func(ctx context.Context) error
	// Now is the clock for _meta timestamps; defaults to time.Now.
	Now func() time.Time
	// MaxBodyBytes caps request bodies; defaults to DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// OpTimeout bounds the storage work of each document request;
	// defaults to DefaultOpTimeout.
	OpTimeout time.Duration
	// TrustedProxies are the networks whose X-Forwarded-For header is
	// believed when finding the client's address (for login throttling).
	TrustedProxies []netip.Prefix
	// Users returns the user service of a realm with auth enabled, or nil
	// (unknown realm or auth disabled). Nil Users disables /_auth routes.
	Users func(realm string) *auth.Users
	// ConfigFingerprint identifies the config this instance runs; /readyz
	// reports it so operators can check every instance runs the same one.
	ConfigFingerprint string
	// Functions runs function calls (the executor client); nil: calls get 503.
	Functions FunctionRunner
	// CallbackKey signs the callback tokens functions reach data with; the
	// internal listener verifies them.
	CallbackKey []byte
	// CallbackURL is backd's internal listener as the executor and
	// functions reach it (bundles and callbacks), e.g. http://backd:8081.
	CallbackURL string
	// ExecutorToken authenticates the executor's bundle fetches.
	ExecutorToken string
	// Dev rereads function bundle manifests on every call instead of once
	// (BACKD_DEV), so a background rebuild is served without a restart.
	Dev bool
}

// DefaultOpTimeout is the per-request storage deadline when none is configured.
const DefaultOpTimeout = 10 * time.Second

// DefaultMaxBodyBytes is the request body limit when none is configured.
const DefaultMaxBodyBytes = 1 << 20

// NewHandler builds the complete HTTP handler.
func NewHandler(cfg Config) http.Handler {
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	r := chi.NewRouter()
	r.Use(withRequestID(cfg.Log), withTrustedProxies(cfg.TrustedProxies), accessLog, recoverer, securityHeaders, cors(cfg.Registry), limitBody(maxBody))
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := cfg.Ready(ctx); err != nil {
			logger(r.Context()).Warn("not ready", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, "service not ready")
			return
		}
		body := map[string]string{"status": "ready"}
		if cfg.ConfigFingerprint != "" {
			body["config"] = cfg.ConfigFingerprint
		}
		writeJSON(w, http.StatusOK, body)
	})

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	opTimeout := cfg.OpTimeout
	if opTimeout <= 0 {
		opTimeout = DefaultOpTimeout
	}
	users := cfg.Users
	if users == nil {
		users = func(string) *auth.Users { return nil }
	}
	authRoutes := &authAPI{users: users, opTimeout: opTimeout}
	authRoutes.routes(r)
	(&adminAPI{users: users, reg: cfg.Registry}).routes(r, authRoutes.resolveRealm, withTimeout(opTimeout))
	docs := &documents{reg: cfg.Registry, store: cfg.Store, now: now, maxBody: maxBody, opTimeout: opTimeout, users: users, callbackKey: cfg.CallbackKey}
	fns := &functions{docs: docs, runner: cfg.Functions, callbackURL: cfg.CallbackURL, executorToken: cfg.ExecutorToken, log: cfg.Log, dev: cfg.Dev, concurrency: limiterFor(cfg.Registry)}
	fns.routes(r)
	docs.routes(r)
	return r
}

// NewInternalHandler builds the internal listener's handler, for the
// functions executor only: bundles by hash (with the executor's token) and
// the data routes for functions calling back (with callback tokens, and
// nothing else). It must never be reachable from outside.
func NewInternalHandler(cfg Config) http.Handler {
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	opTimeout := cfg.OpTimeout
	if opTimeout <= 0 {
		opTimeout = DefaultOpTimeout
	}
	users := cfg.Users
	if users == nil {
		users = func(string) *auth.Users { return nil }
	}
	r := chi.NewRouter()
	r.Use(withRequestID(cfg.Log), accessLog, recoverer, securityHeaders, limitBody(maxBody))
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	docs := &documents{reg: cfg.Registry, store: cfg.Store, now: now, maxBody: maxBody, opTimeout: opTimeout, users: users,
		internal: true, callbackKey: cfg.CallbackKey}
	fns := &functions{docs: docs, runner: cfg.Functions, callbackURL: cfg.CallbackURL, executorToken: cfg.ExecutorToken, log: cfg.Log, dev: cfg.Dev, concurrency: limiterFor(cfg.Registry)}
	r.Get("/_internal/functions/{sha256}", fns.bundle)
	fns.internalRoutes(r)
	docs.routes(r)
	return r
}
