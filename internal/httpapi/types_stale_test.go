package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/typegen"
)

func TestStaleTypesHeader(t *testing.T) {
	f := corsFixture(t)
	out, err := typegen.Generate(f.reg, typegen.Options{Realm: "web"})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path, fingerprint, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if fingerprint != "" {
			req.Header.Set("X-Backd-Types", fingerprint)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec
	}

	if got := get("/v1/web/app/items", out.Info.Fingerprint, "").Header().Get("X-Backd-Types-Stale"); got != "" {
		t.Errorf("current types flagged stale: %q", got)
	}
	rec := get("/v1/web/app/items", "0000", "https://app.example.com")
	if rec.Header().Get("X-Backd-Types-Stale") != "true" {
		t.Errorf("stale types not flagged: %v", rec.Header())
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "X-Backd-Types-Stale") {
		t.Errorf("browsers can't read the header: %v", rec.Header())
	}
	if got := get("/v1/web/app/items", "", "").Header().Get("X-Backd-Types-Stale"); got != "" {
		t.Errorf("a client that sent no fingerprint got %q", got)
	}
	if got := get("/v1/nope/app/items", "0000", "").Header().Get("X-Backd-Types-Stale"); got != "" {
		t.Errorf("unknown realm flagged: %q", got)
	}
	if rec := get("/healthz", "0000", ""); rec.Code != http.StatusOK || rec.Header().Get("X-Backd-Types-Stale") != "" {
		t.Errorf("healthz: %d %v", rec.Code, rec.Header())
	}
}
