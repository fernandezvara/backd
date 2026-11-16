package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// newOIDCFixture is the OAuth fixture plus a generic OpenID Connect provider, "keycloak", whose
// issuer is the fake provider.
func newOIDCFixture(t *testing.T, linkByEmail bool) *oauthFixture {
	t.Helper()
	f := newOAuthFixture(t)
	if err := f.svc.SetSecret(context.Background(), "", "KEYCLOAK_SECRET", "kc-secret", "key:test"); err != nil {
		t.Fatal(err)
	}
	f.svc.Settings.Providers["keycloak"] = &registry.Provider{
		Name: "keycloak", Kind: "oidc", Issuer: f.idp.URL, ClientID: "backd", ClientSecret: "KEYCLOAK_SECRET", LinkByEmail: linkByEmail,
	}
	addFirstStrategies(&f.svc.Settings)
	return f
}

func keycloakUser(sub, email string, verified bool) map[string]any {
	return map[string]any{"iss": "", "aud": "backd", "sub": sub, "email": email, "email_verified": verified}
}

func (f *oauthFixture) keycloakSignIn(t *testing.T, person map[string]any) attempt {
	t.Helper()
	person["iss"] = f.idp.URL
	return f.signIn(t, "keycloak", person, nil)
}

func TestGenericProviderSignsInAndIsListed(t *testing.T) {
	f := newOIDCFixture(t, false)
	if _, out := f.do(t, "GET", "/v1/acme/_auth/providers", ""); !containsItem(out["items"], "keycloak") {
		t.Errorf("providers = %v", out)
	}
	a := f.keycloakSignIn(t, keycloakUser("kc-1", "newkc@example.com", true))
	status, out := f.redeem(t, a)
	if status != 200 || out["new_user"] != true || out["user"].(map[string]any)["email"] != "newkc@example.com" {
		t.Fatalf("redeem: %d %v", status, out)
	}
	// A generic provider doesn't vouch for the address.
	if out["user"].(map[string]any)["email_verified"] != false {
		t.Errorf("a generic provider's address was taken as verified: %v", out["user"])
	}
	_, me := f.doH(t, "GET", "/v1/acme/_auth/me", "", bearer(out["token"].(string)))
	ids := me["identities"].([]any)
	if len(ids) != 1 || ids[0].(map[string]any)["provider"] != "keycloak" || ids[0].(map[string]any)["issuer"] != f.idp.URL {
		t.Errorf("identities = %v", ids)
	}
}

func TestGenericProviderLinksByAddressOnlyWhenTheOperatorSaysSo(t *testing.T) {
	for _, link := range []bool{false, true} {
		f := newOIDCFixture(t, link)
		// ada is a verified account of the rules fixture.
		a := f.keycloakSignIn(t, keycloakUser("kc-ada", "ada@example.com", true))
		if link {
			if status, out := f.redeem(t, a); status != 200 || out["new_user"] != false || out["user"].(map[string]any)["email"] != "ada@example.com" {
				t.Errorf("link_by_email: %d %v", status, out)
			}
			continue
		}
		if got := a.result.Query().Get("backd_error"); got != "account_exists" || a.result.Query().Get("backd_code") != "" {
			t.Errorf("without link_by_email: %v", a.result)
		}
	}
}

func TestGenericProviderIsUnavailableWhenItsDiscoveryIsWrong(t *testing.T) {
	f := newOIDCFixture(t, false)
	f.idp.Discovery(map[string]any{"issuer": "https://evil.example"})
	_, challenge, _ := auth.NewPKCE()
	rec, _ := f.doH(t, "GET", oauthBase+"/keycloak/start?redirect_to="+url.QueryEscape(appRedirect)+"&code_challenge="+challenge, "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("start with a discovery document naming another issuer: %d %s", rec.Code, rec.Body)
	}
}

func TestGenericProviderHasNoNativeSignIn(t *testing.T) {
	f := newOIDCFixture(t, false)
	code, out := f.idToken(t, "keycloak", keycloakUser("kc-9", "x@example.com", true), nil, nil)
	if code != http.StatusNotFound {
		t.Errorf("native sign-in with a generic provider: %d %v", code, out)
	}
}

func containsItem(items any, want string) bool {
	list, _ := items.([]any)
	for _, i := range list {
		if i == want {
			return true
		}
	}
	return false
}
