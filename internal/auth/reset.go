package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

// RequestPasswordReset asks for a reset email. It answers nothing about the
// account: an unknown or disabled address does the same work as a known one,
// minus the queued job. Requests are counted per address and per client (a
// *ThrottledError once past the limit, for every address alike), and the
// email limits apply on top.
func (s *Users) RequestPasswordReset(ctx context.Context, address, redirectTo, ip, requestID string) error {
	if s.Settings.Email == nil {
		return ErrEmailNotConfigured
	}
	normalized, err := registry.NormalizeEmail(address)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(normalized))
	if err := s.RateLimit(ctx, "reset:to:"+hex.EncodeToString(sum[:]), resetPerAddress, resetWindow); err != nil {
		return err
	}
	if k := IPKey(ip); k != "" {
		if err := s.RateLimit(ctx, "reset:"+k, resetPerIP, resetWindow); err != nil {
			return err
		}
	}
	u, err := s.byEmail(ctx, normalized)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	req := EmailRequest{Kind: email.ResetPassword, Address: normalized, RedirectTo: redirectTo, ClientIP: ip, RequestID: requestID}
	if err == nil && !u.Disabled {
		req.UserID, req.Locale = u.ID, s.LocaleOf(u)
	}
	_, qerr := s.QueueEmail(ctx, req)
	var limited *EmailLimitedError
	if qerr != nil && !errors.As(qerr, &limited) {
		return qerr
	}
	return nil
}

// ResetPassword uses a reset token to set a new password. The password is
// checked first, so a refused one (*PolicyError) leaves the token usable.
// Success ends every session of the user, marks the address verified (the
// token proves the person reads that mailbox) and sends password-changed.
// It never starts a session. Every way the token can be wrong is
// ErrInvalidToken.
func (s *Users) ResetPassword(ctx context.Context, token, password string) (EmailToken, error) {
	hash, err := s.hash(ctx, password)
	if err != nil {
		return EmailToken{}, err
	}
	t, err := s.RedeemEmailToken(ctx, token, string(email.TokenPurpose(email.ResetPassword)))
	if err != nil {
		return EmailToken{}, err
	}
	u, err := s.Store.UserByID(ctx, t.UserID)
	if errors.Is(err, ErrNotFound) || (err == nil && u.Disabled) {
		return EmailToken{}, ErrInvalidToken
	}
	if err != nil {
		return EmailToken{}, err
	}
	now := s.now()
	if err := s.putPassword(ctx, u.ID, hash, now); err != nil {
		return EmailToken{}, err
	}
	if !u.EmailVerified {
		yes := true
		if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{EmailVerified: &yes}, now); err != nil {
			return EmailToken{}, err
		}
	}
	s.AuditAs(ctx, userTarget(u.ID), AuditPasswordReset, userTarget(u.ID), map[string]any{"verified_address": !u.EmailVerified})
	if err := s.Store.DeleteSessions(ctx, u.ID); err != nil {
		return EmailToken{}, err
	}
	s.notifyPasswordChanged(ctx, u)
	return t, nil
}

// notifyPasswordChanged queues the password-changed email, when the realm
// sends email. A limit or a failure never fails the change itself.
func (s *Users) notifyPasswordChanged(ctx context.Context, u User) {
	if s.Settings.Email == nil {
		return
	}
	_, err := s.QueueEmail(ctx, EmailRequest{Kind: email.PasswordChanged, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u)})
	var limited *EmailLimitedError
	if err != nil && !errors.As(err, &limited) && s.Log != nil {
		s.Log.Error("queue the password-changed email", "error", err)
	}
}
