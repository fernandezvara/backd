package httpapi

import (
	"net/http"
	"sort"

	"github.com/fernandezvara/backd/internal/auth"
)

// takeConsents removes the `consents` member of a request body (an object of consent
// name to the version accepted) and returns it; absent is none.
func takeConsents(w http.ResponseWriter, r *http.Request, obj map[string]any) (map[string]string, bool) {
	raw, present := obj["consents"]
	if !present || raw == nil {
		delete(obj, "consents")
		return nil, true
	}
	delete(obj, "consents")
	m, ok := raw.(map[string]any)
	if !ok {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "consents", Reason: "must be an object of consent name to the version accepted"})
		return nil, false
	}
	out := make(map[string]string, len(m))
	var bad []string
	for k, v := range m {
		if s, ok := v.(string); ok && s != "" {
			out[k] = s
		} else {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		details := make([]Detail, len(bad))
		for i, k := range bad {
			details[i] = Detail{Path: "consents." + k, Reason: "must be the version accepted, as a string"}
		}
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return nil, false
	}
	return out, true
}

// consentsAccept handles POST /_auth/consents/accept: a consent challenge and the consents
// accepted trade for the session the sign-in was waiting for.
func (a *authAPI) consentsAccept(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge": "string", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	v, err := svc.AcceptConsents(r.Context(), f["challenge"].(string), consents, auth.ConsentContext{IP: clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		authError(w, r, err)
		return
	}
	a.answerSignIn(w, r, v.Redeemed, http.StatusOK, useCookie, v.Factor != auth.ProviderPassword)
}

// acceptConsents handles POST /_auth/consents: a signed-in user accepts consents (a new
// version, or an optional one). It stays open past a reaccept_by date, so that a person
// who is refused can accept.
func (a *authAPI) acceptConsents(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	if _, ok := fields(w, r, obj, map[string]string{}); !ok {
		return
	}
	if len(consents) == 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "consents", Reason: "is required: the consents accepted, by name and version"})
		return
	}
	svc := usersOf(r)
	u, err := svc.AcceptConsentsOf(r.Context(), principalOf(r).User, consents, auth.ConsentContext{IP: clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, consentStateJSON(svc, u))
}

// consentStateJSON is what a person has accepted and what is still to accept.
func consentStateJSON(svc *auth.Users, u auth.User) map[string]any {
	held := map[string]string{}
	for k, v := range u.Consents {
		held[k] = v
	}
	required := []map[string]string{}
	for _, ref := range svc.Settings.ConsentsToAccept(u.Consents) {
		required = append(required, map[string]string{"name": ref.Name, "version": ref.Version})
	}
	return map[string]any{"consents": held, "to_accept": required}
}
