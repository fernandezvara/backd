package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/go-chi/chi/v5"
)

func newTestHandler(t *testing.T, ready error) (http.Handler, *strings.Builder) {
	t.Helper()
	var logs strings.Builder
	h := NewHandler(Config{
		Log:   slog.New(slog.NewJSONHandler(&logs, nil)),
		Ready: func(context.Context) error { return ready },
	})
	return h, &logs
}

func do(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorPayload {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid error body %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

func TestHealthAndReady(t *testing.T) {
	h, _ := newTestHandler(t, nil)
	if rec := do(h, "GET", "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d", rec.Code)
	}
	if rec := do(h, "GET", "/readyz", nil); rec.Code != http.StatusOK {
		t.Errorf("/readyz = %d", rec.Code)
	}

	h, _ = newTestHandler(t, errors.New("mongo down"))
	rec := do(h, "GET", "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec).Code != codeUnavailable {
		t.Errorf("/readyz not ready = %d %s", rec.Code, rec.Body)
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	h, _ := newTestHandler(t, nil)

	rec := do(h, "GET", "/nope", nil)
	e := decodeError(t, rec)
	if rec.Code != http.StatusNotFound || e.Code != codeNotFound || e.RequestID == "" {
		t.Errorf("404 = %d %+v", rec.Code, e)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
	}

	rec = do(h, "POST", "/healthz", nil)
	if rec.Code != http.StatusMethodNotAllowed || decodeError(t, rec).Code != codeMethodNotAllowed {
		t.Errorf("405 = %d %s", rec.Code, rec.Body)
	}
}

func TestRequestID(t *testing.T) {
	h, logs := newTestHandler(t, nil)

	rec := do(h, "GET", "/nope", map[string]string{requestIDHeader: "client-id-1"})
	if got := rec.Header().Get(requestIDHeader); got != "client-id-1" {
		t.Errorf("echoed request id = %q", got)
	}
	if decodeError(t, rec).RequestID != "client-id-1" {
		t.Error("request id missing from error body")
	}
	if !strings.Contains(logs.String(), `"request_id":"client-id-1"`) {
		t.Errorf("request id missing from access log: %s", logs)
	}

	rec = do(h, "GET", "/healthz", map[string]string{requestIDHeader: "bad id\nwith newline"})
	if got := rec.Header().Get(requestIDHeader); got == "" || strings.ContainsAny(got, " \n") {
		t.Errorf("invalid client id not replaced: %q", got)
	}
}

func TestAccessLog(t *testing.T) {
	h, logs := newTestHandler(t, nil)
	do(h, "GET", "/healthz", nil)
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &line); err != nil {
		t.Fatalf("log line: %v", err)
	}
	if line["msg"] != "request" || line["method"] != "GET" || line["status"] != float64(200) || line["route"] != "/healthz" ||
		line["client"] != "192.0.2.1" {
		t.Errorf("unexpected access log line: %v", line)
	}
}

// TestAccessLogClientBehindProxy checks that the logged client address
// follows TRUSTED_PROXIES: X-Forwarded-For counts only from a trusted peer.
func TestAccessLogClientBehindProxy(t *testing.T) {
	for _, tt := range []struct {
		trusted string
		want    string
	}{
		{"192.0.2.1/32", "203.0.113.7"}, // the peer is the proxy
		{"10.0.0.0/8", "192.0.2.1"},     // the peer isn't trusted: header ignored
	} {
		var logs strings.Builder
		h := NewHandler(Config{
			Log:            slog.New(slog.NewJSONHandler(&logs, nil)),
			Ready:          func(context.Context) error { return nil },
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix(tt.trusted)},
		})
		do(h, "GET", "/healthz", map[string]string{"X-Forwarded-For": "203.0.113.7"})
		if want := `"client":"` + tt.want + `"`; !strings.Contains(logs.String(), want) {
			t.Errorf("TRUSTED_PROXIES=%s: log lacks %s: %s", tt.trusted, want, logs.String())
		}
	}
}

func TestRecoverer(t *testing.T) {
	var logs strings.Builder
	r := chi.NewRouter()
	r.Use(withRequestID(slog.New(slog.NewJSONHandler(&logs, nil))), recoverer)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := do(r, "GET", "/boom", nil)
	if rec.Code != http.StatusInternalServerError || decodeError(t, rec).Code != codeInternal {
		t.Errorf("panic response = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Error("panic value leaked to client")
	}
	if !strings.Contains(logs.String(), `"panic":"boom"`) {
		t.Errorf("panic not logged: %s", logs.String())
	}
}

func TestRequestMetricsByRoutePattern(t *testing.T) {
	m := metrics.New("dev", "x")
	h := NewHandler(Config{Log: slog.New(slog.NewJSONHandler(&strings.Builder{}, nil)), Ready: func(context.Context) error { return nil }, Metrics: m})
	do(h, "GET", "/healthz", nil)
	do(h, "GET", "/no/such/thing/for-user-ada@example.com", nil)
	do(h, "GET", "/v1/realm/db/coll/some-document-id", nil)
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, mf := range mfs {
		if mf.GetName() != "backd_http_requests_total" {
			continue
		}
		for _, s := range mf.GetMetric() {
			for _, l := range s.GetLabel() {
				if l.GetName() == "route" {
					routes = append(routes, l.GetValue())
				}
			}
		}
	}
	got := strings.Join(routes, " ")
	if !strings.Contains(got, "/healthz") || !strings.Contains(got, "unmatched") {
		t.Errorf("routes = %q", got)
	}
	for _, leak := range []string{"ada@example.com", "no/such", "some-document-id"} {
		if strings.Contains(got, leak) {
			t.Errorf("a request path leaked into a label: %q", got)
		}
	}
}
