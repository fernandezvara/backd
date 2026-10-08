package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/oauth/oauthtest"
	"github.com/fernandezvara/backd/internal/registry"
)

const (
	appRedirect = "https://app.acme.example/signed-in"
	oauthBase   = "/v1/acme/_auth/oauth"
)

type oauthFixture struct {
	*rulesFixture
	idp *oauthtest.Provider
}

// newOAuthFixture serves realm acme with Google, Microsoft and Apple, all played by one fake
// provider, and the users ada and bob (verified) and carl (not) of the rules fixture.
func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	idp := oauthtest.New(t)
	f := newRulesFixture(t, func(c *Config) { c.OAuth = idp.Service() })
	setUpProviders(t, f)
	return &oauthFixture{rulesFixture: f, idp: idp}
}

// setUpProviders gives the realm of a rules fixture Google, Microsoft and Apple, with their
// secrets, and the apps it may send users back to.
func setUpProviders(t *testing.T, f *rulesFixture) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	f.svc.Realm = "acme"
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Cache = auth.NewSecretCache()
	for name, value := range map[string]string{
		"GOOGLE_SECRET": "g-secret", "MICROSOFT_SECRET": "m-secret",
		"APPLE_KEY": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	} {
		if err := f.svc.SetSecret(context.Background(), "", name, value, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Settings.Providers = map[string]*registry.Provider{
		"google":    {Name: "google", ClientID: "g-client", ClientSecret: "GOOGLE_SECRET", NativeClientIDs: []string{"g-native"}},
		"microsoft": {Name: "microsoft", ClientID: "00000000-0000-0000-0000-000000000001", ClientSecret: "MICROSOFT_SECRET", Tenant: "common"},
		"apple":     {Name: "apple", ClientID: "com.acme.web", NativeClientIDs: []string{"com.acme.ios"}, TeamID: "ABCDE12345", KeyID: "XYZ9876543", PrivateKey: "APPLE_KEY", RevokeOnDelete: true},
	}
	f.svc.Settings.SignInRedirects = []string{"https://app.acme.example", "acme://"}
}

func googleUser(sub, email string, verified bool) map[string]any {
	return map[string]any{"iss": "https://accounts.google.com", "aud": "g-client", "sub": sub, "email": email, "email_verified": verified}
}

// attempt is one pass through the flow: the app's PKCE verifier and where the pieces went.
type attempt struct {
	verifier string
	result   *url.URL // where backd sent the browser at the end
	status   int
}

// signIn runs the redirect flow for a provider as a browser would, with the fake provider
// signing the given person in: start, the provider, the callback.
func (f *oauthFixture) signIn(t *testing.T, provider string, person map[string]any, extra url.Values) attempt {
	t.Helper()
	f.idp.SetUser(person)
	verifier, challenge, _ := auth.NewPKCE()
	q := url.Values{"redirect_to": {appRedirect}, "code_challenge": {challenge}}
	for k, v := range extra {
		q[k] = v
	}
	rec, _ := f.doH(t, "GET", oauthBase+"/"+provider+"/start?"+q.Encode(), "", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	return f.finish(t, provider, rec.Header().Get("Location"), verifier, false)
}

// finish plays the browser from the provider's authorize URL on.
func (f *oauthFixture) finish(t *testing.T, provider, authorizeURL, verifier string, deny bool) attempt {
	t.Helper()
	back := f.idp.Authorize(authorizeURL)
	if deny {
		back = f.idp.Deny(authorizeURL)
	}
	return f.callback(t, provider, back, verifier)
}

// callback delivers the provider's redirect to backd, as a GET or (Apple) a form POST.
func (f *oauthFixture) callback(t *testing.T, provider, back, verifier string) attempt {
	t.Helper()
	u, _ := url.Parse(back)
	var rec *httptest.ResponseRecorder
	if provider == "apple" {
		rec, _ = f.doH(t, "POST", u.Path, u.RawQuery, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	} else {
		rec, _ = f.doH(t, "GET", u.Path+"?"+u.RawQuery, "", nil)
	}
	res := attempt{verifier: verifier, status: rec.Code}
	if loc := rec.Header().Get("Location"); loc != "" {
		res.result, _ = url.Parse(loc)
	}
	return res
}

// redeem trades the login code the flow ended with for a session.
func (f *oauthFixture) redeem(t *testing.T, a attempt) (int, map[string]any) {
	t.Helper()
	if a.result == nil || a.result.Query().Get("code") == "" {
		t.Fatalf("the flow ended without a code: %v (status %d)", a.result, a.status)
	}
	rec, out := f.do(t, "POST", oauthBase+"/token", `{"code": "`+a.result.Query().Get("code")+`", "code_verifier": "`+a.verifier+`"}`)
	return rec.Code, out
}

func (a attempt) errorCode() string {
	if a.result == nil {
		return ""
	}
	return a.result.Query().Get("error")
}

func TestOAuthSignInCreatesAndFindsAUser(t *testing.T) {
	f := newOAuthFixture(t)
	a := f.signIn(t, "google", googleUser("g-1", "new@example.com", true), nil)
	if a.status != 302 || a.result.Host != "app.acme.example" || a.result.Path != "/signed-in" || a.errorCode() != "" {
		t.Fatalf("callback: %d %v", a.status, a.result)
	}
	code, out := f.redeem(t, a)
	if code != 200 || out["token"] == nil {
		t.Fatalf("redeem: %d %v", code, out)
	}
	token := out["token"].(string)
	user := out["user"].(map[string]any)
	if user["email"] != "new@example.com" || user["email_verified"] != true {
		t.Errorf("user: %v", user)
	}
	// The session works, and /me lists the provider as a sign-in method.
	rec, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(token))
	ids, _ := me["identities"].([]any)
	if rec.Code != 200 || len(ids) != 1 || ids[0].(map[string]any)["provider"] != "google" || ids[0].(map[string]any)["email"] != "new@example.com" {
		t.Errorf("me: %d %v", rec.Code, me)
	}
	// The code works once.
	if rec, _ := f.do(t, "POST", oauthBase+"/token", `{"code": "`+a.result.Query().Get("code")+`", "code_verifier": "`+a.verifier+`"}`); rec.Code != 401 {
		t.Errorf("a code used twice: %d", rec.Code)
	}
	// Signing in again is the same user.
	b := f.signIn(t, "google", googleUser("g-1", "new@example.com", true), nil)
	_, out2 := f.redeem(t, b)
	if out2["user"].(map[string]any)["id"] != user["id"] {
		t.Errorf("a second sign-in made another user: %v", out2["user"])
	}
	// What the provider was asked: the client, the PKCE verifier and the secret.
	forms := f.idp.TokenRequests()
	if forms[0].Get("client_id") != "g-client" || forms[0].Get("client_secret") != "g-secret" || forms[0].Get("code_verifier") == "" || forms[0].Get("redirect_uri") != "https://api.acme.example"+registry.ProviderCallbackPath("acme", "google") {
		t.Errorf("token request: %v", forms[0])
	}
}

func TestOAuthCodeNeedsItsVerifier(t *testing.T) {
	f := newOAuthFixture(t)
	a := f.signIn(t, "google", googleUser("g-1", "new@example.com", true), nil)
	code := a.result.Query().Get("code")
	if rec, _ := f.do(t, "POST", oauthBase+"/token", `{"code": "`+code+`", "code_verifier": "somebody-elses-verifier"}`); rec.Code != 401 {
		t.Errorf("a wrong verifier: %d", rec.Code)
	}
	// ...and the code is gone: the right verifier can't redeem it any more.
	if rec, _ := f.do(t, "POST", oauthBase+"/token", `{"code": "`+code+`", "code_verifier": "`+a.verifier+`"}`); rec.Code != 401 {
		t.Errorf("the code survived a wrong verifier: %d", rec.Code)
	}
	for body, want := range map[string]int{`{}`: 400, `{"code": "x"}`: 400, `{"code": "nope", "code_verifier": "x"}`: 401} {
		if rec, _ := f.do(t, "POST", oauthBase+"/token", body); rec.Code != want {
			t.Errorf("%s: %d, want %d", body, rec.Code, want)
		}
	}
}

func TestOAuthLinksVerifiedAddressesOnlyUnderTheRules(t *testing.T) {
	f := newOAuthFixture(t)
	// ada@example.com is verified: Google, trusted, says so too.
	a := f.signIn(t, "google", googleUser("g-ada", "ada@example.com", true), nil)
	if a.errorCode() != "" {
		t.Fatalf("auto-link: %v", a.result)
	}
	_, out := f.redeem(t, a)
	if out["user"].(map[string]any)["id"] != f.adaID {
		t.Errorf("signed in as %v, want ada", out["user"])
	}
	// Google not asserting a verified address: explicit linking only.
	if a := f.signIn(t, "google", googleUser("g-bob", "bob@example.com", false), nil); a.errorCode() != "account_exists" || a.result.Query().Get("provider") != "google" {
		t.Errorf("unverified at the provider: %v", a.result)
	}
	// Microsoft is never trusted to link by address.
	ms := map[string]any{"iss": "https://login.microsoftonline.com/t1/v2.0", "aud": "00000000-0000-0000-0000-000000000001", "tid": "t1", "oid": "o1", "sub": "s", "email": "bob@example.com"}
	if a := f.signIn(t, "microsoft", ms, nil); a.errorCode() != "account_exists" {
		t.Errorf("microsoft: %v", a.result)
	}
	// An unverified backd account (carl) is never taken over by a provider's identity.
	if a := f.signIn(t, "google", googleUser("g-carl", "carl@example.com", true), nil); a.errorCode() != "account_exists" {
		t.Errorf("an unverified backd account: %v", a.result)
	}
	if _, err := f.svc.Store.Identity(context.Background(), "google", "g-carl"); err == nil {
		t.Error("an identity was linked to the unverified account")
	}
}

func TestOAuthSignUpModes(t *testing.T) {
	f := newOAuthFixture(t)
	f.svc.Settings.Signup = registry.SignupClosed
	if a := f.signIn(t, "google", googleUser("g-1", "x@example.com", true), nil); a.errorCode() != "signup_closed" {
		t.Errorf("closed: %v", a.result)
	}
	f.svc.Settings.Signup = registry.SignupInvite
	if a := f.signIn(t, "google", googleUser("g-1", "x@example.com", true), nil); a.errorCode() != "invitation_invalid" {
		t.Errorf("invite without an invitation: %v", a.result)
	}
	_, token, err := f.svc.CreateInvitation(context.Background(), "x@example.com", time.Hour, "admin")
	if err != nil {
		t.Fatal(err)
	}
	a := f.signIn(t, "google", googleUser("g-1", "x@example.com", true), url.Values{"invitation": {token}})
	if a.errorCode() != "" {
		t.Fatalf("with an invitation: %v", a.result)
	}
	if code, _ := f.redeem(t, a); code != 200 {
		t.Errorf("redeem: %d", code)
	}
	if a := f.signIn(t, "google", googleUser("g-2", "y@example.com", true), url.Values{"invitation": {token}}); a.errorCode() != "invitation_invalid" {
		t.Errorf("an invitation used twice: %v", a.result)
	}
	// An unverified Microsoft sign-up in a realm that wants verified addresses.
	f.svc.Settings.Signup = registry.SignupOpen
	f.svc.Settings.Account.RequireVerifiedEmail = true
	ms := map[string]any{"iss": "https://login.microsoftonline.com/t1/v2.0", "aud": "00000000-0000-0000-0000-000000000001", "tid": "t1", "oid": "o2", "sub": "s", "email": "m@corp.example"}
	if a := f.signIn(t, "microsoft", ms, nil); a.errorCode() != "email_not_verified" {
		t.Errorf("unverified: %v", a.result)
	}
	if a := f.signIn(t, "google", map[string]any{"iss": "https://accounts.google.com", "aud": "g-client", "sub": "g-none"}, nil); a.errorCode() != "email_required" {
		t.Errorf("no address: %v", a.result)
	}
}

func TestOAuthRefusesDisabledAndOutOfNetworkUsers(t *testing.T) {
	f := newOAuthFixture(t)
	if a := f.signIn(t, "google", googleUser("g-ada", "ada@example.com", true), nil); a.errorCode() != "" {
		t.Fatal(a.result)
	}
	if err := f.svc.SetDisabled(context.Background(), "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	a := f.signIn(t, "google", googleUser("g-ada", "ada@example.com", true), nil)
	if a.errorCode() != "signin_refused" {
		t.Errorf("disabled: %v", a.result)
	}
	recs, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditSignInRefused})
	if len(recs) != 1 || recs[0].Details["reason"] != "disabled" {
		t.Errorf("audit: %+v", recs)
	}
}

func TestOAuthLinkingAProviderToTheSignedInUser(t *testing.T) {
	f := newOAuthFixture(t)
	f.idp.SetUser(googleUser("g-ada", "ada.work@gmail.example", true))
	_, challenge := func() (string, string) { v, c, _ := auth.NewPKCE(); return v, c }()
	body := `{"redirect_to": "` + appRedirect + `", "code_challenge": "` + challenge + `", "intent": "link"}`
	if rec, _ := f.doH(t, "POST", oauthBase+"/google/start", body, nil); rec.Code != 401 {
		t.Errorf("linking without a session: %d", rec.Code)
	}
	rec, out := f.doH(t, "POST", oauthBase+"/google/start", body, bearer(f.ada))
	if rec.Code != 200 || out["authorize_url"] == nil {
		t.Fatalf("start: %d %v", rec.Code, out)
	}
	a := f.finish(t, "google", out["authorize_url"].(string), "", false)
	if a.status != 302 || a.result.Query().Get("linked") != "google" || a.result.Query().Get("code") != "" {
		t.Fatalf("callback: %d %v", a.status, a.result)
	}
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(f.ada))
	if ids := me["identities"].([]any); len(ids) != 2 || me["email"] != "ada@example.com" {
		t.Errorf("me after linking: %v", me)
	}
	recs, _, _ := f.svc.AuditTrail(context.Background(), auth.AuditFilter{Action: auth.AuditIdentityLinked})
	if len(recs) != 1 {
		t.Errorf("audit: %+v", recs)
	}
	// The same Google account can't be linked to bob.
	_, out = f.doH(t, "POST", oauthBase+"/google/start", body, bearer(f.bob))
	a = f.finish(t, "google", out["authorize_url"].(string), "", false)
	if a.errorCode() != "link_conflict" {
		t.Errorf("a provider account of another user: %v", a.result)
	}
	// ...and signing in with it is ada.
	if a := f.signIn(t, "google", googleUser("g-ada", "ada.work@gmail.example", true), nil); a.errorCode() != "" {
		t.Errorf("sign-in with the linked account: %v", a.result)
	} else if _, o := f.redeem(t, a); o["user"].(map[string]any)["id"] != f.adaID {
		t.Errorf("signed in as %v", o["user"])
	}
	// Removing the method; the password stays and is now the only one.
	if rec, _ := f.doH(t, "DELETE", "/v1/acme/_auth/identities/google", "", bearer(f.ada)); rec.Code != 204 {
		t.Errorf("unlink: %d", rec.Code)
	}
}

func TestOAuthStartRefusals(t *testing.T) {
	f := newOAuthFixture(t)
	_, challenge, _ := auth.NewPKCE()
	good := url.Values{"redirect_to": {appRedirect}, "code_challenge": {challenge}}
	with := func(k, v string) url.Values {
		q := url.Values{}
		for kk, vv := range good {
			q[kk] = vv
		}
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
		return q
	}
	for name, tc := range map[string]struct {
		path string
		want int
	}{
		"unknown provider":    {oauthBase + "/github/start?" + good.Encode(), 404},
		"no redirect_to":      {oauthBase + "/google/start?" + with("redirect_to", "").Encode(), 400},
		"foreign redirect_to": {oauthBase + "/google/start?" + with("redirect_to", "https://evil.example/x").Encode(), 400},
		"javascript redirect": {oauthBase + "/google/start?" + with("redirect_to", "javascript:alert(1)").Encode(), 400},
		"no challenge":        {oauthBase + "/google/start?" + with("code_challenge", "").Encode(), 400},
		"a short challenge":   {oauthBase + "/google/start?" + with("code_challenge", "abc").Encode(), 400},
		"linking in a GET":    {oauthBase + "/google/start?" + with("intent", "link").Encode(), 400},
		"a bad invitation":    {oauthBase + "/google/start?" + with("invitation", "nope").Encode(), 400},
	} {
		if rec, _ := f.doH(t, "GET", tc.path, "", nil); rec.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A realm whose secret isn't set can't offer the provider.
	if err := f.svc.DeleteSecret(context.Background(), "", "GOOGLE_SECRET"); err != nil {
		t.Fatal(err)
	}
	f.svc.Cache = auth.NewSecretCache()
	if rec, out := f.doH(t, "GET", oauthBase+"/google/start?"+good.Encode(), "", nil); rec.Code != 503 || errCode(out) != "provider_unavailable" {
		t.Errorf("a missing secret: %d %v", rec.Code, out)
	}
	// An app scheme in the list is a good redirect.
	if rec, _ := f.doH(t, "GET", oauthBase+"/microsoft/start?"+with("redirect_to", "acme://signed-in?x=1").Encode(), "", nil); rec.Code != 302 {
		t.Errorf("an app scheme: %d", rec.Code)
	}
}

func TestOAuthStartIsRateLimited(t *testing.T) {
	f := newOAuthFixture(t)
	_, challenge, _ := auth.NewPKCE()
	path := oauthBase + "/google/start?redirect_to=" + url.QueryEscape(appRedirect) + "&code_challenge=" + challenge
	for i := range 30 {
		if rec, _ := f.doH(t, "GET", path, "", nil); rec.Code != 302 {
			t.Fatalf("start %d: %d", i, rec.Code)
		}
	}
	rec, _ := f.doH(t, "GET", path, "", nil)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("the 31st start: %d", rec.Code)
	}
	if rec, _ := f.do(t, "POST", oauthBase+"/token", `{"code": "x", "code_verifier": "y"}`); rec.Code != 401 {
		t.Errorf("the token route has its own count: %d", rec.Code)
	}
}

func TestOAuthCallbackErrors(t *testing.T) {
	f := newOAuthFixture(t)
	person := googleUser("g-1", "new@example.com", true)
	// The user said no at the provider.
	f.idp.SetUser(person)
	_, challenge, _ := auth.NewPKCE()
	rec, _ := f.doH(t, "GET", oauthBase+"/google/start?redirect_to="+url.QueryEscape(appRedirect+"?keep=1")+"&code_challenge="+challenge, "", nil)
	a := f.finish(t, "google", rec.Header().Get("Location"), "", true)
	if a.errorCode() != "cancelled" || a.result.Query().Get("keep") != "1" {
		t.Errorf("cancelled: %v", a.result)
	}
	// The provider refuses the code, or fails.
	f.idp.FailToken(400, "invalid_grant")
	if a := f.signIn(t, "google", person, nil); a.errorCode() != "provider_error" {
		t.Errorf("refused code: %v", a.result)
	}
	f.idp.FailToken(500, "server_error")
	if a := f.signIn(t, "google", person, nil); a.errorCode() != "provider_error" {
		t.Errorf("provider outage: %v", a.result)
	}
	f.idp.FailToken(0, "")
	// A token for another application, an expired one, one with another issuer.
	for name, claims := range map[string]map[string]any{
		"another audience": googleUser("g-1", "new@example.com", true),
		"another issuer":   googleUser("g-1", "new@example.com", true),
		"expired":          googleUser("g-1", "new@example.com", true),
	} {
		switch name {
		case "another audience":
			claims["aud"] = "someone-else"
		case "another issuer":
			claims["iss"] = "https://evil.example"
		case "expired":
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
		}
		if a := f.signIn(t, "google", claims, nil); a.errorCode() != "provider_error" {
			t.Errorf("%s: %v", name, a.result)
		}
	}
	if users, _ := f.svc.List(context.Background()); len(users) != 3 {
		t.Errorf("a failed sign-in made a user: %d users", len(users))
	}
}

func TestOAuthErrorPageForAttemptsWithoutAnApp(t *testing.T) {
	f := newOAuthFixture(t)
	for name, path := range map[string]string{
		"no state":      oauthBase + "/google/callback?code=x",
		"unknown state": oauthBase + "/google/callback?code=x&state=nope",
	} {
		rec, _ := f.doH(t, "GET", path, "", nil)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Sign-in didn't complete") || rec.Header().Get("Content-Security-Policy") == "" ||
			rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d %q %v", name, rec.Code, rec.Body, rec.Header())
		}
	}
	// A state works once, and only for the provider it was made for.
	f.idp.SetUser(googleUser("g-1", "new@example.com", true))
	_, challenge, _ := auth.NewPKCE()
	rec, _ := f.doH(t, "GET", oauthBase+"/google/start?redirect_to="+url.QueryEscape(appRedirect)+"&code_challenge="+challenge, "", nil)
	back := f.idp.Authorize(rec.Header().Get("Location"))
	u, _ := url.Parse(back)
	if rec, _ := f.doH(t, "GET", strings.Replace(u.Path, "/google/", "/microsoft/", 1)+"?"+u.RawQuery, "", nil); rec.Code != 400 {
		t.Errorf("a state of another provider: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", u.Path+"?"+u.RawQuery, "", nil); rec.Code != 400 {
		t.Errorf("a state burnt by the wrong callback: %d", rec.Code)
	}
}

func TestOAuthAppleAnswersWithAForm(t *testing.T) {
	f := newOAuthFixture(t)
	person := map[string]any{"iss": "https://appleid.apple.com", "aud": "com.acme.web", "sub": "a-1", "email": "x@privaterelay.appleid.com", "email_verified": "true"}
	a := f.signIn(t, "apple", person, nil)
	if a.status != http.StatusSeeOther || a.errorCode() != "" {
		t.Fatalf("callback: %d %v", a.status, a.result)
	}
	code, out := f.redeem(t, a)
	if code != 200 || out["user"].(map[string]any)["email_verified"] != true {
		t.Errorf("redeem: %d %v", code, out)
	}
	// Apple was sent a signed client secret and no PKCE verifier; its authorize URL asked for a form.
	form := f.idp.TokenRequests()[0]
	if strings.Count(form.Get("client_secret"), ".") != 2 || form.Has("code_verifier") || form.Get("client_id") != "com.acme.web" {
		t.Errorf("token request: %v", form)
	}
	_, challenge, _ := auth.NewPKCE()
	rec, _ := f.doH(t, "GET", oauthBase+"/apple/start?redirect_to="+url.QueryEscape(appRedirect)+"&code_challenge="+challenge, "", nil)
	if loc, _ := url.Parse(rec.Header().Get("Location")); loc.Query().Get("response_mode") != "form_post" || loc.Query().Has("code_challenge") {
		t.Errorf("authorize URL: %s", rec.Header().Get("Location"))
	}
}
