package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics, token string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	m.Handler(token).ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestNilMetricsDoNothing(t *testing.T) {
	var m *Metrics
	m.RequestStarted()
	m.RequestDone("GET", "/x", 200, time.Second)
	m.Register()
}

func TestRequestMetrics(t *testing.T) {
	m := New("v1.2.3", "abc123")
	for _, c := range []struct {
		method, route string
		status        int
	}{
		{"GET", "/v1/{realm}/{database}/{collection}", 200},
		{"GET", "/v1/{realm}/{database}/{collection}", 401},
		{"POST", "", 404},
		{"BREW", "", 404}, // a method nobody uses
		{"GET", "/v1/{realm}/_auth/login", 429},
	} {
		m.RequestStarted()
		m.RequestDone(c.method, c.route, c.status, 5*time.Millisecond)
	}
	code, body := scrape(t, m, "", nil)
	if code != 200 {
		t.Fatalf("scrape = %d", code)
	}
	for _, want := range []string{
		`backd_http_requests_total{method="GET",route="/v1/{realm}/{database}/{collection}",status="200"} 1`,
		`backd_http_requests_total{method="POST",route="unmatched",status="404"} 1`,
		`backd_http_requests_total{method="OTHER",route="unmatched",status="404"} 1`,
		`backd_http_refusals_total{status="401"} 1`,
		`backd_http_refusals_total{status="429"} 1`,
		`backd_http_in_flight_requests 0`,
		`backd_build_info{commit="abc123",version="v1.2.3"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "BREW") {
		t.Error("an arbitrary method became a label")
	}
}

func TestHandlerOnlyServesMetrics(t *testing.T) {
	m := New("dev", "x")
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/metrics", 200}, {"GET", "/", 404}, {"GET", "/v1/realm/db/coll", 404},
		{"POST", "/metrics", 405}, {"GET", "/metrics/x", 404},
	} {
		rec := httptest.NewRecorder()
		m.Handler("").ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
	rec := httptest.NewRecorder()
	m.Handler("").ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the answer may be cached")
	}
}

func TestToken(t *testing.T) {
	m := New("dev", "x")
	const token = "0123456789abcdef0123456789abcdef"
	for name, hdr := range map[string]map[string]string{
		"none":      nil,
		"wrong":     {"Authorization": "Bearer nope"},
		"prefix":    {"Authorization": "Bearer " + token[:10]},
		"suffix":    {"Authorization": "Bearer " + token + "x"},
		"basic":     {"Authorization": "Basic " + token},
		"bare":      {"Authorization": token},
		"lowercase": {"Authorization": "bearer " + token},
	} {
		code, body := scrape(t, m, token, hdr)
		if code != http.StatusUnauthorized || strings.Contains(body, "backd_") {
			t.Errorf("%s: %d %q", name, code, body)
		}
	}
	if code, _ := scrape(t, m, token, map[string]string{"Authorization": "Bearer " + token}); code != 200 {
		t.Errorf("with the token = %d", code)
	}
	// Even a path that doesn't exist asks for the token first: nothing to probe.
	rec := httptest.NewRecorder()
	m.Handler(token).ServeHTTP(rec, httptest.NewRequest("GET", "/other", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown path without a token = %d", rec.Code)
	}
}
