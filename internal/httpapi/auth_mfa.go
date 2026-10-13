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
