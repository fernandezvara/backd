package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Magic links (design/mfa-passwordless.md §7): POST /_auth/magic-link asks for a link and a
// code, GET /_auth/magic-link?token= is the page the link opens, a form POST from that page
// uses the link, and POST /_auth/magic-link/verify takes the code typed in the app.

// magicLinkPost handles POST /_auth/magic-link: a JSON body asks for a link and a code; the
// form the page posts uses the link. One path, told apart by the content type, because the
// link's page and the app's request are the same resource.
func (a *authAPI) magicLinkPost(w http.ResponseWriter, r *http.Request) {
	switch {
	case contentTypeIs(r, "application/json"):
		a.requestMagicLink(w, r)
	case strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded"):
		a.useMagicLink(w, r)
	default:
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMedia, "Content-Type must be application/json")
	}
}

func (a *authAPI) requestMagicLink(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"email": "string", "redirect_to": "string?", "code_challenge": "string", "locale": "string?", "org": "string?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	if !svc.Settings.MagicLink.Enabled {
		authError(w, r, auth.ErrMagicLinkDisabled)
		return
	}
	address, _ := f["email"].(string)
	if _, err := registry.NormalizeEmail(address); err != nil {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "email", Reason: "must be a valid email address"})
		return
	}
	challenge, _ := f["code_challenge"].(string)
	if !auth.ValidPKCEChallenge(challenge) {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "code_challenge", Reason: "is required: the S256 PKCE challenge of a verifier only your app holds"})
		return
	}
	redirectTo, _ := f["redirect_to"].(string)
	if redirectTo != "" && !svc.Settings.SignInRedirectAllowed(redirectTo) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRedirect, "redirect_to isn't within the realm's sign_in.allowed_redirects (or email.allowed_redirects)")
		return
	}
	locale, _ := f["locale"].(string)
	r = withOrgContext(r, f, address)
	id, err := svc.RequestMagicLink(r.Context(), auth.MagicLinkInput{
		Email: address, RedirectTo: redirectTo, CodeChallenge: challenge,
		Locales: append([]string{locale}, acceptLanguages(r.Header.Get("Accept-Language"))...), IP: clientIP(r), RequestID: requestID(r.Context()),
		Consents: consents, Org: chi.URLParam(r, "org"),
	})
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"request_id": id})
}

// magicLinkPageData is the data of the link's page.
func (a *authAPI) magicLinkPageData(r *http.Request, kind, token string) email.PageData {
	realm := chi.URLParam(r, "realm")
	return email.PageData{Realm: realm, Locale: "en", Action: "/v1/" + realm + "/_auth/magic-link", Kind: kind, Token: token}
}

// writeMagicLinkPage renders the page of kind in the best language of the realm.
func (a *authAPI) writeMagicLinkPage(w http.ResponseWriter, r *http.Request, status int, page, org string, d email.PageData) {
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	locale := rl.Settings.BestLocale(acceptLanguages(r.Header.Get("Accept-Language"))...)
	d.Locale = locale
	var body string
	var err error
	if rl.Pages != nil && rl.Pages.Has(page, locale) {
		if a.fns != nil {
			a.fns.pageApp(r.Context(), rl.Name, page, &d, org, "")
		}
		body, err = rl.Pages.Render(page, locale, d)
		if err != nil && d.App != nil {
			d.App = nil // the page couldn't use what the data hook gave
			body, err = rl.Pages.Render(page, locale, d)
		}
	} else {
		d.Locale = "en"
		body, err = email.RenderBuiltin(page, d)
	}
	if err != nil {
		logger(r.Context()).Error("render the magic link page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pageHeaders(w)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// magicLinkPage handles GET /_auth/magic-link?token=: the page with the button. It never uses
// the link up, so a mail scanner that opens it can't spoil it.
func (a *authAPI) magicLinkPage(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if !svc.Settings.MagicLink.Enabled {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
		return
	}
	token := r.URL.Query().Get("token")
	c, err := svc.MagicLinkByToken(r.Context(), token)
	if err != nil {
		a.invalidLinkPage(w, r)
		return
	}
	kind := "confirm"
	if svc.MagicLinkDestination(c) == "" {
		kind = "type-code"
		token = ""
	}
	a.writeMagicLinkPage(w, r, http.StatusOK, email.PageMagicLink, c.Org, a.magicLinkPageData(r, kind, token))
}

// invalidLinkPage is the one page every link that doesn't work shows.
func (a *authAPI) invalidLinkPage(w http.ResponseWriter, r *http.Request) {
	rl := a.reg.Realms[chi.URLParam(r, "realm")]
	locale := rl.Settings.BestLocale(acceptLanguages(r.Header.Get("Accept-Language"))...)
	d := email.PageData{Realm: chi.URLParam(r, "realm"), Locale: locale, Kind: "magic-link"}
	if rl.Pages != nil && rl.Pages.Has(email.PageInvalidToken, locale) {
		body, err := rl.Pages.Render(email.PageInvalidToken, locale, d)
		if err == nil {
			pageHeaders(w)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
			return
		}
	}
	body, err := email.RenderBuiltin(email.PageInvalidToken, d)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pageHeaders(w)
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(body))
}

// useMagicLink is the form post of the page: it signs in and sends the browser back to the app
// with the one-time login code (or `backd_error`), like a provider's callback does.
func (a *authAPI) useMagicLink(w http.ResponseWriter, r *http.Request) {
	svc := usersOf(r)
	if !svc.Settings.MagicLink.Enabled {
		writeError(w, r, http.StatusNotFound, codeNotFound, "resource not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		a.invalidLinkPage(w, r)
		return
	}
	token := r.PostForm.Get("token")
	if err := svc.RateLimit(r.Context(), "magic-link:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	viaOrg := ""
	if c, cerr := svc.MagicLinkByToken(r.Context(), token); cerr == nil {
		viaOrg = c.ViaOrg
	}
	code, dest, err := svc.UseMagicLink(r.Context(), token, clientIP(r))
	switch {
	case errors.Is(err, auth.ErrNoRedirect):
		// Nowhere to send the browser: the link isn't used up; the code in the email is the way.
		org := ""
		if c, cerr := svc.MagicLinkByToken(r.Context(), token); cerr == nil {
			org = c.Org
		}
		a.writeMagicLinkPage(w, r, http.StatusOK, email.PageMagicLink, org, a.magicLinkPageData(r, "type-code", ""))
		return
	case errors.Is(err, auth.ErrInvalidMagicLink):
		a.invalidLinkPage(w, r)
		return
	case err != nil:
		var refused *auth.RefusedError
		failure := oauthProviderError
		switch {
		case errors.As(err, &refused):
			svc.Audit(r.Context(), auth.AuditSignInRefused, "user:"+refused.UserID, map[string]any{"provider": auth.MagicLinkLoginMethod, "reason": refused.Reason})
			failure = oauthSignInRefused
		case errors.Is(err, auth.ErrSignupClosed):
			failure = oauthSignupClosed
		default:
			logger(r.Context()).Error("use a magic link", "error", err)
		}
		a.redirectToApp(w, r, dest, paramError, failure)
		return
	}
	if viaOrg != "" {
		a.redirectToApp(w, r, dest, paramCode, code, paramOrg, viaOrg)
		return
	}
	a.redirectToApp(w, r, dest, paramCode, code)
}

// magicLinkVerify handles POST /_auth/magic-link/verify: the code typed in the app that asked,
// with the request id and the verifier of its PKCE challenge, ends in the session (or the MFA
// challenge), like any first factor.
func (a *authAPI) magicLinkVerify(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{"request_id": "string", "code": "string", "code_verifier": "string", "cookie": "bool?"})
	if !ok {
		return
	}
	svc := usersOf(r)
	if !svc.Settings.MagicLink.Enabled {
		authError(w, r, auth.ErrMagicLinkDisabled)
		return
	}
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	out, err := svc.VerifyMagicLinkCode(r.Context(), f["request_id"].(string), strings.TrimSpace(f["code"].(string)), f["code_verifier"].(string), clientIP(r))
	var refused *auth.RefusedError
	switch {
	case errors.As(err, &refused):
		svc.Audit(r.Context(), auth.AuditSignInRefused, "user:"+refused.UserID, map[string]any{"provider": auth.MagicLinkLoginMethod, "reason": refused.Reason})
		writeError(w, r, http.StatusForbidden, codeSignInRefused, "this account can't sign in")
		return
	case errors.Is(err, auth.ErrSignupClosed):
		writeError(w, r, http.StatusForbidden, codeSignupClosed, err.Error())
		return
	case err != nil:
		authError(w, r, err)
		return
	}
	a.answerSignIn(w, r, out, http.StatusOK, useCookie, true)
}
