package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// Multi-factor authentication: after a first factor (a password, a provider's ID token,
// later a magic link) a user who has a second factor gets a challenge instead of a
// session, and trades it for one with a code. The design is design/mfa-passwordless.md.

// The second factors.
const (
	MethodTOTP     = "totp"
	MethodRecovery = "recovery_code"
)

// The kinds of AuthChallenge.
const (
	// ChallengeMFA waits for a second factor of a user who has one.
	ChallengeMFA = "mfa"
	// ChallengeEnroll waits for a user in a required role to set a second factor up.
	ChallengeEnroll = "enroll"
	// ChallengeTOTPSetup is a TOTP secret waiting for its first code (the user is
	// setting authenticator app up).
	ChallengeTOTPSetup = "totp_setup"
	// ChallengeReauth is the token a re-authentication gave, bound to a session;
	// ChallengeReauthCode a code emailed to a user with no other proof.
	ChallengeReauth     = "reauth"
	ChallengeReauthCode = "reauth_code"
	// ChallengeMagicLink is a request for a sign-in link and code (see magiclink.go).
	ChallengeMagicLink = "magic_link"
	// ChallengeConsent waits for a user to accept consents (see consents_signin.go).
	ChallengeConsent = "consent"
)

// Challenge limits.
const (
	ChallengeTTL         = 5 * time.Minute
	MaxChallengeAttempts = 5
	// A user's failed second factors are counted across challenges for an hour; at
	// mfaLockAfter of them a new attempt waits mfaLockFor after the last failure.
	mfaFailureWindow = time.Hour
	mfaLockAfter     = 10
	mfaLockFor       = 15 * time.Minute
)

// totpStepSeconds and the digits are what every authenticator app supports.
const (
	totpStepSeconds = 30
	totpDigits      = otp.DigitsSix
)

var (
	// ErrInvalidChallenge answers a challenge that is unknown, used, expired or out of
	// attempts: the same for all of them.
	ErrInvalidChallenge = errors.New("the challenge is invalid or expired")
	// ErrSecondStepRequired is what the single-step entry points answer when the user
	// needs a second step (the HTTP layer uses the full outcome instead).
	ErrSecondStepRequired = errors.New("a second factor is required")
	// ErrMFADisabled: the realm doesn't let users turn MFA on.
	ErrMFADisabled = errors.New("multi-factor authentication is not enabled in this realm")
)

// InvalidCodeError is a second-factor code that is wrong; AttemptsLeft counts what the
// challenge still allows.
type InvalidCodeError struct{ AttemptsLeft int }

func (e *InvalidCodeError) Error() string { return "the code is not valid" }

// MFALockedError means too many second factors failed for the user: nothing is tried until
// RetryAfter has passed.
type MFALockedError struct{ RetryAfter time.Duration }

func (e *MFALockedError) Error() string {
	return fmt.Sprintf("too many failed codes; retry in %s", e.RetryAfter.Round(time.Second))
}

// AuthChallenge is a pending step between a first factor and a session. Its key is the
// hash of the secret value the caller holds.
type AuthChallenge struct {
	ID     string // HashToken of the value given to the caller
	Kind   string
	UserID string
	// Factor is the first factor that was proved: ProviderPassword, a provider's name, ...
	Factor string
	// NewUser and Profile are what a provider sign-in hands over with the session: kept
	// until the second step ends it.
	NewUser bool
	Profile map[string]string
	// Secret is a TOTP secret (SealToken form) of a ChallengeTOTPSetup.
	Secret string
	// SessionID is the session a re-authentication token or code is bound to; CodeHash is
	// the keyed hash of the code of a ChallengeReauthCode (set when the email is made).
	SessionID string
	CodeHash  string
	// A magic-link request: TokenHash is the hash of the link's token and CodeHash the code's;
	// CodeChallenge the PKCE challenge of the app that asked; RedirectTo where the link ends
	// ("" for the realm's app_url); Email the address when it has no user yet (sealed);
	// Purpose says what is sent (MagicLinkPurposeLink or MagicLinkPurposeNoAccount); Locale
	// the language for a new user.
	TokenHash     string
	CodeChallenge string
	RedirectTo    string
	// Org is the company context of a magic-link request: the page of its link keeps it.
	Org   string
	Email string
	Purpose       string
	Locale        string
	// Methods are how the session will say it was made (a ChallengeConsent keeps them
	// while the user accepts); Consents are the acceptances a magic-link request carried.
	Methods   []string
	Consents  map[string]string
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time
}

// MFARecord is a user's second-factor state: the TOTP secret (sealed), the last time step
// accepted and the unused recovery codes (keyed hashes).
type MFARecord struct {
	UserID        string
	Secret        string // SealToken form
	EnabledAt     time.Time
	LastStep      int64
	RecoveryCodes []string
}

// IssuedChallenge is a challenge handed to the caller: the value to send back and what it
// accepts.
type IssuedChallenge struct {
	Token   string
	Kind    string
	Methods []string
	// Consents are the consents and versions a ChallengeConsent waits for.
	Consents []registry.ConsentRef
}

// handover is what the sign-in carries to its end.
type handover struct {
	newUser bool
	profile map[string]string
}

// afterFirstFactor decides what a proved first factor leads to: a session, or a challenge
// when the user has a second factor (or must set one up). methods are recorded on the
// session; the first factor is always the first of them.
func (s *Users) afterFirstFactor(ctx context.Context, u User, factor string, h handover) (Redeemed, error) {
	_, err := s.Store.MFAOf(ctx, u.ID)
	enrolled := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Redeemed{}, err
	}
	switch {
	case enrolled:
		return s.issueChallenge(ctx, u, ChallengeMFA, factor, h)
	case s.Settings.MFA.Required(u.Roles):
		return s.issueChallenge(ctx, u, ChallengeEnroll, factor, h)
	}
	return s.finishSignIn(ctx, u, []string{factor}, h)
}

func (s *Users) issueChallenge(ctx context.Context, u User, kind, factor string, h handover) (Redeemed, error) {
	token, err := randomValue()
	if err != nil {
		return Redeemed{}, err
	}
	now := s.now()
	c := AuthChallenge{ID: HashToken(token), Kind: kind, UserID: u.ID, Factor: factor, NewUser: h.newUser, Attempts: 0, CreatedAt: now, ExpiresAt: now.Add(ChallengeTTL)}
	if h.newUser && len(h.profile) > 0 {
		c.Profile = h.profile
	}
	if err := s.Store.PutChallenge(ctx, c); err != nil {
		return Redeemed{}, err
	}
	ic := &IssuedChallenge{Token: token, Kind: kind}
	if kind == ChallengeMFA {
		ic.Methods = []string{MethodTOTP, MethodRecovery}
	}
	return Redeemed{Challenge: ic}, nil
}

// VerifyMFA trades a challenge and a second-factor code for the session the first factor
// was waiting for. method is MethodTOTP or MethodRecovery. ip is the caller's address, for
// the login throttle. The answer to an unknown, used, expired or exhausted challenge is
// always ErrInvalidChallenge.
func (s *Users) VerifyMFA(ctx context.Context, challenge, method, code, ip string) (Verified, error) {
	if challenge == "" {
		return Verified{}, ErrInvalidChallenge
	}
	id := HashToken(challenge)
	c, err := s.Store.GetChallenge(ctx, id, s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && c.Kind != ChallengeMFA) {
		return Verified{}, ErrInvalidChallenge
	}
	if err != nil {
		return Verified{}, err
	}
	ks := keys(s.Settings.Throttle(), "", ip)
	if err := s.checkThrottle(ctx, ks); err != nil {
		return Verified{}, err
	}
	// One check at a time per user: the limits below are read, then written, and a burst of
	// parallel guesses would all be read before any was counted.
	release, err := s.lockLogin(ctx, mfaUserKey(c.UserID))
	if err != nil {
		return Verified{}, err
	}
	defer release()
	if err := s.checkMFALock(ctx, c.UserID); err != nil {
		return Verified{}, err
	}
	var left int
	var ok bool
	switch method {
	case MethodTOTP:
		ok, err = s.checkTOTP(ctx, c.UserID, code)
	case MethodRecovery:
		left, ok, err = s.checkRecoveryCode(ctx, c.UserID, code)
	default:
		return Verified{}, &InvalidCodeError{AttemptsLeft: MaxChallengeAttempts - c.Attempts}
	}
	if err != nil {
		return Verified{}, err
	}
	if !ok {
		return Verified{}, s.secondFactorFailed(ctx, c, ks)
	}
	// One use: whoever deletes the challenge first gets the session.
	if _, err := s.Store.ClaimChallenge(ctx, id, s.now()); errors.Is(err, ErrNotFound) {
		return Verified{}, ErrInvalidChallenge
	} else if err != nil {
		return Verified{}, err
	}
	u, err := s.Store.UserByID(ctx, c.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && (u.Disabled || !u.ErasedAt.IsZero())) {
		return Verified{}, ErrInvalidChallenge
	}
	if err != nil {
		return Verified{}, err
	}
	second := MethodTOTP
	if method == MethodRecovery {
		second = "recovery"
	}
	out, err := s.finishSignIn(ctx, u, []string{c.Factor, second}, handover{newUser: c.NewUser, profile: c.Profile})
	if err != nil {
		return Verified{}, err
	}
	_ = s.Store.ClearLoginAttempts(ctx, mfaUserKey(u.ID))
	if method == MethodRecovery {
		s.recoveryCodeUsed(ctx, u, left)
	}
	if method == MethodRecovery {
		out.RecoveryCodesLeft = &left
	}
	return Verified{Redeemed: out, Factor: c.Factor, Method: method}, nil
}

// Verified is a session a second factor completed.
type Verified struct {
	Redeemed
	Factor string // the first factor
	Method string // the second
}

func mfaUserKey(userID string) string { return "mfa:user:" + userID }

// checkMFALock refuses while the user's failed second factors are locked out.
func (s *Users) checkMFALock(ctx context.Context, userID string) error {
	a, err := s.Store.LoginAttempts(ctx, mfaUserKey(userID))
	if err != nil {
		return err
	}
	if a.Failures >= mfaLockAfter {
		if wait := a.LastFailureAt.Add(mfaLockFor).Sub(s.now()); wait > 0 {
			s.Metrics.RateLimited(s.Realm, "mfa")
			return &MFALockedError{RetryAfter: wait}
		}
	}
	return nil
}

// secondFactorFailed counts a wrong code against the user, the address and the challenge,
// and answers with what is left.
func (s *Users) secondFactorFailed(ctx context.Context, c AuthChallenge, ks []throttleKey) error {
	now := s.now()
	var errs []error
	errs = append(errs, s.Store.RecordLoginFailure(ctx, mfaUserKey(c.UserID), now, now.Add(mfaFailureWindow)))
	errs = append(errs, s.recordFailure(ctx, ks))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	cur, err := s.Store.CountChallengeAttempt(ctx, c.ID, now)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidChallenge
	}
	if err != nil {
		return err
	}
	left := MaxChallengeAttempts - cur.Attempts
	if left <= 0 {
		_ = s.Store.DeleteChallenge(ctx, c.ID)
		left = 0
	}
	s.afterWrongCode(ctx, c, cur.Attempts)
	return &InvalidCodeError{AttemptsLeft: left}
}

// mfaFailedAfter is how many wrong codes after a right password say someone may know the
// password: the user is told, at most once an hour.
const mfaFailedAfter = 3

// afterWrongCode warns the user whose password was right and whose code was wrong.
func (s *Users) afterWrongCode(ctx context.Context, c AuthChallenge, attempts int) {
	if c.Factor != ProviderPassword || attempts < mfaFailedAfter || s.Settings.Email == nil {
		return
	}
	if err := s.RateLimit(ctx, "mfa-failed:"+c.UserID, 1, time.Hour); err != nil {
		return // told within the hour already
	}
	u, err := s.Store.UserByID(ctx, c.UserID)
	if err != nil || u.Disabled {
		return
	}
	s.queueNotice(ctx, EmailRequest{Kind: email.MFAFailed, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), Notice: true})
}

// recoveryCodeUsed tells the user a recovery code was spent.
func (s *Users) recoveryCodeUsed(ctx context.Context, u User, left int) {
	s.Audit(ctx, AuditRecoveryCodeUsed, userTarget(u.ID), map[string]any{"codes_left": left})
	s.securityNotice(ctx, u, "recovery-code-used", left)
}

// checkTOTP says whether code is the user's current TOTP code, and spends its time step: a
// code is accepted once, and an older one can't follow a newer one.
func (s *Users) checkTOTP(ctx context.Context, userID, code string) (bool, error) {
	rec, err := s.Store.MFAOf(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret, err := s.OpenToken(rec.Secret)
	if err != nil {
		return false, err
	}
	step, ok := matchTOTP(secret, code, s.now())
	if !ok {
		return false, nil
	}
	return s.Store.AdvanceMFAStep(ctx, userID, step)
}

// matchTOTP finds the time step (t-1, t or t+1) a code belongs to.
func matchTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	t := now.Unix() / totpStepSeconds
	found := int64(0)
	ok := false
	for _, d := range []int64{-1, 0, 1} {
		step := t + d
		want, err := hotp.GenerateCodeCustom(secret, uint64(step), hotp.ValidateOpts{Digits: totpDigits, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, false
		}
		// All three are compared, so the time taken doesn't say which matched.
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && !ok {
			found, ok = step, true
		}
	}
	return found, ok
}

// normalizeRecoveryCode makes what a person typed comparable.
func normalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// recoveryHash is the stored form of a recovery code: a keyed hash, bound to the user.
func (s *Users) recoveryHash(userID, code string) (string, error) {
	if s.Cipher == nil {
		return "", errors.New("the instance has no secrets key (BACKD_SECRETS_KEY)")
	}
	return s.Cipher.MAC("recovery", s.Realm, userID, normalizeRecoveryCode(code)), nil
}

// checkRecoveryCode spends a recovery code of the user.
func (s *Users) checkRecoveryCode(ctx context.Context, userID, code string) (left int, ok bool, err error) {
	if len(normalizeRecoveryCode(code)) != 10 {
		return 0, false, nil
	}
	h, err := s.recoveryHash(userID, code)
	if err != nil {
		return 0, false, err
	}
	return s.Store.UseRecoveryCode(ctx, userID, h)
}
