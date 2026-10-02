package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// EmailToken is the record of a token sent in an email link (verifying an
// address, resetting a password, ...). backd stores only the SHA-256 hash of
// the token: the token itself exists only in memory and in the message
// handed to the delivery function.
type EmailToken struct {
	Hash       string // hex SHA-256 of the token; the record's id
	Purpose    string // verify-email, reset-password, change-email, revert-email-change or invitation
	UserID     string
	RedirectTo string // where the page after the link may send the user (already checked)
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     time.Time // zero until redeemed
}

// ErrInvalidToken is every way a token can be refused (unknown, expired,
// used, or for another purpose); callers answer them all the same.
var ErrInvalidToken = errors.New("invalid, expired or used token")

// HashEmailToken is the hash a token is stored under.
func HashEmailToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewEmailToken creates a token of 256 random bits for userID and purpose,
// valid for ttl, stores its hash, and returns the token itself. Whoever
// receives it must not store it.
func (s *Users) NewEmailToken(ctx context.Context, purpose, userID, redirectTo string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expiresAt = now.Add(ttl)
	err = s.Store.CreateEmailToken(ctx, EmailToken{
		Hash: HashEmailToken(token), Purpose: purpose, UserID: userID, RedirectTo: redirectTo, CreatedAt: now, ExpiresAt: expiresAt,
	})
	return token, expiresAt, err
}

// PeekEmailToken checks a token for purpose without using it, so a page can
// show its form before the person acts. Every failure is ErrInvalidToken.
func (s *Users) PeekEmailToken(ctx context.Context, token, purpose string) (EmailToken, error) {
	t, err := s.Store.GetEmailToken(ctx, HashEmailToken(token))
	if err != nil {
		return EmailToken{}, err
	}
	if t.Purpose != purpose || !t.UsedAt.IsZero() || !s.now().Before(t.ExpiresAt) {
		return EmailToken{}, ErrInvalidToken
	}
	return t, nil
}

// RedeemEmailToken uses a token for purpose: it hashes it, finds it, checks
// purpose, expiry and use, and marks it used in one atomic step, so of two
// parallel redemptions exactly one succeeds. A success also invalidates the
// user's other outstanding tokens of the same purpose. Every failure is
// ErrInvalidToken.
func (s *Users) RedeemEmailToken(ctx context.Context, token, purpose string) (EmailToken, error) {
	now := s.now()
	t, err := s.Store.RedeemEmailToken(ctx, HashEmailToken(token), purpose, now)
	if err != nil {
		return EmailToken{}, err
	}
	if err := s.Store.InvalidateEmailTokens(ctx, t.UserID, purpose, now); err != nil {
		return t, err
	}
	return t, nil
}
