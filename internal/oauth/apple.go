package oauth

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
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/fernandezvara/backd/internal/registry"
)

// Apple has no client secret to keep: it wants a short JWT signed with the key made
// in the developer console (ES256), as the client secret of every request.
const (
	appleSecretLife  = time.Hour
	appleSecretRenew = 15 * time.Minute // a new one is signed when this much of its life is left
	appleAudience    = "https://appleid.apple.com"
)

type appleSecret struct {
	secret  string
	expires time.Time
	keyHash string
}

// AppleClientSecret returns the client secret to send to Apple for the provider and the
// client id it acts as (the Services ID, or an app's bundle id), signed with the private key (the .p8 file's PEM). It is cached and renewed before
// it expires.
func (s *Service) AppleClientSecret(p *registry.Provider, clientID, privateKeyPEM string) (string, error) {
	now := s.now()
	cacheKey := p.TeamID + "/" + p.KeyID + "/" + clientID
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.apple[cacheKey]; ok && c.keyHash == privateKeyPEM && c.expires.Sub(now) > appleSecretRenew {
		return c.secret, nil
	}
	secret, err := signAppleSecret(p, clientID, privateKeyPEM, now)
	if err != nil {
		return "", err
	}
	if s.apple == nil {
		s.apple = map[string]appleSecret{}
	}
	s.apple[cacheKey] = appleSecret{secret: secret, expires: now.Add(appleSecretLife), keyHash: privateKeyPEM}
	return secret, nil
}

func signAppleSecret(p *registry.Provider, clientID, privateKeyPEM string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("oauth: the Apple private key is not PEM (paste the whole .p8 file)")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("oauth: the Apple private key can't be read: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("oauth: the Apple private key is not an EC key")
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
func (s *Service) Revoke(ctx context.Context, p *registry.Provider, clientID, clientSecret, refreshToken string) error {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")
	var out struct {
		Error string `json:"error"`
	}
	status, err := s.postForm(ctx, s.endpoints(p).Revoke, form, &out)
	switch {
	case err != nil:
		return err
	case status == http.StatusOK:
		return nil
	case status == http.StatusBadRequest && out.Error == "invalid_grant":
		return nil // already revoked, or never valid
	}
	return fmt.Errorf("%w: revoke answered %d (%s)", ErrUnavailable, status, sanitize(out.Error))
}
