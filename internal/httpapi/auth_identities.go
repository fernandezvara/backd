package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// identitiesJSON describes a user's sign-in methods: the provider, the address the
// provider reported (a password has none of its own: the user's counts), and when
// each was linked and last used. Never the provider's subject, a hash or a token.
func identitiesJSON(u auth.User, ids []auth.Identity) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, i := range ids {
		email, verified := i.Email, i.EmailVerified
		if i.Provider == auth.ProviderPassword {
			email, verified = u.Email, u.EmailVerified
		}
		var lastUsed any
		if !i.LastUsedAt.IsZero() {
			lastUsed = formatTime(i.LastUsedAt)
		}
		out = append(out, map[string]any{
			"provider": i.Provider, "email": email, "email_verified": verified,
			"created_at": formatTime(i.CreatedAt), "last_used_at": lastUsed,
		})
	}
	return out
}

// unlinkIdentity handles DELETE /_auth/identities/{provider}: the user removes one of
// their sign-in methods, never the last.
func (a *authAPI) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).Unlink(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "provider"), "self")
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "you have no sign-in method of that provider")
	case errors.Is(err, auth.ErrLastSignInMethod):
		writeError(w, r, http.StatusConflict, codeLastMethod, "this is your only way to sign in: add another one first")
	case err != nil:
		authError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
