package httpapi

import (
	"context"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/push"
	"github.com/fernandezvara/backd/internal/registry"
)

// captureRetention is how long a captured delivery is kept.
const captureRetention = 24 * time.Hour

// captureSender is the `capture` provider (development only): it stores each delivery in the
// realm's push_outbox instead of calling a provider, and answers by the device's token so every
// outcome can be exercised without a device or a provider key:
//
//	test-invalid-…       invalid (the device is removed)
//	test-fail-…          failed
//	test-retry-…         retry on the first attempt, sent after
//	test-retry-always-…  retry every time, until the unit runs out of attempts
//	anything else        sent
type captureSender struct {
	users func(realm string) *auth.Users
	now   func() time.Time
}

func (c *captureSender) Send(ctx context.Context, req push.Request) ([]push.DeviceResult, error) {
	svc := c.users(req.Realm)
	if svc == nil {
		return nil, auth.ErrNotFound
	}
	now := c.now()
	out := make([]push.DeviceResult, 0, len(req.Deliveries))
	for _, d := range req.Deliveries {
		status := captureStatus(d.Token, req.Attempt)
		err := svc.Store.PutPushOutbox(ctx, auth.PushOutboxEntry{
			ID: "po_" + xid.New().String(), SendID: req.SendID, UserID: d.UserID, DeviceID: d.DeviceID, Platform: req.Platform,
			Status: status, Attempt: req.Attempt, Message: d.Message, CreatedAt: now, ExpiresAt: now.Add(captureRetention),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, push.DeviceResult{DeviceID: d.DeviceID, Status: status})
	}
	return out, nil
}

// captureStatus is what the capture provider answers for a token on an attempt.
func captureStatus(token string, attempt int) string {
	switch {
	case strings.HasPrefix(token, "test-invalid-"):
		return push.StatusInvalid
	case strings.HasPrefix(token, "test-fail-"):
		return push.StatusFailed
	case strings.HasPrefix(token, "test-retry-always-"):
		return push.StatusRetry
	case strings.HasPrefix(token, "test-retry-") && attempt <= 1:
		return push.StatusRetry
	}
	return push.StatusSent
}

// devPush handles GET /v1/{realm}/_dev/push: what the capture provider took, newest first, for
// the Mailbox. There is no authentication, like the email outbox it sits next to, so it exists
// only in a realm that has a capture provider (which startup refuses outside development).
func (a *authAPI) devPush(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	capture := false
	if p := svc.Settings.Push; p != nil {
		for _, name := range registry.Platforms {
			capture = capture || p.Platforms[name].Provider == registry.PushProviderCapture
		}
	}
	if !capture {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	list, err := svc.Store.ListPushOutbox(r.Context(), limit)
	if err != nil {
		authError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, e := range list {
		m := e.Message
		var badge any
		if m.Badge != nil {
			badge = *m.Badge
		}
		items[i] = map[string]any{
			"id": e.ID, "send_id": e.SendID, "user_id": e.UserID, "device_id": e.DeviceID, "platform": e.Platform,
			"status": e.Status, "attempt": e.Attempt, "created_at": formatTime(e.CreatedAt),
			"message": map[string]any{
				"kind": m.Kind, "title": m.Title, "body": m.Body, "link": m.Link, "data": m.Data, "badge": badge,
				"sound": m.Sound, "group": m.Group, "priority": m.Priority, "silent": m.Silent,
			},
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "realm": chi.URLParam(r, "realm")})
}

// pushSendersOf are the senders a worker has: the ones the config gives, and the capture
// provider, which needs nothing from outside.
func pushSendersOf(cfg Config, users func(string) *auth.Users, now func() time.Time) map[string]push.Sender {
	out := map[string]push.Sender{push.ProviderCapture: &captureSender{users: users, now: now}}
	maps.Copy(out, cfg.PushSenders)
	return out
}
