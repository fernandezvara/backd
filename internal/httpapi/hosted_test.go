package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/executor"
)

// fakeActions stand in for the flows that plug into the hosted pages: one
// with a password, one without, the second also taking a locale.
func fakeActions() []hostedAction {
	run := func(purpose email.Purpose, password bool) func(context.Context, *auth.Users, hostedInput) (auth.EmailToken, error) {
		return func(ctx context.Context, svc *auth.Users, in hostedInput) (auth.EmailToken, error) {
			if password && len(in.Password) < 12 {
				return auth.EmailToken{}, &auth.PolicyError{Reason: "must have at least 12 characters"}
			}
			return svc.RedeemEmailToken(ctx, in.Token, string(purpose))
		}
	}
	return []hostedAction{
		{Purpose: "reset-password", Page: email.PageResetPassword, Password: true, Locale: true, Run: run("reset-password", true)},
	}
}

func newHostedFixture(t *testing.T) *rulesFixture {
	t.Helper()
	f := newRulesFixture(t, func(c *Config) { c.hostedActions = fakeActions() })
	f.svc.Now = func() time.Time { return *f.clock }
	return f
}

func (f *rulesFixture) page(t *testing.T, method, target, contentType, body, acceptLanguage string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

const formType = "application/x-www-form-urlencoded"

func (f *rulesFixture) token(t *testing.T, purpose string, redirectTo string) string {
	t.Helper()
	tok, _, err := f.svc.NewEmailToken(context.Background(), purpose, f.adaID, redirectTo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// checkPageHeaders asserts what every hosted page sends.
func checkPageHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	for name, want := range map[string]string{
		"Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	for _, d := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP %q lacks %q", csp, d)
		}
	}
	if strings.Contains(rec.Body.String(), "src=") || strings.Contains(rec.Body.String(), "<link") || strings.Contains(rec.Body.String(), "<script") {
		t.Errorf("external resource in the page")
	}
}

// GET shows the form and never uses the token up; POST acts, once.
func TestHostedGetThenPost(t *testing.T) {
	f := newHostedFixture(t)
	token := f.token(t, "verify-email", "https://app.acme.example/welcome")
	link := "/v1/acme/_auth/verify-email?token=" + token

	for i := 0; i < 3; i++ { // a mail scanner opens it first, repeatedly
		rec := f.page(t, "GET", link, "", "", "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="token" value="`+token+`"`) {
			t.Fatalf("GET %d: %d %s", i, rec.Code, rec.Body.String())
		}
		checkPageHeaders(t, rec)
	}
	if _, err := f.svc.PeekEmailToken(context.Background(), token, "verify-email"); err != nil {
		t.Fatalf("GET used the token: %v", err)
	}

	rec := f.page(t, "POST", "/v1/acme/_auth/verify-email", formType, url.Values{"token": {token}}.Encode(), "")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `url=https://app.acme.example/welcome`) {
		t.Fatalf("POST: %d %s", rec.Code, body)
	}
	checkPageHeaders(t, rec)
	if rec.Header().Get("Authorization") != "" || len(rec.Result().Cookies()) != 0 || strings.Contains(body, "session") {
		t.Errorf("a hosted page must not issue a session")
	}

	// Used: both the form and a second post show the one error page.
	for _, rec := range []*httptest.ResponseRecorder{
		f.page(t, "GET", link, "", "", ""),
		f.page(t, "POST", "/v1/acme/_auth/verify-email", formType, url.Values{"token": {token}}.Encode(), ""),
	} {
		if rec.Code != 400 || strings.Contains(rec.Body.String(), `name="token"`) {
			t.Errorf("used token: %d %s", rec.Code, rec.Body.String())
		}
		checkPageHeaders(t, rec)
	}
}

// The redirect stored with the token is used when the realm allows it, else
// the realm's own for the flow.
func TestHostedRedirects(t *testing.T) {
	f := newHostedFixture(t)
	for redirectTo, want := range map[string]string{
		"https://app.acme.example/welcome": "3;url=https://app.acme.example/welcome",
		"https://evil.example/welcome":     "3;url=https://app.acme.example/verified", // not allowed (anymore)
		"":                                 "3;url=https://app.acme.example/verified",
	} {
		token := f.token(t, "verify-email", redirectTo)
		rec := f.page(t, "POST", "/v1/acme/_auth/verify-email", formType, url.Values{"token": {token}}.Encode(), "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `content="`+want+`"`) {
			t.Errorf("redirect_to %q: %d %s", redirectTo, rec.Code, rec.Body.String())
		}
	}
}

// Expired, unknown and wrong-purpose tokens are indistinguishable.
func TestHostedInvalidTokens(t *testing.T) {
	f := newHostedFixture(t)
	reset := f.token(t, "reset-password", "")
	expired := f.token(t, "verify-email", "")
	*f.clock = f.clock.Add(2 * time.Hour)

	var bodies []string
	for _, tok := range []string{"nope", "", reset, expired} {
		rec := f.page(t, "GET", "/v1/acme/_auth/verify-email?token="+url.QueryEscape(tok), "", "", "")
		if rec.Code != 400 {
			t.Errorf("token %q: %d", tok, rec.Code)
		}
		checkPageHeaders(t, rec)
		bodies = append(bodies, rec.Body.String())
		rec = f.page(t, "POST", "/v1/acme/_auth/verify-email", "application/json", `{"token": "`+tok+`"}`, "")
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_token"`) {
			t.Errorf("JSON, token %q: %d %s", tok, rec.Code, rec.Body.String())
		}
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("the error pages differ:\n%s\n%s", bodies[0], b)
		}
	}
}

func TestHostedPasswordForm(t *testing.T) {
	f := newHostedFixture(t)
	token := f.token(t, "reset-password", "")
	post := func(v url.Values) *httptest.ResponseRecorder {
		return f.page(t, "POST", "/v1/acme/_auth/reset-password", formType, v.Encode(), "")
	}

	rec := post(url.Values{"token": {token}, "password": {"dev-p4ssw0rd!2"}, "password_confirm": {"different"}})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `name="token" value="`+token+`"`) || !strings.Contains(rec.Body.String(), "don&#39;t match") {
		t.Errorf("mismatch: %d %s", rec.Code, rec.Body.String())
	}
	rec = post(url.Values{"token": {token}, "password": {"short"}, "password_confirm": {"short"}})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "at least 12 characters") || !strings.Contains(rec.Body.String(), `name="token"`) {
		t.Errorf("policy: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.svc.PeekEmailToken(context.Background(), token, "reset-password"); err != nil {
		t.Fatalf("a refused password used the token: %v", err)
	}
	rec = post(url.Values{"token": {token}, "password": {"dev-p4ssw0rd!2"}, "password_confirm": {"dev-p4ssw0rd!2"}})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "refresh") { // no redirect configured for this flow
		t.Errorf("success: %d %s", rec.Code, rec.Body.String())
	}
}

// The same route answers JSON for apps that host their own pages.
func TestHostedJSON(t *testing.T) {
	f := newHostedFixture(t)
	token := f.token(t, "reset-password", "")
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"token": "` + token + `"}`, 400, "validation_error"},                                       // password missing
		{`{"token": "` + token + `", "password": "short"}`, 400, "validation_error"},                  // the policy
		{`{"token": "` + token + `", "password": "dev-p4ssw0rd!2", "x": 1}`, 400, "validation_error"}, // unknown field
		{`{"token": "` + token + `", "password": "dev-p4ssw0rd!2", "locale": "es"}`, 204, ""},
		{`{"token": "` + token + `", "password": "dev-p4ssw0rd!2"}`, 400, "invalid_token"},
	} {
		rec := f.page(t, "POST", "/v1/acme/_auth/reset-password", "application/json", tc.body, "")
		if rec.Code != tc.status || (tc.code != "" && !strings.Contains(rec.Body.String(), `"`+tc.code+`"`)) {
			t.Errorf("%s → %d %s", tc.body, rec.Code, rec.Body.String())
		}
	}
	if rec := f.page(t, "POST", "/v1/acme/_auth/reset-password", "text/plain", "{}", ""); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("content type: %d", rec.Code)
	}
}

// The page is in the language the browser asks for, among the realm's.
func TestHostedLocale(t *testing.T) {
	f := newHostedFixture(t)
	token := f.token(t, "verify-email", "")
	for accept, want := range map[string]string{"": `lang="en"`, "es-MX,es;q=0.9": `lang="es"`, "fr": `lang="en"`} {
		rec := f.page(t, "GET", "/v1/acme/_auth/verify-email?token="+token, "", "", accept)
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("Accept-Language %q: want %s in %s", accept, want, rec.Body.String())
		}
	}
}

// A realm that doesn't send email has no pages, and flows without an action
// have no route.
func TestHostedRoutes(t *testing.T) {
	f := newHostedFixture(t)
	if rec := f.page(t, "GET", "/v1/acme/_auth/accept-invitation?token=x", "", "", ""); rec.Code != 404 {
		t.Errorf("no action registered: %d", rec.Code)
	}
	if rec := f.page(t, "GET", "/v1/nope/_auth/verify-email?token=x", "", "", ""); rec.Code != 404 {
		t.Errorf("unknown realm: %d", rec.Code)
	}
}

// The token travels in the query, which no log may keep.
func TestHostedKeepsTokensOutOfLogs(t *testing.T) {
	f := newHostedFixture(t)
	token := f.token(t, "verify-email", "")
	f.page(t, "GET", "/v1/acme/_auth/verify-email?token="+token, "", "", "")
	f.page(t, "POST", "/v1/acme/_auth/verify-email", formType, url.Values{"token": {token}}.Encode(), "")
	if strings.Contains(f.log.String(), token) {
		t.Errorf("the token is in the log:\n%s", f.log.String())
	}
	if !strings.Contains(f.log.String(), "/v1/{realm}/_auth/verify-email") {
		t.Errorf("the request wasn't logged by route:\n%s", f.log.String())
	}
}

// links.<flow> sends the link to the app's own page instead of backd's.
func TestLinkOverride(t *testing.T) {
	f := newHostedFixture(t)
	w := newTestWorker(t, f)
	w.reg = f.reg
	ctx := context.Background()
	f.runner.set(func(executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{Status: executor.StatusOK, Output: json.RawMessage(`{}`)}, nil
	})
	for kind, prefix := range map[string]string{
		email.ChangeEmail: "https://app.acme.example/confirm?token=",                    // overridden
		email.VerifyEmail: "https://api.acme.example/v1/acme/_auth/verify-email?token=", // backd's own
	} {
		if _, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: kind, UserID: f.adaID, Address: "ada@example.com", ClientIP: "203.0.113.5"}); err != nil {
			t.Fatal(err)
		}
		w.RunOnce(ctx)
		in := deliveredEmail(t, f.runner.last())
		link, _ := in.Data["link"].(string)
		token := tokenOf(t, in)
		if link != prefix+token || !strings.Contains(in.Text, link) {
			t.Errorf("%s: link %q, want %q+token", kind, link, prefix)
		}
	}
}
