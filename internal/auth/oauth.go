package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Sign-in with an external identity provider (the flow is in internal/httpapi, the
// talk with the provider in internal/oauth; this is what happens to the user).

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
	ID            string // HashToken(state)
	Provider      string
	Intent        string
	CodeChallenge string // the app's PKCE challenge (S256)
	Verifier      string // backd's own PKCE verifier, for the provider
	Nonce         string
	RedirectTo    string
	InvitationID  string // HashToken of the invitation token, if one was passed
	LinkUserID    string // IntentLink: whose identity is being added
	Locales       []string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// LoginCode is the one-time code the app gets in place of a session: it is redeemed
// with the PKCE verifier its challenge came from.
type LoginCode struct {
	ID            string // HashToken(code)
	UserID        string
	CodeChallenge string
	// NewUser says the sign-in made the user, and Profile is what the provider said of them:
	// the redemption hands both over once.
	NewUser   bool
	Profile   map[string]string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// randomValue is a URL-safe random string of 256 bits.
func randomValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewPKCE makes a PKCE verifier and its S256 challenge.
func NewPKCE() (verifier, challenge string, err error) {
	if verifier, err = randomValue(); err != nil {
		return "", "", err
	}
	return verifier, PKCEChallenge(verifier), nil
}

// PKCEChallenge is the S256 challenge of a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidPKCEChallenge says whether s looks like an S256 challenge (43 URL-safe characters).
func ValidPKCEChallenge(s string) bool {
	if len(s) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

// BeginOAuth records a sign-in attempt and returns the state value to send to the
// provider and the nonce and PKCE verifier to use with it.
func (s *Users) BeginOAuth(ctx context.Context, st OAuthState) (state, nonce, verifier string, err error) {
	if state, err = randomValue(); err != nil {
		return "", "", "", err
	}
	if nonce, err = randomValue(); err != nil {
		return "", "", "", err
	}
	if verifier, err = randomValue(); err != nil {
		return "", "", "", err
	}
	now := s.now()
	st.ID, st.Nonce, st.Verifier, st.CreatedAt, st.ExpiresAt = HashToken(state), nonce, verifier, now, now.Add(OAuthStateTTL)
	if err := s.Store.PutOAuthState(ctx, st); err != nil {
		return "", "", "", err
	}
	return state, nonce, verifier, nil
}

// TakeOAuthState returns the attempt a state value belongs to, and forgets it: a state
// is good for one callback. ErrNotFound when it is unknown, used or expired.
func (s *Users) TakeOAuthState(ctx context.Context, state string) (OAuthState, error) {
	if state == "" {
		return OAuthState{}, ErrNotFound
	}
	return s.Store.ClaimOAuthState(ctx, HashToken(state), s.now())
}

// IssueLoginCode makes the one-time code that lets the app, holding the PKCE verifier
// of challenge, get a session for the user.
func (s *Users) IssueLoginCode(ctx context.Context, userID, challenge string, newUser bool, profile map[string]string) (string, error) {
	code, err := randomValue()
	if err != nil {
		return "", err
	}
	now := s.now()
	lc := LoginCode{ID: HashToken(code), UserID: userID, CodeChallenge: challenge, NewUser: newUser, CreatedAt: now, ExpiresAt: now.Add(LoginCodeTTL)}
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
	if code == "" || verifier == "" {
		return Redeemed{}, ErrInvalidCredentials
	}
	lc, err := s.Store.ClaimLoginCode(ctx, HashToken(code), s.now())
	if errors.Is(err, ErrNotFound) {
		return Redeemed{}, ErrInvalidCredentials
	}
	if err != nil {
		return Redeemed{}, err
	}
	if subtle.ConstantTimeCompare([]byte(PKCEChallenge(verifier)), []byte(lc.CodeChallenge)) != 1 {
		return Redeemed{}, ErrInvalidCredentials
	}
	u, err := s.Store.UserByID(ctx, lc.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && (u.Disabled || !u.ErasedAt.IsZero())) {
		return Redeemed{}, ErrInvalidCredentials
	}
	if err != nil {
		return Redeemed{}, err
	}
	p, token, err := s.startSession(ctx, u)
	if err != nil {
		return Redeemed{}, err
	}
	return Redeemed{Principal: p, Token: token, NewUser: lc.NewUser, Profile: lc.Profile}, nil
}

// Redeemed is the outcome of redeeming a login code: the session, whether the sign-in made the
// user and, if so, what the provider said of them.
type Redeemed struct {
	Principal Principal
	Token     string
	NewUser   bool
	Profile   map[string]string
}

// StartProviderSession starts a session for a user a provider login resolved (the native
// flow, which has no login code to redeem).
func (s *Users) StartProviderSession(ctx context.Context, u User) (Principal, string, error) {
	return s.startSession(ctx, u)
}

// ProviderLogin is a person an external provider has vouched for.
type ProviderLogin struct {
	Provider      string
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
	Locales    []string
	IP         string
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

// ResolveProviderLogin decides who a provider's person is, under the rules of the
// design: an identity already linked signs that user in; an address that matches
// an account links only when the provider is trusted, says the address is verified
// and the account's is too; otherwise a new account is made if the realm's sign-up
// allows it. It does not start a session.
func (s *Users) ResolveProviderLogin(ctx context.Context, in ProviderLogin) (ProviderResult, error) {
	email := ""
	if in.Email != "" {
		var err error
		if email, err = registry.NormalizeEmail(in.Email); err != nil {
			email = ""
		}
	}
	existing, err := s.Store.Identity(ctx, in.Provider, in.Subject)
	switch {
	case err == nil:
		return s.knownIdentity(ctx, in, existing, email)
	case !errors.Is(err, ErrNotFound):
		return ProviderResult{}, err
	}
	if in.Intent == IntentLink {
		return s.linkIdentity(ctx, in, email)
	}
	return s.signInNewIdentity(ctx, in, email)
}

// knownIdentity handles a provider account that is already linked.
func (s *Users) knownIdentity(ctx context.Context, in ProviderLogin, id Identity, email string) (ProviderResult, error) {
	if in.Intent == IntentLink && id.UserID != in.LinkUserID {
		return ProviderResult{}, ErrLinkConflict
	}
	u, err := s.Store.UserByID(ctx, id.UserID)
	if errors.Is(err, ErrNotFound) {
		return ProviderResult{}, &RefusedError{UserID: id.UserID, Reason: "user missing"}
	}
	if err != nil {
		return ProviderResult{}, err
	}
	if in.Intent != IntentLink {
		if err := s.mayStartSession(u, in.IP); err != nil {
			return ProviderResult{}, err
		}
	}
	s.recordIdentityUse(ctx, id, in, email)
	return ProviderResult{User: u}, nil
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

// linkIdentity adds a provider account to the signed-in user.
func (s *Users) linkIdentity(ctx context.Context, in ProviderLogin, email string) (ProviderResult, error) {
	u, err := s.Store.UserByID(ctx, in.LinkUserID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ProviderResult{}, err
	}
	if err != nil || u.Disabled || !u.ErasedAt.IsZero() {
		return ProviderResult{}, &RefusedError{UserID: in.LinkUserID, Reason: "link to an unusable user"}
	}
	if err := s.addIdentity(ctx, u, in, email); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{User: u, Linked: true}, nil
}

// addIdentity stores the identity of a provider account for the user and audits it.
func (s *Users) addIdentity(ctx context.Context, u User, in ProviderLogin, email string) error {
	now := s.now()
	err := s.Store.PutIdentity(ctx, Identity{
		ID: xid.New().String(), UserID: u.ID, Provider: in.Provider, Subject: in.Subject,
		Email: email, EmailVerified: in.EmailVerified && email != "", LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if errors.Is(err, ErrIdentityExists) {
		return ErrLinkConflict
	}
	if err != nil {
		return err
	}
	s.Audit(ctx, AuditIdentityLinked, userTarget(u.ID), map[string]any{"provider": in.Provider})
	return nil
}

// signInNewIdentity handles a provider account backd has not seen: it links to the
// account with the same address if the rules allow it, or signs a new user up.
func (s *Users) signInNewIdentity(ctx context.Context, in ProviderLogin, email string) (ProviderResult, error) {
	if email == "" {
		return ProviderResult{}, ErrEmailRequired
	}
	u, err := s.Store.UserByEmail(ctx, email)
	switch {
	case err == nil:
		if !in.TrustsEmail || !in.EmailVerified || !u.EmailVerified {
			return ProviderResult{}, ErrAccountExists
		}
		if err := s.mayStartSession(u, in.IP); err != nil {
			return ProviderResult{}, err
		}
		if err := s.addIdentity(ctx, u, in, email); err != nil {
			return ProviderResult{}, err
		}
		return ProviderResult{User: u, Linked: true}, nil
	case !errors.Is(err, ErrNotFound):
		return ProviderResult{}, err
	}
	return s.signUpWithProvider(ctx, in, email)
}

// signUpWithProvider makes a user from a provider login, as the realm's sign-up mode allows.
func (s *Users) signUpWithProvider(ctx context.Context, in ProviderLogin, email string) (ProviderResult, error) {
	verified := in.TrustsEmail && in.EmailVerified
	restore := func() {}
	switch s.Settings.Signup {
	case registry.SignupOpen:
	case registry.SignupInvite:
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
	s.AuditAs(ctx, userTarget(u.ID), AuditUserSignup, userTarget(u.ID), map[string]any{"provider": in.Provider, "invited": in.Invitation != "", "roles": u.Roles})
	s.queueOnSignup(ctx, u, in.Provider, in.Profile)
	if err := s.mayStartSession(u, in.IP); err != nil {
		return ProviderResult{User: u, NewUser: true, Profile: in.Profile}, err
	}
	return ProviderResult{User: u, NewUser: true, Profile: in.Profile}, nil
}
