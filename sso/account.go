package sso

import (
	"encoding/json"
	"errors"
)

// Intent is what a sign-in attempt is for.
type Intent string

// The intents of an attempt.
const (
	IntentSignIn Intent = "signin"
	IntentLink   Intent = "link"
)

// Valid says whether i is one of the two.
func (i Intent) Valid() bool { return i == IntentSignIn || i == IntentLink }

// Account is a user of the host, as far as sign-in is concerned.
type Account struct {
	ID            string
	Email         string
	EmailVerified bool
	Disabled      bool
	Erased        bool
}

// IdentityKey names a provider account: the identity it makes is unique on it.
type IdentityKey struct {
	Provider string
	Org      string // the organization of a connection; empty for the realm's providers
	Subject  string
}

// Claims are what a provider said of the person, checked: a verified ID token's claims, or the
// fields of a userinfo answer.
type Claims struct {
	// Subject identifies the person for the provider.
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	GivenName     string
	FamilyName    string
	Picture       string
	// Audience is the client id the token was issued to.
	Audience string
	// HostedDomain is Google's hd, Tenant Microsoft's tid and Username its preferred_username,
	// as the verified token states them: what a connection's restriction reads.
	HostedDomain string
	Tenant       string
	Username     string
}

// Profile is what the provider says of the person (name, given_name, family_name, picture):
// handed to the app once, for a new user, and kept nowhere.
type Profile map[string]string

// Request is what the host knows of the HTTP request an engine call came from.
type Request struct {
	IP        string
	UserAgent string
	Locales   []string
}

// Login is a person a provider has vouched for, with the context the host needs to decide what
// to do about it.
type Login struct {
	Provider Provider
	Claims   Claims
	Profile  Profile
	Intent   Intent
	// LinkUserID is, for IntentLink, the signed-in user.
	LinkUserID string
	// Host is the host's own data from the start of the attempt (the library never reads it).
	Host    json.RawMessage
	Request Request
}

// Result is who a login turned out to be.
type Result struct {
	Account Account
	NewUser bool
	// Linked: an identity was added to an existing account.
	Linked  bool
	Profile Profile
}

// Errors the decision and the host's effects end in. The engine turns each into a code for the
// app (CodeOf).
var (
	// ErrAccountExists: an account has the provider's address, but it can't be linked on the
	// provider's word alone.
	ErrAccountExists = errors.New("an account with that address exists")
	// ErrLinkConflict: the provider account belongs to another user, or the user already has an
	// identity of that provider.
	ErrLinkConflict = errors.New("that provider account can't be linked to this user")
	// ErrSignupClosed: sign-up is not open.
	ErrSignupClosed = errors.New("sign-up is closed")
	// ErrInvitationInvalid: sign-up needs an invitation and there is none that fits.
	ErrInvitationInvalid = errors.New("sign-up needs a valid invitation")
	// ErrEmailNotVerified: the realm requires a verified address and the account's is not.
	ErrEmailNotVerified = errors.New("the email address is not verified")
	// ErrEmailRequired: the provider gave no address and accounts need one.
	ErrEmailRequired = errors.New("the provider gave no email address")
	// ErrNotAllowed: the provider's person is not one the place admits.
	ErrNotAllowed = errors.New("the person is not allowed to sign in here")
	// ErrNotFound: the host has no such user or identity.
	ErrNotFound = errors.New("not found")
)

// Refused is a sign-in the account's state refuses (disabled, erased, from this network). The
// app is only told it was refused; Reason is for the audit.
type Refused struct {
	UserID string
	Reason string
}

func (e *Refused) Error() string { return "sign-in refused" }

// The codes a failed attempt gives the app, as backd_error.
const (
	CodeCancelled         = "cancelled"
	CodeAccountExists     = "account_exists"
	CodeSignupClosed      = "signup_closed"
	CodeInvitationInvalid = "invitation_invalid"
	CodeLinkConflict      = "link_conflict"
	CodeEmailNotVerified  = "email_not_verified"
	CodeEmailRequired     = "email_required"
	CodeSignInRefused     = "signin_refused"
	CodeProviderError     = "provider_error"
	CodeNotAllowed        = "not_allowed"
	CodeSSORequired       = "sso_required"
)

// CodeOf is the code the app is given for an error of the decision, or CodeProviderError for
// one it does not know (the host logs it).
func CodeOf(err error) string {
	var refused *Refused
	switch {
	case errors.As(err, &refused):
		return CodeSignInRefused
	case errors.Is(err, ErrAccountExists):
		return CodeAccountExists
	case errors.Is(err, ErrLinkConflict):
		return CodeLinkConflict
	case errors.Is(err, ErrSignupClosed):
		return CodeSignupClosed
	case errors.Is(err, ErrInvitationInvalid):
		return CodeInvitationInvalid
	case errors.Is(err, ErrEmailNotVerified):
		return CodeEmailNotVerified
	case errors.Is(err, ErrEmailRequired):
		return CodeEmailRequired
	case errors.Is(err, ErrNotAllowed):
		return CodeNotAllowed
	}
	return CodeProviderError
}
