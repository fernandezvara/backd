package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/email"
)

// Re-authentication: before a sensitive action the user gives one fresh proof of the
// strongest kind they have, and gets a short-lived token for the header
// X-Backd-Reauth. The proof is a second-factor code for a user with MFA, the password for
// one with a password, a code sent by email for anyone else.

// The proofs (the method of a re-authentication).
const (
	ReauthPassword  = "password"
	ReauthEmailCode = "email_code"
	// ReauthTOTP and ReauthRecovery are the second factors' own names.
	ReauthTOTP     = MethodTOTP
	ReauthRecovery = MethodRecovery
)

// ReauthCodeTTL and the attempts a code allows.
const (
	ReauthCodeTTL      = 10 * time.Minute
	ReauthCodeAttempts = MaxChallengeAttempts
)

var (
	// ErrReauthRequired: the action needs a fresh proof the request didn't have.
	ErrReauthRequired = errors.New("a fresh proof of identity is required")
	// ErrReauthMethod: that proof isn't the kind this user gives.
	ErrReauthMethod = errors.New("that proof is not accepted for this user")
)

// ReauthMethodsOf says which proofs a user gives: the second factor's when they have one
// (a password alone is not enough then), else the password, else a code sent by email. A
// user with none of them (no password, no second factor, and a realm that sends no email)
// has no way to prove anything more than their session, and sensitive actions don't ask.
func (s *Users) ReauthMethodsOf(ctx context.Context, u User) ([]string, error) {
	if _, err := s.Store.MFAOf(ctx, u.ID); err == nil {
		return []string{ReauthTOTP, ReauthRecovery}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if _, err := s.Store.IdentityOf(ctx, u.ID, ProviderPassword); err == nil {
		return []string{ReauthPassword}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if s.Settings.Email != nil {
		return []string{ReauthEmailCode}, nil
	}
	return []string{}, nil
}

// ReauthGrant is a re-authentication token.
type ReauthGrant struct {
	Token   string
	Expires time.Time
}

// Reauthenticate checks one proof of the signed-in user and gives a token bound to their
// session, good for the realm's reauth.ttl and for any number of actions in it. proof is
// the code, or for ReauthPassword the password. Wrong proofs count against the same limits
// as signing in.
func (s *Users) Reauthenticate(ctx context.Context, p Principal, method, proof, ip string) (ReauthGrant, error) {
	methods, err := s.ReauthMethodsOf(ctx, p.User)
	if err != nil {
		return ReauthGrant{}, err
	}
	if !slices.Contains(methods, method) {
		return ReauthGrant{}, ErrReauthMethod
	}
	switch method {
	case ReauthTOTP, ReauthRecovery:
		err = s.reauthSecondFactor(ctx, p.User, method, proof, ip)
	case ReauthPassword:
		err = s.checkPassword(ctx, p.User, proof)
	case ReauthEmailCode:
		err = s.checkReauthCode(ctx, p, proof)
	}
	if err != nil {
		return ReauthGrant{}, err
	}
	token, err := randomValue()
	if err != nil {
		return ReauthGrant{}, err
	}
	now := s.now()
	c := AuthChallenge{ID: HashToken(token), Kind: ChallengeReauth, UserID: p.User.ID, SessionID: p.Session.ID, CreatedAt: now, ExpiresAt: now.Add(s.Settings.ReauthLifetime())}
	if err := s.Store.PutChallenge(ctx, c); err != nil {
		return ReauthGrant{}, err
	}
	s.Audit(ctx, AuditReauth, userTarget(p.User.ID), map[string]any{"method": method})
	return ReauthGrant{Token: token, Expires: c.ExpiresAt}, nil
}

// reauthSecondFactor checks a TOTP or recovery code, counting a wrong one against the user's
// lock and the address's throttle like the sign-in does.
func (s *Users) reauthSecondFactor(ctx context.Context, u User, method, code, ip string) error {
	ks := keys(s.Settings.Throttle(), "", ip)
	if err := s.checkThrottle(ctx, ks); err != nil {
		return err
	}
	if err := s.checkMFALock(ctx, u.ID); err != nil {
		return err
	}
	var ok bool
	var left int
	var err error
	if method == ReauthTOTP {
		ok, err = s.checkTOTP(ctx, u.ID, code)
	} else {
		left, ok, err = s.checkRecoveryCode(ctx, u.ID, code)
	}
	if err != nil {
		return err
	}
	if !ok {
		now := s.now()
		err := errors.Join(
			s.Store.RecordLoginFailure(ctx, mfaUserKey(u.ID), now, now.Add(mfaFailureWindow)),
			s.recordFailure(ctx, ks))
		if err != nil {
			return err
		}
		return &InvalidCodeError{AttemptsLeft: -1}
	}
	_ = s.Store.ClearLoginAttempts(ctx, mfaUserKey(u.ID))
	if method == ReauthRecovery {
		s.recoveryCodeUsed(ctx, u, left)
	}
	return nil
}

// CheckReauth says whether token is a re-authentication of this session (ErrReauthRequired
// otherwise). A user who gives no proof (ReauthMethodsOf is empty) always passes.
func (s *Users) CheckReauth(ctx context.Context, p Principal, token string) error {
	methods, err := s.ReauthMethodsOf(ctx, p.User)
	if err != nil {
		return err
	}
	if len(methods) == 0 {
		return nil
	}
	if token == "" {
		return ErrReauthRequired
	}
	c, err := s.Store.GetChallenge(ctx, HashToken(token), s.now())
	if errors.Is(err, ErrNotFound) {
		return ErrReauthRequired
	}
	if err != nil {
		return err
	}
	if c.Kind != ChallengeReauth || c.UserID != p.User.ID || c.SessionID != p.Session.ID {
		return ErrReauthRequired
	}
	return nil
}

// RequestReauthCode makes a code and emails it to a user whose proof is one (no second
// factor, no password): the code is made when the message is, kept only as a hash, and
// good for 10 minutes and 5 attempts. A new request replaces the earlier code.
func (s *Users) RequestReauthCode(ctx context.Context, p Principal, ip, requestID string) error {
	methods, err := s.ReauthMethodsOf(ctx, p.User)
	if err != nil {
		return err
	}
	if !slices.Contains(methods, ReauthEmailCode) {
		return ErrReauthMethod
	}
	id, err := randomValue()
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.Store.DeleteChallengesOfUser(ctx, p.User.ID, ChallengeReauthCode); err != nil {
		return err
	}
	c := AuthChallenge{ID: HashToken(id), Kind: ChallengeReauthCode, UserID: p.User.ID, SessionID: p.Session.ID, CreatedAt: now, ExpiresAt: now.Add(ReauthCodeTTL)}
	if err := s.Store.PutChallenge(ctx, c); err != nil {
		return err
	}
	_, err = s.QueueEmail(ctx, EmailRequest{
		Kind: email.ReauthCode, UserID: p.User.ID, Address: p.User.Email, Locale: s.LocaleOf(p.User),
		ClientIP: ip, RequestID: requestID, ChallengeID: c.ID, Notice: true,
	})
	if err != nil {
		_ = s.Store.DeleteChallenge(context.WithoutCancel(ctx), c.ID)
	}
	return err
}

// MakeReauthCode is what the worker calls when it writes the email: it makes a fresh code,
// keeps its hash on the challenge and returns the code and when it stops working. A job
// that is tried again gets a new code, and the earlier one stops working.
func (s *Users) MakeReauthCode(ctx context.Context, challengeID string) (string, time.Time, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", time.Time{}, err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	c, err := s.Store.SetChallengeCode(ctx, challengeID, s.codeHash(challengeID, code), s.now())
	if err != nil {
		return "", time.Time{}, err
	}
	return code, c.ExpiresAt, nil
}

// codeHash is the stored form of a code: keyed with the instance's key when it has one.
func (s *Users) codeHash(challengeID, code string) string {
	if s.Cipher != nil {
		return s.Cipher.MAC("code", s.Realm, challengeID, code)
	}
	return HashToken("code|" + challengeID + "|" + code)
}

// checkReauthCode checks the emailed code of the session, five attempts at most.
func (s *Users) checkReauthCode(ctx context.Context, p Principal, code string) error {
	c, err := s.Store.ChallengeOfUser(ctx, p.User.ID, ChallengeReauthCode, s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && (c.SessionID != p.Session.ID || c.CodeHash == "")) {
		return ErrReauthRequired
	}
	if err != nil {
		return err
	}
	cur, err := s.Store.CountChallengeAttempt(ctx, c.ID, s.now())
	if errors.Is(err, ErrNotFound) {
		return ErrReauthRequired
	}
	if err != nil {
		return err
	}
	if cur.Attempts > ReauthCodeAttempts {
		_ = s.Store.DeleteChallenge(ctx, c.ID)
		return ErrReauthRequired
	}
	if subtle.ConstantTimeCompare([]byte(s.codeHash(c.ID, code)), []byte(c.CodeHash)) != 1 {
		if cur.Attempts >= ReauthCodeAttempts {
			_ = s.Store.DeleteChallenge(ctx, c.ID)
		}
		return &InvalidCodeError{AttemptsLeft: max(ReauthCodeAttempts-cur.Attempts, 0)}
	}
	return s.Store.DeleteChallenge(ctx, c.ID)
}
