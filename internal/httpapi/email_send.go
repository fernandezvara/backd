package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// emailSendInput is the body of ctx.email.send().
type emailSendInput struct {
	Kind   string         `json:"kind"`
	ToUser string         `json:"to_user"`
	To     []string       `json:"to"`
	CC     []string       `json:"cc"`
	BCC    []string       `json:"bcc"`
	Data   map[string]any `json:"data"`
	Locale string         `json:"locale"`
}

// emailSend handles POST /v1/{realm}/_email/send on the internal listener:
// the function behind ctx.email.send(). The function's callback token says
// whether it declared `email: true`. Any recipient is allowed (a realm user,
// or addresses); the message text only comes from the realm's templates for
// the kind, and the caps of email.limits bound what a function can do.
func (f *functions) emailSend(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	caller, _, ok := f.docs.identify(w, r, realm)
	if !ok {
		return
	}
	fc := caller.Func
	if fc == nil || fc.Admin || !fc.Email {
		writeError(w, r, http.StatusForbidden, codeEmailNotDeclared, "this function didn't declare `email: true` in its function.yaml")
		return
	}
	svc, rl := f.docs.users(realm), f.docs.reg.Realms[realm]
	if svc == nil || rl == nil || rl.Settings.Email == nil || rl.Email == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm doesn't send email")
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
	var in emailSendInput
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&in) != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be a JSON object with kind, data and a recipient")
		return
	}
	es := rl.Settings.Email
	invalid := func(path, reason string) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid email", Detail{Path: path, Reason: reason})
	}
	switch {
	case in.Kind == "":
		invalid("kind", "is required")
		return
	case slices.Contains(email.SystemKinds, in.Kind):
		invalid("kind", "is a kind backd sends itself; a function sends the realm's own kinds")
		return
	case !rl.Email.Has(in.Kind, es.DefaultLocale):
		invalid("kind", "has no template in the realm's email folder (email/"+in.Kind+"/)")
		return
	case (in.ToUser == "") == (len(in.To) == 0):
		invalid("to", "give exactly one of to_user (a user's id) and to (addresses)")
		return
	}
	count := len(in.To) + len(in.CC) + len(in.BCC)
	if in.ToUser != "" {
		count++
	}
	if count > es.Limits.RecipientsPerMessage {
		invalid("to", "at most "+strconv.Itoa(es.Limits.RecipientsPerMessage)+" recipients per message (email.limits.recipients_per_message)")
		return
	}
	for path, list := range map[string][]string{"to": in.To, "cc": in.CC, "bcc": in.BCC} {
		for _, a := range list {
			if _, err := registry.NormalizeEmail(a); err != nil {
				invalid(path, "contains an invalid email address")
				return
			}
		}
	}

	_, fn, _ := cutFunction(realm, fc.Name)
	req := auth.EmailRequest{
		Kind: in.Kind, RequestID: requestID(r.Context()), Invocation: fc.Invocation, Depth: fc.Depth + 1,
		Custom: &auth.CustomEmail{Function: fn, To: in.To, CC: in.CC, BCC: in.BCC, Data: in.Data},
	}
	if in.ToUser != "" {
		u, err := svc.UserByID(r.Context(), in.ToUser)
		if err != nil || u.Disabled {
			writeError(w, r, http.StatusNotFound, codeNotFound, "user not found")
			return
		}
		req.UserID, req.Address, req.Locale = u.ID, u.Email, svc.LocaleOf(u)
	} else {
		req.Locale = rl.Settings.BestLocale(in.Locale)
	}
	job, err := svc.QueueEmail(r.Context(), req)
	var limited *auth.EmailLimitedError
	switch {
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(max(int((limited.RetryAfter+time.Second-1)/time.Second), 1)))
		writeError(w, r, http.StatusTooManyRequests, codeEmailLimited, limited.Error())
	case err != nil:
		logger(r.Context()).Error("queue a function's email", "function", fn, "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "status": auth.JobQueued})
	}
}

// cutFunction splits "<realm>/<database>/<name>" into the realm and
// "<database>/<name>".
func cutFunction(realm, full string) (string, string, bool) {
	rest, ok := strings.CutPrefix(full, realm+"/")
	return realm, rest, ok
}
