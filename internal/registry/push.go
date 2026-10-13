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
)

// DefaultWebEndpointHosts are the push services of the major browsers.
var DefaultWebEndpointHosts = []string{
	"fcm.googleapis.com",
	"updates.push.services.mozilla.com",
	"web.push.apple.com",
	"*.notify.windows.com",
}

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
}

// PushPlatform is one enabled platform.
type PushPlatform struct {
	Provider string
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
	Web     *pushWebDoc      `yaml:"web"`
	Devices *struct {
		MaxPerUser *int   `yaml:"max_per_user"`
		StaleAfter string `yaml:"stale_after"`
	} `yaml:"devices"`
}

type pushPlatformDoc struct {
	Provider string `yaml:"provider"`
}

type pushWebDoc struct {
	Provider      string   `yaml:"provider"`
	PublicKey     string   `yaml:"public_key"`
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
	p := &PushSettings{Platforms: map[string]PushPlatform{}, MaxDevicesPerUser: DefaultMaxDevicesPerUser, StaleAfter: DefaultDeviceStaleAfter}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf("push."+format, args...)) }
	platform := func(name, provider string) {
		if provider == "" {
			add("%s.provider: is required (%s)", name, strings.Join(pushProvidersOf[name], ", "))
			return
		}
		if !slices.Contains(pushProvidersOf[name], provider) {
			add("%s.provider: %q isn't a provider for %s (use %s)", name, provider, name, strings.Join(pushProvidersOf[name], ", "))
			return
		}
		p.Platforms[name] = PushPlatform{Provider: provider}
	}
	if d.Android != nil {
		platform(PlatformAndroid, d.Android.Provider)
	}
	if d.IOS != nil {
		platform(PlatformIOS, d.IOS.Provider)
	}
	if w := d.Web; w != nil {
		platform(PlatformWeb, w.Provider)
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
