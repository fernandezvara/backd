package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// createKey handles POST /secrets: a field key, generated here or imported. The value of a
// generated key is in this answer and nowhere else, ever (design/field-encryption.md §3.1). It
// needs a full administrator with a fresh proof, and is audited with the name and fingerprint.
func (a *adminAPI) createKey(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "creating a key") {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string", "type": "string", "generate": "bool?", "value": "string?"})
	if !ok {
		return
	}
	if t, _ := f["type"].(string); t != auth.SecretTypeKey {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "type", Reason: `must be "key" (plain secrets are set with PUT /secrets/{name})`})
		return
	}
	name, _ := f["name"].(string)
	if !registry.ValidSecretName(name) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "name", Reason: "must be upper-case letters, digits and _, starting with a letter"})
		return
	}
	generate, _ := f["generate"].(bool)
	value, _ := f["value"].(string)
	if generate == (value != "") {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "generate", Reason: "give exactly one of generate: true and value"})
		return
	}
	caller, _ := callerOf(r)
	key, err := usersOf(r).CreateKey(r.Context(), name, value, caller.Actor())
	switch {
	case errors.Is(err, auth.ErrSecretExists):
		writeError(w, r, http.StatusConflict, codeConflict, "a secret named "+name+" already exists: a key is never overwritten (rotate it, or choose another name)")
	case errors.Is(err, auth.ErrKeyValue):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "value", Reason: err.Error()})
	case err != nil:
		adminError(w, r, err)
	default:
		out := map[string]any{"name": key.Name, "type": auth.SecretTypeKey, "fingerprint": key.Fingerprint}
		if key.Value != "" {
			out["key"] = key.Value
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, out)
	}
}

// deleteKey is the part of DELETE /secrets/{name} for a key: the same proof, and a refusal
// while a field still names it.
func (a *adminAPI) deleteKey(w http.ResponseWriter, r *http.Request) {
	if !needFullWithReauth(w, r, "deleting a key") {
		return
	}
	var inUse *auth.KeyInUseError
	err := usersOf(r).DeleteKey(r.Context(), chi.URLParam(r, "name"))
	switch {
	case errors.As(err, &inUse):
		writeError(w, r, http.StatusConflict, codeConflict, "the key is still used by "+joinUses(inUse.Uses)+": change those fields first")
	case errors.Is(err, auth.ErrSecretNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "secret not found")
	case err != nil:
		adminError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func joinUses(uses []string) string {
	out := ""
	for i, u := range uses {
		if i > 0 {
			out += ", "
		}
		out += u
	}
	return out
}
