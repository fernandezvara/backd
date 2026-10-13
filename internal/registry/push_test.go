package registry

import (
	"strings"
	"testing"
	"time"
)

// A real VAPID public key (an uncompressed P-256 point).
const vapidKey = "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U"

func TestPushSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte(`
push:
  android: {provider: fcm}
  ios: {provider: apns}
  web:
    provider: webpush
    public_key: ` + vapidKey + `
    endpoint_hosts: [push.example.com, "*.notify.example.net"]
  devices:
    max_per_user: 5
    stale_after: 30d
`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	p := s.Push
	if p == nil || !p.Enabled("android") || !p.Enabled("ios") || !p.Enabled("web") || p.Platforms["ios"].Provider != "apns" {
		t.Fatalf("platforms: %+v", p)
	}
	if p.MaxDevicesPerUser != 5 || p.StaleAfter != 30*24*time.Hour || p.WebPublicKey != vapidKey {
		t.Errorf("settings: %+v", p)
	}
	for host, want := range map[string]bool{
		"push.example.com": true, "PUSH.example.com.": true, "a.notify.example.net": true,
		"notify.example.net": false, "evil.example.com": false, "xnotify.example.net": false, "fcm.googleapis.com": false,
	} {
		if got := p.WebHostAllowed(host); got != want {
			t.Errorf("host %q: %v, want %v", host, got, want)
		}
	}
	// Defaults.
	s, errs = parseRealmSettings([]byte("push:\n  web: {provider: webpush}\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.Push.MaxDevicesPerUser != 10 || s.Push.StaleAfter != 60*24*time.Hour || !s.Push.WebHostAllowed("updates.push.services.mozilla.com") || !s.Push.WebHostAllowed("x.notify.windows.com") {
		t.Errorf("defaults: %+v", s.Push)
	}
	if (*PushSettings)(nil).Enabled("web") {
		t.Error("a nil push enables a platform")
	}
	if s, _ := parseRealmSettings([]byte("auth: enabled\n")); s.Push != nil {
		t.Error("push without a section")
	}
}

func TestPushSettingsErrors(t *testing.T) {
	for name, c := range map[string]struct{ doc, want string }{
		"no platform":      {"push: {}\n", "enable at least one platform"},
		"no provider":      {"push:\n  android: {}\n", "android.provider: is required"},
		"wrong provider":   {"push:\n  android: {provider: apns}\n", `"apns" isn't a provider for android`},
		"web with apns":    {"push:\n  web: {provider: apns}\n", "web.provider"},
		"bad vapid":        {"push:\n  web: {provider: webpush, public_key: abc}\n", "web.public_key"},
		"host with path":   {"push:\n  web: {provider: webpush, endpoint_hosts: [\"push.example.com/x\"]}\n", "endpoint_hosts[0]"},
		"host without dot": {"push:\n  web: {provider: webpush, endpoint_hosts: [localhost]}\n", "endpoint_hosts[0]"},
		"host twice":       {"push:\n  web: {provider: webpush, endpoint_hosts: [a.example.com, a.example.com]}\n", "listed twice"},
		"cap":              {"push:\n  android: {provider: fcm}\n  devices: {max_per_user: 0}\n", "max_per_user"},
		"stale":            {"push:\n  android: {provider: fcm}\n  devices: {stale_after: 1h}\n", "stale_after"},
		"unknown key":      {"push:\n  android: {provider: fcm, token: x}\n", "field token not found"},
		"auth disabled":    {"auth: disabled\npush:\n  android: {provider: fcm}\n", "push: only applies when auth is enabled"},
	} {
		_, errs := parseRealmSettings([]byte(c.doc))
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Error(), c.want)
		}
		if !found {
			t.Errorf("%s: want %q, got %v", name, c.want, errs)
		}
	}
}
