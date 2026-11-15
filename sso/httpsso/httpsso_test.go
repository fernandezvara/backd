package httpsso_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/sso/httpsso"
)

func TestRedirectAddsTheParametersAndNeverCaches(t *testing.T) {
	for method, status := range map[string]int{http.MethodGet: http.StatusFound, http.MethodPost: http.StatusSeeOther} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/cb", nil)
		if !httpsso.Redirect(rec, r, "https://app.example/home?tab=1", "backd_code", "c1", "backd_org", "o1") {
			t.Fatal("not a URL")
		}
		u, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != status || u.Query().Get("tab") != "1" || u.Query().Get("backd_code") != "c1" || u.Query().Get("backd_org") != "o1" ||
			rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: %d %v %v", method, rec.Code, rec.Header(), u)
		}
	}
	if httpsso.Redirect(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), "ht tp://%zz") {
		t.Error("a bad URL was redirected to")
	}
}

func TestCallbackReadsTheQueryAndTheForm(t *testing.T) {
	in, ok := httpsso.Callback(httptest.NewRequest("GET", "/cb?state=s&code=c&error=e", nil), "realm", "google")
	if !ok || in.State != "s" || in.Code != "c" || in.Error != "e" || in.Scope != "realm" || in.Provider != "google" {
		t.Errorf("GET = %+v, %v", in, ok)
	}
	req := httptest.NewRequest("POST", "/cb", strings.NewReader("state=s2&code=c2&user=%7B%7D"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if in, ok := httpsso.Callback(req, "realm", "apple"); !ok || in.State != "s2" || in.User != "{}" {
		t.Errorf("POST = %+v, %v", in, ok)
	}
}
