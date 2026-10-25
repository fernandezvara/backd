package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
)

// identitiesJSON describes a user's sign-in methods: the provider, the organization whose
// connection it came through (if any), the address the
// provider reported (a password has none of its own: the user's counts), and when
// each was linked and last used. Never the provider's subject, a hash or a token.
func identitiesJSON(u auth.User, ids []auth.Identity) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, i := range ids {
		email, verified := i.Email, i.EmailVerified
		if i.Provider == auth.ProviderPassword || i.Provider == auth.ProviderEmail {
			email, verified = u.Email, u.EmailVerified
		}
		var lastUsed any
		if !i.LastUsedAt.IsZero() {
			lastUsed = formatTime(i.LastUsedAt)
		}
		item := map[string]any{
			"provider": i.Provider, "email": email, "email_verified": verified,
			"created_at": formatTime(i.CreatedAt), "last_used_at": lastUsed,
		}
		if i.Org != "" {
			item["org"] = i.Org
		}
		out = append(out, item)
	}
	return out
}

// unlinkIdentity handles DELETE /_auth/identities/{provider}: the user removes one of
// their sign-in methods, never the last.
func (a *authAPI) unlinkIdentity(w http.ResponseWriter, r *http.Request) {
	if !a.reauthed(w, r) {
		return
	}
	err := usersOf(r).UnlinkIn(r.Context(), principalOf(r).User.ID, chi.URLParam(r, "provider"), r.URL.Query().Get("org"), "self")
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

// removePassword handles DELETE /_auth/password: the user stops having a password and signs in
// with a link or a provider, never when that would leave them no way in.
func (a *authAPI) removePassword(w http.ResponseWriter, r *http.Request) {
	if !a.reauthed(w, r) {
		return
	}
	err := usersOf(r).RemovePassword(r.Context(), principalOf(r))
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this account has no password")
	case errors.Is(err, auth.ErrLastSignInMethod):
		writeError(w, r, http.StatusConflict, codeLastMethod, "the password is your only way to sign in: add another one first (a provider, or magic links with a verified address)")
	case err != nil:
		authError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
