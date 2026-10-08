package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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

func (a *authAPI) oauthRoutes(r chi.Router, json func(http.Handler) http.Handler) {
	r.Get("/oauth/{provider}/start", a.oauthStartGet)
	r.With(json).Post("/oauth/{provider}/start", a.oauthStartPost)
	r.Get("/oauth/{provider}/callback", a.oauthCallback)
	r.Post("/oauth/{provider}/callback", a.oauthCallback) // Apple answers with a form
	r.With(json).Post("/oauth/{provider}/id-token", a.oauthIDToken)
	r.With(json).Post("/oauth/token", a.oauthToken)
}

// provider returns the realm's provider named in the path, or answers 404.
func (a *authAPI) provider(w http.ResponseWriter, r *http.Request) (*registry.Provider, bool) {
	p := usersOf(r).Settings.Providers[chi.URLParam(r, "provider")]
	if p == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no such sign-in provider")
		return nil, false
	}
	return p, true
}

// providerSecrets reads the realm secrets the provider needs, by name; ok is false
// when one is not set (the provider is then unavailable).
func (a *authAPI) providerSecrets(ctx context.Context, svc *auth.Users, p *registry.Provider) (map[string]string, bool, error) {
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
func (a *authAPI) callbackURL(realm, provider string) (string, bool) {
	base := a.baseURL
	if rl := a.reg.Realms[realm]; rl != nil && rl.Settings.Email != nil && rl.Settings.Email.PublicURL != "" {
		base = rl.Settings.Email.PublicURL
	}
	if base == "" {
		return "", false
	}
	return strings.TrimSuffix(base, "/") + registry.ProviderCallbackPath(realm, provider), true
}

func (a *authAPI) providerUnavailable(w http.ResponseWriter, r *http.Request, why string) {
	writeError(w, r, http.StatusServiceUnavailable, codeProviderUnavail, why)
}

// startInput is what a start call carries.
type startInput struct {
	redirectTo, challenge, intent, invitation, locale string
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
	f, ok := fields(w, r, obj, map[string]string{"redirect_to": "string", "code_challenge": "string", "intent": "string?", "invitation": "string?", "locale": "string?"})
	if !ok {
		return
	}
	str := func(k string) string { v, _ := f[k].(string); return v }
	in := startInput{redirectTo: str("redirect_to"), challenge: str("code_challenge"), intent: str("intent"), invitation: str("invitation"), locale: str("locale")}
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
	p, ok := a.provider(w, r)
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
	if !svc.Settings.SignInRedirectAllowed(in.redirectTo) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to isn't within the realm's sign_in.allowed_redirects (or email.allowed_redirects)")
		return "", false
	}
	if err := svc.RateLimit(r.Context(), "oauth-start:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return "", false
	}
	callback, ok := a.callbackURL(chi.URLParam(r, "realm"), p.Name)
	if !ok {
		a.providerUnavailable(w, r, "backd's public address is unknown (BACKD_URL)")
		return "", false
	}
	if _, ok, err := a.providerSecrets(r.Context(), svc, p); err != nil || !ok {
		if err != nil {
			logger(r.Context()).Error("read the secrets of a sign-in provider", "provider", p.Name, "error", err)
		}
		a.providerUnavailable(w, r, "this provider's secrets are not set")
		return "", false
	}
	st := auth.OAuthState{Provider: p.Name, Intent: in.intent, CodeChallenge: in.challenge, RedirectTo: in.redirectTo, LinkUserID: who.User.ID,
		Locales: append([]string{in.locale}, acceptLanguages(r.Header.Get("Accept-Language"))...)}
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
	if err != nil || st.Provider != chi.URLParam(r, "provider") {
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
	p, ok := svc.Settings.Providers[st.Provider]
	if !ok {
		back("error", oauthProviderError)
		return
	}
	if e := get("error"); e != "" {
		if e == "access_denied" || e == "user_cancelled_authorize" {
			back("error", oauthCancelled)
		} else {
			back("error", oauthProviderError)
		}
		return
	}
	code := get("code")
	callback, ok := a.callbackURL(chi.URLParam(r, "realm"), p.Name)
	secrets, ok2, err := a.providerSecrets(r.Context(), svc, p)
	if code == "" || !ok || !ok2 || err != nil {
		logger(r.Context()).Error("sign-in with a provider can't go on", "provider", p.Name, "code_given", code != "", "secrets_set", ok2, "error", err)
		back("error", oauthProviderError)
		return
	}
	secret := secrets[p.ClientSecret]
	if p.Name == registry.ProviderApple {
		if secret, err = a.oauth.AppleClientSecret(p, p.ClientID, secrets[p.PrivateKey]); err != nil {
			logger(r.Context()).Error("sign Apple's client secret", "error", err)
			back("error", oauthProviderError)
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
		back("error", oauthProviderError)
		return
	}
	claims, err := a.oauth.Verify(r.Context(), p, tokens.IDToken, st.Nonce, false)
	if err != nil {
		logger(r.Context()).Warn("the provider's ID token is not valid", "provider", p.Name, "error", err)
		back("error", oauthProviderError)
		return
	}
	res, err := svc.ResolveProviderLogin(r.Context(), auth.ProviderLogin{
		Provider: p.Name, Subject: claims.Subject, Email: claims.Email, EmailVerified: claims.EmailVerified, TrustsEmail: p.TrustsEmail(),
		Intent: st.Intent, LinkUserID: st.LinkUserID, Invitation: st.InvitationID, Locales: st.Locales, IP: clientIP(r),
	})
	if err != nil {
		a.oauthFailure(w, r, svc, st, p, err)
		return
	}
	a.keepAppleToken(r, svc, p, res.User.ID, p.ClientID, tokens.RefreshToken)
	if st.Intent == auth.IntentLink {
		back("linked", p.Name)
		return
	}
	loginCode, err := svc.IssueLoginCode(r.Context(), res.User.ID, st.CodeChallenge)
	if err != nil {
		logger(r.Context()).Error("issue a login code", "error", err)
		back("error", oauthProviderError)
		return
	}
	setActor(r.Context(), "user:"+res.User.ID)
	back("code", loginCode)
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
		a.redirectToApp(w, r, st.RedirectTo, "error", oauthAccountExists, "provider", p.Name)
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
	a.redirectToApp(w, r, st.RedirectTo, "error", code)
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

// oauthToken redeems a login code for a session: POST /_auth/oauth/token.
func (a *authAPI) oauthToken(w http.ResponseWriter, r *http.Request) {
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
	p, token, err := svc.RedeemLoginCode(r.Context(), f["code"].(string), f["code_verifier"].(string))
	if err != nil {
		authError(w, r, err)
		return
	}
	setActor(r.Context(), "user:"+p.User.ID)
	if useCookie {
		setSessionCookie(w, r, svc, p, token)
		token = ""
	}
	writeJSON(w, http.StatusOK, sessionJSON(p, token, svc.LocaleOf(p.User)))
}
