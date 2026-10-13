package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Magic links: one more first factor. The user asks for a link at their address; the email carries
// the link (opened on the same device, which ends in a one-time login code bound to the
// asking app's PKCE challenge) and a 6-digit code (typed in the asking app, on any device).
// Redeeming one uses up the request, so the other stops working. The design is
// design/mfa-passwordless.md §7.

// What a request sends.
const (
	MagicLinkPurposeLink      = "link"       // a link and a code
	MagicLinkPurposeNoAccount = "no_account" // a message that there is no account for the address
)

// MagicLinkTTL is how long a request's link and code work; MagicLinkCodeAttempts the wrong
// tries of its code.
const (
	MagicLinkTTL          = 10 * time.Minute
	MagicLinkCodeAttempts = MaxChallengeAttempts
	// ProviderEmail is the sign-in method of an address proved by a link or a code: an
	// identity of the user, recorded the first time, so that the rules about a user's last
	// way to sign in see it like any other.
	ProviderEmail = "email"
)

var (
	// ErrMagicLinkDisabled: the realm doesn't have magic links.
	ErrMagicLinkDisabled = errors.New("magic links are not enabled in this realm")
	// ErrInvalidMagicLink answers a link or a code that is unknown, used or expired.
	ErrInvalidMagicLink = errors.New("the link or the code is invalid or has expired")
	// ErrNoRedirect: the request has no place to send the browser (no redirect_to, and the
	// realm has no sign_in.app_url): the code is the way.
	ErrNoRedirect = errors.New("the request has nowhere to send the browser")
)

// MagicLinkInput is a request for a link and a code.
type MagicLinkInput struct {
	Email string
	// RedirectTo is where the link ends (already checked against the realm's allowed
	// redirects); "" for the realm's sign_in.app_url.
	RedirectTo string
	// CodeChallenge is the S256 PKCE challenge of a verifier only the asking app holds.
	CodeChallenge string
	Locales       []string
	IP, RequestID string
}

// RequestMagicLink makes a request and asks for its email, returning the request id the
// asking app keeps to redeem the code. It answers the same, in about the same work, for
// every address: a known one, one without an account (a message says so) and one that
// can't sign in (nothing is sent), so it says nothing about who is registered. An email limit
// that is reached changes nothing for the caller either: the message is skipped.
func (s *Users) RequestMagicLink(ctx context.Context, in MagicLinkInput) (string, error) {
	if !s.Settings.MagicLink.Enabled || s.Settings.Email == nil {
		return "", ErrMagicLinkDisabled
	}
	address, err := registry.NormalizeEmail(in.Email)
	if err != nil {
		return "", err
	}
	if !ValidPKCEChallenge(in.CodeChallenge) {
		return "", errors.New("code_challenge: must be the S256 PKCE challenge of a verifier")
	}
	requestID, err := randomValue()
	if err != nil {
		return "", err
	}
	now := s.now()
	c := AuthChallenge{
		ID: HashToken(requestID), Kind: ChallengeMagicLink, CodeChallenge: in.CodeChallenge, RedirectTo: in.RedirectTo,
		Locale: s.Settings.BestLocale(in.Locales...), CreatedAt: now, ExpiresAt: now.Add(MagicLinkTTL),
	}
	send := true
	kind := email.MagicLink
	u, err := s.Store.UserByEmail(ctx, address)
	switch {
	case err == nil && (u.Disabled || !u.ErasedAt.IsZero()):
		send = false // nothing to send, the answer is the same
		c.Purpose, c.UserID = MagicLinkPurposeLink, u.ID
	case err == nil:
		c.Purpose, c.UserID = MagicLinkPurposeLink, u.ID
	case errors.Is(err, ErrNotFound) && s.Cipher == nil:
		// The address of someone with no account can only be kept sealed (startup refuses a
		// realm with magic links and no key): without the key nothing is sent, and the answer
		// is still the same.
		send = false
		c.Purpose = MagicLinkPurposeNoAccount
	case errors.Is(err, ErrNotFound):
		sealed, serr := s.SealToken(address)
		if serr != nil {
			return "", serr
		}
		c.Email = sealed
		if s.Settings.MagicLink.Signup && s.Settings.Signup == registry.SignupOpen {
			c.Purpose = MagicLinkPurposeLink
		} else {
			c.Purpose, kind = MagicLinkPurposeNoAccount, email.NoAccount
		}
	default:
		return "", err
	}
	if err := s.Store.PutChallenge(ctx, c); err != nil {
		return "", err
	}
	req := EmailRequest{Kind: kind, Address: address, ClientIP: in.IP, RequestID: in.RequestID, ChallengeID: c.ID, Notice: kind == email.NoAccount}
	if u.ID != "" {
		req.UserID, req.Locale = u.ID, s.LocaleOf(u)
	} else {
		req.Locale = c.Locale
	}
	if !send {
		req.ChallengeID, req.UserID = "", "" // counts the limits only
	}
	if _, err := s.QueueEmail(ctx, req); err != nil {
		var limited *EmailLimitedError
		if !errors.As(err, &limited) {
			return "", err
		}
		// Over a limit: nobody is mailed, and the caller isn't told.
		_ = s.Store.DeleteChallenge(context.WithoutCancel(ctx), c.ID)
	}
	return requestID, nil
}

// MagicLinkEmailFor is what the worker needs to write a request's email: the address it goes to
// and, for a request that has a user, who they are.
func (s *Users) MagicLinkEmailFor(ctx context.Context, challengeID string) (AuthChallenge, string, error) {
	c, err := s.Store.GetChallenge(ctx, challengeID, s.now())
	if err != nil {
		return AuthChallenge{}, "", err
	}
	if c.Kind != ChallengeMagicLink {
		return AuthChallenge{}, "", ErrNotFound
	}
	if c.Email == "" {
		return c, "", nil
	}
	address, err := s.OpenToken(c.Email)
	return c, address, err
}

// MakeMagicLinkSecrets makes the link token and the code of a request when its email is
// written, keeps their hashes only, and returns them with when they stop working. A retried
// job gets new ones, and the earlier pair stops working.
func (s *Users) MakeMagicLinkSecrets(ctx context.Context, challengeID string) (token, code string, expires time.Time, err error) {
	token, err = randomValue()
	if err != nil {
		return "", "", time.Time{}, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", "", time.Time{}, err
	}
	code = fmt.Sprintf("%06d", n.Int64())
	c, err := s.Store.SetChallengeSecrets(ctx, challengeID, HashToken(token), s.codeHash(challengeID, code), s.now())
	if err != nil {
		return "", "", time.Time{}, err
	}
	return token, code, c.ExpiresAt, nil
}

// MagicLinkByToken finds the request a link token belongs to, without using it up: opening a
// link in a mail scanner must not spoil it.
func (s *Users) MagicLinkByToken(ctx context.Context, token string) (AuthChallenge, error) {
	c, err := s.Store.ChallengeByToken(ctx, HashToken(token), ChallengeMagicLink, s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && c.Purpose != MagicLinkPurposeLink) {
		return AuthChallenge{}, ErrInvalidMagicLink
	}
	return c, err
}

// MagicLinkDestination is where a request's link sends the browser: its redirect_to, else the
// realm's sign_in.app_url, else "" (then the code is the way).
func (s *Users) MagicLinkDestination(c AuthChallenge) string {
	if c.RedirectTo != "" {
		return c.RedirectTo
	}
	return s.Settings.SignInAppURL
}

// UseMagicLink is what opening the link does, once the person pressed the button: it uses up the
// request and signs the user in as far as a login code, which the browser carries to the asking
// app (bound to its PKCE challenge). It returns the code. Errors are those of a provider
// sign-in: a *RefusedError for an account that can't sign in, ErrSignupClosed.
func (s *Users) UseMagicLink(ctx context.Context, token, ip string) (code, destination string, err error) {
	c, err := s.MagicLinkByToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	destination = s.MagicLinkDestination(c)
	if destination == "" {
		return "", "", ErrNoRedirect
	}
	// Whoever deletes the request first gets the sign-in.
	if _, err := s.Store.ClaimChallenge(ctx, c.ID, s.now()); errors.Is(err, ErrNotFound) {
		return "", destination, ErrInvalidMagicLink
	} else if err != nil {
		return "", destination, err
	}
	u, newUser, err := s.magicLinkUser(ctx, c, ip)
	if err != nil {
		return "", destination, err
	}
	code, err = s.IssueLoginCode(ctx, u.ID, MagicLinkLoginMethod, c.CodeChallenge, newUser, nil)
	return code, destination, err
}

// MagicLinkLoginMethod is the first factor a link or a code stands for in sessions and
// challenges.
const MagicLinkLoginMethod = "link"

// VerifyMagicLinkCode is the code typed in the asking app: with the request id and the verifier
// of its PKCE challenge it signs the user in like the link would, answering a session or
// the MFA challenge. Five wrong codes end the request.
func (s *Users) VerifyMagicLinkCode(ctx context.Context, requestID, code, verifier, ip string) (Redeemed, error) {
	if requestID == "" || code == "" || verifier == "" {
		return Redeemed{}, ErrInvalidMagicLink
	}
	id := HashToken(requestID)
	ks := keys(s.Settings.Throttle(), "", ip)
	if err := s.checkThrottle(ctx, ks); err != nil {
		return Redeemed{}, err
	}
	c, err := s.Store.GetChallenge(ctx, id, s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && (c.Kind != ChallengeMagicLink || c.Purpose != MagicLinkPurposeLink || c.CodeHash == "")) {
		// An address that keeps trying ids that lead nowhere is slowed down like one that
		// keeps trying wrong codes.
		if rerr := s.recordFailure(ctx, ks); rerr != nil {
			return Redeemed{}, rerr
		}
		return Redeemed{}, ErrInvalidMagicLink
	}
	if err != nil {
		return Redeemed{}, err
	}
	cur, err := s.Store.CountChallengeAttempt(ctx, id, s.now())
	if errors.Is(err, ErrNotFound) {
		return Redeemed{}, ErrInvalidMagicLink
	}
	if err != nil {
		return Redeemed{}, err
	}
	if cur.Attempts > MagicLinkCodeAttempts {
		_ = s.Store.DeleteChallenge(ctx, id)
		return Redeemed{}, ErrInvalidMagicLink
	}
	// The code alone is not enough: the verifier ties it to the app that asked.
	okCode := subtle.ConstantTimeCompare([]byte(s.codeHash(id, code)), []byte(c.CodeHash)) == 1
	okVerifier := subtle.ConstantTimeCompare([]byte(PKCEChallenge(verifier)), []byte(c.CodeChallenge)) == 1
	if !okCode || !okVerifier {
		if cur.Attempts >= MagicLinkCodeAttempts {
			_ = s.Store.DeleteChallenge(ctx, id)
		}
		if rerr := s.recordFailure(ctx, ks); rerr != nil {
			return Redeemed{}, rerr
		}
		return Redeemed{}, &InvalidCodeError{AttemptsLeft: max(MagicLinkCodeAttempts-cur.Attempts, 0)}
	}
	if _, err := s.Store.ClaimChallenge(ctx, id, s.now()); errors.Is(err, ErrNotFound) {
		return Redeemed{}, ErrInvalidMagicLink
	} else if err != nil {
		return Redeemed{}, err
	}
	u, newUser, err := s.magicLinkUser(ctx, c, ip)
	if err != nil {
		return Redeemed{}, err
	}
	return s.afterFirstFactor(ctx, u, MagicLinkLoginMethod, handover{newUser: newUser})
}

// magicLinkUser resolves the user a redeemed request is for: the one it was made for, who becomes
// verified (only the owner of the mailbox could have used it), or a new verified user for an
// address with no account when the realm lets them in this way. The account rules apply
// like at any sign-in.
func (s *Users) magicLinkUser(ctx context.Context, c AuthChallenge, ip string) (User, bool, error) {
	var u User
	newUser := false
	switch {
	case c.UserID != "":
		var err error
		if u, err = s.Store.UserByID(ctx, c.UserID); errors.Is(err, ErrNotFound) {
			return User{}, false, &RefusedError{UserID: c.UserID, Reason: "user missing"}
		} else if err != nil {
			return User{}, false, err
		}
	case c.Email != "":
		if !s.Settings.MagicLink.Signup || s.Settings.Signup != registry.SignupOpen {
			return User{}, false, ErrSignupClosed
		}
		address, err := s.OpenToken(c.Email)
		if err != nil {
			return User{}, false, err
		}
		u, err = s.create(ctx, address, nil, c.Locale, true)
		switch {
		case errors.Is(err, ErrEmailTaken):
			// Someone made the account in the meantime: the request signs them in.
			if u, err = s.byEmail(ctx, address); err != nil {
				return User{}, false, err
			}
		case err != nil:
			return User{}, false, err
		default:
			newUser = true
			s.AuditAs(ctx, userTarget(u.ID), AuditUserSignup, userTarget(u.ID), map[string]any{"method": MagicLinkLoginMethod, "roles": u.Roles})
			s.queueOnSignup(ctx, u, MagicLinkLoginMethod, nil)
		}
	default:
		return User{}, false, ErrInvalidMagicLink
	}
	if !u.ErasedAt.IsZero() || u.Disabled {
		reason := "disabled"
		if !u.ErasedAt.IsZero() {
			reason = "erased"
		}
		return User{}, false, &RefusedError{UserID: u.ID, Reason: reason}
	}
	if !u.LoginNetworks.Allows(ip) {
		return User{}, false, &RefusedError{UserID: u.ID, Reason: "network"}
	}
	if !u.EmailVerified {
		verified := true
		if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{EmailVerified: &verified}, s.now()); err != nil {
			return User{}, false, err
		}
		u.EmailVerified = true
		s.Audit(ctx, AuditUserVerifyEmail, userTarget(u.ID), map[string]any{"verified": true, "by": MagicLinkLoginMethod})
	}
	s.recordEmailIdentity(ctx, u)
	return u, newUser, nil
}

// recordEmailIdentity notes that the user signs in by email: an identity recorded the first
// time, used like any other, so that the rule that a user keeps one way to sign in sees it.
func (s *Users) recordEmailIdentity(ctx context.Context, u User) {
	if _, err := s.Store.IdentityOf(ctx, u.ID, ProviderEmail); err == nil {
		s.touchIdentity(ctx, u.ID, ProviderEmail)
		return
	} else if !errors.Is(err, ErrNotFound) {
		if s.Log != nil {
			s.Log.Warn("read the email sign-in method", "error", err)
		}
		return
	}
	now := s.now()
	err := s.Store.PutIdentity(ctx, Identity{
		ID: xid.New().String(), UserID: u.ID, Provider: ProviderEmail, Subject: u.ID, LastUsedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil && !errors.Is(err, ErrIdentityExists) && s.Log != nil {
		s.Log.Warn("record the email sign-in method", "error", err)
	}
}
