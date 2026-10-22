package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/fernandezvara/backd/internal/email"
)

// ErrPasswordExpired means the password is correct but was set before the realm's password
// epoch or the user's own expiry: it opens nothing until the user chooses a new one
// (design/breach-response.md §3).
var ErrPasswordExpired = errors.New("this password has expired: set a new one with a password reset, or ask an administrator")

// PasswordExpiredError is ErrPasswordExpired for a sign-in: it also says whether the
// password-expired email, with a reset link, was queued for the user (errors.Is matches
// ErrPasswordExpired).
type PasswordExpiredError struct{ EmailQueued bool }

func (e *PasswordExpiredError) Error() string { return ErrPasswordExpired.Error() }

// Is makes errors.Is(err, ErrPasswordExpired) true.
func (e *PasswordExpiredError) Is(target error) bool { return target == ErrPasswordExpired }

// PasswordEpoch is the moment before which no password is valid in a realm. It only moves
// forward; a password set after it (reset, change, administrator) is valid again.
type PasswordEpoch struct {
	ValidAfter time.Time
	SetBy      string
	SetAt      time.Time
	Reason     string
}

// passwordExpired says whether id, the user's password identity, was set before the realm's
// epoch or the user's own expiry.
func (s *Users) passwordExpired(ctx context.Context, u User, id Identity) (bool, error) {
	changed := id.PasswordChangedAt
	if changed.IsZero() {
		changed = id.CreatedAt
	}
	if !u.PasswordExpiredAt.IsZero() && changed.Before(u.PasswordExpiredAt) {
		return true, nil
	}
	epoch, err := s.Store.PasswordEpoch(ctx)
	if err != nil {
		return false, err
	}
	return !epoch.ValidAfter.IsZero() && changed.Before(epoch.ValidAfter), nil
}

// ErrEpochNotLater means the realm already has a password epoch as late as now: it only
// moves forward (a clock behind the one that set it, or two commands in the same instant).
var ErrEpochNotLater = errors.New("a password epoch as late as this one already exists")

// ExpireResult is what ExpireAllPasswords did.
type ExpireResult struct {
	ValidAfter time.Time
	// Accounts is how many accounts have a password, and so are affected; Sessions how many
	// sessions ended.
	Accounts, Sessions int64
}

// ExpireAllPasswords moves the realm's password epoch to now: every password set before it
// stops opening its account, and a restore of older system data doesn't bring them back.
// Sessions end too, unless keepSessions (a planned rotation rather than a leak). The epoch
// only moves forward. The reason goes to the audit trail.
func (s *Users) ExpireAllPasswords(ctx context.Context, reason string, keepSessions bool) (ExpireResult, error) {
	now := s.now()
	epoch := PasswordEpoch{ValidAfter: now, SetBy: auditSource(ctx).Actor, SetAt: now, Reason: reason}
	ok, err := s.Store.SetPasswordEpoch(ctx, epoch)
	if err != nil {
		return ExpireResult{}, err
	}
	if !ok {
		return ExpireResult{}, ErrEpochNotLater
	}
	res := ExpireResult{ValidAfter: now}
	if res.Accounts, err = s.Store.CountPasswordIdentities(ctx); err != nil {
		return res, err
	}
	if !keepSessions {
		if res.Sessions, err = s.Store.DeleteAllSessions(ctx); err != nil {
			return res, err
		}
		s.Metrics.SessionsEnded(s.Realm, "passwords_expired", res.Sessions)
	}
	details := map[string]any{"scope": "realm", "accounts": res.Accounts, "sessions": res.Sessions, "keep_sessions": keepSessions}
	if reason != "" {
		details["reason"] = reason
	}
	s.Audit(ctx, AuditPasswordExpire, "realm", details)
	return res, nil
}

// RevokeAllSessions ends every session of the realm and says how many; passwords are not
// touched.
func (s *Users) RevokeAllSessions(ctx context.Context) (int64, error) {
	n, err := s.Store.DeleteAllSessions(ctx)
	if err != nil {
		return 0, err
	}
	s.Metrics.SessionsEnded(s.Realm, "revoked_all", n)
	s.Audit(ctx, AuditSessionRevokeAll, "realm", map[string]any{"sessions": n})
	return n, nil
}

// PasswordExpiredFor says whether the user's password is expired now: they have one, and it was
// set before the realm's epoch or their own expiry. A user with no password isn't.
func (s *Users) PasswordExpiredFor(ctx context.Context, u User) (bool, error) {
	id, err := s.Store.IdentityOf(ctx, u.ID, ProviderPassword)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.passwordExpired(ctx, u, id)
}

// PasswordStatus is the realm's password epoch and how many accounts have a password.
type PasswordStatus struct {
	Epoch    PasswordEpoch // the zero value when none was set
	Accounts int64
}

// PasswordState reports the realm's epoch and the number of accounts with a password, what an
// administrator confirms before expiring every password.
func (s *Users) PasswordState(ctx context.Context) (PasswordStatus, error) {
	epoch, err := s.Store.PasswordEpoch(ctx)
	if err != nil {
		return PasswordStatus{}, err
	}
	n, err := s.Store.CountPasswordIdentities(ctx)
	return PasswordStatus{Epoch: epoch, Accounts: n}, err
}

// ExpirePassword expires one user's password: it stops opening the account until the user
// resets it or an administrator sets a new one. Sessions are left alone.
func (s *Users) ExpirePassword(ctx context.Context, email, reason string) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{PasswordExpiredAt: &now}, now); err != nil {
		return err
	}
	details := map[string]any{"scope": "user"}
	if reason != "" {
		details["reason"] = reason
	}
	s.Audit(ctx, AuditPasswordExpire, userTarget(u.ID), details)
	return nil
}

// mailExpiredPassword sends the email a refused sign-in leads to: the password-expired message
// with a reset link. It is rate-limited like a reset request (per address and per client),
// counts in the email limits, and fails quietly: the answer to the sign-in doesn't depend on
// it. A user who was sent it is marked as told, so a later notification run skips them.
func (s *Users) mailExpiredPassword(ctx context.Context, u User, ip string) bool {
	if s.Settings.Email == nil || u.Disabled {
		return false
	}
	sum := sha256.Sum256([]byte(u.Email))
	if s.RateLimit(ctx, "reset:to:"+hex.EncodeToString(sum[:]), resetPerAddress, resetWindow) != nil {
		return false
	}
	if k := IPKey(ip); k != "" && s.RateLimit(ctx, "reset:"+k, resetPerIP, resetWindow) != nil {
		return false
	}
	epoch, err := s.Store.PasswordEpoch(ctx)
	if err != nil {
		return false
	}
	if _, err := s.QueueEmail(ctx, EmailRequest{Kind: email.PasswordExpired, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), ClientIP: ip, Event: epoch.Reason}); err != nil {
		return false
	}
	now := s.now()
	_ = s.Store.UpdateUser(ctx, u.ID, UserUpdate{PasswordNotifiedAt: &now}, now)
	return true
}
