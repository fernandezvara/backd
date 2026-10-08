package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/oauth"
	"github.com/fernandezvara/backd/internal/registry"
)

// Native sign-in: a mobile app that signs in with the platform (Sign in with Apple, Google
// Sign-In, MSAL) already holds the provider's ID token. It sends it, with the nonce it made
// the sign-in with, and gets a session straight away, under the same rules as the redirect flow.

const (
	codeAccountExists     = "account_exists"
	codeLinkConflict      = "link_conflict"
	codeSignupClosed      = "signup_closed"
	codeInvitationInvalid = "invitation_invalid"
	codeSignInRefused     = "signin_refused"
	codeEmailRequired     = "email_required"
)

func (a *authAPI) oauthIDToken(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{
		"id_token": "string", "nonce": "string", "authorization_code": "string?", "intent": "string?",
		"invitation": "string?", "locale": "string?", "cookie": "bool?",
	})
	if !ok {
		return
	}
	str := func(k string) string { v, _ := f[k].(string); return v }
	svc := usersOf(r)
	p, ok := a.provider(w, r)
	if !ok {
		return
	}
	intent := str("intent")
	if intent == "" {
		intent = auth.IntentSignIn
	}
	var details []Detail
	if intent != auth.IntentSignIn && intent != auth.IntentLink {
		details = append(details, Detail{Path: "intent", Reason: "must be signin or link"})
	}
	if inv := str("invitation"); inv != "" && !strings.HasPrefix(inv, auth.InvitationPrefix) {
		details = append(details, Detail{Path: "invitation", Reason: "isn't an invitation token"})
	}
	if str("id_token") == "" || str("nonce") == "" {
		details = append(details, Detail{Path: "id_token", Reason: "and nonce are required: the provider's ID token and the nonce the app made the sign-in with"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the sign-in isn't described", details...)
		return
	}
	asked, _ := f["cookie"].(bool)
	useCookie, ok := askedForCookie(w, r, svc.Settings, asked)
	if !ok {
		return
	}
	var who auth.Principal
	if intent == auth.IntentLink {
		if who, ok = a.authenticateRequest(w, r); !ok {
			return
		}
	}
	if err := svc.RateLimit(r.Context(), "oauth-idtoken:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	claims, err := a.oauth.Verify(r.Context(), p, str("id_token"), str("nonce"), true)
	switch {
	case errors.Is(err, oauth.ErrInvalidToken):
		logger(r.Context()).Info("a native ID token was not accepted", "provider", p.Name, "error", err)
		writeError(w, r, http.StatusUnauthorized, codeInvalidToken, "the ID token is not valid for this realm")
		return
	case err != nil:
		logger(r.Context()).Warn("could not verify a native ID token", "provider", p.Name, "error", err)
		a.providerUnavailable(w, r, "the provider could not be reached")
		return
	}
	var refresh string
	if p.Name == registry.ProviderApple && p.RevokeOnDelete {
		if refresh, ok = a.appleRefreshToken(w, r, p, claims, str("authorization_code")); !ok {
			return
		}
	}
	login := auth.ProviderLogin{
		Provider: p.Name, Subject: claims.Subject, Email: claims.Email, EmailVerified: claims.EmailVerified, TrustsEmail: p.TrustsEmail(),
		Intent: intent, LinkUserID: who.User.ID, Locales: append([]string{str("locale")}, acceptLanguages(r.Header.Get("Accept-Language"))...), IP: clientIP(r),
		Profile: profileOf(claims, nil),
	}
	if inv := str("invitation"); inv != "" {
		login.Invitation = auth.HashToken(inv)
	}
	res, err := svc.ResolveProviderLogin(r.Context(), login)
	if err != nil {
		a.nativeFailure(w, r, svc, p.Name, err)
		return
	}
	if intent == auth.IntentLink {
		a.keepAppleToken(r, svc, p, res.User.ID, claims.Audience, refresh)
		writeJSON(w, http.StatusOK, map[string]any{"linked": p.Name})
		return
	}
	a.keepAppleToken(r, svc, p, res.User.ID, claims.Audience, refresh)
	session, token, err := svc.StartProviderSession(r.Context(), res.User)
	if err != nil {
		authError(w, r, err)
		return
	}
	setActor(r.Context(), "user:"+session.User.ID)
	if useCookie {
		setSessionCookie(w, r, svc, session, token)
		token = ""
	}
	writeJSON(w, http.StatusOK, providerSessionJSON(session, token, svc.LocaleOf(session.User), res.NewUser, res.Profile))
}

// nativeFailure answers a provider login the rules refuse, in JSON.
func (a *authAPI) nativeFailure(w http.ResponseWriter, r *http.Request, svc *auth.Users, provider string, err error) {
	var refused *auth.RefusedError
	switch {
	case errors.As(err, &refused):
		svc.Audit(r.Context(), auth.AuditSignInRefused, "user:"+refused.UserID, map[string]any{"provider": provider, "reason": refused.Reason})
		writeError(w, r, http.StatusForbidden, codeSignInRefused, "this account can't sign in")
	case errors.Is(err, auth.ErrAccountExists):
		writeError(w, r, http.StatusConflict, codeAccountExists, "an account with that address exists: sign in another way and link this provider from there", Detail{Path: "provider", Reason: provider})
	case errors.Is(err, auth.ErrLinkConflict):
		writeError(w, r, http.StatusConflict, codeLinkConflict, "that provider account can't be linked to this user")
	case errors.Is(err, auth.ErrSignupClosed):
		writeError(w, r, http.StatusForbidden, codeSignupClosed, "sign-up is not open in this realm")
	case errors.Is(err, auth.ErrInvitationRequired), errors.Is(err, auth.ErrInvalidInvitation):
		writeError(w, r, http.StatusBadRequest, codeInvitationInvalid, "sign-up needs a valid invitation", Detail{Path: "invitation", Reason: "is required and must be valid"})
	case errors.Is(err, auth.ErrEmailNotVerified):
		writeError(w, r, http.StatusForbidden, codeEmailNotVerified, err.Error())
	case errors.Is(err, auth.ErrEmailRequired):
		writeError(w, r, http.StatusBadRequest, codeEmailRequired, "the provider gave no email address")
	default:
		authError(w, r, err)
	}
}

// appleRefreshToken trades the authorization code an iOS app sends with its ID token for
// Apple's refresh token, which backd keeps to revoke when the account is deleted. The code
// must belong to the same person and app as the token, or a client could make backd keep
// (and later revoke) someone else's token. It answers and returns false when it can't.
func (a *authAPI) appleRefreshToken(w http.ResponseWriter, r *http.Request, p *registry.Provider, claims oauth.Claims, code string) (string, bool) {
	if code == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the sign-in isn't described", Detail{Path: "authorization_code", Reason: "is required for Apple: backd revokes the user's tokens with it when the account is deleted"})
		return "", false
	}
	secrets, ok, err := a.providerSecrets(r.Context(), usersOf(r), p)
	if err != nil || !ok {
		a.providerUnavailable(w, r, "this provider's secrets are not set")
		return "", false
	}
	secret, err := a.oauth.AppleClientSecret(p, claims.Audience, secrets[p.PrivateKey])
	if err != nil {
		logger(r.Context()).Error("sign Apple's client secret", "error", err)
		a.providerUnavailable(w, r, "this provider is not set up correctly")
		return "", false
	}
	tokens, err := a.oauth.Exchange(r.Context(), p, claims.Audience, secret, code, "", "")
	if err == nil {
		var subject, audience string
		if subject, audience, err = a.oauth.Subject(r.Context(), p, tokens.IDToken, true); err == nil && (subject != claims.Subject || audience != claims.Audience) {
			err = oauth.ErrInvalidToken
		}
	}
	switch {
	case errors.Is(err, oauth.ErrExchange), errors.Is(err, oauth.ErrInvalidToken):
		logger(r.Context()).Info("an Apple authorization code was not accepted", "error", err)
		writeError(w, r, http.StatusUnauthorized, codeInvalidToken, "the authorization code was refused, or is not the one of this ID token")
		return "", false
	case err != nil:
		logger(r.Context()).Warn("could not exchange an Apple authorization code", "error", err)
		a.providerUnavailable(w, r, "the provider could not be reached")
		return "", false
	}
	return tokens.RefreshToken, true
}
