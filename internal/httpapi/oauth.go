package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/oauth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Sign-in with external identity providers: the redirect flow. The app sends the user
// to backd's start; backd sends them to the provider; the provider sends them back to
// the callback; backd sends them on to the app with a one-time login code (or an error
// code), which the app redeems for a session. A session never appears in a URL.

// oauthRateLimit is how many starts, redemptions and native sign-ins one address may
// make a minute.
const oauthRateLimit = 30

// The query parameters backd adds to the app's redirect_to. They carry the prefix so that
// they can't collide with the app's own, since the app may be any page.
const (
	paramCode     = "backd_code"     // the one-time login code
	paramError    = "backd_error"    // what went wrong
	paramProvider = "backd_provider" // with account_exists: the provider that was used
	paramLinked   = "backd_linked"   // a link succeeded: the provider
)

func (a *authAPI) oauthRoutes(r chi.Router, json func(http.Handler) http.Handler) {
	oauthRoutes := func(r chi.Router) {
		r.Get("/oauth/{provider}/start", a.oauthStartGet)
		r.With(json).Post("/oauth/{provider}/start", a.oauthStartPost)
		r.Get("/oauth/{provider}/callback", a.oauthCallback)
		r.Post("/oauth/{provider}/callback", a.oauthCallback) // Apple answers with a form
		r.With(json).Post("/oauth/{provider}/id-token", a.oauthIDToken)
		r.Get("/providers", a.providerList)
	}
	oauthRoutes(r)
	// The same flow for the providers an organization's managers connected (design/org-sso.md).
	r.Route("/orgs/{org}", oauthRoutes)
	r.With(json).Post("/token", a.loginCodeToken)
}

// signInScope is the place a sign-in is about: the realm, with the providers realm.yaml
// declares, or one organization, with the providers of its enabled connections. Both are
// resolved by one function, so the list of providers to offer and the routes that sign in can't
// disagree.
type signInScope struct {
	org       string // the organization's id; "" for the realm
	providers map[string]*registry.Provider
	conns     map[string]auth.OrgConnection
}

// scope resolves the place a request is about. ok is false when it can't be, for a realm
// route never (the realm exists) and for an organization's when the realm has no organization
// sign-in, the organization is unknown, deleted or orphaned, or has no usable connection:
// callers answer all of those the same.
func (a *authAPI) scope(r *http.Request) (*signInScope, bool) {
	svc := usersOf(r)
	ref := chi.URLParam(r, "org")
	if ref == "" {
		return &signInScope{providers: svc.Settings.Providers}, true
	}
	o := svc.Settings.Organizations
	if o == nil || o.SSO == nil {
		return nil, false
	}
	org, err := svc.Store.OrgByID(r.Context(), ref)
	if errors.Is(err, auth.ErrNotFound) {
		org, err = svc.Store.OrgBySlug(r.Context(), ref)
	}
	if err != nil {
		if !errors.Is(err, auth.ErrNotFound) {
			logger(r.Context()).Error("read an organization for a sign-in", "error", err)
		}
		return nil, false
	}
	if org.Status != auth.OrgActive {
		return nil, false
	}
	list, err := svc.Store.OrgConnections(r.Context(), org.ID)
	if err != nil {
		logger(r.Context()).Error("read the connections of an organization", "error", err)
		return nil, false
	}
	sc := &signInScope{org: org.ID, providers: map[string]*registry.Provider{}, conns: map[string]auth.OrgConnection{}}
	for _, c := range list {
		if !c.Enabled || !o.SSO.Allows(c.Provider) {
			continue
		}
		sc.providers[c.Provider] = &registry.Provider{
			Name: c.Provider, ClientID: c.ClientID, ClientSecret: auth.SSOSecretName(c.Provider),
			NativeClientIDs: c.NativeClientIDs, Tenant: c.Tenant, Org: org.ID,
		}
		sc.conns[c.Provider] = c
	}
	return sc, true
}

// provider returns the provider named in the path, of the realm or of the organization the
// route is about, or answers 404. An organization's answers the same whatever is missing.
func (a *authAPI) provider(w http.ResponseWriter, r *http.Request) (*registry.Provider, *signInScope, bool) {
	sc, ok := a.scope(r)
	if ok {
		if p := sc.providers[chi.URLParam(r, "provider")]; p != nil {
			return p, sc, true
		}
	}
	if chi.URLParam(r, "org") != "" {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this organization has no such sign-in provider")
	} else {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no such sign-in provider")
	}
	return nil, nil, false
}

// providerUsable says whether a sign-in through p could start now: its secrets are set and
// backd's public address is known. It is the check start makes, so the list and start agree.
func (a *authAPI) providerUsable(ctx context.Context, svc *auth.Users, realm string, p *registry.Provider) bool {
	if _, ok := a.callbackURL(realm, p); !ok {
		return false
	}
	_, ok, err := a.providerSecrets(ctx, svc, p)
	return err == nil && ok
}

// providerList answers GET /_auth/providers and GET /_auth/orgs/{org}/providers: the names of
// the external providers a client may offer now, nothing else. An unknown organization, one
// without connections and one with sign-in off all answer an empty list.
func (a *authAPI) providerList(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if err := svc.RateLimit(r.Context(), "oauth-providers:"+clientIP(r), providersRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	items := []string{}
	if sc, ok := a.scope(r); ok {
		for _, name := range slices.Sorted(maps.Keys(sc.providers)) {
			if a.providerUsable(r.Context(), svc, chi.URLParam(r, "realm"), sc.providers[name]) {
				items = append(items, name)
			}
		}
	}
	// The same for everybody who asks, so a proxy may keep it for a minute.
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// providersRateLimit is how many times one address may list the providers a minute: apps ask
// when a sign-in page opens.
const providersRateLimit = 60

// providerSecrets reads the realm secrets the provider needs, by name; ok is false
// when one is not set (the provider is then unavailable).
func (a *authAPI) providerSecrets(ctx context.Context, svc *auth.Users, p *registry.Provider) (map[string]string, bool, error) {
	if p.Org != "" {
		// An organization's secret is read where only the sign-in code looks.
		v, ok, err := svc.OrgSecret(ctx, p.Org, p.ClientSecret)
		if err != nil || !ok {
			return nil, false, err
		}
		return map[string]string{p.ClientSecret: v}, true, nil
	}
	names := p.SecretNames()
	refs := make([]registry.SecretRef, len(names))
	for i, n := range names {
		refs[i] = registry.SecretRef{Realm: true, Name: n}
	}
	values, missing, err := svc.ResolveSecrets(ctx, refs, "")
	if err != nil || len(missing) > 0 {
		return nil, false, err
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		out[n] = values["realm."+n]
	}
	return out, true, nil
}

// callbackURL is the address the provider sends the user back to: backd's public
// address (the realm's email.public_url, else BACKD_URL) and the callback path.
func (a *authAPI) callbackURL(realm string, p *registry.Provider) (string, bool) {
	base := a.baseURL
	if rl := a.reg.Realms[realm]; rl != nil && rl.Settings.Email != nil && rl.Settings.Email.PublicURL != "" {
		base = rl.Settings.Email.PublicURL
	}
	if base == "" {
		return "", false
	}
	if p.Org != "" {
		return strings.TrimSuffix(base, "/") + registry.OrgProviderCallbackPath(realm, p.Org, p.Name), true
	}
	return strings.TrimSuffix(base, "/") + registry.ProviderCallbackPath(realm, p.Name), true
}

func (a *authAPI) providerUnavailable(w http.ResponseWriter, r *http.Request, why string) {
	writeError(w, r, http.StatusServiceUnavailable, codeProviderUnavail, why)
}

// startInput is what a start call carries.
type startInput struct {
	redirectTo, challenge, intent, invitation, locale string
	// consents are accepted for the account the sign-in may make (POST only).
	consents map[string]string
}

func (a *authAPI) oauthStartGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("intent") != "" && q.Get("intent") != auth.IntentSignIn {
		writeError(w, r, http.StatusBadRequest, codeValidation, "linking a provider needs a POST (it carries the session)", Detail{Path: "intent", Reason: "only signin is allowed in a GET"})
		return
	}
	in := startInput{redirectTo: q.Get("redirect_to"), challenge: q.Get("code_challenge"), intent: auth.IntentSignIn, invitation: q.Get("invitation"), locale: q.Get("locale")}
	if u, ok := a.startOAuth(w, r, in, auth.Principal{}); ok {
		w.Header().Set("Location", u)
		w.WriteHeader(http.StatusFound)
	}
}

func (a *authAPI) oauthStartPost(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"redirect_to": "string?", "code_challenge": "string", "intent": "string?", "invitation": "string?", "locale": "string?"})
	if !ok {
		return
	}
	str := func(k string) string { v, _ := f[k].(string); return v }
	in := startInput{redirectTo: str("redirect_to"), challenge: str("code_challenge"), intent: str("intent"), invitation: str("invitation"), locale: str("locale"), consents: consents}
	if in.intent == "" {
		in.intent = auth.IntentSignIn
	}
	var who auth.Principal
	if in.intent == auth.IntentLink {
		if who, ok = a.authenticateRequest(w, r); !ok {
			return
		}
	}
	if u, ok := a.startOAuth(w, r, in, who); ok {
		writeJSON(w, http.StatusOK, map[string]any{"authorize_url": u})
	}
}

// startOAuth checks a start call, remembers the attempt and returns the provider's
// authorize URL; it answers and returns false when it can't.
func (a *authAPI) startOAuth(w http.ResponseWriter, r *http.Request, in startInput, who auth.Principal) (string, bool) {
	svc := usersOf(r)
	p, _, ok := a.provider(w, r)
	if !ok {
		return "", false
	}
	var details []Detail
	if in.intent != auth.IntentSignIn && in.intent != auth.IntentLink {
		details = append(details, Detail{Path: "intent", Reason: "must be signin or link"})
	}
	if !auth.ValidPKCEChallenge(in.challenge) {
		details = append(details, Detail{Path: "code_challenge", Reason: "is required: the S256 PKCE challenge of a verifier only your app holds"})
	}
	if in.invitation != "" && !strings.HasPrefix(in.invitation, auth.InvitationPrefix) {
		details = append(details, Detail{Path: "invitation", Reason: "isn't an invitation token"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the sign-in isn't described", details...)
		return "", false
	}
	if in.redirectTo == "" {
		in.redirectTo = svc.Settings.SignInAppURL
		if in.redirectTo == "" {
			writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to is required: the realm has no sign_in.app_url to go back to")
			return "", false
		}
	}
	if !svc.Settings.SignInRedirectAllowed(in.redirectTo) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to isn't within the realm's sign_in.allowed_redirects (or email.allowed_redirects)")
		return "", false
	}
	if err := svc.RateLimit(r.Context(), "oauth-start:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return "", false
	}
	if _, ok, err := a.providerSecrets(r.Context(), svc, p); err != nil || !ok {
		if err != nil {
			logger(r.Context()).Error("read the secrets of a sign-in provider", "provider", p.Name, "error", err)
		}
		if p.Org != "" {
			// A connection without its secret answers as one that isn't there.
			writeError(w, r, http.StatusNotFound, codeNotFound, "this organization has no such sign-in provider")
			return "", false
		}
		a.providerUnavailable(w, r, "this provider's secrets are not set")
		return "", false
	}
	callback, ok := a.callbackURL(chi.URLParam(r, "realm"), p)
	if !ok {
		a.providerUnavailable(w, r, "backd's public address is unknown (BACKD_URL)")
		return "", false
	}
	if err := svc.ValidateConsents(in.consents); err != nil {
		authError(w, r, err)
		return "", false
	}
	st := auth.OAuthState{Provider: p.Name, Org: p.Org, Intent: in.intent, CodeChallenge: in.challenge, RedirectTo: in.redirectTo, LinkUserID: who.User.ID,
		Locales: append([]string{in.locale}, acceptLanguages(r.Header.Get("Accept-Language"))...), Consents: in.consents}
	if in.invitation != "" {
		st.InvitationID = auth.HashToken(in.invitation)
	}
	state, nonce, verifier, err := svc.BeginOAuth(r.Context(), st)
	if err != nil {
		authError(w, r, err)
		return "", false
	}
	params := oauth.AuthorizeParams{RedirectURI: callback, State: state, Nonce: nonce}
	if oauth.UsesPKCE(p) {
		params.CodeChallenge = auth.PKCEChallenge(verifier)
	}
	w.Header().Set("Cache-Control", "no-store")
	return a.oauth.AuthorizeURL(p, params), true
}

// The codes a sign-in that did not work gives the app, as ?error=.
const (
	oauthCancelled         = "cancelled"
	oauthAccountExists     = "account_exists"
	oauthSignupClosed      = "signup_closed"
	oauthInvitationInvalid = "invitation_invalid"
	oauthLinkConflict      = "link_conflict"
	oauthEmailNotVerified  = "email_not_verified"
	oauthEmailRequired     = "email_required"
	oauthSignInRefused     = "signin_refused"
	oauthProviderError     = "provider_error"
	// oauthNotAllowed: the person is not of the organization the connection admits.
	oauthNotAllowed = "not_allowed"
)

// oauthCallback is where the provider sends the user back to.
func (a *authAPI) oauthCallback(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if err := r.ParseForm(); err != nil {
		a.oauthErrorPage(w, r)
		return
	}
	get := func(k string) string { return r.Form.Get(k) }
	st, err := svc.TakeOAuthState(r.Context(), get("state"))
	// The attempt must finish where it began: the same provider, and the same organization (or
	// none), so a code started for one organization can't be finished at another's callback.
	sc, scoped := a.scope(r)
	if err != nil || st.Provider != chi.URLParam(r, "provider") || !scoped || st.Org != sc.org {
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			logger(r.Context()).Error("read a sign-in attempt", "error", err)
		}
		a.oauthErrorPage(w, r)
		return
	}
	// From here the app is known: whatever goes wrong goes back to it.
	back := func(params ...string) {
		a.redirectToApp(w, r, st.RedirectTo, params...)
	}
	p, ok := sc.providers[st.Provider]
	if !ok {
		back(paramError, oauthProviderError)
		return
	}
	if e := get("error"); e != "" {
		if e == "access_denied" || e == "user_cancelled_authorize" {
			back(paramError, oauthCancelled)
		} else {
			back(paramError, oauthProviderError)
		}
		return
	}
	code := get("code")
	callback, ok := a.callbackURL(chi.URLParam(r, "realm"), p)
	secrets, ok2, err := a.providerSecrets(r.Context(), svc, p)
	if code == "" || !ok || !ok2 || err != nil {
		logger(r.Context()).Error("sign-in with a provider can't go on", "provider", p.Name, "code_given", code != "", "secrets_set", ok2, "error", err)
		back(paramError, oauthProviderError)
		return
	}
	secret := secrets[p.ClientSecret]
	if p.Name == registry.ProviderApple {
		if secret, err = a.oauth.AppleClientSecret(p, p.ClientID, secrets[p.PrivateKey]); err != nil {
			logger(r.Context()).Error("sign Apple's client secret", "error", err)
			back(paramError, oauthProviderError)
			return
		}
	}
	verifier := ""
	if oauth.UsesPKCE(p) {
		verifier = st.Verifier
	}
	tokens, err := a.oauth.Exchange(r.Context(), p, p.ClientID, secret, code, callback, verifier)
	if err != nil {
		logger(r.Context()).Warn("the provider didn't give a token", "provider", p.Name, "error", err)
		back(paramError, oauthProviderError)
		return
	}
	claims, err := a.oauth.Verify(r.Context(), p, tokens.IDToken, st.Nonce, false)
	if err != nil {
		logger(r.Context()).Warn("the provider's ID token is not valid", "provider", p.Name, "error", err)
		back(paramError, oauthProviderError)
		return
	}
	if conn, org := sc.conns[p.Name], p.Org != ""; org && !conn.Admits(signInClaimsOf(claims)) {
		logger(r.Context()).Info("a person the connection does not admit", "provider", p.Name, "org", p.Org)
		back(paramError, oauthNotAllowed)
		return
	}
	res, err := svc.ResolveProviderLogin(r.Context(), auth.ProviderLogin{
		Provider: p.Name, Org: p.Org, Subject: claims.Subject, Email: claims.Email, EmailVerified: claims.EmailVerified, TrustsEmail: p.TrustsEmail(),
		Intent: st.Intent, LinkUserID: st.LinkUserID, Invitation: st.InvitationID, Locales: st.Locales, IP: clientIP(r),
		Consents: st.Consents, UserAgent: r.UserAgent(),
		Profile: profileOf(claims, firstAuthorization(p, get("user"))),
	})
	if err != nil {
		a.oauthFailure(w, r, svc, st, p, err)
		return
	}
	a.keepAppleToken(r, svc, p, res.User.ID, p.ClientID, tokens.RefreshToken)
	if st.Intent == auth.IntentLink {
		back(paramLinked, p.Name)
		return
	}
	loginCode, err := svc.IssueLoginCode(r.Context(), res.User.ID, p.Name, st.CodeChallenge, res.NewUser, res.Profile)
	if err != nil {
		logger(r.Context()).Error("issue a login code", "error", err)
		back(paramError, oauthProviderError)
		return
	}
	setActor(r.Context(), "user:"+res.User.ID)
	back(paramCode, loginCode)
}

// signInClaimsOf is what a connection's restriction reads from a verified token.
func signInClaimsOf(c oauth.Claims) auth.SignInClaims {
	return auth.SignInClaims{Email: c.Email, EmailVerified: c.EmailVerified, HostedDomain: c.HostedDomain, Tenant: c.Tenant}
}

// keepAppleToken keeps Apple's refresh token for revoking it when the account goes away
// (unless the provider is configured not to). A failure is logged, not the sign-in's.
func (a *authAPI) keepAppleToken(r *http.Request, svc *auth.Users, p *registry.Provider, userID, clientID, refreshToken string) {
	if p.Name != registry.ProviderApple || refreshToken == "" {
		return
	}
	if err := svc.SaveAppleToken(r.Context(), userID, clientID, refreshToken); err != nil {
		logger(r.Context()).Error("keep Apple's refresh token for revocation", "error", err)
	}
}

// oauthFailure sends the app the code for a sign-in the rules refuse.
func (a *authAPI) oauthFailure(w http.ResponseWriter, r *http.Request, svc *auth.Users, st auth.OAuthState, p *registry.Provider, err error) {
	var refused *auth.RefusedError
	code := oauthProviderError
	switch {
	case errors.As(err, &refused):
		code = oauthSignInRefused
		svc.Audit(r.Context(), auth.AuditSignInRefused, "user:"+refused.UserID, map[string]any{"provider": p.Name, "reason": refused.Reason})
	case errors.Is(err, auth.ErrAccountExists):
		a.redirectToApp(w, r, st.RedirectTo, paramError, oauthAccountExists, paramProvider, p.Name)
		return
	case errors.Is(err, auth.ErrSignupClosed):
		code = oauthSignupClosed
	case errors.Is(err, auth.ErrInvitationRequired), errors.Is(err, auth.ErrInvalidInvitation):
		code = oauthInvitationInvalid
	case errors.Is(err, auth.ErrLinkConflict):
		code = oauthLinkConflict
	case errors.Is(err, auth.ErrEmailNotVerified):
		code = oauthEmailNotVerified
	case errors.Is(err, auth.ErrEmailRequired):
		code = oauthEmailRequired
	default:
		logger(r.Context()).Error("sign in with a provider", "provider", p.Name, "error", err)
	}
	a.redirectToApp(w, r, st.RedirectTo, paramError, code)
}

// redirectToApp sends the browser to the app's redirect_to (checked when the attempt
// began) with the given query parameters added.
func (a *authAPI) redirectToApp(w http.ResponseWriter, r *http.Request, to string, params ...string) {
	u, err := url.Parse(to)
	if err != nil {
		a.oauthErrorPage(w, r)
		return
	}
	q := u.Query()
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	u.RawQuery = q.Encode()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	status := http.StatusFound
	if r.Method == http.MethodPost {
		status = http.StatusSeeOther // the provider's POST becomes the browser's GET
	}
	h.Set("Location", u.String())
	w.WriteHeader(status)
}

// oauthErrorPage answers an attempt that can't be traced to an app: an unknown, used,
// expired or foreign state.
func (a *authAPI) oauthErrorPage(w http.ResponseWriter, r *http.Request) {
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	locale := rl.Settings.BestLocale(acceptLanguages(r.Header.Get("Accept-Language"))...)
	data := email.PageData{Realm: chi.URLParam(r, "realm"), Locale: locale, Kind: email.PageOAuthError}
	var body string
	var err error
	if rl.Pages != nil && rl.Pages.Has(email.PageOAuthError, locale) {
		body, err = rl.Pages.Render(email.PageOAuthError, locale, data)
	} else {
		data.Locale = "en"
		body, err = email.RenderBuiltin(email.PageOAuthError, data)
	}
	if err != nil {
		logger(r.Context()).Error("render the sign-in error page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pageHeaders(w)
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(body))
}

// loginCodeToken redeems a one-time login code for a session: POST /_auth/token.
func (a *authAPI) loginCodeToken(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"code": "string", "code_verifier": "string", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	if err := svc.RateLimit(r.Context(), "oauth-token:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	red, err := svc.RedeemLoginCode(r.Context(), f["code"].(string), f["code_verifier"].(string))
	if err != nil {
		authError(w, r, err)
		return
	}
	a.answerSignIn(w, r, red, http.StatusOK, useCookie, true)
}

// providerSessionJSON is a session given by a provider sign-in: a login's answer plus whether
// the sign-in made the user and, if so, the profile the provider gave. The profile is handed
// over here once and kept nowhere.
func providerSessionJSON(p auth.Principal, token, locale string, newUser bool, profile map[string]string) map[string]any {
	out := sessionJSON(p, token, locale)
	out["new_user"] = newUser
	if newUser && len(profile) > 0 {
		out["profile"] = profile
	}
	return out
}

// profileOf is what the provider says of the person: the ID token's claims, and, for Apple's
// first authorization, the name Apple sends in the callback form (never in the token).
func profileOf(c oauth.Claims, extra map[string]string) map[string]string {
	in := map[string]string{"name": c.Name, "given_name": c.GivenName, "family_name": c.FamilyName, "picture": c.Picture}
	for k, v := range extra {
		if in[k] == "" {
			in[k] = v
		}
	}
	return auth.CleanProfile(in)
}

// firstAuthorization is the name Apple posts with a person's first authorization of the app;
// no other provider sends such a field, and one sent to them is ignored.
func firstAuthorization(p *registry.Provider, raw string) map[string]string {
	if p.Name != registry.ProviderApple {
		return nil
	}
	return appleFirstAuthorization(raw)
}

// appleFirstAuthorization reads the "user" field Apple posts to the callback the first time a
// person authorizes the app: {"name": {"firstName": …, "lastName": …}}.
func appleFirstAuthorization(raw string) map[string]string {
	if raw == "" || len(raw) > 4096 {
		return nil
	}
	var u struct {
		Name struct {
			First string `json:"firstName"`
			Last  string `json:"lastName"`
		} `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &u) != nil {
		return nil
	}
	out := map[string]string{"given_name": u.Name.First, "family_name": u.Name.Last}
	if full := strings.TrimSpace(u.Name.First + " " + u.Name.Last); full != "" {
		out["name"] = full
	}
	return out
}
