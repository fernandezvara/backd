package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
)

// expirePassword handles POST /users/{id}/expire-password: the user's password stops
// opening the account until they reset it or an administrator sets a new one. Their
// sessions are left alone; the optional reason goes to the audit trail.
func (a *adminAPI) expirePassword(w http.ResponseWriter, r *http.Request) {
	obj, ok := optionalObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"reason": "string?"})
	if !ok {
		return
	}
	reason, _ := f["reason"].(string)
	if len(reason) > 500 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "reason", Reason: "is longer than 500 characters"})
		return
	}
	if err := usersOf(r).ExpirePassword(r.Context(), adminUserOf(r).Email, reason); err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// needFullWithReauth refuses an administrator who doesn't hold every admin right, and a
// session without a fresh re-authentication: the actions of a breach reach every account.
func needFullWithReauth(w http.ResponseWriter, r *http.Request, what string) bool {
	if have := adminAccessOf(r); !have.Full() {
		rightRefused(w, r, what+" needs full admin access; you hold: "+have.String())
		return false
	}
	if caller, _ := callerOf(r); caller.Key == nil && caller.User != nil {
		if err := usersOf(r).CheckReauth(r.Context(), *caller.User, r.Header.Get(reauthHeader)); err != nil {
			authError(w, r, err)
			return false
		}
	}
	return true
}

// expireAllPasswords handles POST /passwords/expire: every password set before now stops
// opening its account, and, unless keep_sessions, every session ends. The answer says how
// many accounts and sessions it touched.
func (a *adminAPI) expireAllPasswords(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "expiring every password") {
		return
	}
	obj, ok := optionalObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"reason": "string?", "keep_sessions": "bool?", "notify": "bool?"})
	if !ok {
		return
	}
	reason, _ := f["reason"].(string)
	if len(reason) > 500 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "reason", Reason: "is longer than 500 characters"})
		return
	}
	keep, _ := f["keep_sessions"].(bool)
	notify, _ := f["notify"].(bool)
	svc := usersOf(r)
	if notify && svc.Settings.Email == nil {
		// Refused before anything changes: telling users needs email, and the choice is the operator's.
		writeError(w, r, http.StatusConflict, codeConflict, "the realm has no email section: nothing can be sent. Expire without notify and give each user a reset link (POST /users/{id}/reset-link)")
		return
	}
	res, err := svc.ExpireAllPasswords(r.Context(), reason, keep)
	switch {
	case errors.Is(err, auth.ErrEpochNotLater):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error()+": try again in a moment")
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	out := map[string]any{"valid_after": formatTime(res.ValidAfter), "accounts": res.Accounts, "sessions": res.Sessions}
	if notify {
		job, err := svc.StartPasswordNotice(r.Context(), actorOf(r), requestID(r.Context()))
		if err != nil {
			// The passwords are expired already; say so, and how to start the notification again.
			writeError(w, r, http.StatusConflict, codeConflict, "the passwords were expired, but the notification did not start ("+err.Error()+"): run it again with POST /passwords/notify")
			return
		}
		out["notify_job"] = job.ID
	}
	writeJSON(w, http.StatusOK, out)
}

// notifyExpiredPasswords handles POST /passwords/notify: it starts, or after a cancel or a
// failure starts again, the job that emails every user whose password the realm's epoch
// expired. Users already told are skipped.
func (a *adminAPI) notifyExpiredPasswords(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "telling every user") {
		return
	}
	job, err := usersOf(r).StartPasswordNotice(r.Context(), actorOf(r), requestID(r.Context()))
	var running *auth.ErrPasswordNoticeRunning
	switch {
	case errors.Is(err, auth.ErrEmailNotConfigured):
		writeError(w, r, http.StatusConflict, codeConflict, "the realm has no email section: nothing can be sent. Give each user a reset link instead (POST /users/{id}/reset-link)")
	case errors.Is(err, auth.ErrNoPasswordEpoch):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
	case errors.As(err, &running):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
	case err != nil:
		adminError(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "status": job.Status})
	}
}

// revokeAllSessions handles DELETE /sessions: every session of the realm ends; passwords
// are untouched.
func (a *adminAPI) revokeAllSessions(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "ending every session") {
		return
	}
	n, err := usersOf(r).RevokeAllSessions(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": n})
}

// actorOf names the administrator for a job's caller.
func actorOf(r *http.Request) string {
	caller, _ := callerOf(r)
	return caller.Actor()
}

// resetLink handles POST /users/{id}/reset-link: a one-time link to choose a new password, for
// a user the email can't reach. It is the link a reset email carries, and using it works the
// same; nothing is sent. The link is in this answer only, so it needs full admin access and a
// fresh proof of identity, and the audit record never holds it.
func (a *adminAPI) resetLink(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "making a reset link") {
		return
	}
	obj, ok := optionalObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"redirect_to": "string?"})
	if !ok {
		return
	}
	redirectTo, _ := f["redirect_to"].(string)
	realm := chi.URLParam(r, "realm")
	svc := usersOf(r)
	rl := a.reg.Realms[realm]
	if rl == nil || rl.Pages == nil || svc.Settings.Email == nil {
		writeError(w, r, http.StatusConflict, codeConflict, "the reset page is served with the realm's email section: without it, set a password with POST /users/{id}/password and tell the user")
		return
	}
	es := svc.Settings.Email
	if redirectTo != "" && !es.AllowedRedirect(redirectTo) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to must be an absolute URL within the realm's email.allowed_redirects", Detail{Path: "redirect_to", Reason: "is not allowed"})
		return
	}
	purpose := email.TokenPurpose(email.ResetPassword)
	if _, ok := tokenLink(es, a.baseURL, realm, purpose, ""); !ok {
		writeError(w, r, http.StatusConflict, codeConflict, "backd's public address is unknown (BACKD_URL or email.public_url)")
		return
	}
	token, expires, err := svc.CreateResetLink(r.Context(), adminUserOf(r).ID, redirectTo)
	if errors.Is(err, auth.ErrResetLinkUser) {
		writeError(w, r, http.StatusConflict, codeConflict, err.Error())
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	link, _ := tokenLink(es, a.baseURL, realm, purpose, token)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"link": link, "expires_at": formatTime(expires.UTC())})
}

// passwordState handles GET /passwords: the realm's password epoch (null before any expiry) and
// how many accounts have a password, to confirm an expiry with.
func (a *adminAPI) passwordState(w http.ResponseWriter, r *http.Request) {
	st, err := usersOf(r).PasswordState(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	var epoch any
	if !st.Epoch.ValidAfter.IsZero() {
		e := map[string]any{"valid_after": formatTime(st.Epoch.ValidAfter), "set_by": st.Epoch.SetBy, "set_at": formatTime(st.Epoch.SetAt), "reason": nil}
		if st.Epoch.Reason != "" {
			e["reason"] = st.Epoch.Reason
		}
		epoch = e
	}
	writeJSON(w, http.StatusOK, map[string]any{"epoch": epoch, "accounts": st.Accounts})
}
