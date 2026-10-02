package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fernandezvara/backd/internal/email"
)

// SignupRequest is a sign-up from a client.
type SignupRequest struct {
	Email, Password, Invitation string
	Locales                     []string // most wanted first
	RedirectTo                  string   // already checked against the realm's allowed redirects
	ClientIP, RequestID         string
}

// SignupResult is what a sign-up gives back: a session, or (Pending) none
// because the realm wants the address verified first.
type SignupResult struct {
	Principal Principal
	Token     string
	Pending   bool
}

// SignUp creates the account like Signup, and in a realm with email asks for
// the verification email. The realm may also require the address to be
// verified before any session: then there is none (Pending).
func (s *Users) SignUp(ctx context.Context, r SignupRequest) (SignupResult, error) {
	u, err := s.signup(ctx, r.Email, r.Password, r.Invitation, append([]string{}, r.Locales...))
	if err != nil {
		return SignupResult{}, err
	}
	if !u.EmailVerified {
		s.sendVerification(ctx, u, r.RedirectTo, r.ClientIP, r.RequestID)
	}
	if s.Settings.Account.RequireVerifiedEmail && !u.EmailVerified {
		return SignupResult{Pending: true}, nil
	}
	p, token, err := s.startSession(ctx, u)
	return SignupResult{Principal: p, Token: token}, err
}

// sendVerification queues the verify-email message. A limit or a failure
// never fails the request that caused it: the person can ask again.
func (s *Users) sendVerification(ctx context.Context, u User, redirectTo, ip, requestID string) {
	if s.Settings.Email == nil {
		return
	}
	_, err := s.QueueEmail(ctx, EmailRequest{Kind: email.VerifyEmail, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), RedirectTo: redirectTo, ClientIP: ip, RequestID: requestID})
	var limited *EmailLimitedError
	if err != nil && !errors.As(err, &limited) && s.Log != nil {
		s.Log.Error("queue the verification email", "error", err)
	}
}

// ResendVerification asks again for the verification email of an address.
// It answers nothing about the account: an unknown, disabled or already
// verified address does the same work, minus the queued job, and every
// limit counts the same.
func (s *Users) ResendVerification(ctx context.Context, address, redirectTo, ip, requestID string) error {
	if s.Settings.Email == nil {
		return ErrEmailNotConfigured
	}
	u, err := s.byEmail(ctx, address)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	req := EmailRequest{Kind: email.VerifyEmail, Address: address, RedirectTo: redirectTo, ClientIP: ip, RequestID: requestID}
	if err == nil && !u.EmailVerified && !u.Disabled {
		req.UserID, req.Locale = u.ID, s.LocaleOf(u)
	}
	_, qerr := s.QueueEmail(ctx, req)
	var limited *EmailLimitedError
	if qerr != nil && !errors.As(qerr, &limited) {
		return qerr
	}
	return nil
}

// VerifyEmail uses a verification token: it marks the address verified and,
// if the realm wants it, asks for the welcome email. It never starts a
// session. Every way the token can be wrong is ErrInvalidToken.
func (s *Users) VerifyEmail(ctx context.Context, token string) (EmailToken, error) {
	t, err := s.RedeemEmailToken(ctx, token, string(email.TokenPurpose(email.VerifyEmail)))
	if err != nil {
		return EmailToken{}, err
	}
	u, err := s.Store.UserByID(ctx, t.UserID)
	if errors.Is(err, ErrNotFound) {
		return EmailToken{}, ErrInvalidToken
	}
	if err != nil {
		return EmailToken{}, err
	}
	if !u.EmailVerified {
		yes := true
		if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{EmailVerified: &yes}, s.now()); err != nil {
			return EmailToken{}, err
		}
		s.AuditAs(ctx, userTarget(u.ID), AuditUserVerifyEmail, userTarget(u.ID), map[string]any{"verified": true})
		if s.Settings.Account.WelcomeEmail && s.Settings.Email != nil {
			if _, err := s.QueueEmail(ctx, EmailRequest{Kind: email.Welcome, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u)}); err != nil {
				var limited *EmailLimitedError
				if !errors.As(err, &limited) && s.Log != nil {
					s.Log.Error("queue the welcome email", "error", err)
				}
			}
		}
	}
	return t, nil
}

// purgeBatch bounds one purge pass.
const purgeBatch = 100

// PurgeUnverified deletes accounts that never verified their address and are
// older than account.purge_unverified_after, and returns how many. Accounts
// with roles are never purged. Documents the accounts created stay, as with
// any deletion of a user.
func (s *Users) PurgeUnverified(ctx context.Context) (int, error) {
	after := s.Settings.Account.PurgeUnverifiedAfter
	if after <= 0 {
		return 0, nil
	}
	users, err := s.Store.ListUnverifiedUsers(ctx, s.now().Add(-after), purgeBatch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if len(u.Roles) > 0 {
			continue
		}
		if err := s.Store.DeleteUser(ctx, u.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n++
	}
	if n > 0 {
		s.AuditAs(ctx, ActorSystem, AuditUserPurged, "", map[string]any{"count": n, "older_than": after.String()})
		if s.Log != nil {
			s.Log.Info("purged unverified accounts", slog.Int("count", n), "realm", s.Realm)
		}
	}
	return n, nil
}

// Per-address and per-client limits of reset requests, counted like failed
// logins and for every address, registered or not, so reaching one reveals
// nothing about the account.
const (
	resetPerAddress = 5
	resetPerIP      = 30
	resetWindow     = 15 * time.Minute
)
