package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func pushUsers(t *testing.T) (*Users, *authtest.MemStore, *time.Time) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Realm = "acme"
	svc.Cipher, _ = NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc.Settings.Push = &registry.PushSettings{
		Platforms:         map[string]registry.PushPlatform{"android": {Provider: "fcm"}, "web": {Provider: "webpush"}},
		WebEndpointHosts:  registry.DefaultWebEndpointHosts,
		MaxDevicesPerUser: 3,
		StaleAfter:        60 * 24 * time.Hour,
	}
	return svc, store, &clock
}

func signedIn(t *testing.T, svc *Users, email string) Principal {
	t.Helper()
	if _, err := svc.Create(context.Background(), email, ptr("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	p, _, err := svc.Login(context.Background(), email, "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRegisterDeviceStoresTheTokenSealed(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := pushUsers(t)
	ada := signedIn(t, svc, "ada@example.com")
	d, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: "secret-fcm-token", Name: " Pixel "})
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "Pixel" || d.UserID != ada.User.ID || d.SessionID != ada.Session.ID {
		t.Errorf("device: %+v", d)
	}
	list, _ := store.ListDevices(ctx, ada.User.ID)
	if len(list) != 1 || list[0].Token == "secret-fcm-token" || list[0].TokenHash == "secret-fcm-token" {
		t.Fatalf("stored: %+v", list)
	}
	if got, err := svc.OpenToken(list[0].Token); err != nil || got != "secret-fcm-token" {
		t.Errorf("sealed token: %q %v", got, err)
	}
}

func TestRegisterDeviceRules(t *testing.T) {
	ctx := context.Background()
	svc, _, clock := pushUsers(t)
	ada := signedIn(t, svc, "ada@example.com")
	if _, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "ios", Token: "x"}); !errors.Is(err, ErrPlatformNotEnabled) {
		t.Errorf("platform: %v", err)
	}
	var bad *InvalidDeviceError
	for name, in := range map[string]DeviceInput{
		"empty":     {Platform: "android"},
		"too big":   {Platform: "android", Token: string(make([]byte, MaxDeviceTokenBytes+1))},
		"long name": {Platform: "android", Token: "x", Name: string(make([]byte, 101))},
		"port":      {Platform: "web", Token: `{"endpoint":"https://fcm.googleapis.com:8443/x","keys":{"p256dh":"AAAA","auth":"AAAA"}}`},
		"userinfo":  {Platform: "web", Token: `{"endpoint":"https://u:p@fcm.googleapis.com/x","keys":{"p256dh":"AAAA","auth":"AAAA"}}`},
		"lookalike": {Platform: "web", Token: `{"endpoint":"https://evilfcm.googleapis.com.evil.example/x","keys":{"p256dh":"AAAA","auth":"AAAA"}}`},
		"bare star": {Platform: "web", Token: `{"endpoint":"https://notify.windows.com/x","keys":{"p256dh":"AAAA","auth":"AAAA"}}`},
	} {
		if _, err := svc.RegisterDevice(ctx, ada, in); !errors.As(err, &bad) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Thirty registrations an hour per session.
	for i := range 30 {
		if _, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: "tok-" + string(rune('a'+i%5))}); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	var th *ThrottledError
	if _, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: "tok-a"}); !errors.As(err, &th) {
		t.Errorf("the 31st: %v", err)
	}
	*clock = clock.Add(time.Hour + time.Minute)
	if _, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: "tok-a"}); err != nil {
		t.Errorf("an hour later: %v", err)
	}
}

func TestRegisterDeviceWithoutPush(t *testing.T) {
	svc, _, _ := pushUsers(t)
	ada := signedIn(t, svc, "ada@example.com")
	svc.Settings.Push = nil
	if _, err := svc.RegisterDevice(context.Background(), ada, DeviceInput{Platform: "android", Token: "x"}); !errors.Is(err, ErrPushNotConfigured) {
		t.Errorf("no push: %v", err)
	}
}

func TestSweepDevices(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := pushUsers(t)
	svc.Settings.IdleTimeout, svc.Settings.MaxLifetime = 400*24*time.Hour, 400*24*time.Hour
	ada := signedIn(t, svc, "ada@example.com")
	register := func(tok string) {
		t.Helper()
		if _, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: tok}); err != nil {
			t.Fatal(err)
		}
	}
	register("old")
	*clock = clock.Add(61 * 24 * time.Hour)
	register("new")
	// Only the device not seen for 60 days goes.
	stale, orphaned, err := svc.SweepDevices(ctx)
	if err != nil || stale != 1 || orphaned != 0 {
		t.Fatalf("sweep: %d %d %v", stale, orphaned, err)
	}
	if left, _ := store.ListDevices(ctx, ada.User.ID); len(left) != 1 {
		t.Errorf("left: %d", len(left))
	}
}

// A session that expires without anyone calling anything takes its devices with it.
func TestSweepDevicesOfExpiredSessions(t *testing.T) {
	ctx := context.Background()
	svc, store, clock := pushUsers(t)
	ada := signedIn(t, svc, "ada@example.com")
	if _, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: "x"}); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(31 * 24 * time.Hour) // past the idle timeout, short of stale_after
	stale, orphaned, err := svc.SweepDevices(ctx)
	if err != nil || stale != 0 || orphaned != 1 {
		t.Fatalf("sweep: %d %d %v", stale, orphaned, err)
	}
	if left, _ := store.ListDevices(ctx, ada.User.ID); len(left) != 0 {
		t.Errorf("left: %d", len(left))
	}
}

func TestRemoveDevice(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := pushUsers(t)
	ada := signedIn(t, svc, "ada@example.com")
	bob := signedIn(t, svc, "bob@example.com")
	d, err := svc.RegisterDevice(ctx, ada, DeviceInput{Platform: "android", Token: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveDevice(ctx, bob.User.ID, d.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else's: %v", err)
	}
	if err := svc.RemoveDevice(ctx, ada.User.ID, d.ID, "admin"); err != nil {
		t.Errorf("remove: %v", err)
	}
	recs, _, _ := svc.Store.ListAudit(ctx, AuditFilter{Action: AuditDeviceRemoved, Limit: 10})
	if len(recs) != 1 {
		t.Errorf("audit: %v", recs)
	}
}
