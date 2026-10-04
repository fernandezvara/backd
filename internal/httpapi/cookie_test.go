package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const appOrigin = "https://app.acme.example"

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func TestSessionCookies(t *testing.T) {
	f := newRulesFixture(t)
	post := func(path, body string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		h := map[string]string{"Content-Type": "application/json"}
		for k, v := range hdr {
			h[k] = v
		}
		return f.doH(t, "POST", path, body, h)
	}
	const login = "/v1/acme/_auth/login"
	body := `{"email": "ada@example.com", "password": "dev-p4ssw0rd!", "cookie": true}`

	// A login asking for a cookie must come from an allowed origin.
	if rec, _ := post(login, body, nil); rec.Code != http.StatusForbidden {
		t.Errorf("a cookie login without Origin: %d", rec.Code)
	}
	if rec, _ := post(login, body, map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden || sessionCookie(rec) != nil {
		t.Errorf("a cookie login from another site: %d", rec.Code)
	}

	// From the app: the cookie, and no token in the answer.
	rec, out := post(login, body, map[string]string{"Origin": appOrigin})
	c := sessionCookie(rec)
	if rec.Code != http.StatusOK || c == nil {
		t.Fatalf("cookie login: %d %s", rec.Code, rec.Body)
	}
	if _, has := out["token"]; has || out["session_id"] == nil || out["user"] == nil {
		t.Errorf("the response should hold the session but not the token: %v", out)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/v1/acme" || c.MaxAge <= 0 || !strings.HasPrefix(c.Value, "bds_") {
		t.Errorf("cookie attributes: %+v", c)
	}
	cookie := map[string]string{"Cookie": cookieName + "=" + c.Value}
	with := func(extra map[string]string) map[string]string {
		h := map[string]string{"Content-Type": "application/json"}
		for k, v := range cookie {
			h[k] = v
		}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}

	// Another login in the same realm still gets a token, no cookie.
	rec, out = post(login, strings.Replace(body, `, "cookie": true`, "", 1), nil)
	if rec.Code != http.StatusOK || out["token"] == nil || sessionCookie(rec) != nil {
		t.Errorf("a login without cookie: %d %v", rec.Code, out)
	}

	// The cookie authenticates reads, with the user's rules and private caching.
	rec, out = f.doH(t, "GET", "/v1/acme/_auth/me", "", with(nil))
	if rec.Code != http.StatusOK || out["email"] != "ada@example.com" {
		t.Errorf("me with the cookie: %d %v", rec.Code, out)
	}
	rec, _ = f.doH(t, "GET", posts, "", with(nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-cache" || !strings.Contains(rec.Header().Get("Vary"), "Cookie") {
		t.Errorf("list with the cookie: %d %v", rec.Code, rec.Header())
	}

	// Changes need an allowed Origin: CSRF.
	for name, tt := range map[string]struct {
		hdr  map[string]string
		want int
	}{
		"no Origin":            {nil, http.StatusForbidden},
		"another site":         {map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		"a null origin":        {map[string]string{"Origin": "null"}, http.StatusForbidden},
		"the app":              {map[string]string{"Origin": appOrigin}, http.StatusCreated},
		"the API's own origin": {map[string]string{"Origin": "http://example.com"}, http.StatusCreated}, // the test request's host
	} {
		if rec, _ := post(posts, `{"title": "t", "author": {"email": "ada@example.com"}}`, with(tt.hdr)); rec.Code != tt.want {
			t.Errorf("create with the cookie, %s: %d, want %d (%s)", name, rec.Code, tt.want, rec.Body)
		}
	}
	// Safe requests don't need it.
	if rec, _ := f.doH(t, "GET", posts, "", with(map[string]string{"Origin": "https://evil.example"})); rec.Code != http.StatusOK {
		t.Errorf("a read from another site is just a read: %d", rec.Code)
	}

	// The Authorization header wins: a bad one isn't rescued by a good cookie.
	if rec, _ := f.doH(t, "GET", "/v1/acme/_auth/me", "", with(map[string]string{"Authorization": "Bearer bds_nope"})); rec.Code != http.StatusUnauthorized {
		t.Errorf("a bad header with a good cookie: %d", rec.Code)
	}
	// The admin API takes no cookies.
	if rec, _ := f.doH(t, "GET", admin+"/users", "", with(nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin API with a cookie: %d", rec.Code)
	}

	// CORS allows credentials to the listed origins only.
	rec, _ = f.doH(t, "GET", posts, "", map[string]string{"Origin": appOrigin})
	if rec.Header().Get("Access-Control-Allow-Origin") != appOrigin || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("CORS for the app: %v", rec.Header())
	}
	rec, _ = f.doH(t, "GET", posts, "", map[string]string{"Origin": "https://evil.example"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("CORS for another site: %v", rec.Header())
	}

	// Logout needs the origin too, then clears the cookie; the cookie is dead.
	if rec, _ := post("/v1/acme/_auth/logout", "", with(nil)); rec.Code != http.StatusForbidden {
		t.Errorf("logout without Origin: %d", rec.Code)
	}
	rec, _ = post("/v1/acme/_auth/logout", "", with(map[string]string{"Origin": appOrigin}))
	if cleared := sessionCookie(rec); rec.Code != http.StatusNoContent || cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("logout: %d %v", rec.Code, rec.Header())
	}
	rec, _ = f.doH(t, "GET", "/v1/acme/_auth/me", "", with(nil))
	if cleared := sessionCookie(rec); rec.Code != http.StatusUnauthorized || cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("a dead cookie should be refused and cleared: %d %v", rec.Code, rec.Header())
	}
}

// Sign-up can ask for a cookie too, and a realm that doesn't use them refuses.
func TestSignupCookieAndRealmsWithoutCookies(t *testing.T) {
	f := newRulesFixture(t)
	rec, out := f.doH(t, "POST", "/v1/acme/_auth/signup", `{"email": "new@example.com", "password": "dev-p4ssw0rd!x", "cookie": true}`,
		map[string]string{"Content-Type": "application/json", "Origin": appOrigin})
	if c := sessionCookie(rec); rec.Code != http.StatusCreated && rec.Code != http.StatusAccepted || (rec.Code == http.StatusCreated && (c == nil || out["token"] != nil)) {
		t.Errorf("sign-up with a cookie: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", "/v1/acme/_auth/signup", `{"email": "x@example.com", "password": "dev-p4ssw0rd!x", "cookie": true}`, map[string]string{"Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Errorf("sign-up with a cookie and no Origin: %d", rec.Code)
	}
}
