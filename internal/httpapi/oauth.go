package httpapi

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/httpsso"
)

// Sign-in with external identity providers: the redirect flow. The app sends the user
// to backd's start; backd sends them to the provider; the provider sends them back to
// the callback; backd sends them on to the app with a one-time login code (or an error
// code), which the app redeems for a session. A session never appears in a URL.

// oauthRateLimit is how many starts, redemptions and native sign-ins one address may
// make a minute.
const oauthRateLimit = 30

// The query parameters backd adds to the app's redirect_to are the engine's (sso.ParamCode, …);
// the magic-link flow ends the same way, with the same names.
const (
	paramCode  = sso.ParamCode
	paramError = sso.ParamError
	paramOrg   = sso.ParamOrg
)

// The codes a sign-in that did not work gives the app, as backd_error.
const (
	oauthProviderError = sso.CodeProviderError
	oauthSignInRefused = sso.CodeSignInRefused
	oauthSignupClosed  = sso.CodeSignupClosed
)

func (a *authAPI) oauthRoutes(r chi.Router, json func(http.Handler) http.Handler) {
	oauthRoutes := func(r chi.Router) {
		r.With(a.signInOfOrgs).Get("/oauth/{provider}/start", a.oauthStartGet)
		r.With(json, a.signInOfOrgs).Post("/oauth/{provider}/start", a.oauthStartPost)
		// The provider sends the person back to one address, the realm's: the attempt knows the organization.
		r.Get("/oauth/{provider}/callback", a.oauthCallback)
		r.Post("/oauth/{provider}/callback", a.oauthCallback) // Apple answers with a form
		r.With(json, a.signInOfOrgs).Post("/oauth/{provider}/id-token", a.oauthIDToken)
		r.With(a.signInOfOrgs).Get("/providers", a.providerList)
	}
	oauthRoutes(r)
	// The same flow for the providers an organization's managers connected (design/sso.md §9).
	r.Route("/orgs/{org}", func(r chi.Router) {
		oauthRoutes(r)
		a.orgSignInRoutes(r, json)
	})
	// A new person starts a company, and anyone can ask where they belong.
	r.With(json).Post("/orgs", a.signUpWithOrg)
	r.With(json).Post("/orgs/find", a.findOrgs)
	r.With(json).Post("/token", a.loginCodeToken)
}

// signInMethod is how a session records the first factor of a provider sign-in: the provider's
// name, or, through an organization's connection, the connection itself, so that session is never
// taken for one opened by the realm's own provider.
// strategyMethodOf is the method a provider's sign-in starts a strategy with: the provider's name,
// or sso for a connection of an organization.
func strategyMethodOf(p *registry.Provider) string {
	if p.Org != "" {
		return registry.MethodSSO
	}
	return p.Name
}

func signInMethod(p *registry.Provider) string {
	if p.Org != "" {
		return auth.ConnectionMethod(p.Org, p.Name)
	}
	return p.Name
}

// signInScope is the place a sign-in is about: the realm, with the providers realm.yaml
// declares, or one organization, with the providers of its enabled connections added to the
// realm's. Both are resolved by one function, so the list of providers to offer and the routes
// that sign in can't disagree.
type signInScope struct {
	org string // the organization's id; "" for the realm, and for an organization that can't be signed in to
	// ssoRequired: the organization's people sign in through its connection only.
	ssoRequired bool
	providers   map[string]*registry.Provider
	conns       map[string]auth.OrgConnection
}

// scope resolves the place a request is about. The organization of an organization's route is
// named by its id or its slug; an unknown, deleted or inactive one resolves to a scope with no
// organization and the realm's providers, which is what an organization without connections
// looks like, so nothing says whether it exists. Sign-ins to it end in the same refusal as one
// to an organization the person does not belong to. ok is false only when the realm has no
// organizations.
func (a *authAPI) scope(r *http.Request) (*signInScope, bool) {
	svc := usersOf(r)
	ref := chi.URLParam(r, "org")
	sc := &signInScope{providers: maps.Clone(svc.Settings.Providers), conns: map[string]auth.OrgConnection{}}
	if sc.providers == nil {
		sc.providers = map[string]*registry.Provider{}
	}
	if ref == "" {
		return sc, true
	}
	o := svc.Settings.Organizations
	if o == nil {
		return nil, false
	}
	org, err := svc.OrgByRef(r.Context(), ref)
	if err != nil {
		if !errors.Is(err, auth.ErrNotFound) {
			logger(r.Context()).Error("read an organization for a sign-in", "error", err)
		}
		return sc, true
	}
	sc.org = org.ID
	sc.ssoRequired = org.SSORequired && o.SSO != nil
	if o.SSO == nil || org.Status != auth.OrgActive {
		return sc, true
	}
	list, err := svc.Store.OrgConnections(r.Context(), org.ID)
	if err != nil {
		logger(r.Context()).Error("read the connections of an organization", "error", err)
		return sc, true
	}
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
			// The realm's own providers are no way into an organization that requires its connection.
			if sc.ssoRequired && p.Org == "" {
				authError(w, r, auth.ErrSSORequired)
				return nil, nil, false
			}
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
func (a *authAPI) providerUsable(ctx context.Context, host *auth.SSOHost, sc *signInScope, p *registry.Provider) bool {
	if _, ok := a.callbackURL(host.Realm, p); !ok {
		return false
	}
	_, ok, err := host.ProviderOf(ctx, p, connOf(sc, p))
	return err == nil && ok
}

// connOf is the connection a provider of the scope stands for, nil for the realm's own.
func connOf(sc *signInScope, p *registry.Provider) *auth.OrgConnection {
	if p.Org == "" {
		return nil
	}
	c := sc.conns[p.Name]
	return &c
}

// host is the realm's side of the sso engine's ports for a request.
func (a *authAPI) host(r *http.Request) *auth.SSOHost {
	realm := chi.URLParam(r, "realm")
	base := a.baseURL
	if rl := a.reg.Realms[realm]; rl != nil && rl.Settings.Email != nil && rl.Settings.Email.PublicURL != "" {
		base = rl.Settings.Email.PublicURL
	}
	return &auth.SSOHost{Users: usersOf(r), Realm: realm, BaseURL: base, Endpoints: a.endpoints}
}

// providerList answers GET /_auth/providers and GET /_auth/orgs/{org}/providers: the names of
// the external providers a client may offer now and, for an organization, the other ways of
// signing in to it. An unknown organization, one without connections and one with sign-in off
// all answer alike: the realm's providers and methods.
func (a *authAPI) providerList(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if err := svc.RateLimit(r.Context(), "oauth-providers:"+clientIP(r), providersRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	items := []string{}
	if sc, ok := a.scope(r); ok {
		for _, name := range slices.Sorted(maps.Keys(sc.providers)) {
			if sc.ssoRequired && sc.providers[name].Org == "" {
				continue // only the connection's buttons
			}
			if a.providerUsable(r.Context(), a.host(r), sc, sc.providers[name]) {
				items = append(items, name)
			}
		}
	}
	body := map[string]any{"items": items}
	if chi.URLParam(r, "org") != "" {
		methods := []string{"password"}
		if svc.Settings.MagicLink.Enabled && svc.Settings.Email != nil {
			methods = append(methods, "magic_link")
		}
		required := false
		if sc, ok := a.scope(r); ok && sc.ssoRequired {
			methods, required = []string{}, true // nothing but the connection
		}
		body["methods"] = methods
		body["sso_required"] = required
	}
	// The same for everybody who asks, so a proxy may keep it for a minute.
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, body)
}

// providersRateLimit is how many times one address may list the providers a minute: apps ask
// when a sign-in page opens.
const providersRateLimit = 60

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
	redirectTo, challenge, intent, invitation, locale, strategy string
	// consents are accepted for the account the sign-in may make (POST only).
	consents map[string]string
}

func (a *authAPI) oauthStartGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("intent") != "" && q.Get("intent") != auth.IntentSignIn {
		writeError(w, r, http.StatusBadRequest, codeValidation, "linking a provider needs a POST (it carries the session)", Detail{Path: "intent", Reason: "only signin is allowed in a GET"})
		return
	}
	in := startInput{redirectTo: q.Get("redirect_to"), challenge: q.Get("code_challenge"), intent: auth.IntentSignIn, invitation: q.Get("invitation"), locale: q.Get("locale"), strategy: q.Get("strategy")}
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
	f, ok := fields(w, r, obj, map[string]string{"redirect_to": "string?", "code_challenge": "string", "intent": "string?", "invitation": "string?", "locale": "string?", "strategy": "string?"})
	if !ok {
		return
	}
	str := func(k string) string { v, _ := f[k].(string); return v }
	in := startInput{redirectTo: str("redirect_to"), challenge: str("code_challenge"), intent: str("intent"), invitation: str("invitation"), locale: str("locale"), strategy: str("strategy"), consents: consents}
	if in.intent == "" {
		in.intent = auth.IntentSignIn
	}
	var who auth.Principal
	if in.intent == auth.IntentLink {
		if who, ok = a.authenticateRequest(w, r); !ok {
			return
		}
		// Adding a sign-in method changes what protects the account.
		if !a.notViaConnection(w, r, who) {
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
	p, sc, ok := a.provider(w, r)
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
	if in.invitation != "" && !strings.HasPrefix(in.invitation, invitationPrefixOf(r)) {
		details = append(details, Detail{Path: "invitation", Reason: "isn't an invitation token"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the sign-in isn't described", details...)
		return "", false
	}
	if in.redirectTo == "" {
		in.redirectTo = svc.Settings.SignInAppURL
		if in.redirectTo == "" {
			writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to is required: the realm has no auth.sign_in.app_url to go back to")
			return "", false
		}
	}
	if !svc.Settings.SignInRedirectAllowed(in.redirectTo) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to isn't within the realm's auth.sign_in.allowed_redirects (or email.allowed_redirects)")
		return "", false
	}
	// A sign-in with a provider follows a strategy that starts with it (an organization's connection:
	// with sso); linking signs nobody in.
	strategy := ""
	if in.intent == auth.IntentSignIn {
		st, err := svc.ChooseStrategy(in.strategy, strategyMethodOf(p))
		if err != nil {
			authError(w, r, err)
			return "", false
		}
		strategy = st.Name
	}
	if err := svc.RateLimit(r.Context(), "oauth-start:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return "", false
	}
	host := a.host(r)
	sp, ok, err := host.ProviderOf(r.Context(), p, connOf(sc, p))
	if err != nil || !ok {
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
	hd := auth.SSOHostData{ScopeOrg: routeScope(r, sc), Locales: append([]string{in.locale}, acceptLanguages(r.Header.Get("Accept-Language"))...), Consents: in.consents}
	if in.invitation != "" {
		hd.Invitation = auth.HashToken(in.invitation)
	}
	authorizeURL, err := host.Engine(a.oauth).Start(r.Context(), sso.StartInput{
		Scope: auth.SSOScopeOf(p.Org), Provider: sp, CallbackURL: callback, Intent: sso.Intent(in.intent), Strategy: strategy,
		Challenge: in.challenge, RedirectTo: in.redirectTo, LinkUserID: who.User.ID, Host: hd.Encode(),
	})
	if err != nil {
		authError(w, r, err)
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	return authorizeURL, true
}

// oauthCallback is where the provider sends the user back to. The engine runs the rest; this
// only says where the callback arrived and shows what came out.
func (a *authAPI) oauthCallback(w http.ResponseWriter, r *http.Request) {
	scope := ""
	if sc, ok := a.scope(r); ok {
		scope = auth.SSOScopeOf(sc.org)
	}
	in, ok := httpsso.Callback(r, scope, chi.URLParam(r, "provider"))
	if !ok {
		a.oauthErrorPage(w, r)
		return
	}
	in.Request = sso.Request{IP: clientIP(r), UserAgent: r.UserAgent()}
	out := a.host(r).Engine(a.oauth).Callback(r.Context(), in)
	if out.Err != nil {
		logger(r.Context()).Warn("sign in with a provider", "provider", in.Provider, "error", out.Err)
	}
	if out.Page {
		a.oauthErrorPage(w, r)
		return
	}
	if out.UserID != "" {
		setActor(r.Context(), "user:"+out.UserID)
	}
	a.redirectToApp(w, r, out.RedirectTo, out.Params...)
}

// redirectToApp sends the browser to the app's redirect_to (checked when the attempt
// began) with the given query parameters added.
func (a *authAPI) redirectToApp(w http.ResponseWriter, r *http.Request, to string, params ...string) {
	if !httpsso.Redirect(w, r, to, params...) {
		a.oauthErrorPage(w, r)
	}
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

// invitationPrefixOf is what an invitation token starts with on this route: an organization's
// route takes the organization's invitations, the realm's takes account invitations.
func invitationPrefixOf(r *http.Request) string {
	if chi.URLParam(r, "org") != "" {
		return auth.OrgInvitationPrefix
	}
	return auth.InvitationPrefix
}

// routeScope is the organization a sign-in started at this route is for: the id of the one the
// address names, unknownOrgScope when it names one that can't be signed in to, and "" for the
// realm's address.
func routeScope(r *http.Request, sc *signInScope) string {
	if chi.URLParam(r, "org") != "" && sc.org == "" {
		return auth.UnknownOrgScope
	}
	return sc.org
}
