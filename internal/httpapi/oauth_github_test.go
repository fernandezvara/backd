package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/protocol"
	"github.com/fernandezvara/backd/sso/ssotest"
)

// newOAuth2Fixture is the OAuth fixture with GitHub and a plain OAuth 2 service, "partner", both
// played by one fake service that gives no ID tokens.
func newOAuth2Fixture(t *testing.T, linkPartner bool) *oauthFixture {
	t.Helper()
	o := ssotest.NewOAuth2(t)
	f := newRulesFixture(t, func(c *Config) {
		c.OAuth = (&protocol.Client{HTTP: o.Client(), Dev: true}).Init()
		c.OAuthEndpoints = func(*registry.Provider) sso.Endpoints { return o.Endpoints() }
	})
	setUpProviders(t, f)
	for name, value := range map[string]string{"GITHUB_SECRET": "gh-secret", "PARTNER_SECRET": "p-secret"} {
		if err := f.svc.SetSecret(context.Background(), "", name, value, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.Settings.Providers["github"] = &registry.Provider{Name: "github", Kind: "github", ClientID: "Iv1.abc", ClientSecret: "GITHUB_SECRET"}
	f.svc.Settings.Providers["partner"] = &registry.Provider{
		Name: "partner", Kind: "oauth2", Issuer: "https://partner.example", ClientID: "backd", ClientSecret: "PARTNER_SECRET",
		AuthorizeURL: o.URL + "/authorize", TokenURL: o.URL + "/token", UserInfoURL: o.URL + "/user", LinkByEmail: linkPartner,
		Claims: map[string]string{"subject": "id", "email": "mail", "email_verified": "mail_ok", "name": "display"},
	}
	f.svc.Settings.Organizations = nil
	addFirstStrategies(&f.svc.Settings)
	return &oauthFixture{rulesFixture: f, o2: o}
}

func TestGitHubSignsPeopleIn(t *testing.T) {
	f := newOAuth2Fixture(t, false)
	if _, out := f.do(t, "GET", "/v1/acme/_auth/providers", ""); !containsItem(out["items"], "github") || !containsItem(out["items"], "partner") {
		t.Errorf("providers = %v", out)
	}
	f.o2.SetUser(map[string]any{"id": 583231, "login": "octocat", "name": "The Octocat"})
	f.o2.SetEmails([]map[string]any{{"email": "octo@example.com", "primary": true, "verified": true}})
	status, out := f.redeem(t, f.signIn(t, "github", nil, nil))
	if status != 200 || out["new_user"] != true || out["user"].(map[string]any)["email"] != "octo@example.com" || out["user"].(map[string]any)["email_verified"] != true {
		t.Fatalf("redeem: %d %v", status, out)
	}
	if out["profile"].(map[string]any)["name"] != "The Octocat" {
		t.Errorf("profile = %v", out["profile"])
	}
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(out["token"].(string)))
	id := me["identities"].([]any)[0].(map[string]any)
	if id["provider"] != "github" || id["issuer"] != "https://github.com" {
		t.Errorf("identity = %v", id)
	}
	// The same person under a new login is still the same user: the subject is the id.
	f.o2.SetUser(map[string]any{"id": 583231, "login": "renamed"})
	if status, again := f.redeem(t, f.signIn(t, "github", nil, nil)); status != 200 || again["new_user"] != false || again["user"].(map[string]any)["email"] != "octo@example.com" {
		t.Errorf("after a rename: %d %v", status, again)
	}
}

func TestGitHubLinksALikeGoogleAndNeedsAVerifiedPrimaryAddress(t *testing.T) {
	f := newOAuth2Fixture(t, false)
	// ada is a verified account of the fixture: GitHub's verified primary address links it.
	f.o2.SetUser(map[string]any{"id": 11, "login": "ada"})
	f.o2.SetEmails([]map[string]any{{"email": "ada@example.com", "primary": true, "verified": true}})
	status, out := f.redeem(t, f.signIn(t, "github", nil, nil))
	if status != 200 || out["new_user"] != false || out["user"].(map[string]any)["email"] != "ada@example.com" {
		t.Fatalf("link: %d %v", status, out)
	}
	// carl's account is not verified: it is not taken over.
	f.o2.SetUser(map[string]any{"id": 12, "login": "carl"})
	f.o2.SetEmails([]map[string]any{{"email": "carl@example.com", "primary": true, "verified": true}})
	if a := f.signIn(t, "github", nil, nil); a.result.Query().Get("backd_error") != "account_exists" {
		t.Errorf("an unverified account: %v", a.result)
	}
	// An address GitHub has not verified is no address.
	f.o2.SetUser(map[string]any{"id": 13, "login": "nomail"})
	f.o2.SetEmails([]map[string]any{{"email": "x@example.com", "primary": true, "verified": false}})
	if a := f.signIn(t, "github", nil, nil); a.result.Query().Get("backd_error") != "email_required" {
		t.Errorf("no verified address: %v", a.result)
	}
	// A refused code ends at the app as a provider error.
	f.o2.RefuseWith200("bad_verification_code")
	if a := f.signIn(t, "github", nil, nil); a.result.Query().Get("backd_error") != "provider_error" {
		t.Errorf("a refused code: %v", a.result)
	}
}

func TestPlainOAuth2LinksOnlyWhenTheOperatorSaysSoAndTheServiceVerifies(t *testing.T) {
	for _, link := range []bool{false, true} {
		f := newOAuth2Fixture(t, link)
		f.o2.SetUser(map[string]any{"id": "p-1", "mail": "ada@example.com", "mail_ok": true, "display": "Ada"})
		a := f.signIn(t, "partner", nil, nil)
		if link {
			if status, out := f.redeem(t, a); status != 200 || out["new_user"] != false {
				t.Errorf("link_by_email: %d %v", status, out)
			}
		} else if a.result.Query().Get("backd_error") != "account_exists" {
			t.Errorf("without link_by_email: %v", a.result)
		}
		// A service that does not say the address is verified never links it.
		f.o2.SetUser(map[string]any{"id": "p-2", "mail": "bob@example.com", "mail_ok": false})
		if a := f.signIn(t, "partner", nil, nil); a.result.Query().Get("backd_error") != "account_exists" {
			t.Errorf("link=%v, unverified by the service: %v", link, a.result)
		}
	}
	// A new person of a service that doesn't vouch is stored unverified.
	f := newOAuth2Fixture(t, false)
	f.o2.SetUser(map[string]any{"id": "p-3", "mail": "newp@example.com", "mail_ok": true})
	status, out := f.redeem(t, f.signIn(t, "partner", nil, nil))
	if status != 200 || out["user"].(map[string]any)["email_verified"] != false {
		t.Errorf("new user of a plain service: %d %v", status, out)
	}
}

func TestOAuth2ProvidersHaveNoNativeSignInAndAreNotForOrganizations(t *testing.T) {
	f := newOAuth2Fixture(t, false)
	for _, name := range []string{"github", "partner"} {
		rec, _ := f.doH(t, "POST", oauthBase+"/"+name+"/id-token", `{"id_token":"x","nonce":"n"}`, map[string]string{"Content-Type": "application/json"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s id-token: %d", name, rec.Code)
		}
	}
	_, challenge, _ := auth.NewPKCE()
	rec, _ := f.doH(t, "GET", oauthBase+"/github/start?redirect_to="+url.QueryEscape(appRedirect)+"&code_challenge="+challenge, "", nil)
	if rec.Code != http.StatusFound {
		t.Errorf("start: %d", rec.Code)
	}
}
