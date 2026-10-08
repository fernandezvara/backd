package registry

import (
	"strings"
	"testing"
)

const providersYAML = `auth: enabled
sign_in:
  allowed_redirects: [https://app.acme.example, "acme://"]
providers:
  google:
    client_id: 1234.apps.googleusercontent.com
    client_secret: secret:GOOGLE_CLIENT_SECRET
    native_client_ids: [5678.apps.googleusercontent.com]
  microsoft:
    client_id: 00000000-0000-0000-0000-000000000000
    client_secret: secret:MICROSOFT_CLIENT_SECRET
    tenant: organizations
  apple:
    services_id: com.acme.web
    bundle_ids: [com.acme.ios]
    team_id: ABCDE12345
    key_id: XYZ9876543
    private_key: secret:APPLE_SIGN_IN_KEY
`

func TestProvidersAreParsed(t *testing.T) {
	s, errs := parseRealmSettings([]byte(providersYAML))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	g, m, a := s.Providers["google"], s.Providers["microsoft"], s.Providers["apple"]
	if g == nil || m == nil || a == nil || len(s.Providers) != 3 {
		t.Fatalf("providers: %+v", s.Providers)
	}
	if g.ClientID != "1234.apps.googleusercontent.com" || g.ClientSecret != "GOOGLE_CLIENT_SECRET" || !g.TrustsEmail() || len(g.Audiences(true)) != 2 || len(g.Audiences(false)) != 1 {
		t.Errorf("google: %+v", g)
	}
	if m.Tenant != "organizations" || m.TrustsEmail() {
		t.Errorf("microsoft: %+v", m)
	}
	if a.ClientID != "com.acme.web" || a.PrivateKey != "APPLE_SIGN_IN_KEY" || !a.RevokeOnDelete || !a.TrustsEmail() || a.Audiences(true)[1] != "com.acme.ios" {
		t.Errorf("apple: %+v", a)
	}
	if got := a.SecretNames(); len(got) != 1 || got[0] != "APPLE_SIGN_IN_KEY" {
		t.Errorf("apple secrets: %v", got)
	}
	if !s.SignInRedirectAllowed("https://app.acme.example/welcome") || !s.SignInRedirectAllowed("acme://done") || s.SignInRedirectAllowed("https://evil.example/x") {
		t.Error("sign_in.allowed_redirects")
	}
	if ProviderCallbackPath("acme", "google") != "/v1/acme/_auth/oauth/google/callback" {
		t.Error("callback path")
	}
}

func TestSignInRedirectsFallBackToEmail(t *testing.T) {
	s := RealmSettings{Email: &EmailSettings{AllowedRedirects: []string{"https://app.acme.example"}}}
	if !s.SignInRedirectAllowed("https://app.acme.example/x") || s.SignInRedirectAllowed("https://other.example/x") {
		t.Error("fallback to email.allowed_redirects")
	}
	if (RealmSettings{}).SignInRedirectAllowed("https://app.acme.example/x") {
		t.Error("no list allows nothing")
	}
}

func TestProviderErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"unknown provider", "providers:\n  github: {client_id: x}\n", "isn't a provider backd knows"},
		{"type oidc is not available yet", "providers:\n  keycloak: {type: oidc}\n", "isn't available yet"},
		{"empty", "providers:\n  google:\n", "is empty"},
		{"no client id", "providers:\n  google: {client_secret: secret:S}\n", "client_id: is required"},
		{"secret in the clear", "providers:\n  google: {client_id: abc, client_secret: hunter2}\n", "must be secret:NAME"},
		{"no secret", "providers:\n  google: {client_id: abc}\n", "client_secret: is required"},
		{"bad tenant", "providers:\n  microsoft: {client_id: 00000000-0000-0000-0000-000000000000, client_secret: secret:S, tenant: everyone}\n", "tenant:"},
		{"microsoft client id not a guid", "providers:\n  microsoft: {client_id: abc, client_secret: secret:S}\n", "a GUID"},
		{"apple fields on google", "providers:\n  google: {client_id: abc, client_secret: secret:S, team_id: ABCDE12345}\n", "team_id: doesn't apply to google"},
		{"google fields on apple", "providers:\n  apple: {services_id: com.a.b, team_id: ABCDE12345, key_id: XYZ9876543, private_key: secret:K, client_id: x}\n", "client_id: doesn't apply to apple"},
		{"apple without ids", "providers:\n  apple: {team_id: ABCDE12345, key_id: XYZ9876543, private_key: secret:K}\n", "services_id: is required"},
		{"apple bad team", "providers:\n  apple: {services_id: com.a.b, team_id: abc, key_id: XYZ9876543, private_key: secret:K}\n", "team_id:"},
		{"duplicate native id", "providers:\n  google: {client_id: abc, client_secret: secret:S, native_client_ids: [x1, x1]}\n", "listed twice"},
		{"unknown key", "providers:\n  google: {client_id: abc, client_secret: secret:S, colour: red}\n", "colour"},
		{"no redirects", "auth: enabled\nproviders:\n  google: {client_id: abc, client_secret: secret:S}\n", "need sign_in.allowed_redirects"},
		{"bad redirect", "auth: enabled\nsign_in:\n  allowed_redirects: [https://app.example/path]\n", "must be an origin"},
		{"auth disabled", "auth: disabled\nproviders:\n  google: {client_id: abc, client_secret: secret:S}\n", "only apply when auth is enabled"},
	}
	for _, c := range cases {
		yaml := c.yaml
		if !strings.HasPrefix(yaml, "auth:") {
			yaml = "auth: enabled\nsign_in:\n  allowed_redirects: [https://app.acme.example]\n" + yaml
		}
		_, errs := parseRealmSettings([]byte(yaml))
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Error(), c.want)
		}
		if !found {
			t.Errorf("%s: errors %v, want %q", c.name, errs, c.want)
		}
	}
}
