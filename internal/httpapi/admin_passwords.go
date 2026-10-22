package httpapi

import (
	"errors"
	"net/http"

	"github.com/fernandezvara/backd/internal/auth"
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
	f, ok := fields(w, r, obj, map[string]string{"reason": "string?", "keep_sessions": "bool?"})
	if !ok {
		return
	}
	reason, _ := f["reason"].(string)
	if len(reason) > 500 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "reason", Reason: "is longer than 500 characters"})
		return
	}
	keep, _ := f["keep_sessions"].(bool)
	res, err := usersOf(r).ExpireAllPasswords(r.Context(), reason, keep)
	switch {
	case errors.Is(err, auth.ErrEpochNotLater):
		writeError(w, r, http.StatusConflict, codeConflict, err.Error()+": try again in a moment")
	case err != nil:
		adminError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"valid_after": formatTime(res.ValidAfter), "accounts": res.Accounts, "sessions": res.Sessions})
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
