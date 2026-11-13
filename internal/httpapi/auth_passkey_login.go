package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// Signing in with a passkey as the first method of a strategy (design/passkeys.md §6). No email, no
// session: the options answer alike for everyone, and every failure of the verification is
// 401 invalid_passkey.

// passkeyLoginOptions handles POST /_auth/passkey/login/options { strategy? } (and the same under
// /_auth/orgs/{org}/).
func (a *authAPI) passkeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	obj, ok := optionalObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"strategy": "string?"})
	if !ok {
		return
	}
	strategy, _ := f["strategy"].(string)
	out, err := usersOf(r).BeginPasskeySignIn(r.Context(), strategy, chi.URLParam(r, "org"), clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge_id": out.Ceremony, "strategy": out.Strategy, "options": json.RawMessage(out.Options)})
}

// passkeyLoginVerify handles POST /_auth/passkey/login/verify { challenge_id, credential, cookie? }.
func (a *authAPI) passkeyLoginVerify(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge_id": "string", "credential": "object", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	credential, err := json.Marshal(f["credential"])
	if err != nil {
		authError(w, r, auth.ErrInvalidPasskey)
		return
	}
	out, err := svc.SignInWithPasskey(r.Context(), auth.PasskeySignIn{Ceremony: f["challenge_id"].(string), Credential: credential, IP: clientIP(r), Org: chi.URLParam(r, "org")})
	if err != nil {
		authError(w, r, err)
		return
	}
	a.answerSignIn(w, r, out, http.StatusOK, useCookie, false)
}
