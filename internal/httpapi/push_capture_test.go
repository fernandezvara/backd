package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/push"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestCaptureStatusByToken(t *testing.T) {
	for _, c := range []struct {
		token   string
		attempt int
		want    string
	}{
		{"fcm-token", 1, push.StatusSent},
		{"test-invalid-1", 1, push.StatusInvalid},
		{"test-fail-1", 1, push.StatusFailed},
		{"test-retry-1", 1, push.StatusRetry},
		{"test-retry-1", 2, push.StatusSent},
		{"test-retry-always-1", 1, push.StatusRetry},
		{"test-retry-always-1", 5, push.StatusRetry},
	} {
		if got := captureStatus(c.token, c.attempt); got != c.want {
			t.Errorf("%s attempt %d: %s, want %s", c.token, c.attempt, got, c.want)
		}
	}
}

// With provider: capture the worker needs no sender of its own: deliveries land in the
// push_outbox the Mailbox reads, with the outcome the token asked for.
func TestCaptureProviderEndToEnd(t *testing.T) {
	f := newPushSendFixture(t)
	f.worker.pushSenders = pushSendersOf(Config{}, func(string) *auth.Users { return f.svc }, func() time.Time { return time.Now() })
	for _, p := range []string{"android", "web"} {
		f.svc.Settings.Push.Platforms[p] = registry.PushPlatform{Provider: "capture"}
	}
	f.svc.Settings.Push.MaxDevicesPerUser = 10
	f.register(t, f.ada, "plain-token")
	f.register(t, f.ada, "test-invalid-1")
	f.register(t, f.ada, "test-retry-always-1")
	f.register(t, f.bob, "test-retry-1")
	code, out := f.send(t, `{"users": ["`+f.adaID+`", "`+f.bobID+`"], "title": "Hello", "body": "World", "data": {"k": "v"}}`)
	if code != 202 || out["queued"] != float64(2) {
		t.Fatalf("send: %d %v", code, out)
	}
	f.drain(t)
	// The first attempt: one sent, one dead, two waiting for another try.
	rec := f.record(t, out["send_id"].(string))
	if rec.Sent != 1 || rec.Invalid != 1 || !rec.CompletedAt.IsZero() {
		t.Fatalf("first attempt: %+v", rec)
	}
	// Past the backoff, twice: the retry device is sent, the other runs out of attempts.
	for i := 0; i < 3; i++ {
		offset := time.Duration(i+1) * time.Minute
		f.svc.Now = func() time.Time { return time.Now().Add(offset) }
		f.drain(t)
	}
	rec = f.record(t, out["send_id"].(string))
	if rec.Sent != 2 || rec.Invalid != 1 || rec.Failed != 1 || rec.CompletedAt.IsZero() {
		t.Fatalf("after the retries: %+v", rec)
	}
	// The Mailbox reads them: newest first, with the message.
	r, body := f.doH(t, "GET", "/v1/acme/_dev/push", "", nil)
	items, _ := body["items"].([]any)
	if r.Code != http.StatusOK || len(items) < 5 {
		t.Fatalf("outbox: %d %d %v", r.Code, len(items), body)
	}
	first := items[0].(map[string]any)
	msg := first["message"].(map[string]any)
	if msg["title"] != "Hello" || msg["data"].(map[string]any)["k"] != "v" || first["send_id"] != out["send_id"] {
		t.Errorf("entry: %v", first)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.(map[string]any)["status"].(string)] = true
	}
	for _, s := range []string{"sent", "invalid", "retry"} {
		if !seen[s] {
			t.Errorf("no %s in the outbox", s)
		}
	}
	// A realm without a capture provider has no such door.
	f.svc.Settings.Push.Platforms = map[string]registry.PushPlatform{"android": {Provider: "fcm"}}
	if r, _ := f.doH(t, "GET", "/v1/acme/_dev/push", "", nil); r.Code != http.StatusNotFound || strings.Contains(r.Body.String(), "Hello") {
		t.Errorf("without capture: %d", r.Code)
	}
}
