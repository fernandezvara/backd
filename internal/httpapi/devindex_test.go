package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/devreport"
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
		c.DevProblems = func() []devreport.Problem {
			return []devreport.Problem{{Kind: devreport.Build, File: "shop/main/_functions/refund/index.ts", Line: 3, Message: "shop/main: function refund: deno bundle failed", Output: "error: Expected ';' <deno output>", Hint: "fix it and save"}}
		}
	})
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	for _, want := range []string{"previous config keeps running", "shop/main/_functions/refund/index.ts:3", "deno bundle failed", "error: Expected &#39;;&#39; &lt;deno output&gt;", "fix it and save", "<h2>Realms</h2>"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
}

func TestWaitingHandler(t *testing.T) {
	problems := []devreport.Problem{{Kind: devreport.Config, File: "app/realm.yaml", Line: 2, Message: "invalid YAML", Hint: "fix app/realm.yaml and save"}}
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("the admin ui")) })
	h := NewWaitingHandler(WaitingConfig{Problems: func() []devreport.Problem { return problems }, UI: ui})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	if rec := get("/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d", rec.Code)
	}
	if rec := get("/_ui/r/app"); rec.Code != http.StatusOK || rec.Body.String() != "the admin ui" {
		t.Errorf("/_ui/ = %d %q", rec.Code, rec.Body.String())
	}
	page := get("/")
	if page.Code != http.StatusServiceUnavailable || !strings.Contains(page.Body.String(), "app/realm.yaml:2") || strings.Contains(page.Body.String(), "<h2>Realms</h2>") {
		t.Errorf("/ = %d:\n%s", page.Code, page.Body.String())
	}
	for _, path := range []string{"/readyz", "/v1/app/main/notes", "/v1/app/_auth/login", "/v1/app/_admin/users", "/anything"} {
		rec := get(path)
		var body struct {
			Error struct {
				Code    string
				Details []Detail
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusServiceUnavailable || body.Error.Code != "config_not_loaded" ||
			len(body.Error.Details) != 1 || body.Error.Details[0].Path != "app/realm.yaml:2" || body.Error.Details[0].Code != "config" ||
			!strings.Contains(body.Error.Details[0].Reason, "fix app/realm.yaml") {
			t.Errorf("%s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
