package httpapi

import (
	"context"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/metrics"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
	actorKey
)

// setActor records who made the request (e.g. "user:<id>") for the
// access log. Credentials are never logged.
func setActor(ctx context.Context, actor string) {
	if ref, ok := ctx.Value(actorKey).(*string); ok {
		*ref = actor
	}
}

// requestIDHeader carries the request ID in both directions.
const requestIDHeader = "X-Request-ID"

// validRequestID bounds client-supplied IDs so they are safe to log and echo.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// logger returns the request-scoped logger (carrying request_id).
func logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// withRequestID uses the incoming X-Request-ID when valid, otherwise
// generates one. It echoes it in the response and attaches a logger that
// includes it on every line.
func withRequestID(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if !validRequestID.MatchString(id) {
				id = xid.New().String()
			}
			w.Header().Set(requestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			ctx = context.WithValue(ctx, loggerKey, base.With("request_id", id))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// accessLog writes one line per request. Bodies are never logged. The
// client address follows TRUSTED_PROXIES (see clientIP), so it must run
// after withTrustedProxies.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		actor := new(string)
		ctx := context.WithValue(r.Context(), actorKey, actor)
		// Audit records name the same actor as this line, once known.
		ctx = auth.WithAuditSource(ctx, func() auth.AuditSource {
			return auth.AuditSource{Actor: *actor, RequestID: requestID(ctx), ClientIP: clientIP(r)}
		})
		next.ServeHTTP(ww, r.WithContext(ctx))

		attrs := []any{
			"method", r.Method,
			"status", ww.Status(),
			"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
			"bytes", ww.BytesWritten(),
			"client", clientIP(r),
		}
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				attrs = append(attrs, "route", p)
			}
			for _, k := range []string{"realm", "database", "collection"} {
				if v := rc.URLParam(k); v != "" {
					attrs = append(attrs, k, v)
				}
			}
		}
		if *actor != "" {
			attrs = append(attrs, "actor", *actor)
		}
		logger(r.Context()).Info("request", attrs...)
	})
}

// requestMetrics counts and times every request by method, route pattern and
// status. The pattern is read after routing; a request no route matched is
// one label, whatever its path.
func requestMetrics(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if m == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			m.RequestStarted()
			defer func() {
				route := ""
				if rc := chi.RouteContext(r.Context()); rc != nil {
					route = rc.RoutePattern()
				}
				status := ww.Status()
				if status == 0 {
					status = http.StatusOK
				}
				m.RequestDone(r.Method, route, status, time.Since(start))
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// limitBody caps request bodies at max bytes. Declared lengths over the
// limit are rejected at once; streamed bodies fail when read (see readObject).
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				tooLarge(w, r, max)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

func tooLarge(w http.ResponseWriter, r *http.Request, max int64) {
	writeError(w, r, http.StatusRequestEntityTooLarge, codePayloadTooLarge,
		"request body exceeds "+strconv.FormatInt(max, 10)+" bytes")
}

// requireContentType answers 415 unless the request's media type is want.
// The only accepted parameter is charset=utf-8.
func requireContentType(want string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !contentTypeIs(r, want) {
				writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be "+want)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// contentTypeIs reports whether r's Content-Type is exactly want, with
// no parameters besides charset=utf-8. Reused directly (not as
// middleware) where the requirement is conditional on something only
// known inside the handler, such as a function's mode.
func contentTypeIs(r *http.Request, want string) bool {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	ok := err == nil && mt == want
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			ok = false
		}
	}
	return ok
}

// recoverer turns panics into 500 responses and logs the stack.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger(r.Context()).Error("panic", "panic", v, "stack", string(debug.Stack()))
				writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets headers every response carries: responses are
// JSON and must never be sniffed as another type.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// dataCaching sets the caching headers of document routes. Responses
// depend on the caller, so caches must key them on Authorization; those
// to requests with credentials are private to that caller: shared caches
// never store them, and browsers revalidate before reusing them.
func dataCaching(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addVary(w.Header(), "Authorization")
		if r.Header.Get("Authorization") != "" {
			w.Header().Set("Cache-Control", "private, no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// addVary adds names to the Vary header, keeping it a single header
// line so clients reading only its first value see them all.
func addVary(h http.Header, names ...string) {
	var all []string
	for _, v := range h.Values("Vary") {
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n != "" {
				all = append(all, n)
			}
		}
	}
	for _, n := range names {
		if !slices.ContainsFunc(all, func(x string) bool { return strings.EqualFold(x, n) }) {
			all = append(all, n)
		}
	}
	h.Set("Vary", strings.Join(all, ", "))
}

// noStore keeps responses out of caches: they carry credentials or
// account data.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
