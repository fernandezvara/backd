package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

func corsFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"web/realm.yaml":                "auth: disabled\ncors:\n  origins: [\"https://app.example.com\", \"http://localhost:5173\"]\n",
		"web/app/items/schema.json":     itemSchema,
		"pub/realm.yaml":                "auth: disabled\ncors:\n  origins: [\"*\"]\n",
		"pub/app/items/schema.json":     itemSchema,
		"shop/realm.yaml":               "auth: disabled\n",
		"shop/orders/items/schema.json": itemSchema,
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return newFixtureWith(t, reg, &memStore{})
}

func TestCORSPreflight(t *testing.T) {
	f := corsFixture(t)
	preflight := func(path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("OPTIONS", path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec
	}

	rec := preflight("/v1/web/app/items", "https://app.example.com")
	h := rec.Header()
	if rec.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "PATCH") ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		strings.Contains(h.Get("Access-Control-Allow-Headers"), "On-Behalf") ||
		h.Get("Access-Control-Allow-Credentials") != "" || !strings.Contains(strings.Join(h.Values("Vary"), ","), "Origin") {
		t.Errorf("allowed preflight: %d %v", rec.Code, h)
	}
	// Auth routes of the realm too.
	if rec := preflight("/v1/web/_auth/login", "http://localhost:5173"); rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("_auth preflight: %v", rec.Header())
	}
	if rec := preflight("/v1/pub/app/items", "https://anything.test"); rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("wildcard: %v", rec.Header())
	}
	for _, tt := range []struct{ path, origin string }{
		{"/v1/web/app/items", "https://evil.example.com"},
		{"/v1/web/app/items", "https://app.example.com.evil.test"},
		{"/v1/shop/orders/items", "https://app.example.com"}, // realm without origins
		{"/v1/nope/app/items", "https://app.example.com"},
		{"/healthz", "https://app.example.com"},
	} {
		rec := preflight(tt.path, tt.origin)
		if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Methods") != "" {
			t.Errorf("%s from %s: %d %v", tt.path, tt.origin, rec.Code, rec.Header())
		}
	}
}

func TestCORSResponses(t *testing.T) {
	f := corsFixture(t)
	rec, _ := f.doH(t, "POST", "/v1/web/app/items", `{"name": "a"}`, map[string]string{"Origin": "https://app.example.com"})
	if rec.Code != http.StatusCreated || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		!strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "ETag") {
		t.Errorf("allowed request: %d %v", rec.Code, rec.Header())
	}
	// Errors carry the headers too, so browsers can read them.
	rec, _ = f.doH(t, "POST", "/v1/web/app/items", `{`, map[string]string{"Origin": "https://app.example.com"})
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Errorf("error response: %d %v", rec.Code, rec.Header())
	}
	// Other origins get no CORS headers; the request itself still runs
	// (CORS protects browsers, it isn't access control).
	rec, _ = f.doH(t, "GET", "/v1/web/app/items", "", map[string]string{"Origin": "https://evil.example.com"})
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("other origin: %d %v", rec.Code, rec.Header())
	}
	// Plain OPTIONS without preflight headers isn't a route.
	rec, _ = f.doH(t, "OPTIONS", "/v1/web/app/items", "", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("plain OPTIONS: %d", rec.Code)
	}
}

// TestCachingHeaders checks the headers that keep caches from serving one
// caller's response to another (roadmap 1.4).
func TestCachingHeaders(t *testing.T) {
	f := corsFixture(t)
	send := func(method, path string, hdr map[string]string) http.Header {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if vs := rec.Header().Values("Vary"); len(vs) > 1 {
			t.Errorf("%s %s: Vary split over %d lines: %q", method, path, len(vs), vs)
		}
		return rec.Header()
	}
	vary := func(h http.Header) string { return h.Get("Vary") }

	for _, tt := range []struct {
		name, method, path string
		hdr                map[string]string
		wantVary           string
		wantCache          string
	}{
		{"anonymous data", "GET", "/v1/shop/orders/items", nil, "Origin, Authorization", ""},
		{"data with credentials", "GET", "/v1/shop/orders/items", map[string]string{"Authorization": "Bearer x"}, "Origin, Authorization", "private, no-cache"},
		{"one document with credentials", "GET", "/v1/shop/orders/items/nope", map[string]string{"Authorization": "Bearer x"}, "Origin, Authorization", "private, no-cache"},
		{"an origin that isn't allowed", "GET", "/v1/web/app/items", map[string]string{"Origin": "https://evil.example"}, "Origin, Authorization", ""},
		{"an allowed origin", "GET", "/v1/web/app/items", map[string]string{"Origin": "https://app.example.com"}, "Origin, Authorization", ""},
		{"unknown collection", "GET", "/v1/shop/orders/nope", nil, "Origin, Authorization", ""},
		{"preflight from an origin that isn't allowed", "OPTIONS", "/v1/web/app/items",
			map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "GET"}, "Origin", ""},
		{"preflight from an allowed origin", "OPTIONS", "/v1/web/app/items",
			map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET"}, "Origin, Access-Control-Request-Method, Access-Control-Request-Headers", ""},
		{"health checks", "GET", "/healthz", nil, "", ""},
	} {
		h := send(tt.method, tt.path, tt.hdr)
		if vary(h) != tt.wantVary || h.Get("Cache-Control") != tt.wantCache {
			t.Errorf("%s: Vary %q, Cache-Control %q; want %q and %q", tt.name, vary(h), h.Get("Cache-Control"), tt.wantVary, tt.wantCache)
		}
	}
}
