package auth

import (
	"context"
	"errors"
	"time"
)

// ErrPasswordExpired means the password is correct but was set before the realm's password
// epoch or the user's own expiry: it opens nothing until the user chooses a new one
// (design/breach-response.md §3).
var ErrPasswordExpired = errors.New("this password has expired: set a new one with a password reset, or ask an administrator")

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

// mailExpiredPassword sends the reset email a refused sign-in leads to, as a reset request
// would: it is rate-limited like one, counts in the email limits, and fails quietly (the
// answer to the sign-in does not depend on it).
func (s *Users) mailExpiredPassword(ctx context.Context, u User, ip string) {
	if s.Settings.Email == nil || u.Disabled {
		return
	}
	_ = s.RequestPasswordReset(ctx, u.Email, "", ip, "")
}
