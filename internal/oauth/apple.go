package oauth

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
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

// AppleClientSecret returns the client secret to send to Apple for the provider,
// signed with the private key (the .p8 file's PEM). It is cached and renewed before
// it expires.
func (s *Service) AppleClientSecret(p *registry.Provider, privateKeyPEM string) (string, error) {
	now := s.now()
	cacheKey := p.TeamID + "/" + p.KeyID + "/" + p.ClientID
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.apple[cacheKey]; ok && c.keyHash == privateKeyPEM && c.expires.Sub(now) > appleSecretRenew {
		return c.secret, nil
	}
	secret, err := signAppleSecret(p, privateKeyPEM, now)
	if err != nil {
		return "", err
	}
	if s.apple == nil {
		s.apple = map[string]appleSecret{}
	}
	s.apple[cacheKey] = appleSecret{secret: secret, expires: now.Add(appleSecretLife), keyHash: privateKeyPEM}
	return secret, nil
}

func signAppleSecret(p *registry.Provider, privateKeyPEM string, now time.Time) (string, error) {
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
		"aud": appleAudience, "sub": p.ClientID,
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
