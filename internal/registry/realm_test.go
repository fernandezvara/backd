package registry

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRealmSettingsDefaults(t *testing.T) {
	for _, content := range []string{"", "# only comments\n", "{}"} {
		s, errs := parseRealmSettings([]byte(content))
		if len(errs) > 0 {
			t.Fatalf("%q: %v", content, errs)
		}
		want := RealmSettings{
			AuthEnabled:       true,
			Signup:            SignupClosed,
			IdleTimeout:       30 * 24 * time.Hour,
			MaxLifetime:       90 * 24 * time.Hour,
			PasswordMinLength: 12,
			Roles:             map[string]Role{},
			UserNetworks:      map[string]UserNetworks{},
			AuditRetention:    365 * 24 * time.Hour,
			Account:           AccountSettings{VerifyEmailTTL: DefaultVerifyEmailTTL, ResetPasswordTTL: DefaultResetPasswordTTL, ChangeEmailTTL: 24 * time.Hour, RevertEmailChangeTTL: 7 * 24 * time.Hour},
		}
		if !reflect.DeepEqual(s, want) {
			t.Errorf("%q: got %+v, want %+v", content, s, want)
		}
	}
}

func TestRealmSettingsFull(t *testing.T) {
	s, errs := parseRealmSettings([]byte(`
auth: enabled
signup: open
sessions:
  idle_timeout: 12h
  max_lifetime: 7d
password:
  min_length: 16
audit:
  retention: 30d
cors:
  origins: ["https://app.example.com/", "http://localhost:5173"]
roles:
  admin:
    description: Back office
    admin: true
    users: [" Ops@Example.com ", {email: Lead@Example.com}]
  editor:
`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	want := RealmSettings{
		AuthEnabled:       true,
		Signup:            SignupOpen,
		IdleTimeout:       12 * time.Hour,
		MaxLifetime:       7 * 24 * time.Hour,
		PasswordMinLength: 16,
		CORSOrigins:       []string{"https://app.example.com", "http://localhost:5173"},
		Roles: map[string]Role{
			"admin":  {Description: "Back office", Admin: true, Users: []string{"ops@example.com", "lead@example.com"}},
			"editor": {},
		},
		UserNetworks:   map[string]UserNetworks{},
		AuditRetention: 30 * 24 * time.Hour,
		Account:        AccountSettings{VerifyEmailTTL: DefaultVerifyEmailTTL, ResetPasswordTTL: DefaultResetPasswordTTL, ChangeEmailTTL: 24 * time.Hour, RevertEmailChangeTTL: 7 * 24 * time.Hour},
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("got %+v\nwant %+v", s, want)
	}
}

func TestRealmSettingsAuthDisabled(t *testing.T) {
	s, errs := parseRealmSettings([]byte("auth: disabled\ncors:\n  origins: ['*']\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.AuthEnabled || !reflect.DeepEqual(s.CORSOrigins, []string{"*"}) {
		t.Errorf("got %+v", s)
	}
}

// functions.max_concurrency isn't about users, so it applies in realms
// with auth disabled too (unlike sessions, password, roles, admin, audit).
func TestRealmSettingsFunctionsMaxConcurrency(t *testing.T) {
	for _, content := range []string{
		"functions:\n  max_concurrency: 50\n",
		"auth: disabled\nfunctions:\n  max_concurrency: 50\n",
	} {
		s, errs := parseRealmSettings([]byte(content))
		if len(errs) > 0 {
			t.Fatalf("%q: %v", content, errs)
		}
		if s.FunctionsMaxConcurrency != 50 {
			t.Errorf("%q: FunctionsMaxConcurrency = %d, want 50", content, s.FunctionsMaxConcurrency)
		}
	}
	s, errs := parseRealmSettings([]byte(""))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.FunctionsMaxConcurrency != 0 {
		t.Errorf("default FunctionsMaxConcurrency = %d, want 0 (unlimited)", s.FunctionsMaxConcurrency)
	}
}

func TestRealmSettingsFunctionsLogRetention(t *testing.T) {
	s, errs := parseRealmSettings([]byte("functions:\n  log_retention: 3d\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.FunctionsLogRetention != 3*24*time.Hour {
		t.Errorf("FunctionsLogRetention = %s, want 72h", s.FunctionsLogRetention)
	}

	s, errs = parseRealmSettings([]byte(""))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.FunctionsLogRetention != 0 {
		t.Errorf("default FunctionsLogRetention = %s, want 0 (DefaultLogRetention applies)", s.FunctionsLogRetention)
	}

	// Unlike max_concurrency, log_retention needs auth: there's no
	// system database to store invocation records in otherwise.
	_, errs = parseRealmSettings([]byte("auth: disabled\nfunctions:\n  log_retention: 3d\n"))
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "functions.log_retention: only applies when auth is enabled") {
		t.Errorf("auth disabled: %v", errs)
	}
}

func TestRealmSettingsErrors(t *testing.T) {
	tests := []struct {
		name, content string
		want          []string
	}{
		{"invalid yaml", "auth: [", []string{"invalid YAML"}},
		{"unknown key", "authentication: enabled", []string{"invalid YAML", "authentication"}},
		{"unknown nested key", "sessions:\n  idle: 1h", []string{"invalid YAML", "idle"}},
		{"auth value", "auth: on", []string{`auth: must be enabled or disabled, got "on"`}},
		{"signup value", "signup: public", []string{`signup: must be open, invite or closed`}},
		{"auth disabled with user settings", "auth: disabled\nsignup: open\npassword:\n  min_length: 12\nroles:\n  admin: {}",
			[]string{"signup: only applies when auth is enabled", "password: only applies", "roles: only applies"}},
		{"bad duration", "sessions:\n  idle_timeout: 3 weeks", []string{"sessions.idle_timeout: invalid duration"}},
		{"too short", "sessions:\n  idle_timeout: 10s", []string{"must be at least 1m0s"}},
		{"idle beyond max", "sessions:\n  idle_timeout: 100d", []string{"idle_timeout (2400h0m0s) must not exceed max_lifetime"}},
		{"admin idle beyond idle", "sessions:\n  idle_timeout: 1d\n  admin_idle_timeout: 2d", []string{"admin_idle_timeout (48h0m0s) must not exceed idle_timeout (24h0m0s)"}},
		{"admin max beyond max", "sessions:\n  max_lifetime: 10d\n  admin_max_lifetime: 20d", []string{"admin_max_lifetime (480h0m0s) must not exceed max_lifetime (240h0m0s)"}},
		{"admin idle beyond admin max", "sessions:\n  admin_idle_timeout: 20d\n  admin_max_lifetime: 10d", []string{"admin_idle_timeout (480h0m0s) must not exceed admin_max_lifetime (240h0m0s)"}},
		{"admin idle beyond the regular max", "sessions:\n  max_lifetime: 40d\n  admin_max_lifetime: 3d\n  admin_idle_timeout: 5d", []string{"admin_idle_timeout (120h0m0s) must not exceed admin_max_lifetime (72h0m0s)"}},
		{"admin too short", "sessions:\n  admin_idle_timeout: 10s", []string{"sessions.admin_idle_timeout: must be at least 1m0s"}},
		{"password too short", "password:\n  min_length: 6", []string{"password.min_length: must be between 8 and 128"}},
		{"password too long", "password:\n  min_length: 200", []string{"between 8 and 128"}},
		{"origin with path", "cors:\n  origins: ['https://a.example/app']", []string{"cors.origins[0]: invalid origin"}},
		{"origin scheme", "cors:\n  origins: ['ftp://a.example']", []string{"invalid origin"}},
		{"star not alone", "cors:\n  origins: ['*', 'https://a.example']", []string{`"*" must be the only origin`}},
		{"role name", "roles:\n  Admin: {}", []string{`invalid role name "Admin"`}},
		{"role email", "roles:\n  admin:\n    users: [nobody]", []string{`roles.admin.users[0]: invalid email address "nobody"`}},
		{"duplicate email", "roles:\n  admin:\n    users: [a@x.io, A@X.io]", []string{`roles.admin.users[1]: "a@x.io" is listed twice`}},
		{"duplicate email in object form", "roles:\n  admin:\n    users: [a@x.io, {email: A@X.io}]", []string{`roles.admin.users[1]: "a@x.io" is listed twice`}},
		{"unknown key in user entry", "roles:\n  admin:\n    users: [{email: a@x.io, name: A}]", []string{`unknown key "name"`}},
		{"user entry without email", "roles:\n  admin:\n    users: [{}]", []string{"needs an email"}},
		{"user entry as a list", "roles:\n  admin:\n    users: [[a@x.io]]", []string{"must be an email or an object"}},
		{"admin flag not a boolean", "roles:\n  admin:\n    admin: sometimes", []string{"invalid YAML"}},
		{"bad admin network", "admin:\n  allowed_networks: [10.0.0.0/8, office]", []string{`admin.allowed_networks: "office" is not an IP address`}},
		{"admin settings without auth", "auth: disabled\nadmin:\n  allowed_networks: [10.0.0.0/8]", []string{"admin: only applies"}},
		{"bad user network", "roles:\n  ops:\n    users: [{email: a@x.io, login_networks: [nope]}]", []string{`roles.ops.users[0].login_networks: "nope" is not`}},
		{"user admin network outside the realm's", "admin:\n  allowed_networks: [10.0.0.0/8]\nroles:\n  ops:\n    users: [{email: a@x.io, admin_networks: [192.168.1.0/24]}]", []string{"must lie within admin.allowed_networks [10.0.0.0/8]"}},
		{"user admin network wider than the realm's", "admin:\n  allowed_networks: [10.1.0.0/16]\nroles:\n  ops:\n    users: [{email: a@x.io, admin_networks: [10.0.0.0/8]}]", []string{"must lie within"}},
		{"bad audit retention", "audit:\n  retention: forever", []string{"audit.retention: invalid duration"}},
		{"audit retention too short", "audit:\n  retention: 12h", []string{"audit.retention: must be at least 1d"}},
		{"audit settings without auth", "auth: disabled\naudit:\n  retention: 30d", []string{"audit: only applies"}},
		{"different networks in two roles", "roles:\n  ops:\n    users: [{email: a@x.io, login_networks: [10.0.0.1]}]\n  qa:\n    users: [{email: a@x.io, login_networks: [10.0.0.2]}]", []string{"has different networks in another role"}},
		{"max_concurrency too low", "functions:\n  max_concurrency: 0", []string{"functions.max_concurrency: must be at least 1, got 0"}},
		{"log_retention bad duration", "functions:\n  log_retention: forever", []string{"functions.log_retention: invalid duration"}},
		{"log_retention too short", "functions:\n  log_retention: 1m", []string{"functions.log_retention: must be at least 1h0m0s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := parseRealmSettings([]byte(tt.content))
			var msgs []string
			for _, e := range errs {
				msgs = append(msgs, e.Error())
			}
			got := strings.Join(msgs, "\n")
			if got == "" {
				t.Fatal("expected errors")
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("errors %q do not contain %q", got, w)
				}
			}
		})
	}
}

func TestLoadRealmFile(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shop/realm.yaml":       "signup: open\n",
		"shop/db/c/schema.json": `{}`,
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Realms["shop"].Settings.Signup; got != SignupOpen {
		t.Errorf("signup = %q", got)
	}

	root = writeTree(t, map[string]string{
		"shop/realm.yaml":       omit,
		"shop/db/c/schema.json": `{}`,
		"blog/realm.yaml":       "auth: maybe",
	})
	_, err = Load(root)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, w := range []string{"shop: realm has no realm.yaml (create one with `backd template realm --realm shop`)", "blog/realm.yaml: auth: must be"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 720 * time.Hour, "0d": 0, "90m": 90 * time.Minute, "1h30m": 90 * time.Minute} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "d", "1.5d", "-1d", "30", "abc"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q): expected error", in)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got, err := NormalizeEmail("  Jane.Doe@Example.COM "); err != nil || got != "jane.doe@example.com" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, in := range []string{"", "jane", "@example.com", "jane@", "a@b@c", "ja ne@x.io", "erased-abc@erased.invalid", "x@INVALID", "x@mail.Invalid", "a@x.io,b@y.io", "a@x.io;b@y.io", "Ana <a@x.io>", `"a"@x.io`, "a@x.io\x00", strings.Repeat("a", 250) + "@x.io"} {
		if _, err := NormalizeEmail(in); err == nil {
			t.Errorf("NormalizeEmail(%q): expected error", in)
		}
	}
}

func TestLoadRules(t *testing.T) {
	root := writeTree(t, map[string]string{
		"shop/realm.yaml":           "roles:\n  admin: {}\n",
		"shop/db/items/schema.json": personSchema,
		"shop/db/items/rules.yaml":  "read: \"user != nil && document.name == user.email\"\nwrite: hasRole(user, 'admin')\n",
		"shop/db/plain/schema.json": `{}`,
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := reg.Collection("shop", "db", "items")
	if items.Rules == nil || items.Rules.For("read") == nil || items.Rules.For("delete") == nil {
		t.Errorf("rules not loaded: %+v", items.Rules)
	}
	if plain, _ := reg.Collection("shop", "db", "plain"); plain.Rules != nil {
		t.Error("collection without rules.yaml got rules")
	}

	for name, tt := range map[string]struct {
		files map[string]string
		want  []string
	}{
		"undeclared role": {map[string]string{
			"shop/db/items/schema.json": personSchema,
			"shop/db/items/rules.yaml":  "read: hasRole(user, 'admin')\n",
		}, []string{"items/rules.yaml: read: role \"admin\" is not declared in realm.yaml"}},
		"unknown field": {map[string]string{
			"shop/db/items/schema.json": personSchema,
			"shop/db/items/rules.yaml":  "read: document.nmae == 'x'\n",
		}, []string{"items/rules.yaml: read: unknown field document.nmae"}},
		"auth disabled": {map[string]string{
			"shop/realm.yaml":           "auth: disabled\n",
			"shop/db/items/schema.json": personSchema,
			"shop/db/items/rules.yaml":  "read: 'true'\n",
		}, []string{"items/rules.yaml: rules only apply when the realm has auth enabled"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTree(t, tt.files))
			if err == nil {
				t.Fatal("expected error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestAdminRoles(t *testing.T) {
	s, errs := parseRealmSettings([]byte("roles:\n  ops:\n    admin: true\n  staff:\n    admin: true\n  editor: {}\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := s.AdminRoles(); !reflect.DeepEqual(got, []string{"ops", "staff"}) {
		t.Errorf("AdminRoles = %v", got)
	}
	for roles, want := range map[string]bool{"": false, "editor": false, "staff": true, "editor,ops": true, "unknown": false} {
		if got := s.IsAdmin(strings.Split(roles, ",")); got != want {
			t.Errorf("IsAdmin(%q) = %v, want %v", roles, got, want)
		}
	}
}

func TestNetworksSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte(`
admin:
  allowed_networks: [10.0.0.0/8, "2001:db8::/32"]
roles:
  ops:
    admin: true
    users:
      - email: Ada@Example.com
        admin_networks: [10.1.2.0/24]
        login_networks: [10.1.2.3, "2001:db8::1"]
      - bob@example.com
  qa:
    users:
      - {email: ada@example.com, admin_networks: [10.1.2.0/24], login_networks: [10.1.2.3, "2001:db8::1"]}
`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := s.AdminNetworks.Strings(); !reflect.DeepEqual(got, []string{"10.0.0.0/8", "2001:db8::/32"}) {
		t.Errorf("AdminNetworks = %v", got)
	}
	un, ok := s.UserNetworks["ada@example.com"]
	if !ok || !reflect.DeepEqual(un.Admin.Strings(), []string{"10.1.2.0/24"}) || !reflect.DeepEqual(un.Login.Strings(), []string{"10.1.2.3/32", "2001:db8::1/128"}) {
		t.Errorf("ada's networks = %+v", un)
	}
	if _, ok := s.UserNetworks["bob@example.com"]; ok {
		t.Error("bob got networks")
	}
}

func TestNetworks(t *testing.T) {
	n, err := ParseNetworks([]string{"10.0.0.0/8", " 192.0.2.7 ", "2001:db8::/48"})
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{"10.2.3.4": true, "192.0.2.7": true, "192.0.2.8": false, "2001:db8:0:1::5": true, "2001:db9::1": false, "::ffff:10.0.0.1": true, "": false, "nope": false} {
		if got := n.Allows(addr); got != want {
			t.Errorf("Allows(%q) = %v, want %v", addr, got, want)
		}
	}
	if !(Networks(nil)).Allows("anything") {
		t.Error("an empty list must allow everything")
	}
	inner, _ := ParseNetworks([]string{"10.1.0.0/16", "192.0.2.7"})
	wide, _ := ParseNetworks([]string{"0.0.0.0/0"})
	if !inner.Within(n) || wide.Within(n) || !wide.Within(nil) {
		t.Error("Within")
	}
}

func TestAdminSessionLimits(t *testing.T) {
	load := func(yaml string) RealmSettings {
		t.Helper()
		s, errs := parseRealmSettings([]byte(yaml))
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		return s
	}
	// Unset: 0, meaning the regular limits.
	if s := load("signup: open\n"); s.AdminIdleTimeout != 0 || s.AdminMaxLifetime != 0 {
		t.Errorf("unset admin limits = %v, %v", s.AdminIdleTimeout, s.AdminMaxLifetime)
	}
	// One or both set; the other stays the regular one.
	s := load("sessions:\n  admin_idle_timeout: 30m\n")
	if s.AdminIdleTimeout != 30*time.Minute || s.AdminMaxLifetime != 0 {
		t.Errorf("only admin_idle_timeout: %v, %v", s.AdminIdleTimeout, s.AdminMaxLifetime)
	}
	s = load("sessions:\n  idle_timeout: 7d\n  max_lifetime: 30d\n  admin_idle_timeout: 1h\n  admin_max_lifetime: 12h\n")
	if s.AdminIdleTimeout != time.Hour || s.AdminMaxLifetime != 12*time.Hour || s.IdleTimeout != 7*24*time.Hour {
		t.Errorf("both set: %+v", s)
	}
}
