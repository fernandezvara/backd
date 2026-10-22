package auth

import (
	"context"
	"errors"
	"time"

	"github.com/fernandezvara/backd/internal/email"
)

// ErrResetLinkUser means no reset link can be made for the user: they are disabled or erased.
var ErrResetLinkUser = errors.New("the user is disabled or erased: a reset link would open nothing")

// CreateResetLink makes a one-time password reset token for the user, to be handed to them out
// of band when their mailbox can't be reached (design/breach-response.md §5). It is the token a
// reset email carries: using it sets the password, ends the user's sessions and sends
// password-changed. Nothing is sent. The token is returned once; the audit record names the
// administrator and the user, never the token. redirectTo must already be checked against the
// realm's allowed redirects.
func (s *Users) CreateResetLink(ctx context.Context, userID, redirectTo string) (token string, expires time.Time, err error) {
	u, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	if u.Disabled || !u.ErasedAt.IsZero() {
		return "", time.Time{}, ErrResetLinkUser
	}
	purpose := email.TokenPurpose(email.ResetPassword)
	token, expires, err = s.NewEmailTokenFor(ctx, EmailToken{Purpose: string(purpose), UserID: u.ID, RedirectTo: redirectTo}, s.Settings.Account.TokenLifetime(purpose))
	if err != nil {
		return "", time.Time{}, err
	}
	details := map[string]any{"expires_at": expires.UTC().Format(time.RFC3339)}
	if redirectTo != "" {
		details["redirect_to"] = redirectTo
	}
	s.Audit(ctx, AuditPasswordResetLink, userTarget(u.ID), details)
	return token, expires, nil
}
