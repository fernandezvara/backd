package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/email"
)

// emailTree is a realm "shop" with a "notify" database holding the given
// delivery function, and the default English templates.
func emailTree(t *testing.T, realmYAML, deliverYAML string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"shop/realm.yaml":                              realmYAML,
		"shop/notify/_functions/deno.json":             `{}`,
		"shop/notify/_functions/deliver/function.yaml": deliverYAML,
		"shop/notify/_functions/deliver/index.ts":      "export default () => 1;\n",
	}
	for _, kind := range email.SystemKinds {
		defaults, err := email.DefaultFiles(kind)
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range defaults {
			files["shop/email/"+kind+"/"+name] = string(data)
		}
	}
	for _, kind := range email.PageKinds {
		page, err := email.DefaultPage(kind)
		if err != nil {
			t.Fatal(err)
		}
		files["shop/pages/"+kind+"/en.html"] = string(page)
	}
	for p, c := range extra {
		files[p] = c
	}
	return writeTree(t, files)
}

const goodEmail = "email:\n  function: notify/deliver\n  from: \"Shop <no-reply@shop.example>\"\n  public_url: https://api.shop.example/\n"
const goodDeliver = "internal: true\nmode: async\nsecrets: [POSTMARK_TOKEN]\n"

func TestEmailSettings(t *testing.T) {
	reg, err := Load(emailTree(t, goodEmail+"  reply_to: help@shop.example\n  locales: [en, es]\n  default_locale: en\n  limits:\n    per_recipient: {per_day: 5}\n    per_ip: {per_hour: 50}\n", goodDeliver, esTemplates(t)))
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Realms["shop"].Settings.Email
	if e == nil || e.Function != "notify/deliver" || e.From != "Shop <no-reply@shop.example>" || e.ReplyTo != "help@shop.example" ||
		e.PublicURL != "https://api.shop.example" || e.DefaultLocale != "en" || strings.Join(e.Locales, ",") != "en,es" {
		t.Fatalf("settings: %+v", e)
	}
	if e.Limits != (EmailLimits{PerKindPerHour: 3, PerDay: 5, PerIPPerHour: 50}) {
		t.Errorf("limits: %+v", e.Limits)
	}
	if db, fn := e.DatabaseAndName(); db != "notify" || fn != "deliver" {
		t.Errorf("DatabaseAndName: %s %s", db, fn)
	}
	// The email folder is no database; its templates are loaded.
	rl := reg.Realms["shop"]
	if _, ok := rl.Databases["email"]; ok || rl.Email == nil || !rl.Email.Has(email.VerifyEmail, "es") {
		t.Errorf("email folder: databases %v, templates %+v", rl.Databases, rl.Email)
	}
	// Without an email section nothing changes: no templates, no settings.
	reg, err = Load(emailTree(t, "roles:\n  admin: {admin: true}\n", goodDeliver, nil))
	if err != nil || reg.Realms["shop"].Settings.Email != nil || reg.Realms["shop"].Email != nil {
		t.Errorf("a realm without email: %v", err)
	}
}

// esTemplates copies the English templates as Spanish ones.
func esTemplates(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kind := range email.SystemKinds {
		defaults, _ := email.DefaultFiles(kind)
		for name, data := range defaults {
			out["shop/email/"+kind+"/es"+strings.TrimPrefix(name, "en")] = string(data)
		}
	}
	for _, kind := range email.PageKinds {
		page, _ := email.DefaultPage(kind)
		out["shop/pages/"+kind+"/es.html"] = string(page)
	}
	return out
}

func TestEmailRedirects(t *testing.T) {
	realm := goodEmail + "  redirect_delay: 10s\n  allowed_redirects: [https://app.shop.example/, acme://]\n  redirects:\n    verify_email: https://app.shop.example/verified\n  links:\n    reset_password: https://app.shop.example/reset?token={token}\n"
	reg, err := Load(emailTree(t, realm, goodDeliver, nil))
	if err != nil {
		t.Fatal(err)
	}
	es := reg.Realms["shop"].Settings.Email
	if es.RedirectDelay != 10*time.Second || es.Redirects["verify_email"] != "https://app.shop.example/verified" || !strings.Contains(es.Links["reset_password"], "{token}") {
		t.Errorf("settings: %+v", es)
	}
	for raw, want := range map[string]bool{
		"https://app.shop.example/anything?x=1": true, "acme://welcome": true, "https://app.shop.example.evil.com/": false,
		"http://app.shop.example/": false, "javascript:alert(1)": false, "//app.shop.example/": false, "/relative": false, "": false, "other://x": false,
	} {
		if got := es.AllowedRedirect(raw); got != want {
			t.Errorf("AllowedRedirect(%q) = %v", raw, got)
		}
	}
	// Without settings: the default delay, and nothing is allowed.
	reg, err = Load(emailTree(t, goodEmail, goodDeliver, nil))
	if err != nil {
		t.Fatal(err)
	}
	if es := reg.Realms["shop"].Settings.Email; es.RedirectDelay != DefaultRedirectDelay || es.AllowedRedirect("https://app.shop.example/") {
		t.Errorf("defaults: %+v", es)
	}
}

func TestEmailErrors(t *testing.T) {
	for _, tt := range []struct {
		name, realm, deliver string
		extra                map[string]string
		want                 string
	}{
		{"auth disabled", "auth: disabled\n" + goodEmail, goodDeliver, nil, "email: only applies when auth is enabled"},
		{"no function", strings.Replace(goodEmail, "function: notify/deliver", "function: deliver", 1), goodDeliver, nil, "email.function: must be <database>/<function>"},
		{"unknown function", strings.Replace(goodEmail, "notify/deliver", "notify/nothing", 1), goodDeliver, nil, `"notify/nothing" is not a function of this realm`},
		{"not internal", goodEmail, "mode: async\n", nil, "must have `internal: true`"},
		{"not async", goodEmail, "internal: true\n", nil, "must have `mode: async`"},
		{"no from", "email:\n  function: notify/deliver\n", goodDeliver, nil, "email.from: is required"},
		{"bad from", strings.Replace(goodEmail, "Shop <no-reply@shop.example>", "not an address", 1), goodDeliver, nil, "email.from"},
		{"bad public url", strings.Replace(goodEmail, "https://api.shop.example/", "api.shop.example", 1), goodDeliver, nil, "email.public_url"},
		{"bad locale", goodEmail + "  default_locale: English\n", goodDeliver, nil, "default_locale"},
		{"locale not listed", goodEmail + "  locales: [es]\n", goodDeliver, nil, "locales: must include default_locale (en)"},
		{"locale untranslated", goodEmail + "  locales: [en, fr]\n", goodDeliver, nil, "fr.subject.txt: missing"},
		{"limit", goodEmail + "  limits:\n    per_ip: {per_hour: 0}\n", goodDeliver, nil, "limits.per_ip.per_hour: must be at least 1"},
		{"redirect delay", goodEmail + "  redirect_delay: 5m\n", goodDeliver, nil, "redirect_delay"},
		{"redirect not allowed", goodEmail + "  allowed_redirects: [https://app.shop.example]\n  redirects:\n    verify_email: https://evil.example/x\n", goodDeliver, nil, "redirects.verify_email"},
		{"redirect unknown flow", goodEmail + "  allowed_redirects: [https://app.shop.example]\n  redirects:\n    welcome: https://app.shop.example/x\n", goodDeliver, nil, `unknown flow "welcome"`},
		{"link without token", goodEmail + "  allowed_redirects: [https://app.shop.example]\n  links:\n    reset_password: https://app.shop.example/reset\n", goodDeliver, nil, "{token}"},
		{"link outside", goodEmail + "  allowed_redirects: [https://app.shop.example]\n  links:\n    reset_password: https://evil.example/r?t={token}\n", goodDeliver, nil, "links.reset_password"},
		{"page missing", goodEmail + "  locales: [en, fr]\n", goodDeliver, nil, "fr.html: missing"},
		{"unknown key", goodEmail + "  smtp: x\n", goodDeliver, nil, "field smtp not found"},
		{"template broken", goodEmail, goodDeliver, map[string]string{"shop/email/verify-email/en.txt": "{{.Link"}, "verify-email/en.txt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(emailTree(t, tt.realm, tt.deliver, tt.extra))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want %q", err, tt.want)
			}
		})
	}
	// No templates folder at all.
	root := writeTree(t, map[string]string{
		"shop/realm.yaml":                              goodEmail,
		"shop/notify/_functions/deno.json":             `{}`,
		"shop/notify/_functions/deliver/function.yaml": goodDeliver,
		"shop/notify/_functions/deliver/index.ts":      "",
	})
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "has no email folder") {
		t.Errorf("no templates: %v", err)
	}
}

func TestEmailTemplatesAreInTheFingerprint(t *testing.T) {
	root := emailTree(t, goodEmail, goodDeliver, nil)
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := reg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shop/email/welcome/en.txt"), []byte("changed {{.Realm}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, _ = Load(root)
	after, _ := reg.Fingerprint()
	if before == after {
		t.Error("changing a template doesn't change the config fingerprint")
	}
}
