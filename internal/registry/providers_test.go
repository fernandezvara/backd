package registry

import (
	"strings"
	"testing"
	"time"
)

const providersYAML = `auth:
  strategies:
    password: [password]
    google: [google]
    microsoft: [microsoft]
    apple: [apple]
  sign_in:
    allowed_redirects: [https://app.acme.example, "acme://"]
    app_url: https://app.acme.example/signed-in
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
	if s.SignInAppURL != "https://app.acme.example/signed-in" {
		t.Errorf("sign_in.app_url: %q", s.SignInAppURL)
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
		{"unknown provider", "auth:\n  strategies:\n    password: [password]\n    github: [github]\n  providers:\n    github: {client_id: x}\n", "isn't a provider backd knows"},
		{"type oidc is not available yet", "auth:\n  strategies:\n    password: [password]\n    keycloak: [keycloak]\n  providers:\n    keycloak: {type: oidc}\n", "isn't available yet"},
		{"empty", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google:\n", "is empty"},
		{"no client id", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_secret: secret:S}\n", "client_id: is required"},
		{"secret in the clear", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_id: abc, client_secret: hunter2}\n", "must be secret:NAME"},
		{"no secret", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_id: abc}\n", "client_secret: is required"},
		{"bad tenant", "auth:\n  strategies:\n    password: [password]\n    microsoft: [microsoft]\n  providers:\n    microsoft: {client_id: 00000000-0000-0000-0000-000000000000, client_secret: secret:S, tenant: everyone}\n", "tenant:"},
		{"microsoft client id not a guid", "auth:\n  strategies:\n    password: [password]\n    microsoft: [microsoft]\n  providers:\n    microsoft: {client_id: abc, client_secret: secret:S}\n", "a GUID"},
		{"apple fields on google", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_id: abc, client_secret: secret:S, team_id: ABCDE12345}\n", "team_id: doesn't apply to google"},
		{"google fields on apple", "auth:\n  strategies:\n    password: [password]\n    apple: [apple]\n  providers:\n    apple: {services_id: com.a.b, team_id: ABCDE12345, key_id: XYZ9876543, private_key: secret:K, client_id: x}\n", "client_id: doesn't apply to apple"},
		{"apple without ids", "auth:\n  strategies:\n    password: [password]\n    apple: [apple]\n  providers:\n    apple: {team_id: ABCDE12345, key_id: XYZ9876543, private_key: secret:K}\n", "services_id: is required"},
		{"apple bad team", "auth:\n  strategies:\n    password: [password]\n    apple: [apple]\n  providers:\n    apple: {services_id: com.a.b, team_id: abc, key_id: XYZ9876543, private_key: secret:K}\n", "team_id:"},
		{"duplicate native id", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_id: abc, client_secret: secret:S, native_client_ids: [x1, x1]}\n", "listed twice"},
		{"unknown key", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_id: abc, client_secret: secret:S, colour: red}\n", "colour"},
		{"no redirects", "auth:\n  strategies:\n    password: [password]\n    google: [google]\n  providers:\n    google: {client_id: abc, client_secret: secret:S}\n", "need auth.sign_in.allowed_redirects"},
		{"reserved name password", "auth:\n  strategies:\n    password: [password]\n    password: [password]\n  providers:\n    password: {client_id: abc}\n", "is reserved"},
		{"reserved name email", "auth:\n  strategies:\n    password: [password]\n    email: [email]\n  providers:\n    email: {client_id: abc}\n", "is reserved"},
		{"app url outside", "auth:\n  strategies:\n    password: [password]\n  sign_in:\n    allowed_redirects: [https://app.example]\n    app_url: https://other.example/home\n", "sign_in.app_url"},
		{"bad redirect", "auth:\n  strategies:\n    password: [password]\n  sign_in:\n    allowed_redirects: [https://app.example/path]\n", "must be an origin"},
		{"auth disabled", "auth: disabled\nproviders:\n  google: {client_id: abc, client_secret: secret:S}\n", "providers: has moved"},
	}
	for _, c := range cases {
		yaml := c.yaml
		if !strings.HasPrefix(yaml, "auth:") {
			yaml = authBase + "  sign_in:\n    allowed_redirects: [https://app.acme.example]\n" + yaml
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

func TestOnSignupMustBeAnInternalAsyncFunction(t *testing.T) {
	tree := func(realm, fn string) string {
		return writeTree(t, map[string]string{
			"shop/realm.yaml":                         realm,
			"shop/app/_functions/deno.json":           `{}`,
			"shop/app/_functions/hello/function.yaml": fn,
			"shop/app/_functions/hello/index.ts":      "export default () => 1;\n",
		})
	}
	reg, err := Load(tree("auth:\n  strategies:\n    password: [password]\n  signup: open\naccount:\n  on_signup: app/hello\n", "internal: true\nmode: async\ntimeout: 20s\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := reg.Realms["shop"].Settings.Account
	if db, name := a.OnSignupDatabaseAndName(); db != "app" || name != "hello" || a.OnSignupTimeout != 20*time.Second {
		t.Errorf("account: %+v", a)
	}
	for name, tc := range map[string]struct{ realm, fn, want string }{
		"not a function":   {"auth:\n  strategies:\n    password: [password]\n  signup: open\naccount:\n  on_signup: app/nothing\n", "internal: true\nmode: async\n", "is not a function of this realm"},
		"not internal":     {"auth:\n  strategies:\n    password: [password]\n  signup: open\naccount:\n  on_signup: app/hello\n", "mode: async\n", "must have `internal: true`"},
		"not async":        {"auth:\n  strategies:\n    password: [password]\n  signup: open\naccount:\n  on_signup: app/hello\n", "internal: true\n", "must have `mode: async`"},
		"not a path":       {"auth:\n  strategies:\n    password: [password]\n  signup: open\naccount:\n  on_signup: hello\n", "internal: true\nmode: async\n", "must be <database>/<function>"},
		"auth is disabled": {"auth: disabled\naccount:\n  on_signup: app/hello\n", "internal: true\nmode: async\n", "account: only applies when auth is enabled"},
	} {
		if _, err := Load(tree(tc.realm, tc.fn)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestMFASettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte("auth:\n  strategies:\n    password: [password]\n  totp:\n    issuer: \" Acme Co \"\n"))
	if len(errs) > 0 || s.MFA.Off || s.MFA.Issuer != "Acme Co" {
		t.Errorf("default: %+v %v", s.MFA, errs)
	}
	s, errs = parseRealmSettings([]byte("auth:\n  strategies:\n    password: [password]\n  totp:\n    enabled: false\n"))
	if len(errs) > 0 || !s.MFA.Off {
		t.Errorf("off: %+v %v", s.MFA, errs)
	}
	for name, yaml := range map[string]string{
		"auth disabled": "auth: disabled\nmfa:\n  enabled: true\n",
		"bad issuer":    "auth:\n  strategies:\n    password: [password]\n  totp:\n    issuer: \"a:b\"\n",
		"unknown key":   "auth:\n  strategies:\n    password: [password]\n  totp:\n    colour: red\n",
	} {
		if _, errs := parseRealmSettings([]byte(yaml)); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReauthTTL(t *testing.T) {
	s, errs := parseRealmSettings([]byte("auth:\n  strategies:\n    password: [password]\n"))
	if len(errs) > 0 || s.ReauthLifetime() != 5*time.Minute {
		t.Errorf("default: %v %v", s.ReauthLifetime(), errs)
	}
	s, errs = parseRealmSettings([]byte("auth:\n  strategies:\n    password: [password]\n  reauth:\n    ttl: 10m\n"))
	if len(errs) > 0 || s.ReauthLifetime() != 10*time.Minute {
		t.Errorf("10m: %v %v", s.ReauthLifetime(), errs)
	}
	for _, ttl := range []string{"30s", "2h", "soon"} {
		if _, errs := parseRealmSettings([]byte(authBase + "  reauth:\n    ttl: " + ttl + "\n")); len(errs) == 0 {
			t.Errorf("%s: accepted", ttl)
		}
	}
}
