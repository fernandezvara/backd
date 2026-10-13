package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// userIdentities answers GET /users/{id}/identities: the user's sign-in methods, as
// GET /_auth/me lists them for the user themselves. Never a subject, a hash or a token.
func (a *adminAPI) userIdentities(w http.ResponseWriter, r *http.Request) {
	u := adminUserOf(r)
	ids, err := usersOf(r).Identities(r.Context(), u.ID)
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": identitiesJSON(u, ids)})
}

// unlinkUserIdentity answers DELETE /users/{id}/identities/{provider}: an administrator
// removes one of the user's sign-in methods, never the last, and it is audited as
// identity.unlinked with the administrator as the actor.
func (a *adminAPI) unlinkUserIdentity(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).Unlink(r.Context(), adminUserOf(r).ID, chi.URLParam(r, "provider"), "admin")
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "the user has no sign-in method of that provider")
	case errors.Is(err, auth.ErrLastSignInMethod):
		writeError(w, r, http.StatusConflict, codeLastMethod, "this is the user's only way to sign in")
	case err != nil:
		adminError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
