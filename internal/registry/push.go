package registry

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Push notifications (design/push-notifications.md): a realm enables a platform by giving it a
// block in `push:`. This file holds what the device registry needs; the providers' credentials,
// delivery and limits are parsed by the issues that use them.

// The platforms a device can be registered for.
const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
	PlatformWeb     = "web"
)

// Platforms lists the platforms in the order they are shown.
var Platforms = []string{PlatformAndroid, PlatformIOS, PlatformWeb}

// The built-in push providers.
const (
	PushProviderFCM     = "fcm"
	PushProviderAPNs    = "apns"
	PushProviderWebPush = "webpush"
	PushProviderCapture = "capture"
)

// Defaults of push.devices.
const (
	DefaultMaxDevicesPerUser = 10
	DefaultDeviceStaleAfter  = 60 * 24 * time.Hour
	maxDevicesPerUserLimit   = 100
	maxPushParallelism       = 200
)

// DefaultWebEndpointHosts are the push services of the major browsers.
var DefaultWebEndpointHosts = []string{
	"fcm.googleapis.com",
	"updates.push.services.mozilla.com",
	"web.push.apple.com",
	"*.notify.windows.com",
}

// Defaults of push.delivery, push.limits and push.send_retention.
const (
	DefaultPushTimeout       = 60 * time.Second
	DefaultPushAttempts      = 5
	DefaultPushBackoff       = 30 * time.Second
	DefaultPushParallelism   = 20
	DefaultPushPerMinute     = 5
	DefaultPushPerDay        = 24
	DefaultPushSendRetention = 30 * 24 * time.Hour
)

// PushSettings are the push settings of a realm.
type PushSettings struct {
	// Platforms holds the enabled platforms and their provider.
	Platforms map[string]PushPlatform
	// WebPublicKey is the VAPID public key (base64url, uncompressed P-256 point) browsers
	// subscribe with; WebEndpointHosts the hosts a subscription may point at: an exact host,
	// or `*.example.com` for any subdomain.
	WebPublicKey     string
	WebEndpointHosts []string
	// MaxDevicesPerUser caps a user's devices; StaleAfter removes devices not seen for that long.
	MaxDevicesPerUser int
	StaleAfter        time.Duration
	// Delivery tunes the built-in senders; Limits are the per-user limits; SendRetention is how
	// long the record of a send is kept.
	Delivery      PushDelivery
	Limits        PushLimits
	SendRetention time.Duration
}

// PushDelivery is push.delivery: a delivery unit's time limit, the attempts a unit gets and
// the provider requests a worker keeps in flight.
type PushDelivery struct {
	Timeout       time.Duration
	RetryAttempts int
	RetryBackoff  time.Duration
	Parallelism   int
}

// PushLimits are push.limits.per_user: the pushes a user gets in a minute and a day.
type PushLimits struct {
	PerMinute, PerDay int
}

// PushPlatform is one enabled platform: its provider and that provider's credentials. The
// secrets are named, never held: Credentials, Key, PrivateKey and Subject are names of realm
// secrets (written secret:NAME in realm.yaml).
type PushPlatform struct {
	Provider string
	// FCM: the service-account JSON.
	Credentials string
	// APNs: the .p8 signing key, its id, the team and the app's bundle id; Sandbox targets
	// api.sandbox.push.apple.com.
	Key           string
	KeyID, TeamID string
	Topic         string
	Sandbox       bool
	// Web Push: the VAPID private key and the contact URI (mailto: or https:).
	PrivateKey string
	Subject    string
}

// SecretNames are the realm secrets the platform's provider needs.
func (p PushPlatform) SecretNames() []string {
	var out []string
	for _, n := range []string{p.Credentials, p.Key, p.PrivateKey, p.Subject} {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// SecretNames are the realm secrets push needs, sorted and without repeats.
func (p *PushSettings) SecretNames() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, name := range Platforms {
		for _, n := range p.Platforms[name].SecretNames() {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Enabled says whether a device may be registered for the platform.
func (p *PushSettings) Enabled(platform string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Platforms[platform]
	return ok
}

// WebHostAllowed says whether a web subscription may point at the host.
func (p *PushSettings) WebHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range p.WebEndpointHosts {
		pat = strings.ToLower(pat)
		if rest, ok := strings.CutPrefix(pat, "*."); ok {
			if strings.HasSuffix(host, "."+rest) && len(host) > len(rest)+1 {
				return true
			}
			continue
		}
		if host == pat {
			return true
		}
	}
	return false
}

// pushDoc mirrors the push section of realm.yaml.
type pushDoc struct {
	Android *pushPlatformDoc `yaml:"android"`
	IOS     *pushPlatformDoc `yaml:"ios"`
	Web     *pushPlatformDoc `yaml:"web"`
	Devices *struct {
		MaxPerUser *int   `yaml:"max_per_user"`
		StaleAfter string `yaml:"stale_after"`
	} `yaml:"devices"`
	Delivery *struct {
		Timeout string `yaml:"timeout"`
		Retry   *struct {
			Attempts *int   `yaml:"attempts"`
			Backoff  string `yaml:"backoff"`
		} `yaml:"retry"`
		Parallelism *int `yaml:"parallelism"`
	} `yaml:"delivery"`
	Limits *struct {
		PerUser *struct {
			PerMinute *int `yaml:"per_minute"`
			PerDay    *int `yaml:"per_day"`
		} `yaml:"per_user"`
	} `yaml:"limits"`
	SendRetention string `yaml:"send_retention"`
}

// pushPlatformDoc is a platform's block: every provider's keys, of which a provider takes its own.
type pushPlatformDoc struct {
	Provider      string   `yaml:"provider"`
	Credentials   string   `yaml:"credentials"`
	Key           string   `yaml:"key"`
	KeyID         string   `yaml:"key_id"`
	TeamID        string   `yaml:"team_id"`
	Topic         string   `yaml:"topic"`
	Sandbox       *bool    `yaml:"sandbox"`
	PublicKey     string   `yaml:"public_key"`
	PrivateKey    string   `yaml:"private_key"`
	Subject       string   `yaml:"subject"`
	EndpointHosts []string `yaml:"endpoint_hosts"`
}

// providersOf lists the providers a platform may use.
var pushProvidersOf = map[string][]string{
	PlatformAndroid: {PushProviderFCM, PushProviderCapture},
	PlatformIOS:     {PushProviderAPNs, PushProviderFCM, PushProviderCapture},
	PlatformWeb:     {PushProviderWebPush, PushProviderFCM, PushProviderCapture},
}

// parsePush checks the push section of realm.yaml.
func parsePush(d *pushDoc) (*PushSettings, []error) {
	p := &PushSettings{
		Platforms: map[string]PushPlatform{}, MaxDevicesPerUser: DefaultMaxDevicesPerUser, StaleAfter: DefaultDeviceStaleAfter,
		Delivery:      PushDelivery{Timeout: DefaultPushTimeout, RetryAttempts: DefaultPushAttempts, RetryBackoff: DefaultPushBackoff, Parallelism: DefaultPushParallelism},
		Limits:        PushLimits{PerMinute: DefaultPushPerMinute, PerDay: DefaultPushPerDay},
		SendRetention: DefaultPushSendRetention,
	}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("push."+format, args...)) }
	secret := func(field, value string, dst *string) {
		n, found := strings.CutPrefix(value, secretPrefix)
		switch {
		case value == "":
			add("%s: is required, written secret:NAME (a secret of the realm: backd secret set)", field)
		case !found || !ValidSecretName(n):
			add("%s: %q must be secret:NAME with NAME in upper case letters, digits and _ (the secret itself never goes in realm.yaml)", field, value)
		default:
			*dst = n
		}
	}
	platform := func(name string, d *pushPlatformDoc) {
		if d == nil {
			return
		}
		pp := PushPlatform{Provider: d.Provider}
		switch {
		case d.Provider == "":
			add("%s.provider: is required (%s)", name, strings.Join(pushProvidersOf[name], ", "))
			return
		case !slices.Contains(pushProvidersOf[name], d.Provider):
			add("%s.provider: %q isn't a provider for %s (use %s)", name, d.Provider, name, strings.Join(pushProvidersOf[name], ", "))
			return
		}
		only := func(field string, present bool, providers ...string) {
			if present && !slices.Contains(providers, d.Provider) {
				add("%s.%s: doesn't apply to provider %s", name, field, d.Provider)
			}
		}
		only("credentials", d.Credentials != "", PushProviderFCM)
		only("key", d.Key != "", PushProviderAPNs)
		only("key_id", d.KeyID != "", PushProviderAPNs)
		only("team_id", d.TeamID != "", PushProviderAPNs)
		only("topic", d.Topic != "", PushProviderAPNs)
		only("sandbox", d.Sandbox != nil, PushProviderAPNs)
		only("private_key", d.PrivateKey != "", PushProviderWebPush)
		only("subject", d.Subject != "", PushProviderWebPush)
		if name != PlatformWeb {
			only("public_key", d.PublicKey != "")
			only("endpoint_hosts", len(d.EndpointHosts) > 0)
		}
		switch d.Provider {
		case PushProviderFCM:
			secret(name+".credentials", d.Credentials, &pp.Credentials)
		case PushProviderAPNs:
			secret(name+".key", d.Key, &pp.Key)
			for field, v := range map[string]*string{"key_id": &pp.KeyID, "team_id": &pp.TeamID, "topic": &pp.Topic} {
				val := map[string]string{"key_id": d.KeyID, "team_id": d.TeamID, "topic": d.Topic}[field]
				if val == "" {
					add("%s.%s: is required for APNs", name, field)
				}
				*v = val
			}
			pp.Sandbox = d.Sandbox != nil && *d.Sandbox
		case PushProviderWebPush:
			secret(name+".private_key", d.PrivateKey, &pp.PrivateKey)
			secret(name+".subject", d.Subject, &pp.Subject)
			if d.PublicKey == "" {
				add("%s.public_key: is required (backd push web-keys makes the key pair and prints it)", name)
			}
		}
		p.Platforms[name] = pp
	}
	platform(PlatformAndroid, d.Android)
	platform(PlatformIOS, d.IOS)
	platform(PlatformWeb, d.Web)
	if w := d.Web; w != nil {
		p.WebEndpointHosts = slices.Clone(DefaultWebEndpointHosts)
		if len(w.EndpointHosts) > 0 {
			p.WebEndpointHosts = nil
			for i, h := range w.EndpointHosts {
				host := strings.ToLower(strings.TrimSpace(h))
				bare := strings.TrimPrefix(host, "*.")
				switch {
				case host == "" || strings.ContainsAny(bare, "/:@ ") || !strings.Contains(bare, "."):
					add("web.endpoint_hosts[%d]: %q must be a host name such as push.example.com, or *.example.com", i, h)
				case slices.Contains(p.WebEndpointHosts, host):
					add("web.endpoint_hosts[%d]: %q is listed twice", i, h)
				default:
					p.WebEndpointHosts = append(p.WebEndpointHosts, host)
				}
			}
		}
		if w.PublicKey != "" {
			if err := checkVAPIDPublicKey(w.PublicKey); err != nil {
				add("web.public_key: %v", err)
			} else {
				p.WebPublicKey = w.PublicKey
			}
		}
	}
	positive := func(field string, v *int, dst *int) {
		switch {
		case v == nil:
		case *v < 1:
			add("%s: must be at least 1, got %d", field, *v)
		default:
			*dst = *v
		}
	}
	span := func(field, v string, min time.Duration, dst *time.Duration) {
		if v == "" {
			return
		}
		t, err := ParseDuration(v)
		switch {
		case err != nil:
			add("%s: %v", field, err)
		case t < min:
			add("%s: must be at least %s, got %s", field, min, v)
		default:
			*dst = t
		}
	}
	if dl := d.Delivery; dl != nil {
		span("delivery.timeout", dl.Timeout, time.Second, &p.Delivery.Timeout)
		positive("delivery.parallelism", dl.Parallelism, &p.Delivery.Parallelism)
		if dl.Parallelism != nil && *dl.Parallelism > maxPushParallelism {
			add("delivery.parallelism: at most %d, got %d", maxPushParallelism, *dl.Parallelism)
			p.Delivery.Parallelism = DefaultPushParallelism
		}
		if r := dl.Retry; r != nil {
			positive("delivery.retry.attempts", r.Attempts, &p.Delivery.RetryAttempts)
			span("delivery.retry.backoff", r.Backoff, time.Second, &p.Delivery.RetryBackoff)
		}
	}
	if l := d.Limits; l != nil && l.PerUser != nil {
		positive("limits.per_user.per_minute", l.PerUser.PerMinute, &p.Limits.PerMinute)
		positive("limits.per_user.per_day", l.PerUser.PerDay, &p.Limits.PerDay)
	}
	span("send_retention", d.SendRetention, 24*time.Hour, &p.SendRetention)
	if dv := d.Devices; dv != nil {
		if dv.MaxPerUser != nil {
			if *dv.MaxPerUser < 1 || *dv.MaxPerUser > maxDevicesPerUserLimit {
				add("devices.max_per_user: must be between 1 and %d, got %d", maxDevicesPerUserLimit, *dv.MaxPerUser)
			} else {
				p.MaxDevicesPerUser = *dv.MaxPerUser
			}
		}
		if dv.StaleAfter != "" {
			if t, err := ParseDuration(dv.StaleAfter); err != nil {
				add("devices.stale_after: %v", err)
			} else if t < 24*time.Hour {
				add("devices.stale_after: must be at least 24h, got %s", dv.StaleAfter)
			} else {
				p.StaleAfter = t
			}
		}
	}
	if len(p.Platforms) == 0 && len(errs) == 0 {
		add(": enable at least one platform (android, ios or web) with its provider")
	}
	return p, errs
}

// checkVAPIDPublicKey says whether v is an uncompressed P-256 point in base64url, as browsers
// take it for applicationServerKey.
func checkVAPIDPublicKey(v string) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
	if err != nil {
		return fmt.Errorf("must be the VAPID public key in base64url (backd push web-keys prints it)")
	}
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return fmt.Errorf("isn't a valid P-256 public key")
	}
	return nil
}
