package auth

import (
	"context"
	"errors"
)

// ErrCodeRequired means the account uses a second factor and the request carried no code.
var ErrCodeRequired = errors.New("this account uses a second factor: send a code")

// PasswordUpdate is a user choosing a new password while signed out, by proving the current one.
type PasswordUpdate struct {
	Email, Password, NewPassword string
	// Method and Code are the second factor of an account that has one: MethodTOTP (the default)
	// or MethodRecovery.
	Method, Code string
	IP           string
}

// UpdatePassword replaces a user's password when they prove the current one, expired or not. It
// is the way out of a password an administrator expired for the user alone: the realm-wide
// expiry of a leak is refused (the old password may be in the leak), and the user resets it
// with the email instead. An account with a second factor must also give a code. Success ends
// every session of the user and starts none: they sign in again, second factor included.
// Wrong proofs count against the same limits as signing in.
func (s *Users) UpdatePassword(ctx context.Context, in PasswordUpdate) error {
	next, err := CheckPassword(in.NewPassword, s.Settings.PasswordMinLength)
	if err != nil {
		return err
	}
	var u User
	var id Identity
	var expired bool
	err = s.throttled(ctx, in.Email, in.IP, func() error {
		var err error
		if u, id, err = s.verifyLogin(ctx, in.Email, in.Password); err != nil {
			return err
		}
		// Outside the user's login networks the right password fails like a wrong one.
		if !u.LoginNetworks.Allows(in.IP) {
			return ErrInvalidCredentials
		}
		if byRealm, err := s.expiredByRealm(ctx, id); err != nil {
			return err
		} else if byRealm {
			return ErrPasswordExpired
		}
		expired, err = s.passwordExpired(ctx, u, id)
		return err
	})
	if errors.Is(err, ErrPasswordExpired) {
		err = &PasswordExpiredError{EmailQueued: s.mailExpiredPassword(ctx, u, in.IP)}
	}
	if err != nil {
		return err
	}
	// Before a code is spent: the new password must be another one.
	if same, err := s.Hasher.Verify(ctx, next, id.PasswordHash); err != nil {
		return err
	} else if same {
		return &PolicyError{"must be different from the current one"}
	}
	if _, err := s.Store.MFAOf(ctx, u.ID); err == nil {
		method := in.Method
		if method == "" {
			method = ReauthTOTP
		}
		if method != ReauthTOTP && method != ReauthRecovery {
			return ErrReauthMethod
		}
		if in.Code == "" {
			return ErrCodeRequired
		}
		if err := s.reauthSecondFactor(ctx, u, method, in.Code, in.IP); err != nil {
			return err
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	hash, err := s.Hasher.Hash(ctx, next)
	if err != nil {
		return err
	}
	now := s.now()
	if err := s.putPassword(ctx, u.ID, hash, now); err != nil {
		return err
	}
	if err := s.clearPasswordExpiry(ctx, u, now); err != nil {
		return err
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditPasswordChange, userTarget(u.ID), map[string]any{"by": "current_password", "was_expired": expired})
	if err := s.endSessions(ctx, u.ID, "password_changed"); err != nil {
		return err
	}
	s.notifyPasswordChanged(ctx, u)
	return nil
}

// expiredByRealm says whether the realm's password epoch, not only the user's own expiry,
// covers the password.
func (s *Users) expiredByRealm(ctx context.Context, id Identity) (bool, error) {
	changed := id.PasswordChangedAt
	if changed.IsZero() {
		changed = id.CreatedAt
	}
	epoch, err := s.Store.PasswordEpoch(ctx)
	if err != nil {
		return false, err
	}
	return !epoch.ValidAfter.IsZero() && changed.Before(epoch.ValidAfter), nil
}
