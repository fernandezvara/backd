package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/sso/pkce"
)

// Sign-in with an external identity provider (the flow is in internal/httpapi, the
// talk with the provider in sso/; this is what happens to the user).

// The lifetimes of the two short-lived records of the redirect flow.
const (
	OAuthStateTTL = 10 * time.Minute
	LoginCodeTTL  = time.Minute
)

// Intents of a sign-in attempt.
const (
	IntentSignIn = "signin"
	IntentLink   = "link"
)

var (
	// ErrAccountExists: an account has the provider's address, but it can't be
	// linked on the provider's word alone: sign in another way and link explicitly.
	ErrAccountExists = errors.New("an account with that address exists")
	// ErrLinkConflict: the provider account belongs to another user, or the user
	// already has an identity of that provider.
	ErrLinkConflict = errors.New("that provider account can't be linked to this user")
	// ErrSignInRefused: the account can't sign in (disabled, or from this network);
	// the caller is not told which.
	ErrSignInRefused = errors.New("sign-in refused")
	// ErrEmailRequired: the provider gave no address and accounts need one.
	ErrEmailRequired = errors.New("the provider gave no email address")
)

// RefusedError is a sign-in the account's state refuses; it is ErrSignInRefused to
// the caller, who is not told why, and says why to the audit.
type RefusedError struct {
	UserID string
	Reason string // disabled, network, erased
}

func (e *RefusedError) Error() string        { return ErrSignInRefused.Error() }
func (e *RefusedError) Is(target error) bool { return target == ErrSignInRefused }

// OAuthState is a sign-in attempt in progress: what backd must remember between
// sending the user to the provider and the provider sending them back. Its key is
// the hash of the state value the provider echoes.
type OAuthState struct {
	ID       string // HashToken(state)
	Provider string
	// Strategy is the strategy the app asked for ("" when it named none).
	Strategy string
	// Org is the organization whose connection the attempt goes through ("" for the realm's
	// own providers): the callback must be the one of that organization.
	Org string
	// ScopeOrg is the organization the person is signing in to: the session records it. It is
	// the connection's organization for a connection, and the organization of the route for a
	// realm provider used to sign in to one.
	ScopeOrg      string
	Intent        string
	CodeChallenge string // the app's PKCE challenge (S256)
	Verifier      string // backd's own PKCE verifier, for the provider
	Nonce         string
	RedirectTo    string
	InvitationID  string // HashToken of the invitation token, if one was passed (an organization's for a ScopeOrg)
	LinkUserID    string // IntentLink: whose identity is being added
	Locales       []string
	// Host is the sso engine's opaque host data, stored and returned untouched.
	Host []byte
	// Consents are the consents the app accepted for a sign-up through this attempt.
	Consents  map[string]string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// LoginCode is the one-time code the app gets in place of a session: it is redeemed
// with the PKCE verifier its challenge came from.
type LoginCode struct {
	ID     string // HashToken(code)
	UserID string
	// Method is the first factor the code stands for: the provider's name (magic links will
	// add their own), so that a second step can be asked for after the code is redeemed.
	Method string
	// Strategy is the strategy the sign-in follows.
	Strategy      string
	CodeChallenge string
	// NewUser says the sign-in made the user, and Profile is what the provider said of them:
	// the redemption hands both over once.
	NewUser bool
	Profile map[string]string
	// Org is the organization whose connection signed the person in; the session records it.
	Org       string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// randomValue is a URL-safe random string of 256 bits.
func randomValue() (string, error) {
	return newOpaqueToken("")
}

// NewPKCE makes a PKCE verifier and its S256 challenge.
func NewPKCE() (verifier, challenge string, err error) { return pkce.New() }

// PKCEChallenge is the S256 challenge of a verifier.
func PKCEChallenge(verifier string) string { return pkce.Challenge(verifier) }

// ValidPKCEChallenge says whether s looks like an S256 challenge (43 URL-safe characters).
func ValidPKCEChallenge(s string) bool { return pkce.Valid(s) }

// IssueLoginCode makes the one-time code that lets the app, holding the PKCE verifier
// of challenge, get a session for the user; method is the first factor that proved them.
func (s *Users) IssueLoginCode(ctx context.Context, userID, method, challenge string, newUser bool, profile map[string]string) (string, error) {
	return s.IssueLoginCodeIn(ctx, userID, method, challenge, newUser, profile, "")
}

// IssueLoginCodeIn is IssueLoginCode for a person an organization's connection signed in: the
// session the code becomes records the organization.
func (s *Users) IssueLoginCodeIn(ctx context.Context, userID, method, challenge string, newUser bool, profile map[string]string, org string) (string, error) {
	return s.IssueLoginCodeFor(ctx, userID, method, "", challenge, newUser, profile, org)
}

// IssueLoginCodeFor is IssueLoginCodeIn for a sign-in that follows the named strategy.
func (s *Users) IssueLoginCodeFor(ctx context.Context, userID, method, strategy, challenge string, newUser bool, profile map[string]string, org string) (string, error) {
	code, err := randomValue()
	if err != nil {
		return "", err
	}
	now := s.now()
	lc := LoginCode{ID: HashToken(code), UserID: userID, Method: method, Strategy: strategy, CodeChallenge: challenge, NewUser: newUser, Org: org, CreatedAt: now, ExpiresAt: now.Add(LoginCodeTTL)}
	if newUser && len(profile) > 0 {
		lc.Profile = profile
	}
	err = s.Store.PutLoginCode(ctx, lc)
	return code, err
}

// RedeemLoginCode trades a login code and the PKCE verifier for a session. The code is
// used up whether or not the verifier fits, so a stolen code can't be tried again.
// Every failure is ErrInvalidCredentials, and the answer is the same for an unknown,
// expired or used code.
func (s *Users) RedeemLoginCode(ctx context.Context, code, verifier string) (Redeemed, error) {
	failed := func(userID, strategy string) {
		s.auditSignInFailure(ctx, signInFailure{action: AuditUserLoginFailed, userID: userID, strategy: strategy, reason: ReasonInvalidCode})
	}
	if code == "" || verifier == "" {
		failed("", "")
		return Redeemed{}, ErrInvalidCredentials
	}
	lc, err := s.Store.ClaimLoginCode(ctx, HashToken(code), s.now())
	if errors.Is(err, ErrNotFound) {
		failed("", "")
		return Redeemed{}, ErrInvalidCredentials
	}
	if err != nil {
		return Redeemed{}, err
	}
	if subtle.ConstantTimeCompare([]byte(PKCEChallenge(verifier)), []byte(lc.CodeChallenge)) != 1 {
		failed(lc.UserID, lc.Strategy)
		return Redeemed{}, ErrInvalidCredentials
	}
	u, err := s.Store.UserByID(ctx, lc.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && (u.Disabled || !u.ErasedAt.IsZero())) {
		return Redeemed{}, ErrInvalidCredentials
	}
	if err != nil {
		return Redeemed{}, err
	}
	method := lc.Method
	if method == "" {
		method = "oauth"
	}
	return s.afterFirstFactor(ctx, u, method, handover{newUser: lc.NewUser, profile: lc.Profile, org: lc.Org, strategy: lc.Strategy})
}

// Redeemed is the outcome of redeeming a login code: the session, whether the sign-in made the
// user and, if so, what the provider said of them.
type Redeemed struct {
	Principal Principal
	Token     string
	NewUser   bool
	Profile   map[string]string
	// Challenge is set instead of a session when the user needs a second step.
	Challenge *IssuedChallenge
	// RecoveryCodesLeft is set when a recovery code ended the sign-in: how many remain.
	RecoveryCodesLeft *int
}

// StartProviderSession starts a session for a user a provider login resolved (the native
// flow, which has no login code to redeem).
func (s *Users) StartProviderSession(ctx context.Context, u User, provider string, newUser bool, profile map[string]string) (Redeemed, error) {
	return s.StartProviderSessionIn(ctx, u, provider, newUser, profile, "")
}

// StartProviderSessionIn is StartProviderSession for a person an organization's connection
// signed in: the session records the organization.
func (s *Users) StartProviderSessionIn(ctx context.Context, u User, provider string, newUser bool, profile map[string]string, org string) (Redeemed, error) {
	return s.StartProviderSessionFor(ctx, u, provider, "", newUser, profile, org)
}

// StartProviderSessionFor is StartProviderSessionIn for a sign-in that follows the named strategy.
func (s *Users) StartProviderSessionFor(ctx context.Context, u User, provider, strategy string, newUser bool, profile map[string]string, org string) (Redeemed, error) {
	return s.afterFirstFactor(ctx, u, provider, handover{newUser: newUser, profile: profile, org: org, strategy: strategy})
}

// ProviderLogin is a person an external provider has vouched for.
type ProviderLogin struct {
	Provider string
	// Org is the organization whose connection vouched for the person; their identity is
	// (Provider, Org, Subject). Such a login never links by address: TrustsEmail is false.
	Org string
	// Issuer is the issuer that vouched for Subject: with the provider and the organization, the
	// key of the identity.
	Issuer        string
	Subject       string // the provider's stable id for the person
	Email         string
	EmailVerified bool // as the provider asserts it
	// TrustsEmail: the provider's verified addresses may link an existing account
	// and mark a new one verified (Google and Apple).
	TrustsEmail bool
	Intent      string
	LinkUserID  string // IntentLink: the signed-in user
	// Invitation is the hash of the invitation token the attempt carried.
	Invitation string
	// ScopeOrg and OrgInvitation: the sign-in is to an organization (not through a connection of
	// its own) and the attempt carried one of its invitations (a hash), which lets a person with
	// no account make one whatever the realm's sign-up mode.
	ScopeOrg, OrgInvitation string
	Locales                 []string
	IP                      string
	// Consents and UserAgent: what a sign-up through the login accepted, and the proof's context.
	Consents  map[string]string
	UserAgent string
	// Profile is what the provider says of the person (CleanProfile's fields): handed to
	// the app and to account.on_signup when the login makes a new user, and kept nowhere else.
	Profile map[string]string
}

// ProviderResult is the outcome of a provider login.
type ProviderResult struct {
	User    User
	NewUser bool
	Linked  bool // an identity was added to an existing user
	// Profile is what the provider said of a new user (empty for anyone else).
	Profile map[string]string
}

// mayStartSession applies the rules every sign-in answers to.
func (s *Users) mayStartSession(u User, ip string) error {
	switch {
	case !u.ErasedAt.IsZero():
		return &RefusedError{UserID: u.ID, Reason: "erased"}
	case u.Disabled:
		return &RefusedError{UserID: u.ID, Reason: "disabled"}
	case !u.LoginNetworks.Allows(ip):
		return &RefusedError{UserID: u.ID, Reason: "network"}
	case s.Settings.Account.RequireVerifiedEmail && !u.EmailVerified:
		return ErrEmailNotVerified
	}
	return nil
}

// recordIdentityUse notes a sign-in with the identity and what the provider says of the
// address now. The user's own address never changes with it.
func (s *Users) recordIdentityUse(ctx context.Context, id Identity, in ProviderLogin, email string) {
	now := s.now()
	upd := IdentityUpdate{LastUsedAt: &now}
	if email != "" {
		verified := in.EmailVerified
		upd.Email, upd.EmailVerified = &email, &verified
	}
	if err := s.Store.UpdateIdentity(ctx, id.ID, upd, now); err != nil && s.Log != nil {
		s.Log.Warn("record the use of a sign-in method", "provider", in.Provider, "error", err)
	}
}

// addIdentity stores the identity of a provider account for the user and audits it.
func (s *Users) addIdentity(ctx context.Context, u User, in ProviderLogin, email string) error {
	now := s.now()
	err := s.Store.PutIdentity(ctx, Identity{
		ID: xid.New().String(), UserID: u.ID, Provider: in.Provider, Org: in.Org, Issuer: in.Issuer, Subject: in.Subject,
		Email: email, EmailVerified: in.EmailVerified && email != "", LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if errors.Is(err, ErrIdentityExists) {
		return ErrLinkConflict
	}
	if err != nil {
		return err
	}
	details := map[string]any{"provider": in.Provider}
	if in.Org != "" {
		details["org"] = in.Org
	}
	s.Audit(ctx, AuditIdentityLinked, userTarget(u.ID), details)
	return nil
}

// signUpWithProvider makes a user from a provider login, as the realm's sign-up mode allows.
func (s *Users) signUpWithProvider(ctx context.Context, in ProviderLogin, email string) (ProviderResult, error) {
	verified := in.TrustsEmail && in.EmailVerified
	restore := func() {}
	switch {
	case in.Org != "":
		// A person of an organization's connection is let in whatever the sign-up mode: the
		// connection's restriction is the gate, and the realm switched the feature on.
	case in.OrgInvitation != "" && s.admitsByInvitation(ctx, in, email, &verified):
		// An organization invited them: that is the way in, whatever the sign-up mode.
	case s.Settings.Signup == registry.SignupOpen:
	case s.Settings.Signup == registry.SignupInvite:
		if in.Invitation == "" {
			return ProviderResult{}, ErrInvitationRequired
		}
		inv, err := s.Store.ClaimInvitation(ctx, in.Invitation, s.now())
		if errors.Is(err, ErrNotFound) {
			return ProviderResult{}, ErrInvalidInvitation
		}
		if err != nil {
			return ProviderResult{}, err
		}
		restore = func() { _ = s.Store.CreateInvitation(context.WithoutCancel(ctx), inv) }
		if inv.Email != "" && inv.Email != email {
			restore()
			return ProviderResult{}, ErrInvalidInvitation
		}
		// An invitation bound to an address was sent to it: its owner vouches for it.
		verified = verified || inv.Email != ""
	default:
		return ProviderResult{}, ErrSignupClosed
	}
	now := s.now()
	roles := s.seedRoles(email)
	if roles == nil {
		roles = []string{}
	}
	u := User{ID: xid.New().String(), Email: email, Roles: roles, Locale: s.Settings.BestLocale(in.Locales...), EmailVerified: verified, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.CreateUser(ctx, u); err != nil {
		restore()
		if errors.Is(err, ErrEmailTaken) {
			return ProviderResult{}, ErrAccountExists // someone signed up with it a moment ago
		}
		return ProviderResult{}, err
	}
	if err := s.addIdentity(ctx, u, in, email); err != nil {
		_ = s.Store.DeleteUser(context.WithoutCancel(ctx), u.ID)
		restore()
		return ProviderResult{}, err
	}
	if len(in.Consents) > 0 {
		if err := s.recordGiven(ctx, u.ID, in.Consents, ConsentMethodSignup, ConsentContext{IP: in.IP, UserAgent: in.UserAgent}, u.Locale); err != nil {
			_ = s.Store.DeleteUser(context.WithoutCancel(ctx), u.ID)
			restore()
			return ProviderResult{}, err
		}
		u.Consents = mergeConsents(u.Consents, in.Consents)
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditUserSignup, userTarget(u.ID), map[string]any{"provider": in.Provider, "invited": in.Invitation != "", "roles": u.Roles})
	s.queueOnSignup(ctx, u, in.Provider, in.Profile)
	if err := s.mayStartSession(u, in.IP); err != nil {
		return ProviderResult{User: u, NewUser: true, Profile: in.Profile}, err
	}
	return ProviderResult{User: u, NewUser: true, Profile: in.Profile}, nil
}

// admitsByInvitation says whether the organization invitation of a provider login lets this
// address make an account. An invitation bound to the address was sent to it, so its owner
// vouches for it: verified is set.
func (s *Users) admitsByInvitation(ctx context.Context, in ProviderLogin, email string, verified *bool) bool {
	ok, viaAddress := s.invitationAdmits(ctx, in.OrgInvitation, in.ScopeOrg, email)
	if ok && viaAddress {
		*verified = true
	}
	return ok
}
