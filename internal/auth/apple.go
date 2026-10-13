package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Apple requires an app that offers Sign in with Apple and account deletion to revoke the
// user's tokens when the account is deleted. backd keeps Apple's refresh token, sealed with
// the realm's secrets cipher, on the Apple identity for that and nothing else; when the
// identity goes away (the account is erased or deleted, or Apple is unlinked) a revoke job
// takes the sealed token and a worker revokes it, trying again after a failure.

const (
	// ProviderApple is the identity provider of Sign in with Apple.
	ProviderApple = "apple"
	// RevokeOrigin is the origin of a revoke job in the job list.
	RevokeOrigin = "backd:apple.revoke"
	revokeFunc   = "revoke-apple"
	revokeLease  = 2 * time.Minute
)

// RevokeJob is a job that revokes an Apple refresh token. It holds the token sealed, and a
// worker drops it once Apple has revoked it.
type RevokeJob struct {
	UserID   string
	ClientID string // the client id the token was issued to
	Token    string // sealed (SealToken)
}

// SealToken encrypts a token for storage with the realm's secrets cipher. The result
// names the key it was sealed under, so a rotated key is noticed.
func (s *Users) SealToken(token string) (string, error) {
	if s.Cipher == nil {
		return "", errors.New("the instance has no secrets key (BACKD_SECRETS_KEY)")
	}
	ct, nonce, err := s.Cipher.Seal(token)
	if err != nil {
		return "", err
	}
	return s.Cipher.KeyID + "." + nonce + "." + ct, nil
}

// OpenToken decrypts what SealToken made.
func (s *Users) OpenToken(sealed string) (string, error) {
	if s.Cipher == nil {
		return "", errors.New("the instance has no secrets key (BACKD_SECRETS_KEY)")
	}
	parts := strings.SplitN(sealed, ".", 3)
	if len(parts) != 3 {
		return "", errors.New("the stored token is not in the sealed form")
	}
	return s.Cipher.Open(parts[2], parts[1], parts[0])
}

// SaveAppleToken keeps Apple's refresh token on the user's Apple identity, sealed, unless the
// provider is configured with revoke_on_delete: false (then nothing is stored).
func (s *Users) SaveAppleToken(ctx context.Context, userID, clientID, refreshToken string) error {
	p := s.Settings.Providers[ProviderApple]
	if p == nil || !p.RevokeOnDelete || refreshToken == "" {
		return nil
	}
	sealed, err := s.SealToken(refreshToken)
	if err != nil {
		return err
	}
	id, err := s.Store.IdentityOf(ctx, userID, ProviderApple)
	if err != nil {
		return err
	}
	return s.Store.UpdateIdentity(ctx, id.ID, IdentityUpdate{AppleRefreshToken: &sealed, AppleClientID: &clientID}, s.now())
}

// queueAppleRevocation queues the revocation of the user's Apple token, if the user has one.
// It is called before the identity is removed, so the token is not lost with it.
func (s *Users) queueAppleRevocation(ctx context.Context, userID string) error {
	id, err := s.Store.IdentityOf(ctx, userID, ProviderApple)
	if errors.Is(err, ErrNotFound) || (err == nil && id.AppleRefreshToken == "") {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.EnqueueJob(ctx, Job{
		Database: eraseDatabase, Function: revokeFunc, CallerActor: "system", Origin: RevokeOrigin,
		TimeoutMS: revokeLease.Milliseconds(), Revoke: &RevokeJob{UserID: userID, ClientID: id.AppleClientID, Token: id.AppleRefreshToken},
	})
	if err != nil {
		return fmt.Errorf("queue the revocation of the Apple token: %w", err)
	}
	return nil
}
