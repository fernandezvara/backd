package registry

import (
	"strings"
	"testing"
	"time"
)

// A real VAPID public key (an uncompressed P-256 point).
const vapidKey = "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U"

const fullPush = `
push:
  android:
    provider: fcm
    credentials: secret:FCM_SERVICE_ACCOUNT
  ios:
    provider: apns
    key: secret:APNS_KEY
    key_id: AB12CD34EF
    team_id: T123456789
    topic: com.acme.app
    sandbox: true
  web:
    provider: webpush
    public_key: ` + vapidKey + `
    private_key: secret:VAPID_PRIVATE_KEY
    subject: secret:VAPID_SUBJECT
    endpoint_hosts: [push.example.com, "*.notify.example.net"]
  devices:
    max_per_user: 5
    stale_after: 30d
  delivery:
    timeout: 30s
    retry: {attempts: 3, backoff: 10s}
    parallelism: 8
  limits:
    per_user: {per_minute: 2, per_day: 10}
  send_retention: 7d
`

func TestPushSettings(t *testing.T) {
	s, errs := parseRealmSettings([]byte(fullPush))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	p := s.Push
	if p == nil || !p.Enabled("android") || !p.Enabled("ios") || !p.Enabled("web") || p.Platforms["ios"].Provider != "apns" {
		t.Fatalf("platforms: %+v", p)
	}
	ios := p.Platforms["ios"]
	if ios.Key != "APNS_KEY" || ios.KeyID != "AB12CD34EF" || ios.TeamID != "T123456789" || ios.Topic != "com.acme.app" || !ios.Sandbox {
		t.Errorf("apns: %+v", ios)
	}
	if p.Platforms["android"].Credentials != "FCM_SERVICE_ACCOUNT" || p.Platforms["web"].PrivateKey != "VAPID_PRIVATE_KEY" || p.Platforms["web"].Subject != "VAPID_SUBJECT" {
		t.Errorf("secrets: %+v", p.Platforms)
	}
	if got := strings.Join(p.SecretNames(), ","); got != "APNS_KEY,FCM_SERVICE_ACCOUNT,VAPID_PRIVATE_KEY,VAPID_SUBJECT" {
		t.Errorf("secret names: %s", got)
	}
	if p.MaxDevicesPerUser != 5 || p.StaleAfter != 30*24*time.Hour || p.WebPublicKey != vapidKey {
		t.Errorf("settings: %+v", p)
	}
	if p.Delivery != (PushDelivery{Timeout: 30 * time.Second, RetryAttempts: 3, RetryBackoff: 10 * time.Second, Parallelism: 8}) ||
		p.Limits != (PushLimits{PerMinute: 2, PerDay: 10}) || p.SendRetention != 7*24*time.Hour {
		t.Errorf("tuning: %+v %+v %v", p.Delivery, p.Limits, p.SendRetention)
	}
	for host, want := range map[string]bool{
		"push.example.com": true, "PUSH.example.com.": true, "a.notify.example.net": true,
		"notify.example.net": false, "evil.example.com": false, "xnotify.example.net": false, "fcm.googleapis.com": false,
	} {
		if got := p.WebHostAllowed(host); got != want {
			t.Errorf("host %q: %v, want %v", host, got, want)
		}
	}
	// Defaults: capture needs no credentials.
	s, errs = parseRealmSettings([]byte("push:\n  web: {provider: capture}\n"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	p = s.Push
	if p.MaxDevicesPerUser != 10 || p.StaleAfter != 60*24*time.Hour || !p.WebHostAllowed("updates.push.services.mozilla.com") || !p.WebHostAllowed("x.notify.windows.com") {
		t.Errorf("defaults: %+v", p)
	}
	if p.Delivery != (PushDelivery{Timeout: 60 * time.Second, RetryAttempts: 5, RetryBackoff: 30 * time.Second, Parallelism: 20}) ||
		p.Limits != (PushLimits{PerMinute: 5, PerDay: 24}) || p.SendRetention != 30*24*time.Hour || len(p.SecretNames()) != 0 {
		t.Errorf("tuning defaults: %+v %+v %v", p.Delivery, p.Limits, p.SendRetention)
	}
	if (*PushSettings)(nil).Enabled("web") || (*PushSettings)(nil).SecretNames() != nil {
		t.Error("a nil push enables a platform")
	}
	if s, _ := parseRealmSettings([]byte("auth: enabled\n")); s.Push != nil {
		t.Error("push without a section")
	}
	// iOS over FCM takes the service account, not the APNs key.
	s, errs = parseRealmSettings([]byte("push:\n  ios: {provider: fcm, credentials: secret:FCM}\n"))
	if len(errs) > 0 || s.Push.Platforms["ios"].Credentials != "FCM" {
		t.Errorf("ios over fcm: %v %+v", errs, s.Push)
	}
}

func TestPushSettingsErrors(t *testing.T) {
	for name, c := range map[string]struct{ doc, want string }{
		"no platform":      {"push: {}\n", "enable at least one platform"},
		"no provider":      {"push:\n  android: {}\n", "android.provider: is required"},
		"wrong provider":   {"push:\n  android: {provider: apns}\n", `"apns" isn't a provider for android`},
		"web with apns":    {"push:\n  web: {provider: apns}\n", "web.provider"},
		"fcm no secret":    {"push:\n  android: {provider: fcm}\n", "android.credentials: is required"},
		"secret in clear":  {"push:\n  android: {provider: fcm, credentials: '{\"a\":1}'}\n", "must be secret:NAME"},
		"apns missing":     {"push:\n  ios: {provider: apns, key: secret:K}\n", "ios.key_id: is required"},
		"apns key field":   {"push:\n  ios: {provider: apns, key: secret:K, key_id: a, team_id: b, topic: c, credentials: secret:X}\n", "credentials: doesn't apply to provider apns"},
		"webpush no key":   {"push:\n  web: {provider: webpush, private_key: secret:P, subject: secret:S}\n", "web.public_key: is required"},
		"webpush no subj":  {"push:\n  web: {provider: webpush, public_key: " + vapidKey + ", private_key: secret:P}\n", "web.subject: is required"},
		"bad vapid":        {"push:\n  web: {provider: capture, public_key: abc}\n", "web.public_key"},
		"host with path":   {"push:\n  web: {provider: capture, endpoint_hosts: [\"push.example.com/x\"]}\n", "endpoint_hosts[0]"},
		"host without dot": {"push:\n  web: {provider: capture, endpoint_hosts: [localhost]}\n", "endpoint_hosts[0]"},
		"host twice":       {"push:\n  web: {provider: capture, endpoint_hosts: [a.example.com, a.example.com]}\n", "listed twice"},
		"cap":              {"push:\n  android: {provider: capture}\n  devices: {max_per_user: 0}\n", "max_per_user"},
		"stale":            {"push:\n  android: {provider: capture}\n  devices: {stale_after: 1h}\n", "stale_after"},
		"timeout":          {"push:\n  android: {provider: capture}\n  delivery: {timeout: 0s}\n", "delivery.timeout"},
		"parallelism":      {"push:\n  android: {provider: capture}\n  delivery: {parallelism: 0}\n", "delivery.parallelism"},
		"parallelism cap":  {"push:\n  android: {provider: capture}\n  delivery: {parallelism: 100000}\n", "delivery.parallelism: at most 200"},
		"attempts":         {"push:\n  android: {provider: capture}\n  delivery: {retry: {attempts: 0}}\n", "delivery.retry.attempts"},
		"per minute":       {"push:\n  android: {provider: capture}\n  limits: {per_user: {per_minute: 0}}\n", "per_minute"},
		"retention":        {"push:\n  android: {provider: capture}\n  send_retention: 1h\n", "send_retention"},
		"unknown key":      {"push:\n  android: {provider: capture, token: x}\n", "field token not found"},
		"email locale":     {"locales: [en, en]\npush:\n  android: {provider: capture}\n", "listed twice"},
		"auth disabled":    {"auth: disabled\npush:\n  android: {provider: capture}\n", "push: only applies when auth is enabled"},
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
