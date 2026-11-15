package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Sign-in entries of the audit trail (design/sign-in-audit.md). Like every audit action they
// are switched on or off with `audit.entries`.
const (
	// AuditUserLogin is a successful sign-in by a user without an admin role (administrators
	// record admin.login, passkeys passkey.login, organization connections org.sso.login).
	// Details: strategy, methods, session_id. Off by default.
	AuditUserLogin = "user.login"
	// AuditUserLoginFailed is a first step that failed. The target is the user, or `anonymous`
	// with address_hash when the address has no account. Details: strategy, reason, attempts
	// (when the window saw more than one) and throttled.
	AuditUserLoginFailed = "user.login_failed"
)

// Reasons of a failed sign-in. The app is told what it is told today; the trail may be more
// precise because only administrators read it.
const (
	ReasonInvalidCredentials = "invalid_credentials" // a wrong password and an unknown address look the same
	ReasonDisabled           = "disabled"
	ReasonUnverified         = "unverified"
	ReasonNetwork            = "network"
	ReasonThrottled          = "throttled"
	ReasonPasswordExpired    = "password_expired"
	ReasonInvalidCode        = "invalid_code"
	ReasonInvalidLink        = "invalid_link"
)

// signInFailureWindow is how long failures of one target from one address are counted together.
const signInFailureWindow = 10 * time.Minute

// auditLogin records a successful sign-in that has no entry of its own.
func (s *Users) auditLogin(ctx context.Context, u User, sess Session, methods []string, passkey bool) {
	if len(methods) == 0 || s.Settings.IsAdmin(u.Roles) || passkey || strings.HasPrefix(methods[0], "sso:") {
		return
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditUserLogin, userTarget(u.ID), map[string]any{"session_id": sess.ID, "strategy": sess.Strategy, "methods": methods})
}

// signInFailure describes one failed sign-in for auditSignInFailure.
type signInFailure struct {
	action   string // AuditUserLoginFailed (or the step action, below)
	email    string // the address the person typed or the challenge was made for; "" if none
	userID   string // the account, when the failure knows it
	strategy string
	step     string // the method of a failed step
	reason   string
	ip       string
}

// auditSignInFailure records a failed sign-in, at most once per target and address in a window
// plus again at 10, 100, 1,000… failures (with the count), so an attacker cannot make the trail
// grow with the number of guesses. Failures that are not about a person's credentials are not
// passed here.
func (s *Users) auditSignInFailure(ctx context.Context, f signInFailure) {
	if !s.Settings.AuditEnabled(f.action) {
		return
	}
	target, details := s.failureTarget(ctx, f)
	details["reason"] = f.reason
	if f.strategy != "" {
		details["strategy"] = f.strategy
	}
	if f.step != "" {
		details["step"] = f.step
	}
	ip := f.ip
	if ip == "" {
		ip = auditSource(ctx).ClientIP
	}
	key := "audit-fail:" + f.action + ":" + target + ":" + IPKey(ip)
	if f.reason == ReasonThrottled {
		// The failure that made the throttle refuse is recorded once per window.
		key += ":throttled"
		details["throttled"] = true
	}
	n, _, err := s.Store.IncrementCounter(ctx, key, s.now(), s.now().Add(signInFailureWindow))
	if err != nil {
		return // the trail must not make a failed sign-in fail differently
	}
	if f.reason == ReasonThrottled {
		if n != 1 {
			return
		}
	} else if !isPowerOfTen(n) {
		return
	} else if n > 1 {
		details["attempts"] = n
	}
	s.Audit(ctx, f.action, target, details)
}

func isPowerOfTen(n int) bool {
	if n < 1 {
		return false
	}
	for n%10 == 0 {
		n /= 10
	}
	return n == 1
}

// failureTarget names who a failure was about without ever storing an address: the user's id
// when the account exists, else `anonymous` with a keyed hash of the address. It also refines
// the reason of a refused sign-in with what the account says.
func (s *Users) failureTarget(ctx context.Context, f signInFailure) (string, map[string]any) {
	details := map[string]any{}
	var u User
	found := false
	switch {
	case f.userID != "":
		if got, err := s.Store.UserByID(ctx, f.userID); err == nil {
			u, found = got, true
		}
	case f.email != "":
		if got, err := s.byEmail(ctx, f.email); err == nil {
			u, found = got, true
		}
	}
	if found {
		return userTarget(u.ID), details
	}
	if f.email != "" && s.Cipher != nil {
		// A keyed hash of the address under the instance's secrets key: it tells two failures
		// about the same address apart, and keeps nothing a dictionary could undo.
		if norm, err := registry.NormalizeEmail(f.email); err == nil {
			details["address_hash"] = s.Cipher.MAC("audit-address", s.Realm, norm)[:24]
		}
	}
	return ActorAnonymous, details
}

// passwordFailureReason says why a password sign-in failed, or "" when it did not fail on the
// person's credentials (a storage error, say).
func (s *Users) passwordFailureReason(ctx context.Context, email, ip string, err error) string {
	var throttled *ThrottledError
	var expired *PasswordExpiredError
	switch {
	case errors.As(err, &throttled):
		return ReasonThrottled
	case errors.Is(err, ErrEmailNotVerified):
		return ReasonUnverified
	case errors.Is(err, ErrPasswordExpired), errors.As(err, &expired):
		return ReasonPasswordExpired
	case errors.Is(err, ErrInvalidCredentials):
		if u, uerr := s.byEmail(ctx, email); uerr == nil {
			switch {
			case u.Disabled:
				return ReasonDisabled
			case !u.LoginNetworks.Allows(ip):
				return ReasonNetwork
			}
		}
		return ReasonInvalidCredentials
	}
	return ""
}
