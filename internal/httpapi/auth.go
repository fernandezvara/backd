package httpapi

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/oauth"
	"github.com/fernandezvara/backd/internal/registry"
)

// authAPI serves /v1/{realm}/_auth/….
type authAPI struct {
	// users returns the user service of an auth-enabled realm, or nil.
	users     func(realm string) *auth.Users
	reg       *registry.Registry
	actions   []hostedAction // the flows behind links in emails
	opTimeout time.Duration
	// oauth talks to the identity providers; baseURL is backd's public address
	// (BACKD_URL), where they send the user back to.
	oauth   *oauth.Service
	baseURL string
	// fns calls the template data hooks of the hosted pages; set once the function routes exist.
	fns *functions
}

type usersKey struct{}
type principalKey struct{}

func (a *authAPI) routes(r chi.Router) {
	// What the capture push provider took, for the Mailbox: development only (see devPush).
	r.With(noStore, a.resolveRealm).Get("/v1/{realm}/_dev/push", a.devPush)
	a.consentRoutes(r)
	a.orgRoutes(r)
	r.Route("/v1/{realm}/_auth", func(r chi.Router) {
		r.Use(noStore, a.resolveRealm, withTimeout(a.opTimeout))
		json := requireContentType("application/json")
		r.With(json, a.signInOfOrgs).Post("/signup", a.signup)
		// The public key a browser subscribes with: no session, cacheable.
		r.Get("/devices/web-key", a.webKey)
		r.With(json).Post("/login", a.login)
		r.With(json).Post("/consents/accept", a.consentsAccept)
		r.With(json).Post("/mfa/verify", a.mfaVerify)
		// A magic link: JSON asks for one, a form (the page's button) uses it.
		r.Post("/magic-link", a.magicLinkPost)
		r.Get("/magic-link", a.magicLinkPage)
		r.With(json).Post("/magic-link/verify", a.magicLinkVerify)
		// Both act on a session, or on the challenge of a sign-in that must set a second
		// factor up: they look for either themselves.
		r.Post("/mfa/totp/setup", a.mfaSetup)
		r.Post("/mfa/totp/confirm", a.mfaConfirm)
		r.With(json).Post("/verify-email/resend", a.resendVerification)
		r.With(json).Post("/reset-password/request", a.requestPasswordReset)
		a.hostedRoutes(r)
		a.oauthRoutes(r, json)
		r.Group(func(r chi.Router) {
			r.Use(a.requireSessionIgnoringConsents)
			r.Post("/logout", a.logout)
			r.Post("/logout-all", a.logoutAll)
			r.Get("/me", a.me)
			r.With(json).Post("/consents", a.acceptConsents)
			r.Get("/consents", a.consentsGet)
			// Leaving: a person who won't accept a new version must be able to delete the
			// account (withdrawing a required consent points them here).
			r.Delete("/me", a.deleteMe)
			r.Delete("/consents/{name}", a.consentsWithdraw)
		})
		r.Group(func(r chi.Router) {
			r.Use(a.requireSession)
			r.With(json).Patch("/me", a.updateMe)
			r.With(json).Post("/password", a.changePassword)
			r.Delete("/password", a.removePassword)
			r.With(json).Post("/email", a.changeEmail)
			r.Delete("/identities/{provider}", a.unlinkIdentity)
			r.With(json).Post("/reauth", a.reauth)
			r.Post("/reauth/email-code", a.reauthEmailCode)
			r.Get("/mfa", a.mfaStatus)
			r.Delete("/mfa/totp", a.mfaDisable)
			r.Post("/mfa/recovery-codes", a.mfaRecoveryCodes)
			r.Get("/sessions", a.sessions)
			r.With(json).Post("/devices", a.registerDevice)
			r.Get("/devices", a.devices)
			r.Delete("/devices/{id}", a.removeDevice)
			r.Delete("/sessions/{id}", a.revokeSession)
			a.exportRoutes(r)
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
	return a.sessionMiddleware(next, false)
}

// requireSessionIgnoringConsents is requireSession for the routes that stay open to a user
// who must still accept consents: the consent routes, sign-out and the account's state.
func (a *authAPI) requireSessionIgnoringConsents(next http.Handler) http.Handler {
	return a.sessionMiddleware(next, true)
}

func (a *authAPI) sessionMiddleware(next http.Handler, ignoreConsents bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticate(w, r, ignoreConsents)
		if !ok {
			return
		}
		ctx := auth.WithSessionMFA(context.WithValue(r.Context(), principalKey{}, p), p.Session.MFA)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticateRequest resolves the request's session, or answers why it can't.
func (a *authAPI) authenticateRequest(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	return a.authenticate(w, r, false)
}

// authenticate is authenticateRequest; ignoreConsents skips the consent deadline, for the
// routes a person needs to accept what is missing, or to leave.
func (a *authAPI) authenticate(w http.ResponseWriter, r *http.Request, ignoreConsents bool) (auth.Principal, bool) {
	svc := usersOf(r)
	token, src, headerOK := credentialOf(r, svc.Settings)
	if !headerOK || src == noCredential {
		unauthenticated(w, r, false, "authentication required")
		return auth.Principal{}, false
	}
	authenticate := svc.Authenticate
	if ignoreConsents {
		authenticate = svc.AuthenticateIgnoringConsents
	}
	p, err := authenticate(r.Context(), token)
	if err != nil {
		if src == fromCookie {
			clearSessionCookie(w, r, svc.Settings)
		}
		authError(w, r, err)
		return auth.Principal{}, false
	}
	if src == fromCookie && !csrfCheck(w, r, svc.Settings) {
		return auth.Principal{}, false
	}
	if !allowedFrom(w, r, auth.Caller{User: &p}) {
		return auth.Principal{}, false
	}
	setActor(r.Context(), "user:"+p.User.ID)
	return p, true
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
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "password": "string", "invitation": "string?", "locale": "string?", "redirect_to": "string?", "org": "string?", "cookie": "bool?"})
	if !ok {
		return
	}
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, usersOf(r).Settings, asked)
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
	r = withOrgContext(r, f, email)
	// The language asked for in the request comes first, then the browser's.
	req := auth.SignupRequest{
		Email: email, Password: password, Invitation: invitation, RedirectTo: redirectTo, ClientIP: clientIP(r), RequestID: requestID(r.Context()),
		Locales:  append([]string{requested}, acceptLanguages(r.Header.Get("Accept-Language"))...),
		Consents: consents, UserAgent: r.UserAgent(),
	}
	var res auth.SignupResult
	var err error
	if ref := chi.URLParam(r, "org"); ref != "" {
		// To an organization: the invitation is its, and the session is scoped to it.
		res, err = usersOf(r).SignUpToOrg(r.Context(), auth.OrgSignupRequest{SignupRequest: req, Org: ref})
	} else {
		res, err = usersOf(r).SignUp(r.Context(), req)
	}
	if err != nil {
		orgError(w, r, err, "password")
		return
	}
	if res.Pending {
		// No session until the address is verified.
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "verification_required"})
		return
	}
	a.answerSignIn(w, r, auth.Redeemed{Principal: res.Principal, Token: res.Token, Challenge: res.Challenge}, http.StatusCreated, useCookie, false)
}

func (a *authAPI) login(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "password": "string", "invitation": "string?", "org": "string?", "cookie": "bool?"})
	if !ok {
		return
	}
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, usersOf(r).Settings, asked)
	if !ok {
		return
	}
	r = withOrgContext(r, f, f["email"].(string))
	var out auth.Redeemed
	var err error
	if ref := chi.URLParam(r, "org"); ref != "" {
		// To an organization: the person must belong to it (or hold an invitation), and the
		// session is scoped to it.
		invitation, _ := f["invitation"].(string)
		out, err = usersOf(r).SignInToOrg(r.Context(), auth.OrgSignIn{Org: ref, Email: f["email"].(string), Password: f["password"].(string), IP: clientIP(r), Invitation: invitation})
	} else {
		out, err = usersOf(r).SignIn(r.Context(), f["email"].(string), f["password"].(string), clientIP(r))
	}
	if err != nil {
		orgError(w, r, err)
		return
	}
	a.answerSignIn(w, r, out, http.StatusOK, useCookie, false)
}

// answerSignIn writes what a first or second factor led to: the session, or the challenge
// that asks for the next step. provider says the sign-in is a provider's, whose answer
// also says whether it made the user.
func (a *authAPI) answerSignIn(w http.ResponseWriter, r *http.Request, out auth.Redeemed, status int, useCookie, provider bool) {
	a.answerSignInWith(w, r, out, status, useCookie, provider, nil)
}

// answerSignInWith is answerSignIn with more fields in the session's answer.
func (a *authAPI) answerSignInWith(w http.ResponseWriter, r *http.Request, out auth.Redeemed, status int, useCookie, provider bool, extra map[string]any) {
	svc := usersOf(r)
	if c := out.Challenge; c != nil {
		// What the step carries along (recovery codes shown once) must not be lost with it.
		body := challengeJSON(c)
		maps.Copy(body, extra)
		writeJSON(w, http.StatusOK, body)
		return
	}
	p, token := out.Principal, out.Token
	setActor(r.Context(), "user:"+p.User.ID)
	if useCookie {
		setSessionCookie(w, r, svc, p, token)
		token = "" // the token stays in the cookie, out of the response
	}
	body := sessionJSON(p, token, svc.LocaleOf(p.User))
	if provider {
		body = providerSessionJSON(p, token, svc.LocaleOf(p.User), out.NewUser, out.Profile)
	}
	if out.RecoveryCodesLeft != nil {
		body["recovery_codes_left"] = *out.RecoveryCodesLeft
	}
	maps.Copy(body, extra)
	writeJSON(w, status, body)
}

// challengeJSON is the answer of a first factor whose user needs a second step.
func challengeJSON(c *auth.IssuedChallenge) map[string]any {
	if c.Kind == auth.ChallengeEnroll {
		return map[string]any{"mfa_enrollment_required": true, "challenge": c.Token}
	}
	if c.Kind == auth.ChallengeConsent {
		list := make([]map[string]string, len(c.Consents))
		for i, ref := range c.Consents {
			list[i] = map[string]string{"name": ref.Name, "version": ref.Version}
		}
		return map[string]any{"consent_required": true, "challenge": c.Token, "consents": list}
	}
	return map[string]any{"mfa_required": true, "challenge": c.Token, "methods": c.Methods}
}

func (a *authAPI) logout(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).Logout(r.Context(), principalOf(r)); err != nil && !errors.Is(err, auth.ErrNotFound) {
		authError(w, r, err)
		return
	}
	forgetCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := usersOf(r).LogoutAll(r.Context(), principalOf(r)); err != nil {
		authError(w, r, err)
		return
	}
	forgetCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) me(w http.ResponseWriter, r *http.Request) {
	u := principalOf(r).User
	out := userJSON(u, usersOf(r).LocaleOf(u))
	ids, err := usersOf(r).Identities(r.Context(), u.ID)
	if err != nil {
		authError(w, r, err)
		return
	}
	out["identities"] = identitiesJSON(u, ids)
	methods, err := usersOf(r).ReauthMethodsOf(r.Context(), u)
	if err != nil {
		authError(w, r, err)
		return
	}
	out["reauth_methods"] = methods
	signIn, err := usersOf(r).SignInMethods(r.Context(), u)
	if err != nil {
		authError(w, r, err)
		return
	}
	out["sign_in_methods"] = signIn
	if org := principalOf(r).Session.Org; org != "" {
		out["org"] = a.sessionOrgJSON(r, u.ID, org)
	}
	if len(usersOf(r).Settings.Consents) > 0 {
		maps.Copy(out, consentStateJSON(usersOf(r), u))
	}
	writeJSON(w, http.StatusOK, out)
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
		if il, ok := errors.AsType[*auth.InvalidLocaleError](err); ok {
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
	for part := range strings.SplitSeq(header, ",") {
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
	if !a.accountReauthed(w, r) {
		return
	}
	if err := usersOf(r).DeactivateAccount(r.Context(), principalOf(r)); err != nil {
		authError(w, r, err)
		return
	}
	forgetCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *authAPI) changePassword(w http.ResponseWriter, r *http.Request) {
	f, ok := readStrings(w, r, "new_password")
	if !ok {
		return
	}
	if !a.accountReauthed(w, r) {
		return
	}
	if err := usersOf(r).ChangePassword(r.Context(), principalOf(r), f["new_password"]); err != nil {
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
		// The organization each session is scoped to (none for an operator's), and the provider
		// of the connection that opened it.
		if s.Org != "" {
			items[i]["org"] = s.Org
		}
		if _, provider, via := s.Connection(); via {
			items[i]["connection"] = provider
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
	var el *auth.EmailLimitedError
	var locked *auth.MFALockedError
	var badCode *auth.InvalidCodeError
	var badDevice *auth.InvalidDeviceError
	var needConsent *auth.ConsentRequiredError
	var badConsents *auth.InvalidConsentsError
	switch {
	case errors.As(err, &te):
		secs := int((te.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "too many failed attempts; retry later")
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(max(int((locked.RetryAfter+time.Second-1)/time.Second), 1)))
		writeError(w, r, http.StatusTooManyRequests, codeMFALocked, "too many wrong codes; try again later")
	case errors.As(err, &badCode):
		if badCode.AttemptsLeft < 0 {
			writeError(w, r, http.StatusBadRequest, codeInvalidCode, "the code is not valid")
			break
		}
		writeError(w, r, http.StatusBadRequest, codeInvalidCode, "the code is not valid", Detail{Path: "attempts_left", Reason: strconv.Itoa(badCode.AttemptsLeft)})
	case errors.Is(err, auth.ErrPushNotConfigured):
		writeError(w, r, http.StatusNotFound, codePushNotConfigured, err.Error())
	case errors.Is(err, auth.ErrPushUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codePushUnavailable, err.Error())
	case errors.Is(err, auth.ErrPlatformNotEnabled):
		writeError(w, r, http.StatusBadRequest, codePlatformDisabled, err.Error(), Detail{Path: "platform", Reason: "isn't enabled in this realm's push settings"})
	case errors.Is(err, auth.ErrWebKeyUnavailable):
		writeError(w, r, http.StatusNotFound, codePushNotConfigured, err.Error())
	case errors.As(err, &badDevice):
		writeError(w, r, http.StatusBadRequest, codeInvalidDevice, "invalid device", Detail{Path: "token", Reason: badDevice.Reason})
	case errors.As(err, &needConsent):
		details := make([]Detail, len(needConsent.Consents))
		for i, c := range needConsent.Consents {
			details[i] = Detail{Path: "consents." + c.Name, Reason: "must be accepted at version " + c.Version}
		}
		if needConsent.Session {
			writeError(w, r, http.StatusForbidden, codeConsentRequired, "consents must be accepted to continue: accept them with POST /_auth/consents", details...)
		} else {
			writeError(w, r, http.StatusBadRequest, codeConsentRequired, "required consents must be accepted", details...)
		}
	case errors.As(err, &badConsents):
		details := make([]Detail, len(badConsents.Reasons))
		for i, reason := range badConsents.Reasons {
			path, msg, _ := strings.Cut(reason, ": ")
			details[i] = Detail{Path: path, Reason: msg}
		}
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid consents", details...)
	case errors.Is(err, auth.ErrUnknownConsent), errors.Is(err, auth.ErrUnknownConsentVersion):
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid consents", Detail{Path: "consents", Reason: err.Error()})
	case errors.Is(err, auth.ErrConsentRequiredForAccount):
		writeError(w, r, http.StatusConflict, "consent_required_for_account", err.Error())
	case errors.Is(err, auth.ErrMFADisabled):
		writeError(w, r, http.StatusForbidden, codeMFADisabled, err.Error())
	case errors.Is(err, auth.ErrMFAUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeMFAUnavailable, err.Error())
	case errors.Is(err, auth.ErrMFARequiredByRole):
		writeError(w, r, http.StatusForbidden, codeMFARequiredByRole, err.Error())
	case errors.Is(err, auth.ErrMFAAlreadyEnabled):
		writeError(w, r, http.StatusConflict, codeMFAAlreadyEnabled, err.Error())
	case errors.Is(err, auth.ErrMFANotEnabled):
		writeError(w, r, http.StatusConflict, codeMFANotEnabled, err.Error())
	case errors.Is(err, auth.ErrReauthRequired):
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+chi.URLParam(r, "realm")+`", error="insufficient_user_authentication"`)
		writeError(w, r, http.StatusUnauthorized, codeReauthRequired, "this action needs a fresh proof of identity: POST /_auth/reauth and send the token in "+reauthHeader)
	case errors.Is(err, auth.ErrReauthMethod):
		writeError(w, r, http.StatusBadRequest, codeValidation, "that proof is not accepted for this user", Detail{Path: "method", Reason: "use one of the user's reauth_methods"})
	case errors.Is(err, auth.ErrMagicLinkDisabled):
		writeError(w, r, http.StatusForbidden, codeMagicLinkDisabled, err.Error())
	case errors.Is(err, auth.ErrInvalidMagicLink):
		writeError(w, r, http.StatusBadRequest, codeInvalidMagicLink, err.Error())
	case errors.Is(err, auth.ErrInvalidChallenge):
		writeError(w, r, http.StatusBadRequest, codeInvalidChallenge, "the challenge is invalid or expired: sign in again")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, r, http.StatusUnauthorized, codeInvalidCreds, err.Error())
	case errors.Is(err, auth.ErrPasswordExpired):
		var details []Detail
		if pe := (*auth.PasswordExpiredError)(nil); errors.As(err, &pe) && pe.EmailQueued {
			// The reset email is on its way: the app can say so.
			details = append(details, Detail{Path: "reset_email", Reason: "queued"})
		}
		writeError(w, r, http.StatusUnauthorized, codePasswordExpired, err.Error(), details...)
	case errors.Is(err, auth.ErrSameEmail):
		writeError(w, r, http.StatusBadRequest, codeValidation, err.Error(), Detail{Path: "new_email", Reason: "is already the address of this account"})
	case errors.As(err, &el):
		w.Header().Set("Retry-After", strconv.Itoa(max(int((el.RetryAfter+time.Second-1)/time.Second), 1)))
		writeError(w, r, http.StatusTooManyRequests, codeTooManyRequests, "too many emails; retry later")
	case errors.Is(err, auth.ErrEmailNotVerified):
		writeError(w, r, http.StatusForbidden, codeEmailNotVerified, err.Error())
	case errors.Is(err, auth.ErrUnauthenticated):
		unauthenticated(w, r, true, err.Error())
	case errors.Is(err, auth.ErrSSORequired):
		// The organization's people sign in through its connection: the app shows only its buttons.
		writeError(w, r, http.StatusBadRequest, codeSSORequired, err.Error())
	case errors.Is(err, auth.ErrSSOSession):
		writeError(w, r, http.StatusForbidden, codeSSOSession, err.Error())
	case errors.Is(err, auth.ErrSSONotReady):
		writeError(w, r, http.StatusConflict, codeSSONotReady, err.Error())
	case errors.Is(err, auth.ErrSessionUnscoped):
		// The session is good but has no organization (it is from before sessions were scoped): the
		// person signs in again, to one.
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+chi.URLParam(r, "realm")+`", error="invalid_token"`)
		writeError(w, r, http.StatusUnauthorized, codeSessionUnscoped, err.Error())
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

// sessionJSON is the answer to a login or sign-up. With no token (the session
// went into a cookie) the response doesn't have the token fields at all.
func sessionJSON(p auth.Principal, token, locale string) map[string]any {
	out := map[string]any{
		"session_id": p.Session.ID,
		"expires_at": formatTime(p.Session.ExpiresAt),
		"user":       userJSON(p.User, locale),
	}
	if token != "" {
		out["token"] = token
		out["token_type"] = "Bearer"
	}
	if p.Session.Org != "" {
		out["org"] = p.Session.Org
	}
	return out
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

// sessionOrgJSON is the organization a session is scoped to as its person sees it: what to call it
// and what they are there. When they are not a member any more only the id is left.
func (a *authAPI) sessionOrgJSON(r *http.Request, userID, org string) map[string]any {
	out := map[string]any{"id": org}
	m, err := usersOf(r).MembershipOf(r.Context(), userID, org)
	if err != nil {
		return out
	}
	out["name"], out["slug"] = m.Org.Name, nil
	if m.Org.Slug != "" {
		out["slug"] = m.Org.Slug
	}
	out["roles"], out["teams"] = nonNil(m.RoleNames()), nonNil(m.Member.Teams)
	// Whether the organization asks for a second factor and this session has one: the app uses it
	// to ask for the factor.
	out["require_mfa"] = m.Org.RequireMFA
	out["mfa_met"] = !m.Org.RequireMFA || principalOf(r).Session.MFA
	return out
}
