package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// userDevices answers GET /users/{id}/devices: the user's devices, without tokens.
func (a *adminAPI) userDevices(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).Devices(r.Context(), adminUserOf(r).ID)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, d := range list {
		items[i] = deviceJSON(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// removeUserDevice answers DELETE /users/{id}/devices/{device_id}: it removes one device of
// the user, and is audited.
func (a *adminAPI) removeUserDevice(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RemoveDevice(r.Context(), adminUserOf(r).ID, chi.URLParam(r, "device_id"), "admin")
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "device not found")
	case err != nil:
		adminError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
