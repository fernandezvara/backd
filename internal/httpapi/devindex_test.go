package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The dev landing page lists the running realms and links the things a
// developer opens next; it exists only while BACKD_DEV=true.

func TestDevIndexOnlyInDevMode(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET / without dev mode: %d", rec.Code)
	}
}

func TestDevIndexListsRealmsAndLinks(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Dev = true
		c.UI = http.NotFoundHandler()
		c.ConfigFingerprint = "abc123fingerprint"
		c.DevLinks = []DevLink{{Label: "MinIO console", URL: "http://127.0.0.1:9001"}}
	})
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET / in dev mode: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"shop", "orders", // realm and database from the fixture
		"/_ui/r/shop", "/_ui/", // admin UI links
		"/v1/shop/_openapi.json", // per-realm OpenAPI
		"/healthz", "/readyz",    // probes
		"abc123fingerprint",                      // config fingerprint
		"MinIO console", "http://127.0.0.1:9001", // BACKD_DEV_LINKS entry
		"fernandezvara.github.io/backd/docs", // docs
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestDevIndexHidesUIWhenDisabled(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Dev = true })
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rec.Body.String(), "/_ui") {
		t.Errorf("page links the admin UI though it is disabled")
	}
}

func TestDevIndexShowsReloadErrors(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Dev = true
		c.ReloadErrors = func() []string { return []string{"bad schema in shop/orders/items"} }
	})
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "bad schema in shop/orders/items") {
		t.Errorf("page lacks the reload error")
	}
}
