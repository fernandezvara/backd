package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// Managing the signed-in user's passkeys (design/passkeys.md §8). Registering and removing need a
// fresh proof and a session that was not opened through an organization's connection; renaming and
// listing do not.

func passkeyJSON(p auth.PasskeyInfo) map[string]any {
	out := map[string]any{
		"id": p.ID, "name": p.Name, "created_at": formatTime(p.CreatedAt), "transports": p.Transports,
		"backup_eligible": p.BackupEligible, "backup_state": p.BackupState, "last_used_at": nil,
	}
	if !p.LastUsedAt.IsZero() {
		out["last_used_at"] = formatTime(p.LastUsedAt)
	}
	return out
}

// passkeys handles GET /_auth/passkeys.
func (a *authAPI) passkeys(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).ListPasskeys(r.Context(), principalOf(r).User.ID)
	if err != nil {
		authError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, p := range list {
		items[i] = passkeyJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// passkeyRegisterOptions handles POST /_auth/passkeys/register/options: the options for
// navigator.credentials.create and the id of the ceremony to send back.
func (a *authAPI) passkeyRegisterOptions(w http.ResponseWriter, r *http.Request) {
	if !a.accountReauthed(w, r) {
		return
	}
	options, ceremony, err := usersOf(r).BeginPasskeyRegistration(r.Context(), principalOf(r).User)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge_id": ceremony, "options": json.RawMessage(options)})
}

// passkeyRegisterVerify handles POST /_auth/passkeys/register/verify { challenge_id, credential, name }.
func (a *authAPI) passkeyRegisterVerify(w http.ResponseWriter, r *http.Request) {
	if !a.accountReauthed(w, r) {
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"challenge_id": "string", "credential": "object", "name": "string?"})
	if !ok {
		return
	}
	credential, err := json.Marshal(f["credential"])
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "credential", Reason: "must be the JSON of the PublicKeyCredential"})
		return
	}
	name, _ := f["name"].(string)
	p, err := usersOf(r).FinishPasskeyRegistration(r.Context(), principalOf(r).User, f["challenge_id"].(string), credential, name)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, passkeyJSON(p))
}

// passkeyRename handles PATCH /_auth/passkeys/{id} { name }: no proof needed.
func (a *authAPI) passkeyRename(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"name": "string"})
	if !ok {
		return
	}
	p, err := usersOf(r).RenamePasskey(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "id"), f["name"].(string))
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "passkey not found")
		return
	}
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyJSON(p))
}

// passkeyRemove handles DELETE /_auth/passkeys/{id}.
func (a *authAPI) passkeyRemove(w http.ResponseWriter, r *http.Request) {
	if !a.accountReauthed(w, r) {
		return
	}
	err := usersOf(r).RemovePasskey(r.Context(), principalOf(r).User, chi.URLParam(r, "id"))
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "passkey not found")
		return
	}
	if err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
