package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/push"
	"github.com/fernandezvara/backd/internal/registry"
)

// Codes of a push that can't be sent.
const (
	codeUnknownPushKind   = "unknown_push_kind"
	codeInvalidPushMsg    = "invalid_push_message"
	codePushNotDeclared   = "push_not_declared"
	codeAudienceNotDeclrd = "push_audience_not_declared"
)

// pushSendInput is the body of ctx.push.send().
type pushSendInput struct {
	User     string                    `json:"user"`
	Users    []string                  `json:"users"`
	Audience json.RawMessage           `json:"audience"`
	Kind     string                    `json:"kind"`
	Title    string                    `json:"title"`
	Body     string                    `json:"body"`
	Link     string                    `json:"link"`
	Data     map[string]any            `json:"data"`
	Badge    *int                      `json:"badge"`
	Sound    string                    `json:"sound"`
	Group    string                    `json:"group"`
	TTL      any                       `json:"ttl"`
	Priority string                    `json:"priority"`
	Silent   bool                      `json:"silent"`
	Platform map[string]map[string]any `json:"platform"`
	Wait     bool                      `json:"wait"`
	// RequiresConsent skips users who don't hold the consent: they count as no_consent.
	RequiresConsent string `json:"requires_consent"`
}

// pushWaitStep is how often a send that waits looks at its record.
const pushWaitStep = 100 * time.Millisecond

// pushSend handles POST /v1/{realm}/_push/send on the internal listener: the function behind
// ctx.push.send(). The callback token says whether the function declared `push`; what it may
// name (kinds, audiences) is what its function.yaml declares.
func (f *functions) pushSend(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	caller, _, ok := f.docs.identify(w, r, realm)
	if !ok {
		return
	}
	fc := caller.Func
	if fc == nil || fc.Admin || !fc.Push {
		writeError(w, r, http.StatusForbidden, codePushNotDeclared, "this function didn't declare `push` in its function.yaml")
		return
	}
	svc, rl := f.docs.users(realm), f.docs.reg.Realms[realm]
	if svc == nil || rl == nil || rl.Settings.Push == nil {
		writeError(w, r, http.StatusNotFound, codePushNotConfigured, "this realm doesn't send push notifications")
		return
	}
	if !contentTypeIs(r, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be application/json")
		return
	}
	body, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, tooBig.Limit)
		return
	}
	var in pushSendInput
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&in) != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be a JSON object: user, users or audience, and kind or title and body")
		return
	}
	_, fnName, _ := cutFunction(realm, fc.Name)
	database, name, _ := strings.Cut(fnName, "/")
	var declared *registry.FunctionPush
	if fn := f.lookup(realm, database, name); fn != nil {
		declared = fn.Push
	}
	if declared == nil {
		writeError(w, r, http.StatusForbidden, codePushNotDeclared, "this function didn't declare `push` in its function.yaml")
		return
	}
	invalid := func(path, reason string) {
		writeError(w, r, http.StatusBadRequest, codeInvalidPushMsg, "invalid push message", Detail{Path: path, Reason: reason})
	}
	// Who.
	n := 0
	for _, set := range []bool{in.User != "", len(in.Users) > 0, len(in.Audience) > 0} {
		if set {
			n++
		}
	}
	if n != 1 {
		invalid("user", "give exactly one of user, users and audience")
		return
	}
	if in.RequiresConsent != "" && rl.Settings.Consents[in.RequiresConsent] == nil {
		invalid("requires_consent", "is not a consent of this realm")
		return
	}
	send := auth.PushSendInput{Kind: in.Kind, Sender: "function:" + fnName, RequiresConsent: in.RequiresConsent}
	if send.Kind == "" {
		send.Kind = push.RawKind
	}
	switch {
	case in.User != "":
		send.Users = []string{in.User}
	case len(in.Users) > 0:
		send.Users = in.Users
	default:
		if !declared.Audience {
			writeError(w, r, http.StatusForbidden, codeAudienceNotDeclrd, "this function didn't declare `push: { audience: true }` in its function.yaml")
			return
		}
		aud, err := parseAudience(in.Audience)
		if err != nil {
			invalid("audience", err.Error())
			return
		}
		if aud.Consent != "" && rl.Settings.Consents[aud.Consent] == nil {
			invalid("audience.consent", "is not a consent of this realm")
			return
		}
		send.Audience = aud
	}
	// What.
	prepared, ok := preparePush(w, r, svc, rl, realm, in, declared.AllowsKind)
	if !ok {
		return
	}
	send.Messages = prepared
	started, err := svc.StartPushSend(r.Context(), send, requestID(r.Context()))
	if err != nil {
		logger(r.Context()).Error("start a push send", "function", fnName, "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	out := map[string]any{"send_id": started.SendID, "users": nil, "jobs": started.Jobs, "queued": nil, "limited": nil, "no_devices": nil, "no_consent": nil}
	if started.Users >= 0 {
		out["users"] = started.Users
	}
	if started.Known {
		out["queued"], out["limited"], out["no_devices"], out["no_consent"] = started.Queued, started.Limited, started.NoDevice, started.NoConsent
	}
	if in.Wait {
		f.waitForPush(r, svc, started.SendID, fc.Expires, out)
	}
	writeJSON(w, http.StatusAccepted, out)
}

// waitForPush adds the totals of a send to out, waiting for it to finish within the function's
// time; what is known when the time is up is returned as it stands.
func (f *functions) waitForPush(r *http.Request, svc *auth.Users, id string, expires time.Time, out map[string]any) {
	deadline := time.Now().Add(30 * time.Second)
	if !expires.IsZero() {
		deadline = expires.Add(-callbackMargin)
	}
	for {
		rec, err := svc.PushSendByID(r.Context(), id)
		if err == nil {
			out["queued"], out["limited"], out["no_devices"], out["no_consent"] = rec.Queued, rec.Limited, rec.NoDevices, rec.NoConsent
			out["sent"], out["invalid"], out["failed"] = rec.Sent, rec.Invalid, rec.Failed
			out["completed"] = !rec.CompletedAt.IsZero()
			if rec.Users >= 0 {
				out["users"] = rec.Users
			}
			if !rec.CompletedAt.IsZero() {
				return
			}
		}
		if time.Now().Add(pushWaitStep).After(deadline) || r.Context().Err() != nil {
			return
		}
		time.Sleep(pushWaitStep)
	}
}

// pushMessage turns the input into the portable message: data values must be strings.
func pushMessage(in pushSendInput) (push.Message, error) {
	m := push.Message{Title: in.Title, Body: in.Body, Link: in.Link, Badge: in.Badge, Sound: in.Sound, Group: in.Group, Priority: in.Priority, Silent: in.Silent, Platform: in.Platform}
	if in.Data != nil {
		m.Data = make(map[string]string, len(in.Data))
		for k, v := range in.Data {
			s, ok := v.(string)
			if !ok {
				return m, errors.New(k + ": data values are strings; send numbers and booleans as text")
			}
			m.Data[k] = s
		}
	}
	switch v := in.TTL.(type) {
	case nil:
	case float64:
		d := time.Duration(v * float64(time.Second))
		m.TTL = &d
	case string:
		d, err := registry.ParseDuration(v)
		if err != nil {
			return m, errors.New("ttl: " + err.Error())
		}
		m.TTL = &d
	default:
		return m, errors.New("ttl: a duration such as 1h or 7d, or seconds")
	}
	return m, nil
}

// parseAudience reads "all" or an object of the allowed fields.
func parseAudience(raw json.RawMessage) (*auth.PushAudience, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s != "all" {
			return nil, errors.New(`must be "all" or an object with roles, email_verified, locale, consent, created_before or created_after`)
		}
		return &auth.PushAudience{All: true}, nil
	}
	var d struct {
		Roles         []string `json:"roles"`
		EmailVerified *bool    `json:"email_verified"`
		Locale        string   `json:"locale"`
		CreatedBefore string   `json:"created_before"`
		CreatedAfter  string   `json:"created_after"`
		Consent       string   `json:"consent"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, errors.New("an object with roles, email_verified, locale, consent, created_before and created_after (anything else is refused)")
	}
	a := &auth.PushAudience{Roles: d.Roles, EmailVerified: d.EmailVerified, Locale: d.Locale, Consent: d.Consent}
	for field, v := range map[string]*string{"created_before": &d.CreatedBefore, "created_after": &d.CreatedAfter} {
		if *v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, *v)
		if err != nil {
			return nil, errors.New(field + ": an RFC 3339 timestamp")
		}
		if field == "created_before" {
			a.CreatedBefore = t
		} else {
			a.CreatedAfter = t
		}
	}
	if len(a.Roles) == 0 && a.EmailVerified == nil && a.Locale == "" && a.Consent == "" && a.CreatedBefore.IsZero() && a.CreatedAfter.IsZero() {
		return nil, errors.New(`name who: at least one field, or "all"`)
	}
	return a, nil
}

func secretRefs(names []string) []registry.SecretRef {
	refs := make([]registry.SecretRef, len(names))
	for i, n := range names {
		refs[i] = registry.SecretRef{Realm: true, Name: n}
	}
	return refs
}

// preparePush renders the message of a send in every language and checks it against the
// providers, and that the secrets they need are set. It answers the error itself and says
// false when the send can't go on. allowed says whether the sender may name a kind.
func preparePush(w http.ResponseWriter, r *http.Request, svc *auth.Users, rl *registry.Realm, realm string, in pushSendInput, allowed func(kind string) bool) (map[string]push.Message, bool) {
	kind := in.Kind
	if kind != "" && kind != push.RawKind && !allowed(kind) {
		writeError(w, r, http.StatusBadRequest, codeUnknownPushKind, "this function didn't declare the push kind "+kind+" in its function.yaml")
		return nil, false
	}
	invalid := func(path, reason string) {
		writeError(w, r, http.StatusBadRequest, codeInvalidPushMsg, "invalid push message", Detail{Path: path, Reason: reason})
	}
	msg, err := pushMessage(in)
	if err != nil {
		invalid("data", err.Error())
		return nil, false
	}
	var targets []push.Target
	for _, p := range registry.Platforms {
		if pp, ok := rl.Settings.Push.Platforms[p]; ok {
			targets = append(targets, push.Target{Platform: p, Provider: pp.Provider})
		}
	}
	_, locales := rl.Settings.Languages()
	prepared, err := push.Prepare(rl.Push, realm, locales, targets, kind, msg)
	var (
		vErr *push.ValidationError
		tl   *push.TooLargeError
	)
	switch {
	case errors.Is(err, push.ErrUnknownKind):
		writeError(w, r, http.StatusBadRequest, codeUnknownPushKind, err.Error())
		return nil, false
	case errors.As(err, &tl):
		writeError(w, r, http.StatusRequestEntityTooLarge, codePayloadTooLarge, err.Error(), Detail{Path: "message", Reason: err.Error()})
		return nil, false
	case errors.As(err, &vErr):
		invalid(vErr.Field, vErr.Reason)
		return nil, false
	case err != nil:
		logger(r.Context()).Error("prepare a push", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
		return nil, false
	}
	// The providers' secrets must be set: a send that no provider could deliver is refused now.
	if refs := secretRefs(rl.Settings.Push.SecretNames()); len(refs) > 0 {
		_, missing, err := svc.ResolveSecrets(r.Context(), refs, "")
		if err != nil {
			logger(r.Context()).Error("read the push secrets", "error", err)
			writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
			return nil, false
		}
		if len(missing) > 0 {
			writeError(w, r, http.StatusServiceUnavailable, codePushUnavailable, auth.ErrPushUnavailable.Error())
			return nil, false
		}
	}
	return prepared, true
}
