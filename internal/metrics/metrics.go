// Package metrics is backd's Prometheus instrumentation (design/metrics.md).
//
// A process builds one *Metrics and passes it to what it measures. Every
// method accepts a nil receiver and does nothing, so code that is measured
// never asks whether metrics are on. Label values come from closed sets or
// from the configuration (route patterns, realm, database and function
// names), never from what a caller sent: no ids, emails, addresses, tokens,
// key names or free text.
package metrics

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace prefixes every metric name.
const Namespace = "backd"

// UnmatchedRoute is the route label of a request no route matched, so
// probing random paths can't create series.
const UnmatchedRoute = "unmatched"

// DurationBuckets are the buckets of request and function durations, in seconds.
var DurationBuckets = []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}

// Metrics holds a process's collectors and the registry they are in. The
// registry is its own (not Prometheus' global one).
type Metrics struct {
	Registry *prometheus.Registry

	requests     *prometheus.CounterVec
	requestTime  *prometheus.HistogramVec
	inFlight     prometheus.Gauge
	authRefusals *prometheus.CounterVec

	mongoTime *prometheus.HistogramVec
	mongoUp   prometheus.Gauge
}

// New builds the registry with the Go runtime and process collectors, the
// build information and the collectors of the HTTP API.
func New(version, commit string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "http_requests_total",
			Help: "Requests answered, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		requestTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "http_request_duration_seconds",
			Help: "Time to answer a request, by method and route pattern.", Buckets: DurationBuckets,
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "http_in_flight_requests", Help: "Requests being answered right now.",
		}),
		authRefusals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace, Name: "http_refusals_total",
			Help: "Answers that refuse a caller: 401, 403, 429 and 503.",
		}, []string{"status"}),
		mongoTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace, Name: "mongodb_operation_duration_seconds",
			Help: "Time MongoDB took to run a command, by command and outcome (ok, error or timeout).", Buckets: MongoBuckets,
		}, []string{"operation", "outcome"}),
		mongoUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace, Name: "mongodb_up", Help: "1 while the last check of MongoDB worked, 0 when it failed.",
		}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace, Name: "build_info", Help: "The version and commit this process was built from (always 1).",
	}, []string{"version", "commit"})
	build.WithLabelValues(version, commit).Set(1)
	reg.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		build, m.requests, m.requestTime, m.inFlight, m.authRefusals, m.mongoTime, m.mongoUp,
	)
	return m
}

// Register adds collectors a package defines for itself.
func (m *Metrics) Register(c ...prometheus.Collector) {
	if m == nil {
		return
	}
	m.Registry.MustRegister(c...)
}

// RequestStarted and RequestDone bracket a request being answered.
func (m *Metrics) RequestStarted() {
	if m != nil {
		m.inFlight.Inc()
	}
}

// RequestDone records a finished request. route is the router's pattern ("" for
// a request no route matched).
func (m *Metrics) RequestDone(method, route string, status int, took time.Duration) {
	if m == nil {
		return
	}
	m.inFlight.Dec()
	if route == "" {
		route = UnmatchedRoute
	}
	method = knownMethod(method)
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.requestTime.WithLabelValues(method, route).Observe(took.Seconds())
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		m.authRefusals.WithLabelValues(strconv.Itoa(status)).Inc()
	}
}

// knownMethod maps anything but the common methods to OTHER: the method is
// chosen by the caller, and must not create series.
func knownMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// Handler serves the metrics at /metrics and nothing else. With a token,
// every request must carry it as a bearer token.
func (m *Metrics) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Timeout:       10 * time.Second,
	}))
	var want [sha256.Size]byte
	if token != "" {
		want = sha256.Sum256([]byte(token))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := sha256.Sum256([]byte(bearer(r)))
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && h[:len(prefix)] == prefix {
		return h[len(prefix):]
	}
	return ""
}

// Serve answers /metrics on addr until ctx ends. An empty addr means metrics
// are off and it returns at once. The listener is its own server: whatever
// happens to it never touches the API.
func (m *Metrics) Serve(ctx context.Context, addr, token string, shutdown time.Duration, log *slog.Logger) error {
	if m == nil || addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           m.Handler(token),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Info("metrics listening", "addr", ln.Addr().String(), "token", token != "")
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdown)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
