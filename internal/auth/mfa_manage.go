package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"image/png"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/fernandezvara/backd/internal/email"
)

// TOTPSetupTTL is how long a TOTP secret waits for its first code.
const TOTPSetupTTL = 15 * time.Minute

// RecoveryCodeCount is how many recovery codes a user gets.
const RecoveryCodeCount = 10

var (
	// ErrMFARequiredByRole: a role of the user requires MFA, so it can't be turned off.
	ErrMFARequiredByRole = errors.New("a role of this user requires multi-factor authentication")
	// ErrMFAUnavailable: the instance has no secrets key, which TOTP secrets are encrypted with.
	ErrMFAUnavailable = errors.New("multi-factor authentication needs the instance's secrets key (BACKD_SECRETS_KEY)")
	// ErrMFAAlreadyEnabled: the user already has a second factor.
	ErrMFAAlreadyEnabled = errors.New("multi-factor authentication is already enabled")
	// ErrMFANotEnabled: the user has no second factor.
	ErrMFANotEnabled = errors.New("multi-factor authentication is not enabled")
)

// MFAStatus is what a user can see of their second factor.
type MFAStatus struct {
	// Available says the realm lets users turn TOTP on (it may be off with users who
	// still have it).
	Available         bool
	Enabled           bool
	Methods           []string
	RecoveryCodesLeft int
	// Required says a role of the user requires MFA.
	Required bool
}

// MFAStatusOf reports a user's second-factor state.
func (s *Users) MFAStatusOf(ctx context.Context, u User) (MFAStatus, error) {
	st := MFAStatus{Available: !s.Settings.MFA.Off && s.Cipher != nil, Methods: []string{}, Required: s.Settings.MFA.Required(u.Roles)}
	rec, err := s.Store.MFAOf(ctx, u.ID)
	if errors.Is(err, ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return MFAStatus{}, err
	}
	st.Enabled, st.Methods, st.RecoveryCodesLeft = true, []string{MethodTOTP, MethodRecovery}, len(rec.RecoveryCodes)
	return st, nil
}

// UsersWithoutRequiredMFA lists, up to limit, the users whose role requires a second factor
// and who have none, oldest first by address, and says how many there are in all. Erased and
// disabled users are not counted: they can't sign in.
func (s *Users) UsersWithoutRequiredMFA(ctx context.Context, limit int) ([]User, int, error) {
	if !s.Settings.MFA.RequiredAll && len(s.Settings.MFA.RequiredRoles) == 0 {
		return nil, 0, nil
	}
	have, err := s.Store.MFAUserIDs(ctx)
	if err != nil {
		return nil, 0, err
	}
	enrolled := make(map[string]bool, len(have))
	for _, id := range have {
		enrolled[id] = true
	}
	var out []User
	total := 0
	after := ""
	for {
		page, more, err := s.Store.ListUsersPage(ctx, "", after, 0, 500)
		if err != nil {
			return nil, 0, err
		}
		for _, u := range page {
			if u.Disabled || !u.ErasedAt.IsZero() || enrolled[u.ID] || !s.Settings.MFA.Required(u.Roles) {
				continue
			}
			total++
			if len(out) < limit {
				out = append(out, u)
			}
		}
		if !more || len(page) == 0 {
			return out, total, nil
		}
		after = page[len(page)-1].Email
	}
}

// TOTPSetup is a secret an authenticator app is to be given.
type TOTPSetup struct {
	SetupID string
	Secret  string // base32, for typing in
	URI     string // otpauth://
	QR      string // a PNG data URI of the URI
	Expires time.Time
}

// issuer is the name authenticator apps show: the realm's setting, else its name.
func (s *Users) issuer() string {
	if i := s.Settings.MFA.Issuer; i != "" {
		return i
	}
	if s.Realm != "" {
		return s.Realm
	}
	return "backd"
}

// BeginTOTPSetup makes a TOTP secret for the user, held for 15 minutes until a first code
// from the app proves it was received. A new setup replaces any pending one.
func (s *Users) BeginTOTPSetup(ctx context.Context, u User) (TOTPSetup, error) {
	if s.Settings.MFA.Off {
		return TOTPSetup{}, ErrMFADisabled
	}
	if s.Cipher == nil {
		return TOTPSetup{}, ErrMFAUnavailable
	}
	if _, err := s.Store.MFAOf(ctx, u.ID); err == nil {
		return TOTPSetup{}, ErrMFAAlreadyEnabled
	} else if !errors.Is(err, ErrNotFound) {
		return TOTPSetup{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: s.issuer(), AccountName: u.Email, Period: totpStepSeconds, SecretSize: 20, Digits: totpDigits, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPSetup{}, err
	}
	sealed, err := s.SealToken(key.Secret())
	if err != nil {
		return TOTPSetup{}, err
	}
	setupID, err := randomValue()
	if err != nil {
		return TOTPSetup{}, err
	}
	img, err := key.Image(256, 256)
	if err != nil {
		return TOTPSetup{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return TOTPSetup{}, err
	}
	if err := s.Store.DeleteChallengesOfUser(ctx, u.ID, ChallengeTOTPSetup); err != nil {
		return TOTPSetup{}, err
	}
	now := s.now()
	c := AuthChallenge{ID: HashToken(setupID), Kind: ChallengeTOTPSetup, UserID: u.ID, Secret: sealed, CreatedAt: now, ExpiresAt: now.Add(TOTPSetupTTL)}
	if err := s.Store.PutChallenge(ctx, c); err != nil {
		return TOTPSetup{}, err
	}
	return TOTPSetup{
		SetupID: setupID, Secret: key.Secret(), URI: key.URL(),
		QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), Expires: c.ExpiresAt,
	}, nil
}

// ConfirmTOTP turns TOTP on for the user with the first code of the setup, and returns the
// recovery codes: shown once, kept only as keyed hashes. Every other session of the user
// ends, so a session from before can't pass a rule that asks for MFA; keepSession is the
// one that stays ("" for none). A wrong code is an *InvalidCodeError; five end the setup.
func (s *Users) ConfirmTOTP(ctx context.Context, u User, keepSession, setupID, code string) ([]string, error) {
	if s.Settings.MFA.Off {
		return nil, ErrMFADisabled
	}
	if s.Cipher == nil {
		return nil, ErrMFAUnavailable
	}
	if setupID == "" {
		return nil, ErrInvalidChallenge
	}
	id := HashToken(setupID)
	c, err := s.Store.GetChallenge(ctx, id, s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && (c.Kind != ChallengeTOTPSetup || c.UserID != u.ID)) {
		return nil, ErrInvalidChallenge
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.OpenToken(c.Secret)
	if err != nil {
		return nil, err
	}
	step, ok := matchTOTP(secret, code, s.now())
	if !ok {
		cur, err := s.Store.CountChallengeAttempt(ctx, id, s.now())
		if errors.Is(err, ErrNotFound) {
			return nil, ErrInvalidChallenge
		}
		if err != nil {
			return nil, err
		}
		left := max(MaxChallengeAttempts-cur.Attempts, 0)
		if left == 0 {
			_ = s.Store.DeleteChallenge(ctx, id)
		}
		return nil, &InvalidCodeError{AttemptsLeft: left}
	}
	// One use: whoever deletes the setup first enables.
	if _, err := s.Store.ClaimChallenge(ctx, id, s.now()); errors.Is(err, ErrNotFound) {
		return nil, ErrInvalidChallenge
	} else if err != nil {
		return nil, err
	}
	return s.enableMFA(ctx, u, c.Secret, step, keepSession)
}

// enableMFA stores the second factor with fresh recovery codes and ends the user's other
// sessions.
func (s *Users) enableMFA(ctx context.Context, u User, sealed string, step int64, keepSession string) ([]string, error) {
	codes, hashes, err := s.newRecoveryCodes(u.ID)
	if err != nil {
		return nil, err
	}
	if err := s.Store.PutMFA(ctx, MFARecord{UserID: u.ID, Secret: sealed, EnabledAt: s.now(), LastStep: step, RecoveryCodes: hashes}); err != nil {
		return nil, err
	}
	if err := s.Store.DeleteChallengesOfUser(ctx, u.ID, ChallengeTOTPSetup); err != nil {
		return nil, err
	}
	if err := s.endOtherSessions(ctx, u.ID, keepSession, "mfa_enabled"); err != nil {
		return nil, err
	}
	s.Audit(ctx, AuditMFAEnabled, userTarget(u.ID), nil)
	s.securityNotice(ctx, u, "mfa-enabled", 0)
	return codes, nil
}

// enrollChallenge resolves the challenge of a sign-in that must set a second factor up to
// its user. Any other challenge, an unknown, used or expired one, is ErrInvalidChallenge.
func (s *Users) enrollChallenge(ctx context.Context, token string) (AuthChallenge, User, error) {
	if token == "" {
		return AuthChallenge{}, User{}, ErrInvalidChallenge
	}
	c, err := s.Store.GetChallenge(ctx, HashToken(token), s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && c.Kind != ChallengeEnroll) {
		return AuthChallenge{}, User{}, ErrInvalidChallenge
	}
	if err != nil {
		return AuthChallenge{}, User{}, err
	}
	u, err := s.Store.UserByID(ctx, c.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && (u.Disabled || !u.ErasedAt.IsZero())) {
		return AuthChallenge{}, User{}, ErrInvalidChallenge
	}
	return c, u, err
}

// BeginEnrollment starts the TOTP setup of a user whose sign-in is waiting for one (their
// role requires MFA): the same secret and QR as BeginTOTPSetup, asked for with the challenge
// the first factor gave instead of a session.
func (s *Users) BeginEnrollment(ctx context.Context, challenge string) (TOTPSetup, error) {
	_, u, err := s.enrollChallenge(ctx, challenge)
	if err != nil {
		return TOTPSetup{}, err
	}
	return s.BeginTOTPSetup(ctx, u)
}

// CompleteEnrollment confirms the TOTP setup of a waiting sign-in with its first code, and
// finishes the sign-in: the outcome is the session the first factor was waiting for, with
// the second one recorded, and the recovery codes, shown once. Five wrong codes end the
// challenge.
func (s *Users) CompleteEnrollment(ctx context.Context, challenge, setupID, code string) (Verified, []string, error) {
	c, u, err := s.enrollChallenge(ctx, challenge)
	if err != nil {
		return Verified{}, nil, err
	}
	codes, err := s.ConfirmTOTP(ctx, u, "", setupID, code)
	var bad *InvalidCodeError
	if errors.As(err, &bad) {
		cur, cerr := s.Store.CountChallengeAttempt(ctx, c.ID, s.now())
		if errors.Is(cerr, ErrNotFound) || (cerr == nil && cur.Attempts >= MaxChallengeAttempts) {
			_ = s.Store.DeleteChallenge(ctx, c.ID)
			return Verified{}, nil, ErrInvalidChallenge
		}
	}
	if err != nil {
		return Verified{}, nil, err
	}
	// One use: the sign-in ends here, whoever got here first.
	if _, err := s.Store.ClaimChallenge(ctx, c.ID, s.now()); errors.Is(err, ErrNotFound) {
		return Verified{}, nil, ErrInvalidChallenge
	} else if err != nil {
		return Verified{}, nil, err
	}
	p, token, err := s.startSessionWith(ctx, u, []string{c.Factor, MethodTOTP}, true)
	if err != nil {
		return Verified{}, nil, err
	}
	return Verified{Redeemed: Redeemed{Principal: p, Token: token, NewUser: c.NewUser, Profile: c.Profile}, Factor: c.Factor, Method: MethodTOTP}, codes, nil
}

// DisableMFA removes the user's second factor and recovery codes; refused while a role of
// the user requires one.
func (s *Users) DisableMFA(ctx context.Context, u User) error {
	if s.Settings.MFA.Required(u.Roles) {
		return ErrMFARequiredByRole
	}
	if _, err := s.Store.MFAOf(ctx, u.ID); errors.Is(err, ErrNotFound) {
		return ErrMFANotEnabled
	} else if err != nil {
		return err
	}
	if err := s.Store.DeleteMFA(ctx, u.ID); err != nil {
		return err
	}
	if err := s.Store.DeleteChallengesOfUser(ctx, u.ID, ChallengeMFA, ChallengeEnroll, ChallengeTOTPSetup); err != nil {
		return err
	}
	s.Audit(ctx, AuditMFADisabled, userTarget(u.ID), nil)
	s.securityNotice(ctx, u, "mfa-disabled", 0)
	return nil
}

// RegenerateRecoveryCodes replaces every recovery code of the user and returns the new
// ones, shown once.
func (s *Users) RegenerateRecoveryCodes(ctx context.Context, u User) ([]string, error) {
	rec, err := s.Store.MFAOf(ctx, u.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrMFANotEnabled
	}
	if err != nil {
		return nil, err
	}
	codes, hashes, err := s.newRecoveryCodes(u.ID)
	if err != nil {
		return nil, err
	}
	rec.RecoveryCodes = hashes
	if err := s.Store.PutMFA(ctx, rec); err != nil {
		return nil, err
	}
	s.Audit(ctx, AuditRecoveryRegenerate, userTarget(u.ID), nil)
	s.securityNotice(ctx, u, "recovery-codes-regenerated", 0)
	return codes, nil
}

// recoveryAlphabet has no characters that look alike (no 0/O, 1/I/L).
const recoveryAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// newRecoveryCodes makes the codes shown to the user (xxxxx-xxxxx) and their stored form.
func (s *Users) newRecoveryCodes(userID string) (codes, hashes []string, err error) {
	for range RecoveryCodeCount {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		for j := range b {
			b[j] = recoveryAlphabet[int(b[j])%len(recoveryAlphabet)]
		}
		code := string(b[:5]) + "-" + string(b[5:])
		h, err := s.recoveryHash(userID, code)
		if err != nil {
			return nil, nil, err
		}
		codes, hashes = append(codes, code), append(hashes, h)
	}
	return codes, hashes, nil
}

// KeyCreatedBySession tells the signed-in administrator who made an API key that they did, by
// email: a key outlives the session, so a stolen session making one is noticed.
func (s *Users) KeyCreatedBySession(ctx context.Context, p Principal) {
	s.securityNotice(ctx, p.User, "api-key-created", 0)
}

// securityNotice tells the user about a change to their sign-in security. A limit or a
// failure never fails the change: the notice is a courtesy and a tripwire, not a gate.
func (s *Users) securityNotice(ctx context.Context, u User, event string, codesLeft int) {
	if s.Settings.Email == nil {
		return
	}
	s.queueNotice(ctx, EmailRequest{Kind: email.SecurityNotice, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), Notice: true, Event: strings.TrimSpace(event), CodesLeft: codesLeft})
}

// queueNotice queues an email that only informs, logging what fails.
func (s *Users) queueNotice(ctx context.Context, r EmailRequest) {
	_, err := s.QueueEmail(ctx, r)
	var limited *EmailLimitedError
	if err != nil && !errors.As(err, &limited) && s.Log != nil {
		s.Log.Error("queue a security notice", "kind", r.Kind, "error", err)
	}
}
