package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Signing in to an organization (design/scoped-sessions.md §5). The routes under
// /_auth/orgs/{org}/ run before there is a session, so {org} is the organization's id or its
// slug, and every failure to get in looks the same whether the organization exists or not.

// orgSignInRoutes registers the sign-in routes of one organization. login, signup, the magic
// link request and the password reset request are the realm's handlers: they read the
// organization from the path.
func (a *authAPI) orgSignInRoutes(r chi.Router, json func(http.Handler) http.Handler) {
	r.With(json).Post("/login", a.login)
	r.With(json).Post("/signup", a.signup)
	r.With(json).Post("/magic-link", a.requestMagicLink)
	r.With(json).Post("/reset-password/request", a.requestPasswordReset)
	r.With(json).Post("/invitations/preview", a.previewOrgInvitationOf)
}

// previewOrgInvitationOf handles POST /_auth/orgs/{org}/invitations/preview: what an invitation
// to this organization offers, to someone who has not signed in. The token proves the right to
// know; every token that does not work answers the same.
func (a *authAPI) previewOrgInvitationOf(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"token": "string"})
	if !ok {
		return
	}
	svc := usersOf(r)
	if err := svc.RateLimit(r.Context(), "org-invitation-preview:"+clientIP(r), providersRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	pv, err := svc.PreviewOrgInvitationOf(r.Context(), chi.URLParam(r, "org"), f["token"].(string))
	if err != nil {
		orgError(w, r, err)
		return
	}
	teams := make([]map[string]any, len(pv.Teams))
	for i, t := range pv.Teams {
		teams[i] = map[string]any{"id": t.ID, "name": t.Name}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"org": map[string]any{"id": pv.Org.ID, "name": pv.Org.Name}, "roles": nonNil(pv.Roles), "teams": teams,
		"binding": pv.Binding, "expires_at": formatTime(pv.ExpiresAt),
	})
}

// signUpWithOrg handles POST /_auth/orgs: a new person creates an account and their organization
// and is signed in to it.
func (a *authAPI) signUpWithOrg(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if svc.Settings.Organizations == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no organizations")
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{
		"email": "string", "password": "string", "name": "string", "slug": "string", "invitation": "string?", "locale": "string?", "redirect_to": "string?", "cookie": "bool?",
	})
	if !ok {
		return
	}
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	email := f["email"].(string)
	if _, err := registry.NormalizeEmail(email); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid sign-up request", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	redirectTo, _ := f["redirect_to"].(string)
	if !a.checkRedirect(w, r, redirectTo) {
		return
	}
	requested, _ := f["locale"].(string)
	invitation, _ := f["invitation"].(string)
	res, m, err := svc.SignUpWithOrg(r.Context(), auth.NewOrgSignupRequest{
		SignupRequest: auth.SignupRequest{
			Email: email, Password: f["password"].(string), Invitation: invitation, RedirectTo: redirectTo, ClientIP: clientIP(r), RequestID: requestID(r.Context()),
			Locales:  append([]string{requested}, acceptLanguages(r.Header.Get("Accept-Language"))...),
			Consents: consents, UserAgent: r.UserAgent(),
		},
		Name: f["name"].(string), Slug: f["slug"].(string),
	})
	if err != nil {
		orgError(w, r, err, "password")
		return
	}
	if res.Pending {
		// No session until the address is verified; the answer is the same for a registered address.
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_required"})
		return
	}
	a.answerSignInWith(w, r, auth.Redeemed{Principal: res.Principal, Token: res.Token, Challenge: res.Challenge}, http.StatusCreated, useCookie, false,
		map[string]any{"organization": membershipJSON(m, res.Principal.Session.MFA)})
}

// findOrgs handles POST /_auth/orgs/find: "which companies am I in?". It always answers 202; the
// answer goes to the mailbox.
func (a *authAPI) findOrgs(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if o := svc.Settings.Organizations; o == nil || o.SigninLink == "" || svc.Settings.Email == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
		return
	}
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "locale": "string?"})
	if !ok {
		return
	}
	address := f["email"].(string)
	if _, err := registry.NormalizeEmail(address); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	locale, _ := f["locale"].(string)
	if err := svc.FindOrgs(r.Context(), address, locale, clientIP(r), requestID(r.Context())); err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

// signInToAnOrg answers 404 to a sign-in that names no organization in a realm that has them: the
// person signs in to an organization (/_auth/orgs/{org}/…), and only the operators have a door of
// their own (POST /_auth/login). It returns false when it answered.
func (a *authAPI) signInToAnOrg(w http.ResponseWriter, r *http.Request) bool {
	if usersOf(r).Settings.Organizations != nil && chi.URLParam(r, "org") == "" {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found: in a realm with organizations people sign in to one, at /_auth/orgs/{org}/…")
		return false
	}
	return true
}

// signInOfOrgs is signInToAnOrg as middleware, for the routes that only make sense for an organization.
func (a *authAPI) signInOfOrgs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.signInToAnOrg(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}
