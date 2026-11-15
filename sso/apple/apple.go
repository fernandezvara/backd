// Package apple holds what Sign in with Apple needs beyond OpenID Connect: the client secret
// (a JWT signed with the developer's key) and the revocation of a user's tokens.
package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/fernandezvara/backd/sso"
	"github.com/fernandezvara/backd/sso/oauth2"
)

// Apple has no client secret to keep: it wants a short JWT signed with the key made
// in the developer console (ES256), as the client secret of every request.
const (
	appleSecretLife  = time.Hour
	appleSecretRenew = 15 * time.Minute // a new one is signed when this much of its life is left
	appleAudience    = "https://appleid.apple.com"
)

// Signer makes and caches Apple's client secrets.
type Signer struct {
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]secret // by key id, team and client id
}

func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type secret struct {
	secret  string
	expires time.Time
	keyHash string
}

// ClientSecret returns the client secret to send to Apple for the provider and the
// client id it acts as (the Services ID, or an app's bundle id), signed with the private key (the .p8 file's PEM). It is cached and renewed before
// it expires.
func (s *Signer) ClientSecret(p sso.Provider, clientID, privateKeyPEM string) (string, error) {
	now := s.now()
	cacheKey := p.TeamID + "/" + p.KeyID + "/" + clientID
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[cacheKey]; ok && c.keyHash == privateKeyPEM && c.expires.Sub(now) > appleSecretRenew {
		return c.secret, nil
	}
	sec, err := signAppleSecret(p, clientID, privateKeyPEM, now)
	if err != nil {
		return "", err
	}
	if s.cache == nil {
		s.cache = map[string]secret{}
	}
	s.cache[cacheKey] = secret{secret: sec, expires: now.Add(appleSecretLife), keyHash: privateKeyPEM}
	return sec, nil
}

func signAppleSecret(p sso.Provider, clientID, privateKeyPEM string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("sso: the Apple private key is not PEM (paste the whole .p8 file)")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("sso: the Apple private key can't be read: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("sso: the Apple private key is not an EC key")
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", p.KeyID))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"iss": p.TeamID, "iat": now.Unix(), "exp": now.Add(appleSecretLife).Unix(),
		"aud": appleAudience, "sub": clientID,
	})
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

// Revoke tells Apple to revoke a refresh token, which ends the user's link between the app
// and their Apple ID. clientID is the one the token was issued to. A token Apple no longer
// knows counts as revoked.
func Revoke(ctx context.Context, c *oauth2.Client, p sso.Provider, clientID, clientSecret, refreshToken string) error {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")
	var out struct {
		Error string `json:"error"`
	}
	status, err := c.PostForm(ctx, p.Endpoints.Revoke, form, &out)
	switch {
	case err != nil:
		return err
	case status == http.StatusOK:
		return nil
	case status == http.StatusBadRequest && out.Error == "invalid_grant":
		return nil // already revoked, or never valid
	}
	return fmt.Errorf("%w: revoke answered %d (%s)", sso.ErrUnavailable, status, oauth2.Sanitize(out.Error))
}
