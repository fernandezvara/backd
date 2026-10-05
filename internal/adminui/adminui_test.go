package adminui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><title>admin</title>")},
		"assets/app-abc.js": {Data: []byte("console.log(1)")},
		"favicon.svg":       {Data: []byte("<svg/>")},
	}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestNoAssetsNoHandler(t *testing.T) {
	if Handler(time.Minute) != nil {
		t.Fatal("a build without assets must serve nothing")
	}
}

func TestServing(t *testing.T) {
	h := newHandler(testFS(), 90*time.Second)
	cases := []struct {
		path, cache string
		status      int
	}{
		{"/_ui/", "no-store", 200},
		{"/_ui/r/acme/users", "no-store", 200}, // deep link: the page
		{"/_ui/assets/app-abc.js", "public, max-age=31536000, immutable", 200},
		{"/_ui/assets/missing.js", "", 404},
		{"/_ui/favicon.svg", "no-cache", 200},
		{"/_ui/missing.png", "", 404},
		{"/_ui/config.json", "no-store", 200},
	}
	for _, tc := range cases {
		w := get(t, h, "GET", tc.path)
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, w.Code, tc.status)
		}
		if tc.cache != "" && w.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s: Cache-Control %q, want %q", tc.path, w.Header().Get("Cache-Control"), tc.cache)
		}
	}
	if b := get(t, h, "GET", "/_ui/config.json").Body.String(); b != `{"idle_seconds":90}` {
		t.Errorf("config.json = %s", b)
	}
	if w := get(t, h, "POST", "/_ui/"); w.Code != 405 {
		t.Errorf("POST = %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHandler(testFS(), time.Minute)
	for _, p := range []string{"/_ui/", "/_ui/assets/app-abc.js", "/_ui/nothing.png"} {
		hd := get(t, h, "GET", p).Header()
		csp := hd.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %s", p, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-") {
			t.Errorf("%s: CSP has an unsafe source: %s", p, csp)
		}
		for _, k := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
			if hd.Get(k) == "" {
				t.Errorf("%s: no %s", p, k)
			}
		}
	}
}
