package httpapi

import (
	"net/http"
	"strings"

	"github.com/fernandezvara/backd/internal/auth"
)

// mfaVerify handles POST /_auth/mfa/verify: the second step of a sign-in. The challenge a
// first factor answered with and a code of the user's second factor give the session the
// first factor was waiting for, answered like the first factor would have.
func (a *authAPI) mfaVerify(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string", "method": "string", "code": "string", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	method := strings.TrimSpace(f["method"].(string))
	v, err := svc.VerifyMFA(r.Context(), f["challenge"].(string), method, f["code"].(string), clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	// The answer is the first factor's own: a provider's says whether it made the user.
	a.answerSignIn(w, r, v.Redeemed, http.StatusOK, useCookie, v.Factor != auth.ProviderPassword)
}

// mfaStatus handles GET /_auth/mfa: the signed-in user's second-factor state.
func (a *authAPI) mfaStatus(w http.ResponseWriter, r *http.Request) {
	st, err := usersOf(r).MFAStatusOf(r.Context(), principalOf(r).User)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": st.Available, "enabled": st.Enabled, "methods": st.Methods,
		"recovery_codes_left": st.RecoveryCodesLeft, "required": st.Required,
	})
}

// mfaSetup handles POST /_auth/mfa/totp/setup: a new TOTP secret, pending until confirmed.
func (a *authAPI) mfaSetup(w http.ResponseWriter, r *http.Request) {
	setup, err := usersOf(r).BeginTOTPSetup(r.Context(), principalOf(r).User)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_id": setup.SetupID, "secret": setup.Secret, "otpauth_uri": setup.URI, "qr": setup.QR, "expires_at": formatTime(setup.Expires),
	})
}

// mfaConfirm handles POST /_auth/mfa/totp/confirm: the first code turns TOTP on and gives
// the recovery codes, once.
func (a *authAPI) mfaConfirm(w http.ResponseWriter, r *http.Request) {
	f, ok := readStrings(w, r, "setup_id", "code")
	if !ok {
		return
	}
	p := principalOf(r)
	codes, err := usersOf(r).ConfirmTOTP(r.Context(), p.User, p.Session.ID, f["setup_id"], f["code"])
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// mfaDisable handles DELETE /_auth/mfa/totp.
func (a *authAPI) mfaDisable(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).DisableMFA(r.Context(), principalOf(r).User); err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mfaRecoveryCodes handles POST /_auth/mfa/recovery-codes: the codes are replaced.
func (a *authAPI) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := usersOf(r).RegenerateRecoveryCodes(r.Context(), principalOf(r).User)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}
