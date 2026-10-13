package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// Devices (design/push-notifications.md §5): the signed-in user registers the devices that
// receive their pushes. Tokens are never returned.

// deviceJSON describes a device without its token.
func deviceJSON(d auth.Device) map[string]any {
	var name any
	if d.Name != "" {
		name = d.Name
	}
	return map[string]any{
		"id": d.ID, "platform": d.Platform, "name": name,
		"created_at": formatTime(d.CreatedAt), "last_seen": formatTime(d.LastSeen),
	}
}

// registerDevice handles POST /_auth/devices: it registers the device or refreshes it, and
// answers it without its token. Apps call it at each launch and after signing in.
func (a *authAPI) registerDevice(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"platform": "string", "token": "string", "name": "string?"})
	if !ok {
		return
	}
	in := auth.DeviceInput{Platform: f["platform"].(string), Token: f["token"].(string)}
	in.Name, _ = f["name"].(string)
	d, err := usersOf(r).RegisterDevice(r.Context(), principalOf(r), in)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deviceJSON(d))
}

// devices handles GET /_auth/devices.
func (a *authAPI) devices(w http.ResponseWriter, r *http.Request) {
	list, err := usersOf(r).Devices(r.Context(), principalOf(r).User.ID)
	if err != nil {
		authError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, d := range list {
		items[i] = deviceJSON(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// removeDevice handles DELETE /_auth/devices/{id}: one of the caller's devices; someone
// else's is a 404.
func (a *authAPI) removeDevice(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RemoveDevice(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "id"), "")
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "device not found")
	case err != nil:
		authError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// webKey handles GET /_auth/devices/web-key: the VAPID public key, public and cacheable.
func (a *authAPI) webKey(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	p := svc.Settings.Push
	if p == nil {
		authError(w, r, auth.ErrPushNotConfigured)
		return
	}
	if !p.Enabled("web") || p.WebPublicKey == "" {
		authError(w, r, auth.ErrWebKeyUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{"public_key": p.WebPublicKey})
}
