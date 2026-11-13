package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
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
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string", "method": "string", "code": "string?", "credential": "object?", "cookie": "bool?"})
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
	var v auth.Verified
	var err error
	if method == auth.MethodPasskey {
		// A passkey step answers with the credential the browser made, in place of a code.
		cred, has := f["credential"]
		if !has {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "credential", Reason: "is required for the passkey method"})
			return
		}
		v, err = svc.VerifyMFAPasskey(r.Context(), f["challenge"].(string), jsonBytes(cred), clientIP(r))
	} else {
		code, has := f["code"].(string)
		if !has {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "code", Reason: "is required for this method"})
			return
		}
		v, err = svc.VerifyMFA(r.Context(), f["challenge"].(string), method, code, clientIP(r))
	}
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
		"recovery_codes_left": st.RecoveryCodesLeft, "required": st.Required, "passkeys": st.Passkeys,
	})
}

// optionalObject reads a JSON object body that may be absent (an empty object then).
func optionalObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
		return nil, false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, true
	}
	if !contentTypeIs(r, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be application/json")
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return readObject(w, r)
}

// mfaSetup handles POST /_auth/mfa/totp/setup: a new TOTP secret, pending until confirmed.
// It acts on the session of the request, or on the `challenge` of a sign-in that must set
// a second factor up.
func (a *authAPI) mfaSetup(w http.ResponseWriter, r *http.Request) {
	obj, ok := optionalObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	var setup auth.TOTPSetup
	var err error
	if challenge, _ := f["challenge"].(string); challenge != "" {
		setup, err = svc.BeginEnrollment(r.Context(), challenge)
	} else {
		p, ok := a.authenticateRequest(w, r)
		if !ok {
			return
		}
		if !a.accountReauthedAs(w, r, p) {
			return
		}
		setup, err = svc.BeginTOTPSetup(r.Context(), p.User)
	}
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_id": setup.SetupID, "secret": setup.Secret, "otpauth_uri": setup.URI, "qr": setup.QR, "expires_at": formatTime(setup.Expires),
	})
}

// mfaConfirm handles POST /_auth/mfa/totp/confirm: the first code turns TOTP on and gives
// the recovery codes, once. With a `challenge` it also ends the sign-in that waited for it:
// the answer then has the session.
func (a *authAPI) mfaConfirm(w http.ResponseWriter, r *http.Request) {
	if !contentTypeIs(r, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be application/json")
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"setup_id": "string", "code": "string", "challenge": "string?", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	if challenge, _ := f["challenge"].(string); challenge != "" {
		asked, _ := f["cookie"].(bool)
		useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
		if !ok {
			return
		}
		v, codes, err := svc.CompleteEnrollment(r.Context(), challenge, f["setup_id"].(string), f["code"].(string))
		if err != nil {
			authError(w, r, err)
			return
		}
		a.answerSignInWith(w, r, v.Redeemed, http.StatusOK, useCookie, v.Factor != auth.ProviderPassword, map[string]any{"recovery_codes": codes})
		return
	}
	p, ok := a.authenticateRequest(w, r)
	if !ok {
		return
	}
	if !a.accountReauthedAs(w, r, p) {
		return
	}
	codes, err := svc.ConfirmTOTP(r.Context(), p.User, p.Session.ID, f["setup_id"].(string), f["code"].(string))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// mfaDisable handles DELETE /_auth/mfa/totp.
func (a *authAPI) mfaDisable(w http.ResponseWriter, r *http.Request) {
	if !a.accountReauthed(w, r) {
		return
	}
	if err := usersOf(r).DisableMFA(r.Context(), principalOf(r).User); err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mfaRecoveryCodes handles POST /_auth/mfa/recovery-codes: the codes are replaced.
func (a *authAPI) mfaRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if !a.accountReauthed(w, r) {
		return
	}
	codes, err := usersOf(r).RegenerateRecoveryCodes(r.Context(), principalOf(r).User)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// mfaPasskeyOptions handles POST /_auth/mfa/passkey/options { challenge }: the options for the
// passkey a sign-in challenge waits for (a later step of the strategy).
func (a *authAPI) mfaPasskeyOptions(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string"})
	if !ok {
		return
	}
	options, err := usersOf(r).BeginPasskeyStep(r.Context(), f["challenge"].(string))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": json.RawMessage(options)})
}

// mfaPasskeyRegisterOptions handles POST /_auth/mfa/passkey/register/options { challenge }: the options
// for adding the passkey a sign-in is waiting for, to a person who has none.
func (a *authAPI) mfaPasskeyRegisterOptions(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string"})
	if !ok {
		return
	}
	options, err := usersOf(r).BeginPasskeyEnrollment(r.Context(), f["challenge"].(string))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": json.RawMessage(options)})
}

// mfaPasskeyRegisterVerify handles POST /_auth/mfa/passkey/register/verify { challenge, credential, name?,
// cookie? }: it ends the sign-in with the session (a passkey step makes no recovery codes).
func (a *authAPI) mfaPasskeyRegisterVerify(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string", "credential": "object", "name": "string?", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	name, _ := f["name"].(string)
	v, err := svc.CompletePasskeyEnrollment(r.Context(), f["challenge"].(string), jsonBytes(f["credential"]), name)
	if err != nil {
		authError(w, r, err)
		return
	}
	a.answerSignIn(w, r, v.Redeemed, http.StatusOK, useCookie, v.Factor != auth.ProviderPassword)
}
