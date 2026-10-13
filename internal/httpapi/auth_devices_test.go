package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

func pushFixture(t *testing.T) *rulesFixture {
	t.Helper()
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Settings.Push = &registry.PushSettings{
		Platforms:         map[string]registry.PushPlatform{"android": {Provider: "fcm"}, "web": {Provider: "webpush"}},
		WebEndpointHosts:  registry.DefaultWebEndpointHosts,
		WebPublicKey:      "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U",
		MaxDevicesPerUser: 2,
		StaleAfter:        60 * 24 * time.Hour,
	}
	return f
}

const devices = "/v1/acme/_auth/devices"

func TestDevicesOverHTTP(t *testing.T) {
	f := pushFixture(t)
	// Registering, listing and removing: the token never comes back.
	rec, out := f.doH(t, "POST", devices, `{"platform": "android", "token": "fcm-token-1", "name": "Ana's Pixel"}`, jsonHdr(f.ada))
	if rec.Code != 200 || out["platform"] != "android" || out["name"] != "Ana's Pixel" || out["id"] == nil {
		t.Fatalf("register: %d %v", rec.Code, out)
	}
	if strings.Contains(rec.Body.String(), "fcm-token-1") {
		t.Errorf("the token came back: %s", rec.Body)
	}
	id := out["id"].(string)
	// Registering it again refreshes the same device.
	if rec, out := f.doH(t, "POST", devices, `{"platform": "android", "token": "fcm-token-1"}`, jsonHdr(f.ada)); rec.Code != 200 || out["id"] != id || out["name"] != "Ana's Pixel" {
		t.Errorf("refresh: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "GET", devices, "", bearer(f.ada))
	items, _ := out["items"].([]any)
	if rec.Code != 200 || len(items) != 1 || strings.Contains(rec.Body.String(), "fcm-token-1") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	// Another user's device is a 404; so is one that doesn't exist.
	if rec, _ := f.doH(t, "DELETE", devices+"/"+id, "", bearer(f.bob)); rec.Code != http.StatusNotFound {
		t.Errorf("someone else's device: %d", rec.Code)
	}
	// Without a session, or with an API key, there are no devices.
	if rec, _ := f.doH(t, "POST", devices, `{"platform": "android", "token": "x"}`, jsonHdr("")); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", devices, `{"platform": "android", "token": "x"}`, jsonHdr(f.key)); rec.Code != http.StatusUnauthorized {
		t.Errorf("api key: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", devices+"/"+id, "", bearer(f.ada)); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	// Validation.
	for name, c := range map[string]struct{ body, code string }{
		"platform off":   {`{"platform": "ios", "token": "x"}`, "platform_not_enabled"},
		"no token":       {`{"platform": "android", "token": ""}`, "invalid_device"},
		"big token":      {`{"platform": "android", "token": "` + strings.Repeat("a", 4097) + `"}`, "invalid_device"},
		"web not json":   {`{"platform": "web", "token": "abc"}`, "invalid_device"},
		"web http":       {`{"platform": "web", "token": "{\"endpoint\":\"http://fcm.googleapis.com/x\",\"keys\":{\"p256dh\":\"AAAA\",\"auth\":\"AAAA\"}}"}`, "invalid_device"},
		"web other host": {`{"platform": "web", "token": "{\"endpoint\":\"https://internal.example/x\",\"keys\":{\"p256dh\":\"AAAA\",\"auth\":\"AAAA\"}}"}`, "invalid_device"},
		"web private ip": {`{"platform": "web", "token": "{\"endpoint\":\"https://10.0.0.5/x\",\"keys\":{\"p256dh\":\"AAAA\",\"auth\":\"AAAA\"}}"}`, "invalid_device"},
		"web no keys":    {`{"platform": "web", "token": "{\"endpoint\":\"https://fcm.googleapis.com/x\"}"}`, "invalid_device"},
		"unknown field":  {`{"platform": "android", "token": "x", "locale": "es"}`, "validation_error"},
	} {
		rec, out := f.doH(t, "POST", devices, c.body, jsonHdr(f.ada))
		if rec.Code != 400 || errCode(out) != c.code {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	// A web subscription on an allowed host, including a wildcard.
	sub := func(host, keyP string) string {
		return `{"platform": "web", "token": "{\"endpoint\":\"https://` + host + `/send/abc\",\"keys\":{\"p256dh\":\"` + keyP + `\",\"auth\":\"BBBB\"}}"}`
	}
	rec, out = f.doH(t, "POST", devices, sub("wns2-par02p.notify.windows.com", "AAAA"), jsonHdr(f.ada))
	if rec.Code != 200 {
		t.Fatalf("web: %d %v", rec.Code, out)
	}
	webID := out["id"]
	// A browser that subscribes again gets new keys under the same endpoint: the same device.
	if rec, out := f.doH(t, "POST", devices, sub("wns2-par02p.notify.windows.com", "CCCC"), jsonHdr(f.ada)); rec.Code != 200 || out["id"] != webID {
		t.Errorf("re-subscribe: %d %v", rec.Code, out)
	}
	// The key browsers subscribe with is public.
	rec, out = f.doH(t, "GET", devices+"/web-key", "", nil)
	if rec.Code != 200 || out["public_key"] == nil || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Errorf("web-key: %d %v %v", rec.Code, out, rec.Header())
	}
}

func TestDeviceCapMovesAndSessionEnds(t *testing.T) {
	f := pushFixture(t)
	ctx := context.Background()
	reg := func(token, tok string) map[string]any {
		t.Helper()
		rec, out := f.doH(t, "POST", devices, `{"platform": "android", "token": "`+tok+`"}`, jsonHdr(token))
		if rec.Code != 200 {
			t.Fatalf("register %s: %d %v", tok, rec.Code, out)
		}
		return out
	}
	ids := map[string]string{}
	for _, tok := range []string{"t1", "t2", "t3"} {
		ids[tok] = reg(f.ada, tok)["id"].(string)
		time.Sleep(2 * time.Millisecond)
	}
	// The cap is 2: the least recently seen goes.
	_, out := f.doH(t, "GET", devices, "", bearer(f.ada))
	if items, _ := out["items"].([]any); len(items) != 2 {
		t.Fatalf("cap: %v", out)
	}
	if rec, _ := f.doH(t, "DELETE", devices+"/"+ids["t1"], "", bearer(f.ada)); rec.Code != http.StatusNotFound {
		t.Errorf("the oldest should be gone: %d", rec.Code)
	}
	// A token registered by another user moves to them, with a new id; the first owner loses it.
	moved := reg(f.bob, "t3")
	if moved["id"] == ids["t3"] {
		t.Error("the id of the first owner reached the second")
	}
	_, out = f.doH(t, "GET", devices, "", bearer(f.ada))
	if items, _ := out["items"].([]any); len(items) != 1 {
		t.Errorf("ada after the move: %v", out)
	}
	// Signing out removes the devices the session registered.
	rec, _ := f.doH(t, "POST", "/v1/acme/_auth/logout", "", bearer(f.bob))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	bobDevices, _ := f.svc.Store.ListDevices(ctx, f.bobID)
	if len(bobDevices) != 0 {
		t.Errorf("devices after logout: %d", len(bobDevices))
	}
	// Ada's session is revoked by all: all of her devices go too.
	if rec, _ := f.doH(t, "POST", "/v1/acme/_auth/logout-all", "", bearer(f.ada)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout-all: %d", rec.Code)
	}
	if left, _ := f.svc.Store.ListDevices(ctx, f.adaID); len(left) != 0 {
		t.Errorf("devices after logout-all: %d", len(left))
	}
}

func TestDevicesNeedPush(t *testing.T) {
	f := newRulesFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", devices, `{"platform": "android", "token": "x"}`},
		{"GET", devices, ""},
		{"GET", devices + "/web-key", ""},
		{"DELETE", devices + "/nope", ""},
	} {
		rec, out := f.doH(t, c.method, c.path, c.body, jsonHdr(f.ada))
		if rec.Code != 404 || errCode(out) != "push_not_configured" {
			t.Errorf("%s %s: %d %v", c.method, c.path, rec.Code, out)
		}
	}
}

func TestAdminDevices(t *testing.T) {
	f := pushFixture(t)
	f.doH(t, "POST", devices, `{"platform": "android", "token": "t1", "name": "Pixel"}`, jsonHdr(f.ada))
	const admin = "/v1/acme/_admin"
	rec, out := f.doH(t, "GET", admin+"/users/"+f.adaID+"/devices", "", jsonHdr(f.key))
	items, _ := out["items"].([]any)
	if rec.Code != 200 || len(items) != 1 || strings.Contains(rec.Body.String(), `"t1"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	id := items[0].(map[string]any)["id"].(string)
	// Someone without the users area can't.
	if rec, _ := f.doH(t, "GET", admin+"/users/"+f.adaID+"/devices", "", jsonHdr(f.carl)); rec.Code != http.StatusForbidden {
		t.Errorf("a plain user: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/users/"+f.adaID+"/devices/"+id, "", jsonHdr(f.key)); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", admin+"/users/"+f.adaID+"/devices/"+id, "", jsonHdr(f.key)); rec.Code != http.StatusNotFound {
		t.Errorf("again: %d", rec.Code)
	}
	if got := f.auditActions(t, "device."); len(got) != 1 {
		t.Errorf("audit: %v", got)
	}
}
