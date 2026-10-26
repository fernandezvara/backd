package httpapi

import (
	"net/http"

	"github.com/fernandezvara/backd/internal/auth"
)

// reauthHeader carries the token POST /_auth/reauth gave, on the requests of sensitive
// actions.
const reauthHeader = "X-Backd-Reauth"

// reauthed answers 401 reauth_required (and returns false) unless the request of the
// signed-in session carries a re-authentication token that is good for it.
func (a *authAPI) reauthed(w http.ResponseWriter, r *http.Request) bool {
	return a.reauthedAs(w, r, principalOf(r))
}

// accountReauthed is reauthed for the actions that change what protects the person's account
// (credentials, second factors, sign-in methods, erasing or exporting it). A session an
// organization's connection opened can't take them: its provider vouches for who the person is
// there, not that the account is theirs (design/scoped-sessions.md §7). It answers 403 sso_session
// before asking for any proof.
func (a *authAPI) accountReauthed(w http.ResponseWriter, r *http.Request) bool {
	return a.accountReauthedAs(w, r, principalOf(r))
}

func (a *authAPI) accountReauthedAs(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if !a.notViaConnection(w, r, p) {
		return false
	}
	return a.reauthedAs(w, r, p)
}

// notViaConnection answers 403 sso_session (and returns false) for a session an organization's
// connection opened.
func (a *authAPI) notViaConnection(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if _, _, via := p.Session.Connection(); via {
		authError(w, r, auth.ErrSSOSession)
		return false
	}
	return true
}

func (a *authAPI) reauthedAs(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if err := usersOf(r).CheckReauth(r.Context(), p, r.Header.Get(reauthHeader)); err != nil {
		authError(w, r, err)
		return false
	}
	return true
}

// reauth handles POST /_auth/reauth: one fresh proof of the signed-in user, which gives the
// token for the header of sensitive requests.
func (a *authAPI) reauth(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"method": "string", "code": "string?", "password": "string?"})
	if !ok {
		return
	}
	method, _ := f["method"].(string)
	var proof string
	switch method {
	case auth.ReauthPassword:
		pw, ok := f["password"].(string)
		if !ok {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "password", Reason: "is required for the password method"})
			return
		}
		proof = pw
	case auth.ReauthTOTP, auth.ReauthRecovery, auth.ReauthEmailCode:
		c, ok := f["code"].(string)
		if !ok {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "code", Reason: "is required for this method"})
			return
		}
		proof = c
	default:
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "method", Reason: "must be totp, recovery_code, password or email_code"})
		return
	}
	g, err := usersOf(r).Reauthenticate(r.Context(), principalOf(r), method, proof, clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reauth_token": g.Token, "expires_at": formatTime(g.Expires)})
}

// reauthEmailCode handles POST /_auth/reauth/email-code: a code is emailed to a user who has
// no password and no second factor.
func (a *authAPI) reauthEmailCode(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RequestReauthCode(r.Context(), principalOf(r), clientIP(r), requestID(r.Context()))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}
