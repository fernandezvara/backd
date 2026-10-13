package httpapi

import (
	"errors"
	"net/http"

	"github.com/fernandezvara/backd/internal/auth"
)

// mfaOverviewLimit caps the users GET /_admin/mfa names.
const mfaOverviewLimit = 200

// mfaOverview answers GET /mfa: which roles the realm requires MFA for and the users of
// those roles who have none yet (their next sign-in sets it up).
func (a *adminAPI) mfaOverview(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	m := svc.Settings.MFA
	users, total, err := svc.UsersWithoutRequiredMFA(r.Context(), mfaOverviewLimit)
	if err != nil {
		adminError(w, r, err)
		return
	}
	items := make([]map[string]any, len(users))
	for i, u := range users {
		roles := u.Roles
		if roles == nil {
			roles = []string{}
		}
		items[i] = map[string]any{"id": u.ID, "email": u.Email, "roles": roles}
	}
	roles := m.RequiredRoles
	if roles == nil {
		roles = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": !m.Off, "required_all": m.RequiredAll, "required_roles": roles,
		"users_without_mfa": items, "total_without_mfa": total,
	})
}

// userMFA answers GET /users/{id}/mfa: the user's second-factor state, as GET /_auth/mfa
// shows it to them. Never a secret or a code.
func (a *adminAPI) userMFA(w http.ResponseWriter, r *http.Request) {
	st, err := usersOf(r).MFAStatusOf(r.Context(), adminUserOf(r))
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": st.Available, "enabled": st.Enabled, "methods": st.Methods,
		"recovery_codes_left": st.RecoveryCodesLeft, "required": st.Required,
	})
}

// resetUserMFA answers POST /users/{id}/mfa/reset: an administrator removes the user's second
// factor and recovery codes, which ends all of their sessions. It is the way back for someone
// who lost both their authenticator and their recovery codes; it is never available on yourself.
func (a *adminAPI) resetUserMFA(w http.ResponseWriter, r *http.Request) {
	actor := ""
	if c, ok := callerOf(r); ok && c.Key == nil && c.User != nil {
		actor = c.User.User.ID
	}
	err := usersOf(r).ResetMFA(r.Context(), adminUserOf(r).ID, actor)
	switch {
	case errors.Is(err, auth.ErrSelfMFAReset):
		writeError(w, r, http.StatusForbidden, codeSelfReset, "an administrator can't reset their own second factor: ask another administrator, or an operator with access to the instance")
	case errors.Is(err, auth.ErrMFANotEnabled):
		writeError(w, r, http.StatusConflict, codeMFANotEnabled, "the user has no second factor")
	case err != nil:
		adminError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
