package httpapi

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// authAPI serves /v1/{realm}/_auth/….
type authAPI struct {
	// users returns the user service of an auth-enabled realm, or nil.
	users     func(realm string) *auth.Users
	reg       *registry.Registry
	actions   []hostedAction // the flows behind links in emails
	opTimeout time.Duration
}

type usersKey struct{}
type principalKey struct{}

func (a *authAPI) routes(r chi.Router) {
	r.Route("/v1/{realm}/_auth", func(r chi.Router) {
		r.Use(noStore, a.resolveRealm, withTimeout(a.opTimeout))
		json := requireContentType("application/json")
		r.With(json).Post("/signup", a.signup)
		r.With(json).Post("/login", a.login)
		r.With(json).Post("/verify-email/resend", a.resendVerification)
		a.hostedRoutes(r)
		r.Group(func(r chi.Router) {
			r.Use(a.requireSession)
			r.Post("/logout", a.logout)
			r.Post("/logout-all", a.logoutAll)
			r.Get("/me", a.me)
			r.With(json).Patch("/me", a.updateMe)
			r.With(json).Delete("/me", a.deleteMe)
			r.With(json).Post("/password", a.changePassword)
			r.Get("/sessions", a.sessions)
			r.Delete("/sessions/{id}", a.revokeSession)
		})
	})
}

// resolveRealm answers 404 unless the realm exists with auth enabled.
func (a *authAPI) resolveRealm(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := a.users(chi.URLParam(r, "realm"))
		if u == nil {
			writeError(w, r, http.StatusNotFound, codeNotFound, "realm not found or authentication disabled")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), usersKey{}, u)))
	})
}

func usersOf(r *http.Request) *auth.Users { return r.Context().Value(usersKey{}).(*auth.Users) }

func principalOf(r *http.Request) auth.Principal {
	return r.Context().Value(principalKey{}).(auth.Principal)
}

// requireSession authenticates the Bearer session token. The realm comes
// from the path, so a token from another realm is simply unknown here.
func (a *authAPI) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			unauthenticated(w, r, false, "authentication required")
			return
		}
		p, err := usersOf(r).Authenticate(r.Context(), token)
		if err != nil {
			authError(w, r, err)
			return
		}
		if !allowedFrom(w, r, auth.Caller{User: &p}) {
			return
		}
		setActor(r.Context(), "user:"+p.User.ID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// bearerToken extracts the token of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	return token, ok && strings.EqualFold(scheme, "Bearer") && token != ""
}

func unauthenticated(w http.ResponseWriter, r *http.Request, invalidToken bool, message string) {
	v := `Bearer realm="` + chi.URLParam(r, "realm") + `"`
	if invalidToken {
		v += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", v)
	writeError(w, r, http.StatusUnauthorized, codeUnauthenticated, message)
}

func (a *authAPI) signup(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "password": "string", "invitation": "string?", "locale": "string?", "redirect_to": "string?"})
	if !ok {
		return
	}
	email, password := f["email"].(string), f["password"].(string)
	invitation, _ := f["invitation"].(string)
	requested, _ := f["locale"].(string)
	redirectTo, _ := f["redirect_to"].(string)
	if !a.checkRedirect(w, r, redirectTo) {
		return
	}
	if _, err := registry.NormalizeEmail(email); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid sign-up request", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	// The language asked for in the request comes first, then the browser's.
	res, err := usersOf(r).SignUp(r.Context(), auth.SignupRequest{
		Email: email, Password: password, Invitation: invitation, RedirectTo: redirectTo, ClientIP: clientIP(r), RequestID: requestID(r.Context()),
		Locales: append([]string{requested}, acceptLanguages(r.Header.Get("Accept-Language"))...),
	})
	if err != nil {
		authError(w, r, err, "password")
		return
	}
	if res.Pending {
		// No session until the address is verified.
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_required"})
		return
	}
	p, token := res.Principal, res.Token
	setActor(r.Context(), "user:"+p.User.ID)
	writeJSON(w, http.StatusCreated, sessionJSON(p, token, usersOf(r).LocaleOf(p.User)))
}

func (a *authAPI) login(w http.ResponseWriter, r *http.Request) {
	f, ok := readStrings(w, r, "email", "password")
	if !ok {
		return
	}
	p, token, err := usersOf(r).Login(r.Context(), f["email"], f["password"], clientIP(r))
	if err != nil {
		authError(w, r, err)
		return
	}
	setActor(r.Context(), "user:"+p.User.ID)
	writeJSON(w, http.StatusOK, sessionJSON(p, token, usersOf(r).LocaleOf(p.User)))
}

func (a *authAPI) logout(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).Logout(r.Context(), principalOf(r)); err != nil && !errors.Is(err, auth.ErrNotFound) {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).LogoutAll(r.Context(), principalOf(r)); err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userJSON(principalOf(r).User, usersOf(r).LocaleOf(principalOf(r).User)))
}

// updateMe handles PATCH /_auth/me: the user changes their own settings.
// Today that is their language, which must be one the realm lists.
func (a *authAPI) updateMe(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"locale": "string?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	u := principalOf(r).User
	if locale, ok := f["locale"].(string); ok {
		var err error
		u, err = svc.SetLocale(r.Context(), u.ID, locale)
		var il *auth.InvalidLocaleError
		if errors.As(err, &il) {
			writeError(w, r, http.StatusBadRequest, codeInvalidLocale, "this realm doesn't offer that language", Detail{Path: "locale", Reason: il.Error()})
			return
		}
		if err != nil {
			authError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, userJSON(u, svc.LocaleOf(u)))
}

// acceptLanguages parses an Accept-Language header into language tags, most
// wanted first (by q, then in the order given), leaving out `*` and q=0.
func acceptLanguages(header string) []string {
	type tag struct {
		name string
		q    float64
	}
	var tags []tag
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = parsed
			}
		}
		if q > 0 {
			tags = append(tags, tag{name, q})
		}
	}
	slices.SortStableFunc(tags, func(a, b tag) int { return cmp.Compare(b.q, a.q) })
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.name
	}
	return out
}

func (a *authAPI) deleteMe(w http.ResponseWriter, r *http.Request) {
	f, ok := readStrings(w, r, "password")
	if !ok {
		return
	}
	if err := usersOf(r).DeleteAccount(r.Context(), principalOf(r), f["password"]); err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) changePassword(w http.ResponseWriter, r *http.Request) {
	f, ok := readStrings(w, r, "current_password", "new_password")
	if !ok {
		return
	}
	if err := usersOf(r).ChangePassword(r.Context(), principalOf(r), f["current_password"], f["new_password"]); err != nil {
		authError(w, r, err, "new_password")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) sessions(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	list, err := usersOf(r).Sessions(r.Context(), p)
	if err != nil {
		authError(w, r, err)
		return
	}
	items := make([]map[string]any, len(list))
	for i, s := range list {
		items[i] = map[string]any{
			"id":           s.ID,
			"created_at":   formatTime(s.CreatedAt),
			"last_used_at": formatTime(s.LastUsedAt),
			"expires_at":   formatTime(s.ExpiresAt),
			"current":      s.ID == p.Session.ID,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *authAPI) revokeSession(w http.ResponseWriter, r *http.Request) {
	err := usersOf(r).RevokeSession(r.Context(), principalOf(r), chi.URLParam(r, "id"))
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "session not found")
		return
	}
	if err != nil {
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readStrings reads a JSON object with exactly the named string fields.
func readStrings(w http.ResponseWriter, r *http.Request, names ...string) (map[string]string, bool) {
	obj, ok := readObject(w, r)
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	var details []Detail
	for _, n := range names {
		switch v := obj[n].(type) {
		case string:
			out[n] = v
		case nil:
			details = append(details, Detail{Path: n, Reason: "is required"})
		default:
			details = append(details, Detail{Path: n, Reason: "must be a string"})
		}
		delete(obj, n)
	}
	var unknown []string
	for k := range obj {
		unknown = append(unknown, k)
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		details = append(details, Detail{Path: k, Reason: "is not allowed"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return nil, false
	}
	return out, true
}

// authError maps auth errors to responses. passwordField names the body
// field a password policy error refers to.
func authError(w http.ResponseWriter, r *http.Request, err error, passwordField ...string) {
	var pe *auth.PolicyError
	var te *auth.ThrottledError
	switch {
	case errors.As(err, &te):
		secs := int((te.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "too many failed attempts; retry later")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, r, http.StatusUnauthorized, codeInvalidCreds, err.Error())
	case errors.Is(err, auth.ErrEmailNotVerified):
		writeError(w, r, http.StatusForbidden, codeEmailNotVerified, err.Error())
	case errors.Is(err, auth.ErrUnauthenticated):
		unauthenticated(w, r, true, err.Error())
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, codeEmailTaken, err.Error(), Detail{Path: "email", Reason: "is already registered"})
	case errors.Is(err, auth.ErrSignupClosed), errors.Is(err, auth.ErrInvitationRequired), errors.Is(err, auth.ErrInvalidInvitation):
		writeError(w, r, http.StatusForbidden, codeForbidden, err.Error())
	case errors.As(err, &pe) && len(passwordField) > 0:
		writeError(w, r, http.StatusBadRequest, codeValidation, "password does not meet the policy", Detail{Path: passwordField[0], Reason: pe.Reason})
	case errors.Is(err, auth.ErrBusy), errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusServiceUnavailable, codeUnavailable, "service busy; try again later")
	default:
		logger(r.Context()).Error("auth error", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

func sessionJSON(p auth.Principal, token, locale string) map[string]any {
	return map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"session_id": p.Session.ID,
		"expires_at": formatTime(p.Session.ExpiresAt),
		"user":       userJSON(p.User, locale),
	}
}

func userJSON(u auth.User, locale string) map[string]any {
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	return map[string]any{
		"id":             u.ID,
		"email":          u.Email,
		"email_verified": u.EmailVerified,
		"roles":          roles,
		"locale":         locale,
		"created_at":     formatTime(u.CreatedAt),
	}
}

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }
