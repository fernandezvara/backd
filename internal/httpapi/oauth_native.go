package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/sso"
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
	codeNotAllowed        = "not_allowed"
	codeEmailRequired     = "email_required"
	codeInvalidChallenge  = "invalid_challenge"
	codeInvalidCode       = "invalid_code"
	codeMFALocked         = "mfa_locked"
	codeMFADisabled       = "mfa_disabled"
	codeMagicLinkDisabled = "magic_link_disabled"
	codeInvalidMagicLink  = "invalid_magic_link"
	codeSelfReset         = "self_reset_not_allowed"
	codeReauthRequired    = "reauth_required"
	codeMFAUnavailable    = "mfa_unavailable"
	codeMFARequiredByRole = "mfa_required_by_role"
	codeMFAAlreadyEnabled = "mfa_already_enabled"
	codeMFANotEnabled     = "mfa_not_enabled"
)

func (a *authAPI) oauthIDToken(w http.ResponseWriter, r *http.Request) {
	obj, ok := readObject(w, r)
	if !ok {
		return
	}
	consents, ok := takeConsents(w, r, obj)
	if !ok {
		return
	}
	f, ok := fields(w, r, obj, map[string]string{
		"id_token": "string", "nonce": "string", "authorization_code": "string?", "intent": "string?",
		"invitation": "string?", "locale": "string?", "strategy": "string?", "cookie": "bool?",
	})
	if !ok {
		return
	}
	str := func(k string) string { v, _ := f[k].(string); return v }
	svc := usersOf(r)
	p, sc, ok := a.provider(w, r)
	if !ok {
		return
	}
	// Only the built-in providers have native apps whose audiences are configured.
	if p.Kind != registry.ProviderGoogle && p.Kind != registry.ProviderMicrosoft && p.Kind != registry.ProviderApple {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this provider has no native sign-in: use the redirect flow")
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
	if inv := str("invitation"); inv != "" && !strings.HasPrefix(inv, invitationPrefixOf(r)) {
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
	// A sign-in with a provider follows a strategy that starts with it (an organization's connection: with sso).
	strategy := ""
	if intent == auth.IntentSignIn {
		st, err := svc.ChooseStrategy(str("strategy"), strategyMethodOf(p))
		if err != nil {
			authError(w, r, err)
			return
		}
		strategy = st.Name
	}
	var who auth.Principal
	if intent == auth.IntentLink {
		if who, ok = a.authenticateRequest(w, r); !ok || !a.notViaConnection(w, r, who) {
			return
		}
	}
	if err := svc.RateLimit(r.Context(), "oauth-idtoken:"+clientIP(r), oauthRateLimit, time.Minute); err != nil {
		authError(w, r, err)
		return
	}
	host := a.host(r)
	sp, secretsSet, err := host.ProviderOf(r.Context(), p, connOf(sc, p))
	if err != nil {
		logger(r.Context()).Error("read the secrets of a sign-in provider", "provider", p.Name, "error", err)
		a.providerUnavailable(w, r, "this provider is not set up correctly")
		return
	}
	if !secretsSet && p.Name == registry.ProviderApple && p.RevokeOnDelete {
		a.providerUnavailable(w, r, "this provider's secrets are not set")
		return
	}
	if err := svc.ValidateConsents(consents); err != nil {
		authError(w, r, err)
		return
	}
	hd := auth.SSOHostData{ScopeOrg: routeScope(r, sc), Locales: append([]string{str("locale")}, acceptLanguages(r.Header.Get("Accept-Language"))...), Consents: consents}
	if inv := str("invitation"); inv != "" {
		hd.Invitation = auth.HashToken(inv)
	}
	res, err := host.Engine(a.oauth).IDToken(r.Context(), sso.IDTokenInput{
		Scope: auth.SSOScopeOf(p.Org), Provider: sp, IDToken: str("id_token"), Nonce: str("nonce"), AuthorizationCode: str("authorization_code"),
		Intent: sso.Intent(intent), LinkUserID: who.User.ID, Host: hd.Encode(), Request: sso.Request{IP: clientIP(r), UserAgent: r.UserAgent()},
	})
	if err != nil {
		a.nativeFailure(w, r, p.Name, err)
		return
	}
	if intent == auth.IntentLink {
		writeJSON(w, http.StatusOK, map[string]any{"linked": p.Name})
		return
	}
	user, err := host.UserOf(r.Context(), res.Account.ID)
	if err != nil {
		authError(w, r, err)
		return
	}
	out, err := svc.StartProviderSessionFor(r.Context(), user, sp.Method, strategy, res.NewUser, res.Profile, res.Scope)
	if err != nil {
		authError(w, r, err)
		return
	}
	a.answerSignIn(w, r, out, http.StatusOK, useCookie, true)
}

// nativeFailure answers a native sign-in the engine or the rules refuse, in JSON.
func (a *authAPI) nativeFailure(w http.ResponseWriter, r *http.Request, provider string, err error) {
	var refused *sso.Refused
	switch {
	case errors.Is(err, sso.ErrInvalidToken):
		logger(r.Context()).Info("a native ID token was not accepted", "provider", provider, "error", err)
		writeError(w, r, http.StatusUnauthorized, codeInvalidToken, "the ID token is not valid for this realm")
	case errors.Is(err, sso.ErrExchange):
		logger(r.Context()).Info("an Apple authorization code was not accepted", "error", err)
		writeError(w, r, http.StatusUnauthorized, codeInvalidToken, "the authorization code was refused, or is not the one of this ID token")
	case errors.Is(err, sso.ErrUnavailable):
		logger(r.Context()).Warn("could not reach the provider for a native sign-in", "provider", provider, "error", err)
		a.providerUnavailable(w, r, "the provider could not be reached")
	case errors.Is(err, sso.ErrNotAllowed):
		writeError(w, r, http.StatusForbidden, codeNotAllowed, "this account is not allowed to sign in through this organization")
	case errors.Is(err, sso.ErrNotFound):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this organization has no such sign-in provider")
	case errors.Is(err, sso.ErrAuthorizationCodeRequired):
		writeError(w, r, http.StatusBadRequest, codeValidation, "the sign-in isn't described", Detail{Path: "authorization_code", Reason: "is required for Apple: backd revokes the user's tokens with it when the account is deleted"})
	case errors.As(err, &refused):
		writeError(w, r, http.StatusForbidden, codeSignInRefused, "this account can't sign in")
	case errors.Is(err, sso.ErrAccountExists):
		writeError(w, r, http.StatusConflict, codeAccountExists, "an account with that address exists: sign in another way and link this provider from there", Detail{Path: "provider", Reason: provider})
	case errors.Is(err, sso.ErrLinkConflict):
		writeError(w, r, http.StatusConflict, codeLinkConflict, "that provider account can't be linked to this user")
	case errors.Is(err, sso.ErrSignupClosed):
		writeError(w, r, http.StatusForbidden, codeSignupClosed, "sign-up is not open in this realm")
	case errors.Is(err, sso.ErrInvitationInvalid):
		writeError(w, r, http.StatusBadRequest, codeInvitationInvalid, "sign-up needs a valid invitation", Detail{Path: "invitation", Reason: "is required and must be valid"})
	case errors.Is(err, sso.ErrEmailNotVerified):
		writeError(w, r, http.StatusForbidden, codeEmailNotVerified, err.Error())
	case errors.Is(err, sso.ErrEmailRequired):
		writeError(w, r, http.StatusBadRequest, codeEmailRequired, "the provider gave no email address")
	default:
		authError(w, r, err)
	}
}
