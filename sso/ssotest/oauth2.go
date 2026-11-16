package ssotest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/fernandezvara/backd/sso"
)

// OAuth2 is a small OAuth 2 service for tests, without ID tokens: it plays GitHub (a /user and a
// /user/emails) or a plain service (a userinfo at /user) and checks that the access token it gave is
// the one presented.
type OAuth2 struct {
	*httptest.Server

	mu        sync.Mutex
	user      map[string]any
	emails    []map[string]any
	codes     map[string]string // code → PKCE challenge
	tokens    map[string]bool   // access tokens given
	forms     []url.Values
	status    int
	errCode   string
	errBody   string // answer 200 with this error (GitHub does)
	userCode  int    // a status for /user instead of the user
	userCalls int
}

// NewOAuth2 starts the service and stops it when the test ends.
func NewOAuth2(t testing.TB) *OAuth2 {
	t.Helper()
	o := &OAuth2{codes: map[string]string{}, tokens: map[string]bool{}}
	o.Server = httptest.NewServer(http.HandlerFunc(o.serve))
	t.Cleanup(o.Close)
	return o
}

// Endpoints points a provider at the service.
func (o *OAuth2) Endpoints() sso.Endpoints {
	return sso.Endpoints{Authorize: o.URL + "/authorize", Token: o.URL + "/token", UserInfo: o.URL + "/user", Emails: o.URL + "/user/emails"}
}

// Host is the service's host, without the port.
func (o *OAuth2) Host() string {
	u, _ := url.Parse(o.URL)
	return u.Hostname()
}

// Client is an HTTP client that reaches the service only (it listens on a loopback address).
func (o *OAuth2) Client() *http.Client { return ClientFor(o.Server) }

// SetUser sets what /user answers; SetEmails what /user/emails answers.
func (o *OAuth2) SetUser(u map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.user = u
}

// SetEmails sets the list /user/emails answers.
func (o *OAuth2) SetEmails(e []map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.emails = e
}

// FailToken makes the token endpoint answer status with an error code, until FailToken(0, "").
func (o *OAuth2) FailToken(status int, code string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status, o.errCode = status, code
}

// RefuseWith200 makes the token endpoint answer 200 with an error, as GitHub does for a bad code.
func (o *OAuth2) RefuseWith200(code string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.errBody = code
}

// FailUser makes /user answer status instead of the user, until FailUser(0).
func (o *OAuth2) FailUser(status int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.userCode = status
}

// TokenRequests are the forms the token endpoint received.
func (o *OAuth2) TokenRequests() []url.Values {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]url.Values(nil), o.forms...)
}

// UserCalls is how many times /user was asked.
func (o *OAuth2) UserCalls() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.userCalls
}

// Authorize plays the browser who signs in and consents: it returns the URL the service sends the
// browser back to (backd's callback, with the code and the state).
func (o *OAuth2) Authorize(authorizeURL string) string {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	code := "code-" + randomString()
	o.mu.Lock()
	o.codes[code] = q.Get("code_challenge")
	o.mu.Unlock()
	back, _ := url.Parse(q.Get("redirect_uri"))
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

func (o *OAuth2) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case r.URL.Path == "/token" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		o.forms = append(o.forms, r.PostForm)
		if o.status != 0 {
			w.WriteHeader(o.status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": o.errCode})
			return
		}
		if o.errBody != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": o.errBody})
			return
		}
		if _, ok := o.codes[r.PostForm.Get("code")]; !ok {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		delete(o.codes, r.PostForm.Get("code"))
		tok := "at-" + randomString()
		o.tokens[tok] = true
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "token_type": "bearer", "scope": "read:user,user:email"})
	case r.URL.Path == "/user" || r.URL.Path == "/user/emails":
		tok := r.Header.Get("Authorization")
		if len(tok) < 8 || !o.tokens[tok[7:]] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/user" {
			o.userCalls++
			if o.userCode != 0 {
				w.WriteHeader(o.userCode)
				return
			}
			_ = json.NewEncoder(w).Encode(o.user)
			return
		}
		_ = json.NewEncoder(w).Encode(o.emails)
	default:
		http.NotFound(w, r)
	}
}
