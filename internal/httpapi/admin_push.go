package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/push"
)

// pushSendJSON describes a send: its totals, never a message, a user or a token.
func pushSendJSON(p auth.PushSend) map[string]any {
	var users, completed any
	if p.Users >= 0 {
		users = p.Users
	}
	if !p.CompletedAt.IsZero() {
		completed = formatTime(p.CompletedAt)
	}
	return map[string]any{
		"id": p.ID, "kind": p.Kind, "sender": p.Sender, "created_at": formatTime(p.CreatedAt),
		"users": users, "planned": p.Planned, "batches": p.Batches, "batches_done": p.BatchesDone,
		"units": p.Units, "units_done": p.UnitsDone,
		"queued": p.Queued, "limited": p.Limited, "no_devices": p.NoDevices,
		"sent": p.Sent, "invalid": p.Invalid, "failed": p.Failed,
		"completed": !p.CompletedAt.IsZero(), "completed_at": completed,
	}
}

// listPushSends answers GET /push/sends: the realm's sends, newest first.
func (a *adminAPI) listPushSends(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auth.PushSendFilter{Kind: q.Get("kind"), Limit: defaultLimit}
	if fn := q.Get("function"); fn != "" {
		f.Sender = "function:" + fn
	}
	var details []Detail
	for k := range q {
		if !slices.Contains([]string{"kind", "function", "since", "until", "limit", "skip"}, k) {
			details = append(details, Detail{Path: k, Reason: "unknown query parameter"})
		}
	}
	for _, t := range []struct {
		name string
		dst  *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if s := q.Get(t.name); s != "" {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				details = append(details, Detail{Path: t.name, Reason: "must be an RFC 3339 date-time"})
			}
			*t.dst = v
		}
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			details = append(details, Detail{Path: "limit", Reason: "must be an integer between 1 and " + strconv.Itoa(maxLimit)})
		}
		f.Limit = n
	}
	if s := q.Get("skip"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			details = append(details, Detail{Path: "skip", Reason: "must be a non-negative integer"})
		}
		f.Skip = n
	}
	if len(details) > 0 {
		sort.Slice(details, func(i, j int) bool { return details[i].Path < details[j].Path })
		writeError(w, r, http.StatusBadRequest, codeInvalidQuery, "invalid query parameters", details...)
		return
	}
	list, more, err := usersOf(r).PushSends(r.Context(), f)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, p := range list {
		items[i] = pushSendJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": f.Limit, "skip": f.Skip, "has_more": more})
}

// getPushSend answers GET /push/sends/{id}.
func (a *adminAPI) getPushSend(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if svc.Settings.Push == nil {
		adminError(w, r, auth.ErrPushNotConfigured)
		return
	}
	rec, err := svc.PushSendByID(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, auth.ErrPushSendNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "send not found")
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pushSendJSON(rec))
}

// pushTestInput is the body of POST /push/test: one user, and a message.
type pushTestInput struct {
	User     string                    `json:"user"`
	Email    string                    `json:"email"`
	Kind     string                    `json:"kind"`
	Title    string                    `json:"title"`
	Body     string                    `json:"body"`
	Link     string                    `json:"link"`
	Data     map[string]any            `json:"data"`
	Silent   bool                      `json:"silent"`
	Platform map[string]map[string]any `json:"platform"`
}

// testPush answers POST /push/test: a push to one user, by an administrator. It goes through
// the same limits as any push (and counts toward the user's), and is audited.
func (a *adminAPI) testPush(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	if svc == nil || rl == nil || rl.Settings.Push == nil {
		writeError(w, r, http.StatusNotFound, codePushNotConfigured, auth.ErrPushNotConfigured.Error())
		return
	}
	body, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	var in pushTestInput
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&in) != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be a JSON object: user or email, and kind or title and body")
		return
	}
	if (in.User == "") == (in.Email == "") {
		writeError(w, r, http.StatusBadRequest, codeInvalidPushMsg, "invalid push message", Detail{Path: "user", Reason: "give exactly one of user (an id) and email"})
		return
	}
	var u auth.User
	if in.Email != "" {
		u, err = svc.Find(r.Context(), in.Email)
	} else {
		u, err = svc.UserByID(r.Context(), in.User)
	}
	if err != nil || u.Disabled || !u.ErasedAt.IsZero() {
		writeError(w, r, http.StatusNotFound, codeNotFound, "user not found")
		return
	}
	prepared, ok := preparePush(w, r, svc, rl, chi.URLParam(r, "realm"), pushSendInput{
		Kind: in.Kind, Title: in.Title, Body: in.Body, Link: in.Link, Data: in.Data, Silent: in.Silent, Platform: in.Platform,
	}, func(string) bool { return true })
	if !ok {
		return
	}
	caller, _ := callerOf(r)
	kind := in.Kind
	if kind == "" {
		kind = push.RawKind
	}
	started, err := svc.StartPushSend(r.Context(), auth.PushSendInput{Kind: kind, Sender: "admin:" + caller.Actor(), Messages: prepared, Users: []string{u.ID}}, requestID(r.Context()))
	if err != nil {
		adminError(w, r, err)
		return
	}
	svc.Audit(r.Context(), auth.AuditPushTest, "user:"+u.ID, map[string]any{"send": started.SendID, "kind": kind})
	out := map[string]any{"send_id": started.SendID, "users": 1, "queued": started.Queued, "limited": started.Limited, "no_devices": started.NoDevice}
	writeJSON(w, http.StatusAccepted, out)
}
