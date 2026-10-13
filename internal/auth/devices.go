package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// The device registry (design/push-notifications.md §5): the devices that receive a user's
// pushes, owned and kept accurate by backd. A device belongs to the session that registered
// it, so it goes with the session.

// MaxDeviceTokenBytes is the largest token a device may register.
const MaxDeviceTokenBytes = 4096

// deviceRegistrationsPerHour is the registrations a session may make in an hour.
const deviceRegistrationsPerHour = 30

var (
	// ErrPushNotConfigured: the realm has no push: section.
	ErrPushNotConfigured = errors.New("push notifications are not configured in this realm")
	// ErrPlatformNotEnabled: the realm has no provider for the platform.
	ErrPlatformNotEnabled = errors.New("that platform is not enabled in this realm")
	// ErrPushUnavailable: a secret the realm's push providers need is not set.
	ErrPushUnavailable = errors.New("push notifications are unavailable: a secret the providers need is not set")
	// ErrWebKeyUnavailable: the realm has no web platform or no VAPID public key yet.
	ErrWebKeyUnavailable = errors.New("web push is not enabled in this realm")
)

// InvalidDeviceError says why a token isn't acceptable.
type InvalidDeviceError struct{ Reason string }

func (e *InvalidDeviceError) Error() string { return "invalid device: " + e.Reason }

// Device is a registered device of a user. Token is the token sealed with the realm's key;
// it never leaves the store and the senders.
type Device struct {
	ID        string
	UserID    string
	SessionID string // the session that registered it
	Platform  string
	Name      string
	TokenHash string // of the token, or of a web subscription's endpoint
	Token     string // sealed
	CreatedAt time.Time
	LastSeen  time.Time
}

// DeviceInput is a registration: the platform, the token the provider gave the app (for web,
// the subscription as JSON) and an optional label.
type DeviceInput struct {
	Platform string
	Token    string
	Name     string
}

// webSubscription is the part of a Push API subscription backd needs.
type webSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// RegisterDevice registers or refreshes a device of the signed-in user and returns it. A
// token that exists moves to this user and session; above the cap the least recently seen
// device goes.
func (s *Users) RegisterDevice(ctx context.Context, p Principal, in DeviceInput) (Device, error) {
	push := s.Settings.Push
	if push == nil {
		return Device{}, ErrPushNotConfigured
	}
	if !push.Enabled(in.Platform) {
		return Device{}, ErrPlatformNotEnabled
	}
	identity, err := s.deviceIdentity(push, in)
	if err != nil {
		return Device{}, err
	}
	if len(in.Name) > 100 {
		return Device{}, &InvalidDeviceError{Reason: "name: at most 100 characters"}
	}
	if err := s.RateLimit(ctx, "device:"+p.Session.ID, deviceRegistrationsPerHour, time.Hour); err != nil {
		return Device{}, err
	}
	sealed, err := s.SealToken(in.Token)
	if err != nil {
		return Device{}, err
	}
	now := s.now()
	d, moved, err := s.Store.PutDevice(ctx, Device{
		ID: xid.New().String(), UserID: p.User.ID, SessionID: p.Session.ID, Platform: in.Platform,
		Name: strings.TrimSpace(in.Name), TokenHash: HashToken(identity), Token: sealed, CreatedAt: now, LastSeen: now,
	})
	if err != nil {
		return Device{}, err
	}
	// The cap: the least recently seen devices beyond it go, never the one just registered.
	if moved {
		s.Metrics.PushPruned(s.Realm, "moved", 1)
	}
	capped, err := s.Store.DeleteOldestDevices(ctx, p.User.ID, push.MaxDevicesPerUser, d.ID)
	if err != nil {
		return Device{}, err
	}
	s.Metrics.PushPruned(s.Realm, "cap", capped)
	return d, nil
}

// deviceIdentity validates the token and returns what identifies the device: the token, or
// for a web subscription its endpoint (the browser gets new keys when it subscribes again).
func (s *Users) deviceIdentity(push *registry.PushSettings, in DeviceInput) (string, error) {
	if in.Token == "" {
		return "", &InvalidDeviceError{Reason: "token: is required"}
	}
	if len(in.Token) > MaxDeviceTokenBytes {
		return "", &InvalidDeviceError{Reason: "token: at most 4 KiB"}
	}
	if in.Platform != registry.PlatformWeb {
		return in.Token, nil
	}
	var sub webSubscription
	if err := json.Unmarshal([]byte(in.Token), &sub); err != nil || sub.Endpoint == "" {
		return "", &InvalidDeviceError{Reason: "token: a web device registers its push subscription as JSON (endpoint and keys)"}
	}
	u, err := url.Parse(sub.Endpoint)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.Fragment != "":
		return "", &InvalidDeviceError{Reason: "endpoint: isn't a valid URL"}
	case u.Scheme != "https":
		return "", &InvalidDeviceError{Reason: "endpoint: must be an https URL"}
	case u.Port() != "" && u.Port() != "443":
		return "", &InvalidDeviceError{Reason: "endpoint: must use the standard https port"}
	case !push.WebHostAllowed(u.Hostname()):
		return "", &InvalidDeviceError{Reason: "endpoint: that host is not a push service this realm accepts (push.web.endpoint_hosts)"}
	}
	for name, key := range map[string]string{"p256dh": sub.Keys.P256dh, "auth": sub.Keys.Auth} {
		if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(key, "=")); err != nil || len(raw) == 0 {
			return "", &InvalidDeviceError{Reason: "keys." + name + ": is required, in base64url"}
		}
	}
	return sub.Endpoint, nil
}

// Devices lists a user's devices, most recently seen first, without tokens' meaning to the
// caller (the sealed value is in each one: the HTTP layer never returns it).
func (s *Users) Devices(ctx context.Context, userID string) ([]Device, error) {
	if s.Settings.Push == nil {
		return nil, ErrPushNotConfigured
	}
	return s.Store.ListDevices(ctx, userID)
}

// RemoveDevice removes one of the user's devices; ErrNotFound when it isn't theirs. An
// administrator's removal (by != "") is audited.
func (s *Users) RemoveDevice(ctx context.Context, userID, id, by string) error {
	if s.Settings.Push == nil {
		return ErrPushNotConfigured
	}
	var d Device
	if by != "" {
		devices, err := s.Store.ListDevices(ctx, userID)
		if err != nil {
			return err
		}
		for _, x := range devices {
			if x.ID == id {
				d = x
			}
		}
	}
	if err := s.Store.DeleteDevice(ctx, userID, id); err != nil {
		return err
	}
	if by != "" {
		s.Audit(ctx, AuditDeviceRemoved, userTarget(userID), map[string]any{"platform": d.Platform, "device": id})
	}
	return nil
}

// SweepDevices removes the devices not seen for push.devices.stale_after and those whose
// session ended without anyone calling anything (it expired). Running it in every worker is
// harmless.
func (s *Users) SweepDevices(ctx context.Context) (stale, orphaned int64, err error) {
	push := s.Settings.Push
	if push == nil {
		return 0, 0, nil
	}
	now := s.now()
	if stale, err = s.Store.DeleteStaleDevices(ctx, now.Add(-push.StaleAfter)); err != nil {
		return 0, 0, err
	}
	s.Metrics.PushPruned(s.Realm, "stale", stale)
	orphaned, err = s.Store.DeleteOrphanedDevices(ctx, now)
	s.Metrics.PushPruned(s.Realm, "session", orphaned)
	return stale, orphaned, err
}
