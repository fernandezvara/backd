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
	"github.com/fernandezvara/backd/internal/metrics"
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
	// MaxUploadBytes caps one file of a proxy upload (BACKD_MAX_UPLOAD_BYTES), apart
	// from MaxBodyBytes, which does not apply to file uploads; defaults to
	// DefaultMaxUploadBytes.
	MaxUploadBytes int64
	// ImageMaxPixels is the instance's limit on an image's pixels
	// (BACKD_IMAGE_MAX_PIXELS); a file field's max_pixels may only be lower. Zero
	// means 40 million.
	ImageMaxPixels int64
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
	// BackdURL is backd's public address, the base of the links in emails
	// (BACKD_URL); a realm's email.public_url overrides it.
	BackdURL string
	// Dev rereads function bundle manifests on every call instead of once
	// (BACKD_DEV), so a background rebuild is served without a restart.
	Dev bool
	// DisableAdminAPI leaves every /v1/{realm}/_admin route out: they answer
	// 404 like any route that doesn't exist (BACKD_ADMIN_API=false).
	DisableAdminAPI bool
	// UI, when set, is served at /_ui/ (the admin web interface).
	UI http.Handler
	// Metrics records what the API does; nil turns it off.
	Metrics *metrics.Metrics
}

// DefaultOpTimeout is the per-request storage deadline when none is configured.
const DefaultOpTimeout = 10 * time.Second

// DefaultMaxUploadBytes is the largest file a proxy upload carries when none is configured.
const DefaultMaxUploadBytes = 100 << 20

func maxUploadOf(cfg Config) int64 {
	if cfg.MaxUploadBytes > 0 {
		return cfg.MaxUploadBytes
	}
	return DefaultMaxUploadBytes
}

// DefaultMaxBodyBytes is the request body limit when none is configured.
const DefaultMaxBodyBytes = 1 << 20

// NewHandler builds the complete HTTP handler.
func NewHandler(cfg Config) http.Handler {
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	r := chi.NewRouter()
	r.Use(withRequestID(cfg.Log), withTrustedProxies(cfg.TrustedProxies), requestMetrics(cfg.Metrics), accessLog, recoverer, securityHeaders, cors(cfg.Registry), limitBody(maxBody))
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
	authRoutes := &authAPI{users: users, reg: cfg.Registry, actions: hostedActions(), opTimeout: opTimeout}
	authRoutes.routes(r)
	docs := &documents{reg: cfg.Registry, store: cfg.Store, now: now, maxBody: maxBody, opTimeout: opTimeout, users: users, callbackKey: cfg.CallbackKey, objects: newRealmObjects(cfg.Registry, users), baseURL: cfg.BackdURL, maxUpload: maxUploadOf(cfg), imageMaxPixels: cfg.ImageMaxPixels}
	fns := &functions{docs: docs, runner: cfg.Functions, callbackURL: cfg.CallbackURL, executorToken: cfg.ExecutorToken, log: cfg.Log, dev: cfg.Dev, concurrency: limiterFor(cfg.Registry), metrics: cfg.Metrics}
	if !cfg.DisableAdminAPI {
		(&adminAPI{users: users, reg: cfg.Registry, fns: fns, fingerprint: cfg.ConfigFingerprint, imageMaxPixels: cfg.ImageMaxPixels}).routes(r, authRoutes.resolveRealm, withTimeout(opTimeout))
	}
	if cfg.UI != nil {
		r.Handle("/_ui", http.RedirectHandler("/_ui/", http.StatusMovedPermanently))
		r.Handle("/_ui/*", cfg.UI)
	}
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
	r.Use(withRequestID(cfg.Log), requestMetrics(cfg.Metrics), accessLog, recoverer, securityHeaders, limitBody(maxBody))
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	docs := &documents{reg: cfg.Registry, store: cfg.Store, now: now, maxBody: maxBody, opTimeout: opTimeout, users: users, objects: newRealmObjects(cfg.Registry, users), baseURL: cfg.BackdURL, maxUpload: maxUploadOf(cfg), imageMaxPixels: cfg.ImageMaxPixels,
		internal: true, callbackKey: cfg.CallbackKey}
	fns := &functions{docs: docs, runner: cfg.Functions, callbackURL: cfg.CallbackURL, executorToken: cfg.ExecutorToken, log: cfg.Log, dev: cfg.Dev, concurrency: limiterFor(cfg.Registry), metrics: cfg.Metrics}
	r.Get("/_internal/functions/{sha256}", fns.bundle)
	fns.internalRoutes(r)
	docs.routes(r)
	return r
}
