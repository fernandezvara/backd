package sso

import (
	"context"
	"encoding/json"
	"time"
)

// Attempt is a browser flow in progress: what must be remembered between sending the person to
// the provider and the provider sending them back. Its key is the hash of the state value.
type Attempt struct {
	Provider string
	// Scope is where the attempt began (the realm, an organization); the callback must be the
	// one of the same scope.
	Scope string
	// Org is the organization of the connection the attempt goes through; empty for the realm's.
	Org      string
	Intent   Intent
	Strategy string
	// Challenge is the app's PKCE challenge (S256); Verifier and Nonce are backd's own with the
	// provider.
	Challenge  string
	Verifier   string
	Nonce      string
	RedirectTo string
	// LinkUserID: IntentLink, whose identity is being added.
	LinkUserID string
	// Host is the host's own data (the invitation, consents, locales, the organization signed in
	// to…): stored and returned untouched.
	Host      json.RawMessage
	ExpiresAt time.Time
}

// Code is the one-time login code's record: what the app gets a session for when it redeems
// the code with the verifier of the challenge.
type Code struct {
	UserID string
	// Method is the first factor the code stands for.
	Method   string
	Strategy string
	// Challenge is the app's PKCE challenge.
	Challenge string
	NewUser   bool
	Profile   Profile
	// Scope is where the session is scoped to (an organization's id), empty for none.
	Scope     string
	ExpiresAt time.Time
}

// Store keeps what the library creates and must find again. Reads of one-time records are atomic:
// two concurrent takes of the same key get the record once.
type Store interface {
	// PutAttempt saves the attempt under the hash of its state value.
	PutAttempt(ctx context.Context, key string, a Attempt) error
	// TakeAttempt returns the attempt and forgets it: ErrNotFound when it is unknown, used or expired.
	TakeAttempt(ctx context.Context, key string) (Attempt, error)
	// PutCode saves a login code under the hash of its value.
	PutCode(ctx context.Context, key string, c Code) error
	// TakeCode returns the code's record and forgets it: ErrNotFound when it is unknown, used or expired.
	TakeCode(ctx context.Context, key string) (Code, error)
}

// Catalog says which providers a place offers and holds what they need to be used.
type Catalog interface {
	// Provider returns the provider named in a scope, ready to use. ErrNotFound for every reason it
	// can't be (unknown, off, no secret, the place inactive), never saying which.
	Provider(ctx context.Context, scope, name string) (Provider, error)
	// CallbackURL is the address the provider sends the person back to; false when backd's own
	// public address is unknown.
	CallbackURL(scope string, p Provider) (string, bool)
}

// Entry is a login that has been resolved to a user, about to be let into the place it was for.
type Entry struct {
	Login   Login
	Result  Result
	Attempt Attempt
}

// Directory is the host's accounts, as far as sign-in is concerned. Every method that changes
// something is the host's whole business for it: audit, quotas, on-signup hooks.
type Directory interface {
	// NormalizeEmail returns the address as accounts hold it, or "" when it isn't one.
	NormalizeEmail(raw string) string
	// Identity returns the owner of a provider account (ErrNotFound when it is not linked).
	Identity(ctx context.Context, key IdentityKey) (Account, error)
	// User returns a user by id (ErrNotFound).
	User(ctx context.Context, id string) (Account, error)
	// UserByEmail returns the account that has the (normalized) address (ErrNotFound).
	UserByEmail(ctx context.Context, email string) (Account, error)
	// MaySignIn applies the rules every sign-in answers to (not erased, not disabled, from an
	// allowed network, a verified address when the realm requires one): a *Refused or ErrEmailNotVerified.
	MaySignIn(ctx context.Context, a Account, req Request) error
	// Touch records a sign-in with a linked identity and what the provider says of the address now.
	Touch(ctx context.Context, key IdentityKey, l Login, email string)
	// Link adds the identity to the account; ErrLinkConflict when it is taken or the account
	// already has one of the provider.
	Link(ctx context.Context, a Account, l Login, email string) error
	// Create makes a user and its identity, as the place's sign-up rules allow: ErrSignupClosed,
	// ErrInvitationInvalid or ErrAccountExists (a race for the address). verified says the provider
	// vouches for the address.
	Create(ctx context.Context, l Login, email string, verified bool) (Account, error)
	// Enter lets a resolved login into the place it was for (joins the organization, uses the
	// invitation). allowed is false when the person may not sign in there; scope is what the session
	// is scoped to.
	Enter(ctx context.Context, e Entry) (scope string, allowed bool)
	// Superseded says whether the attempt may not finish because the place's rules changed since it
	// began (the organization now requires its connection): the code to give the app, or "".
	Superseded(ctx context.Context, a Attempt, p Provider) string
	// CleanProfile builds the profile handed to the app from the claims and the extra fields some
	// providers send in the callback (Apple's name): bounded, with only the known fields.
	CleanProfile(c Claims, extra map[string]string) Profile
}

// Event is something that happened because of a sign-in, for the host's audit and jobs.
type Event struct {
	// Kind is one of the Event constants.
	Kind     string
	Provider Provider
	UserID   string
	// Reason is, for EventRefused, why.
	Reason string
	// Token is, for EventAppleToken, Apple's refresh token to keep for revocation, with Audience
	// the client id it was issued to. It is never logged.
	Token, Audience string
}

// The kinds of Event.
const (
	// EventRefused: the account's state refused a sign-in (audit with the reason).
	EventRefused = "refused"
	// EventAppleToken: keep this refresh token for revoking it when the account goes away.
	EventAppleToken = "apple_token"
)

// Events receives what happens because of sign-ins.
type Events interface {
	Emit(ctx context.Context, e Event)
}

// Tokens are what a code exchange gives back that the library keeps using.
type Tokens struct {
	IDToken string
	// AccessToken is what an OAuth 2 provider without an ID token gives; it is used once, to read the
	// person, and never kept.
	AccessToken  string
	RefreshToken string
}

// AuthorizeParams are what an authorization request carries.
type AuthorizeParams struct {
	RedirectURI   string
	State         string
	Nonce         string
	CodeChallenge string // empty: none
}

// Protocol speaks to a provider. sso/protocol implements it over the oidc, oauth2 and apple
// packages; tests use a fake.
type Protocol interface {
	// Prepare completes a provider before it is used: a generic OpenID Connect provider's endpoints
	// are read from its issuer's discovery document (cached). ErrUnavailable when they can't be.
	Prepare(ctx context.Context, p Provider) (Provider, error)
	// UsesPKCE says whether a PKCE challenge goes to the provider.
	UsesPKCE(p Provider) bool
	AuthorizeURL(p Provider, a AuthorizeParams) string
	// Exchange trades the authorization code for the person's tokens.
	Exchange(ctx context.Context, p Provider, code, redirectURI, verifier string) (Tokens, error)
	// Identify says who the tokens of a code exchange are for: an OpenID Connect provider's ID token
	// is verified against the nonce, an OAuth 2 provider's person is read with the access token.
	Identify(ctx context.Context, p Provider, t Tokens, nonce string) (Claims, error)
	// Verify checks an ID token and returns what it says; native admits the mobile apps' audiences
	// and the hashed nonce.
	Verify(ctx context.Context, p Provider, idToken, nonce string, native bool) (Claims, error)
	// AppleRefresh trades the authorization code a native app sends with its ID token for Apple's
	// refresh token, checking that both belong to the same person and app.
	AppleRefresh(ctx context.Context, p Provider, claims Claims, code string) (string, error)
	// FirstAuthorization reads the extra fields a provider posts to the callback the first time a
	// person authorizes the app (Apple's name); nil for the others.
	FirstAuthorization(p Provider, raw string) map[string]string
}
