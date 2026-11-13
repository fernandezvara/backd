package registry

import (
	"strings"
	"testing"
)

func authErrors(t *testing.T, yaml string) string {
	t.Helper()
	_, errs := parseRealmSettings([]byte(yaml))
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return strings.Join(msgs, "\n")
}

func TestAuthSectionIsRequired(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"missing":          {"cors:\n  origins: ['*']\n", "auth: is missing"},
		"empty":            {"", "auth: is missing"},
		"enabled is gone":  {"auth: enabled\n", "`enabled` is gone"},
		"no strategies":    {"auth:\n  signup: open\n", "needs `strategies`"},
		"empty strategies": {"auth:\n  strategies: {}\n", "needs `strategies`"},
		"a word":           {"auth: on\n", "must be `disabled` or a section"},
	} {
		if got := authErrors(t, tc.yaml); !strings.Contains(got, tc.want) {
			t.Errorf("%s: errors %q, want %q", name, got, tc.want)
		}
	}
	if got := authErrors(t, "auth: disabled\n"); got != "" {
		t.Errorf("auth: disabled: %q", got)
	}
}

func TestMovedKeysNameTheirNewPlace(t *testing.T) {
	for _, key := range []string{"signup: open", "sessions: {}", "password: {}", "login_throttle: {}", "providers: {}", "sign_in: {}", "magic_link: {}", "reauth: {}", "mfa: {}"} {
		got := authErrors(t, authBase+key+"\n")
		name := strings.SplitN(key, ":", 2)[0]
		want := name + ": has moved, it is now auth." + name
		if name == "mfa" {
			want = "mfa: has moved, it is now auth.totp"
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: errors %q, want %q", key, got, want)
		}
	}
}

func TestStrategies(t *testing.T) {
	good := "auth:\n  strategies:\n    web: [password]\n    admin: [password, totp]\n    mobile: [password, totp]\n"
	s, errs := parseRealmSettings([]byte(good))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(s.Strategies) != 3 || s.Strategies[1].Name != "admin" || strings.Join(s.Strategies[1].Methods, ",") != "password,totp" {
		t.Errorf("strategies = %+v", s.Strategies)
	}
	for name, tc := range map[string]struct{ yaml, want string }{
		"totp first":      {"auth:\n  strategies:\n    a: [totp]\n", "totp cannot be the first method"},
		"unknown method":  {"auth:\n  strategies:\n    a: [fingerprint]\n", "is not a method"},
		"twice":           {"auth:\n  strategies:\n    a: [password, totp, totp]\n", "lists \"totp\" twice"},
		"empty list":      {"auth:\n  strategies:\n    a: []\n", "non-empty list"},
		"bad name":        {"auth:\n  strategies:\n    A b: [password]\n", "not a valid name"},
		"passkey off":     {"auth:\n  strategies:\n    a: [passkey]\n", "auth.passkeys.enabled"},
		"magic link off":  {"auth:\n  strategies:\n    a: [magic_link]\n", "auth.magic_link.enabled"},
		"totp off":        {"auth:\n  strategies:\n    a: [password, totp]\n  totp:\n    enabled: false\n", "totp is disabled"},
		"recovery code":   {"auth:\n  strategies:\n    a: [password, recovery_code]\n", "not a method"},
		"provider later":  {"auth:\n  strategies:\n    a: [password, google]\n  providers:\n    google: {client_id: x, client_secret: secret:S}\n", "can only be the first method"},
		"undeclared name": {"auth:\n  strategies:\n    a: [google]\n", "not a method"},
	} {
		if got := authErrors(t, tc.yaml); !strings.Contains(got, tc.want) {
			t.Errorf("%s: errors %q, want %q", name, got, tc.want)
		}
	}
}

func TestRolesListTheirStrategies(t *testing.T) {
	base := "auth:\n  strategies:\n    web: [password]\n    admin: [password, totp]\n"
	s, errs := parseRealmSettings([]byte(base + "roles:\n  owner:\n    admin: true\n    strategies: [admin]\n  staff: {}\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := s.Roles["owner"].Strategies; len(got) != 1 || got[0] != "admin" {
		t.Errorf("owner strategies = %v", got)
	}
	if s.Roles["staff"].Strategies != nil {
		t.Errorf("a role with no list allows every strategy, got %v", s.Roles["staff"].Strategies)
	}
	// A role whose every strategy has a totp step makes its holders enrol an authenticator.
	if !s.TOTPRequired([]string{"owner"}) || s.TOTPRequired([]string{"staff"}) {
		t.Errorf("TOTPRequired: owner %v, staff %v", s.TOTPRequired([]string{"owner"}), s.TOTPRequired([]string{"staff"}))
	}
	for name, tc := range map[string]struct{ roles, want string }{
		"unknown strategy": {"roles:\n  owner:\n    strategies: [ghost]\n", `no strategy named "ghost"`},
		"twice":            {"roles:\n  owner:\n    strategies: [web, web]\n", "listed twice"},
		"empty":            {"roles:\n  owner:\n    strategies: []\n", "is empty"},
	} {
		if got := authErrors(t, base+tc.roles); !strings.Contains(got, tc.want) {
			t.Errorf("%s: errors %q, want %q", name, got, tc.want)
		}
	}
	// Every strategy has a second step: everyone needs it.
	s, errs = parseRealmSettings([]byte("auth:\n  strategies:\n    a: [password, totp]\n"))
	if len(errs) > 0 || !s.TOTPRequired(nil) {
		t.Errorf("all: %v", errs)
	}
}

func TestPasskeysSection(t *testing.T) {
	ok := "auth:\n  strategies:\n    web: [password]\n  passkeys:\n    enabled: true\n    rp_id: example.com\n    rp_name: Example\n    origins: [https://app.example.com]\n"
	s, errs := parseRealmSettings([]byte(ok))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !s.Passkeys.Enabled || s.Passkeys.RPID != "example.com" || s.Passkeys.RPName != "Example" || len(s.Passkeys.Origins) != 1 {
		t.Errorf("passkeys = %+v", s.Passkeys)
	}
	if got := authErrors(t, strings.Replace(ok, "origins: [https://app.example.com]", "origins: [https://other.test]", 1)); !strings.Contains(got, "rp_id") {
		t.Errorf("an origin outside rp_id: %q", got)
	}
	if got := authErrors(t, strings.Replace(ok, "    rp_id: example.com\n", "", 1)); !strings.Contains(got, "rp_id") {
		t.Errorf("missing rp_id: %q", got)
	}
}

func TestEveryMethodIsClassified(t *testing.T) {
	for _, m := range AllMethods {
		if _, ok := methodSteps[m]; !ok {
			t.Errorf("method %q is not classified in methodSteps (first step, later step or both)", m)
		}
	}
	if len(methodSteps) != len(AllMethods) {
		t.Errorf("methodSteps has %d entries and AllMethods %d: list every method in both", len(methodSteps), len(AllMethods))
	}
	if st := MethodSteps("google"); !st.First || st.Later {
		t.Errorf("a provider is a first step only, got %+v", st)
	}
}

func TestStrategyFor(t *testing.T) {
	s := RealmSettings{Strategies: []Strategy{
		{Name: "web", Methods: []string{"password"}},
		{Name: "admin", Methods: []string{"password", "totp"}},
		{Name: "google", Methods: []string{"google"}},
	}}
	if _, err := s.StrategyFor("", "password"); err != ErrStrategyRequired {
		t.Errorf("two start with password and none is named: %v", err)
	}
	if st, err := s.StrategyFor("admin", "password"); err != nil || st.Name != "admin" {
		t.Errorf("admin: %+v %v", st, err)
	}
	if st, err := s.StrategyFor("", "google"); err != nil || st.Name != "google" {
		t.Errorf("only one starts with google: %+v %v", st, err)
	}
	for name, tc := range map[string][2]string{"not listed": {"ghost", "password"}, "wrong first method": {"google", "password"}, "no strategy starts with it": {"", "magic_link"}} {
		if _, err := s.StrategyFor(tc[0], tc[1]); err != ErrUnknownStrategy {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestActiveRoles(t *testing.T) {
	s := RealmSettings{Roles: map[string]Role{
		"admin":  {Strategies: []string{"admin_password"}},
		"editor": {Strategies: []string{"users_passkey"}},
		"staff":  {},
	}}
	active, inactive := s.ActiveRoles([]string{"admin", "editor", "staff"}, "admin_password")
	if len(active) != 2 || active[0] != "admin" || active[1] != "staff" || len(inactive) != 1 || inactive[0] != "editor" {
		t.Errorf("admin_password: active %v inactive %v", active, inactive)
	}
	if active, inactive := s.ActiveRoles([]string{"admin"}, "users_password"); len(active) != 0 || len(inactive) != 1 {
		t.Errorf("a role listed for no matching strategy: %v %v", active, inactive)
	}
	if active, _ := s.ActiveRoles([]string{"admin", "editor"}, ""); len(active) != 2 {
		t.Errorf("a session with no strategy keeps every role: %v", active)
	}
}
