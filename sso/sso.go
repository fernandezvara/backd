// Package sso is the sign-in library: how a person proves who they are with an account
// at an identity provider (Google, Microsoft, Apple, GitHub, any OpenID Connect or OAuth 2
// server) and what backd does about it. It knows neither backd's users, organizations or
// sessions nor MongoDB nor HTTP routing: backd plugs in through the ports of the engine.
// A test fails if anything under sso/ imports a backd package (design/sso.md §3).
//
// This root package holds the types every part shares; the protocol lives in the
// packages beside it: oidc, oauth2, apple, pkce and safehttp.
package sso

import (
	"context"
	"errors"
	"net/url"
)

// Kind says how a provider is spoken to and which rules its tokens follow.
type Kind string

// The built-in kinds.
const (
	Google    Kind = "google"
	Microsoft Kind = "microsoft"
	Apple     Kind = "apple"
	// OIDC is any OpenID Connect provider the operator names (type: oidc): its endpoints come from
	// its issuer's discovery document.
	OIDC Kind = "oidc"
)

// Errors a provider's answers can lead to. None of them carries anything the provider said
// beyond its own error code.
var (
	// ErrExchange: the provider refused the authorization code.
	ErrExchange = errors.New("sso: the provider refused the authorization code")
	// ErrInvalidToken: an ID token that does not verify (signature, issuer, audience, expiry
	// or nonce), or is missing what is needed.
	ErrInvalidToken = errors.New("sso: the ID token is not valid")
	// ErrUnavailable: the provider could not be reached, or answered nonsense.
	ErrUnavailable = errors.New("sso: the provider is not available")
)

// Endpoints are the URLs of a provider.
type Endpoints struct {
	Authorize, Token, JWKS, Revoke string
}

// Provider is a provider ready to be spoken to: what the protocol packages need and nothing
// of where the configuration came from.
type Provider struct {
	Name string // what identities, URLs and the audit call it
	Kind Kind
	// ClientID is the application's id with the provider (Apple: the Services ID).
	ClientID string
	// NativeClientIDs (Apple: bundle ids) are the extra audiences the ID-token endpoint accepts.
	NativeClientIDs []string
	// Tenant is Microsoft's: common, organizations, consumers or a tenant id.
	Tenant string
	// Issuer is the issuer a generic OpenID Connect provider is configured with; its ID tokens must
	// name exactly it. Built-in providers know theirs. Empty: unknown (Microsoft across tenants).
	Issuer string
	// Scopes are extra scopes asked for besides openid email profile (generic providers).
	Scopes []string
	// Hosts are the hosts the library may reach for this provider besides the built-in ones: the
	// issuer's, and after discovery the endpoints' that share its registered domain.
	Hosts []string
	// AllowPrivate lets the provider's own hosts resolve to private ranges (never loopback,
	// link-local or the metadata address): an identity provider on the operator's network.
	AllowPrivate bool
	// TeamID and KeyID are Apple's: they sign the client secret.
	TeamID, KeyID string
	Endpoints     Endpoints

	// Org is the organization of a connection; empty for the realm's providers. Identities of
	// a connection are (Name, Org, subject).
	Org string
	// Method is how a session records the first factor of this provider (its name, or the
	// connection's).
	Method string
	// Link says whether the provider's verified addresses may link an existing account.
	Link LinkPolicy
	// Secret is the client secret (Apple: the .p8 private key's PEM), read by the host when it
	// resolved the provider; the library keeps none.
	Secret string
	// RevokeOnDelete: keep Apple's refresh token so a deleted account's tokens can be revoked.
	RevokeOnDelete bool
	// Admit says whether the place admits the person the provider vouched for (a connection's
	// restriction); nil admits everyone.
	Admit func(Claims) bool
	// Authority is a last check before a connection signs anybody in (its setter still holds
	// the roles it was set with); nil is none. An error ends the attempt as an unavailable provider.
	Authority func(ctx context.Context) error
}

// LinkPolicy says when a provider's word about an address is enough to link an account.
type LinkPolicy int

// The policies.
const (
	// LinkNever: an account with the address is reported (account_exists), never linked.
	LinkNever LinkPolicy = iota
	// LinkVerifiedEmail: a verified address the provider asserts links the account that has
	// it, if the account's own address is verified too.
	LinkVerifiedEmail
)

// Audiences are the client ids an ID token may be issued to: the web client and, for the
// ID-token endpoint, the native ones.
func (p Provider) Audiences(native bool) []string {
	out := []string{}
	if p.ClientID != "" {
		out = append(out, p.ClientID)
	}
	if native {
		out = append(out, p.NativeClientIDs...)
	}
	return out
}

// BuiltinEndpoints are the endpoints of the built-in kinds.
func BuiltinEndpoints(kind Kind, tenant string) Endpoints {
	switch kind {
	case Google:
		return Endpoints{
			Authorize: "https://accounts.google.com/o/oauth2/v2/auth",
			Token:     "https://oauth2.googleapis.com/token",
			JWKS:      "https://www.googleapis.com/oauth2/v3/certs",
		}
	case Microsoft:
		base := "https://login.microsoftonline.com/" + url.PathEscape(tenant)
		return Endpoints{Authorize: base + "/oauth2/v2.0/authorize", Token: base + "/oauth2/v2.0/token", JWKS: base + "/discovery/v2.0/keys"}
	case Apple:
		return Endpoints{
			Authorize: "https://appleid.apple.com/auth/authorize",
			Token:     "https://appleid.apple.com/auth/token",
			JWKS:      "https://appleid.apple.com/auth/keys",
			Revoke:    "https://appleid.apple.com/auth/revoke",
		}
	}
	return Endpoints{}
}
