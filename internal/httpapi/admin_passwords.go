package httpapi

import (
	"net/http"
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
