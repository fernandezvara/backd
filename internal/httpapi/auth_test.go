package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

const authBase = "/v1/acme/_auth"

type authFixture struct {
	*fixture
	svc   *auth.Users
	store *authtest.MemStore
}

// newAuthFixture serves realm "acme" with auth enabled and open sign-up.
func newAuthFixture(t *testing.T, opts ...func(*Config)) *authFixture {
	t.Helper()
	store := authtest.NewMemStore()
	svc := &auth.Users{
		Store:  store,
		Hasher: auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{
			AuthEnabled: true, Signup: registry.SignupOpen,
			IdleTimeout: time.Hour, MaxLifetime: 24 * time.Hour, PasswordMinLength: 12,
		},
	}
	users := func(realm string) *auth.Users {
		if realm == "acme" {
			return svc
		}
		return nil
	}
	f := newFixtureWith(t, itemsRegistry(t), &memStore{}, append([]func(*Config){func(c *Config) { c.Users = users }}, opts...)...)
	return &authFixture{fixture: f, svc: svc, store: store}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// signup creates a user over HTTP and returns the session token.
func (f *authFixture) signup(t *testing.T, email string) (string, map[string]any) {
	t.Helper()
	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %s: %d %v", email, rec.Code, out)
	}
	return out["token"].(string), out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestAuthSignupAndLogin(t *testing.T) {
	f := newAuthFixture(t)

	token, out := f.signup(t, "Ada@Example.com")
	user := out["user"].(map[string]any)
	if !strings.HasPrefix(token, "bds_") || out["token_type"] != "Bearer" || out["session_id"] == "" || out["expires_at"] == "" ||
		user["email"] != "ada@example.com" || user["email_verified"] != false || user["id"] == "" {
		t.Errorf("signup response = %v", out)
	}

	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusConflict || errCode(out) != "email_taken" {
		t.Errorf("duplicate signup: %d %v", rec.Code, out)
	}

	rec, out = f.do(t, "POST", authBase+"/login", `{"email": "ADA@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusOK || !strings.HasPrefix(out["token"].(string), "bds_") || out["token"] == token {
		t.Errorf("login: %d %v", rec.Code, out)
	}

	for _, body := range []string{
		`{"email": "ada@example.com", "password": "dev-p4ssw0rd!0"}`,
		`{"email": "nobody@example.com", "password": "dev-p4ssw0rd!"}`,
	} {
		rec, out := f.do(t, "POST", authBase+"/login", body)
		if rec.Code != http.StatusUnauthorized || errCode(out) != "invalid_credentials" {
			t.Errorf("login %s: %d %v", body, rec.Code, out)
		}
	}
}

func TestAuthSignupValidation(t *testing.T) {
	f := newAuthFixture(t)
	tests := []struct {
		name, body string
		wantCode   int
		wantErr    string
		wantPath   string
	}{
		{"missing password", `{"email": "a@x.io"}`, 400, "validation_error", "password"},
		{"wrong type", `{"email": "a@x.io", "password": 12345678901234}`, 400, "validation_error", "password"},
		{"unknown field", `{"email": "a@x.io", "password": "dev-p4ssw0rd!", "role": "admin"}`, 400, "validation_error", "role"},
		{"bad email", `{"email": "nope", "password": "dev-p4ssw0rd!"}`, 400, "validation_error", "email"},
		{"weak password", `{"email": "a@x.io", "password": "short"}`, 400, "validation_error", "password"},
		{"not json", `{`, 400, "invalid_json", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, out := f.do(t, "POST", authBase+"/signup", tt.body)
			if rec.Code != tt.wantCode || errCode(out) != tt.wantErr {
				t.Fatalf("%d %v", rec.Code, out)
			}
			if tt.wantPath != "" && !strings.Contains(rec.Body.String(), `"path":"`+tt.wantPath+`"`) {
				t.Errorf("details don't mention %q: %s", tt.wantPath, rec.Body)
			}
		})
	}

	rec, _ := f.doH(t, "POST", authBase+"/login", `{}`, map[string]string{"Content-Type": "text/plain"})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("login without JSON content type: %d", rec.Code)
	}
}

func TestAuthSignupModes(t *testing.T) {
	f := newAuthFixture(t)
	f.svc.Settings.Signup = registry.SignupClosed
	rec, out := f.do(t, "POST", authBase+"/signup", `{"email": "a@x.io", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusForbidden || errCode(out) != "forbidden" {
		t.Errorf("closed signup: %d %v", rec.Code, out)
	}
}

func TestAuthRealmResolution(t *testing.T) {
	f := newAuthFixture(t)
	// "shop" has auth disabled; "nope" doesn't exist.
	for _, realm := range []string{"shop", "nope"} {
		rec, out := f.do(t, "POST", "/v1/"+realm+"/_auth/login", `{"email": "a@x.io", "password": "x"}`)
		if rec.Code != http.StatusNotFound || errCode(out) != "not_found" {
			t.Errorf("%s: %d %v", realm, rec.Code, out)
		}
	}
}

func TestAuthRequireSession(t *testing.T) {
	f := newAuthFixture(t)
	token, _ := f.signup(t, "ada@example.com")

	rec, out := f.doH(t, "GET", authBase+"/me", "", nil)
	if rec.Code != http.StatusUnauthorized || errCode(out) != "unauthenticated" || rec.Header().Get("WWW-Authenticate") != `Bearer realm="acme"` {
		t.Errorf("no token: %d %v %q", rec.Code, out, rec.Header().Get("WWW-Authenticate"))
	}
	for _, hdr := range []string{"Bearer bds_unknown", "Bearer " + token + "x", "Basic " + token, "Bearer "} {
		rec, out := f.doH(t, "GET", authBase+"/me", "", map[string]string{"Authorization": hdr})
		if rec.Code != http.StatusUnauthorized || errCode(out) != "unauthenticated" {
			t.Errorf("%q: %d %v", hdr, rec.Code, out)
		}
	}
	rec, _ = f.doH(t, "GET", authBase+"/me", "", bearer("bds_unknown"))
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="acme", error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q", got)
	}

	rec, out = f.doH(t, "GET", authBase+"/me", "", map[string]string{"Authorization": "bearer " + token})
	if rec.Code != http.StatusOK || out["email"] != "ada@example.com" || out["roles"] == nil {
		t.Errorf("me: %d %v", rec.Code, out)
	}
}

func TestAuthSessionEndpoints(t *testing.T) {
	f := newAuthFixture(t)
	t1, first := f.signup(t, "ada@example.com")
	_, login := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	t2 := login["token"].(string)

	rec, out := f.doH(t, "GET", authBase+"/sessions", "", bearer(t1))
	items, _ := out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 2 {
		t.Fatalf("sessions: %d %v", rec.Code, out)
	}
	current := 0
	for _, it := range items {
		m := it.(map[string]any)
		if m["current"] == true {
			current++
			if m["id"] != first["session_id"] {
				t.Errorf("current session = %v, want %v", m["id"], first["session_id"])
			}
		}
		if _, leaked := m["token_hash"]; leaked {
			t.Error("token hash exposed")
		}
	}
	if current != 1 {
		t.Errorf("%d sessions marked current", current)
	}

	// Revoke the second session from the first.
	rec, _ = f.doH(t, "DELETE", authBase+"/sessions/"+login["session_id"].(string), "", bearer(t1))
	if rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(t2)); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", rec.Code)
	}
	rec, out = f.doH(t, "DELETE", authBase+"/sessions/nope", "", bearer(t1))
	if rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown: %d %v", rec.Code, out)
	}

	// Another user can't revoke Ada's session.
	bob, _ := f.signup(t, "bob@example.com")
	if rec, _ := f.doH(t, "DELETE", authBase+"/sessions/"+first["session_id"].(string), "", bearer(bob)); rec.Code != http.StatusNotFound {
		t.Errorf("revoke other's session: %d", rec.Code)
	}

	rec, _ = f.doH(t, "POST", authBase+"/logout", "", bearer(t1))
	if rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(t1)); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", rec.Code)
	}

	_, a := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	_, b := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec, _ := f.doH(t, "POST", authBase+"/logout-all", "", bearer(a["token"].(string))); rec.Code != http.StatusNoContent {
		t.Errorf("logout-all: %d", rec.Code)
	}
	for _, tok := range []any{a["token"], b["token"]} {
		if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(tok.(string))); rec.Code != http.StatusUnauthorized {
			t.Errorf("after logout-all: %d", rec.Code)
		}
	}
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(bob)); rec.Code != http.StatusOK {
		t.Errorf("bob logged out by Ada's logout-all: %d", rec.Code)
	}
}

func TestAuthPasswordAndDeleteMe(t *testing.T) {
	f := newAuthFixture(t)
	token, _ := f.signup(t, "ada@example.com")
	_, other := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	jsonBearer := func(tok string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"}
	}

	rec, out := f.doH(t, "POST", authBase+"/password", `{"current_password": "dev-p4ssw0rd!0", "new_password": "dev-p4ssw0rd!2"}`, jsonBearer(token))
	if rec.Code != http.StatusUnauthorized || errCode(out) != "invalid_credentials" {
		t.Errorf("wrong current password: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "POST", authBase+"/password", `{"current_password": "dev-p4ssw0rd!", "new_password": "short"}`, jsonBearer(token))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"path":"new_password"`) {
		t.Errorf("weak new password: %d %v", rec.Code, out)
	}
	rec, _ = f.doH(t, "POST", authBase+"/password", `{"current_password": "dev-p4ssw0rd!", "new_password": "dev-p4ssw0rd!2"}`, jsonBearer(token))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change password: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(token)); rec.Code != http.StatusOK {
		t.Errorf("current session ended by password change: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(other["token"].(string))); rec.Code != http.StatusUnauthorized {
		t.Errorf("other session survived password change: %d", rec.Code)
	}

	rec, _ = f.doH(t, "DELETE", authBase+"/me", `{"password": "dev-p4ssw0rd!"}`, jsonBearer(token))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("delete with old password: %d", rec.Code)
	}
	rec, _ = f.doH(t, "DELETE", authBase+"/me", `{"password": "dev-p4ssw0rd!2"}`, jsonBearer(token))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete me: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", authBase+"/me", "", bearer(token)); rec.Code != http.StatusUnauthorized {
		t.Errorf("session survived account deletion: %d", rec.Code)
	}
	rec, out = f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!2"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted user logged in: %d %v", rec.Code, out)
	}
}

func TestAuthErrorMapping(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{auth.ErrBusy, http.StatusServiceUnavailable, "unavailable"},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, "unavailable"},
		{auth.ErrInvalidCredentials, http.StatusUnauthorized, "invalid_credentials"},
		{auth.ErrSignupClosed, http.StatusForbidden, "forbidden"},
		{errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		authError(rec, httptest.NewRequest("POST", authBase+"/login", nil), tt.err)
		if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), `"code":"`+tt.wantCode+`"`) {
			t.Errorf("%v: %d %s", tt.err, rec.Code, rec.Body)
		}
		if tt.wantStatus == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") != "1" {
			t.Errorf("%v: Retry-After = %q", tt.err, rec.Header().Get("Retry-After"))
		}
	}
}

func TestAccessLogActor(t *testing.T) {
	var buf bytes.Buffer
	f := newAuthFixture(t, func(c *Config) { c.Log = slog.New(slog.NewJSONHandler(&buf, nil)) })
	token, out := f.signup(t, "ada@example.com")
	id := out["user"].(map[string]any)["id"].(string)
	buf.Reset()
	f.doH(t, "GET", authBase+"/me", "", bearer(token))
	log := buf.String()
	if !strings.Contains(log, `"actor":"user:`+id+`"`) {
		t.Errorf("actor missing from access log: %s", log)
	}
	if strings.Contains(log, token) || strings.Contains(log, token[4:]) {
		t.Error("token written to the log")
	}
	buf.Reset()
	f.do(t, "GET", "/healthz", "")
	if strings.Contains(buf.String(), `"actor"`) {
		t.Errorf("actor logged for an unauthenticated request: %s", buf.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newAuthFixture(t)
	token, _ := f.signup(t, "ada@example.com")
	for _, path := range []string{authBase + "/me", "/v1/acme/_admin/users"} {
		rec, _ := f.doH(t, "GET", path, "", bearer(token))
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %v", path, rec.Header())
		}
	}
	rec, _ := f.do(t, "POST", authBase+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("login response cacheable: %v", rec.Header())
	}
	rec, _ = f.do(t, "GET", "/healthz", "")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "" {
		t.Errorf("healthz: %v", rec.Header())
	}
}

// A sign-up starts a session, and logging in with the same credentials right
// after works, even many times at once: nothing makes a fresh account's first
// login fail. (The workshop tour logs in first to find out whether a demo
// account exists, and gets the 401 of an unknown email; it signs up after.)
func TestLoginRightAfterSignup(t *testing.T) {
	f := newRulesFixture(t)
	creds := `{"email": "fresh@example.com", "password": "dev-p4ssw0rd!"}`
	post := func(path, body string) (int, map[string]any) {
		rec, out := f.doH(t, "POST", "/v1/acme/_auth/"+path, body, map[string]string{"Content-Type": "application/json"})
		return rec.Code, out
	}

	// Unknown email: the 401 a probe sees, before the account exists.
	if code, out := post("login", creds); code != 401 || out["error"].(map[string]any)["code"] != "invalid_credentials" {
		t.Fatalf("login before sign-up: %d %v", code, out)
	}
	code, signup := post("signup", creds)
	if code != 201 || signup["token"] == nil {
		t.Fatalf("signup: %d %v", code, signup)
	}
	// The sign-up's own session works.
	if code, _ := f.as(t, signup["token"].(string), "GET", "/v1/acme/_auth/me", ""); code != 200 {
		t.Errorf("the session of the sign-up: %d", code)
	}
	// Logging in at once, and many at once, succeeds with a session of its own.
	results := make(chan int, 10)
	tokens := make(chan string, 10)
	for i := 0; i < 10; i++ {
		go func() {
			code, out := post("login", creds)
			results <- code
			if t, ok := out["token"].(string); ok {
				tokens <- t
			} else {
				tokens <- ""
			}
		}()
	}
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		if code := <-results; code != 200 {
			t.Errorf("login %d right after sign-up: %d", i, code)
		}
		seen[<-tokens] = true
	}
	if seen[""] || len(seen) != 10 {
		t.Errorf("each login should get its own session token: %d distinct", len(seen))
	}
}
