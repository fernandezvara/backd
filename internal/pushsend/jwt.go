package pushsend

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

var b64 = base64.RawURLEncoding

// signJWT makes a compact JWT with the key: ES256 for an *ecdsa.PrivateKey on P-256 (the
// signature is r||s, 64 bytes), RS256 for an *rsa.PrivateKey.
func signJWT(key crypto.Signer, header, claims map[string]any) (string, error) {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
		if err != nil {
			return "", err
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return signing + "." + b64.EncodeToString(sig), nil
	case *rsa.PrivateKey:
		sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
		if err != nil {
			return "", err
		}
		return signing + "." + b64.EncodeToString(sig), nil
	}
	return "", fmt.Errorf("unsupported signing key %T", key)
}

// privateKeyFromPEM reads a PKCS#8 (or PKCS#1 / SEC1) private key.
func privateKeyFromPEM(data string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(data))
	if block == nil {
		return nil, errors.New("the key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("the key is not a PKCS#8, PKCS#1 or SEC1 private key")
}

// vapidKeyFromBase64 reads the VAPID private key backd push web-keys stores: the 32 bytes of
// the P-256 scalar in base64url.
func vapidKeyFromBase64(v string) (*ecdsa.PrivateKey, error) {
	raw, err := b64.DecodeString(v)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("the VAPID private key must be 32 bytes in base64url")
	}
	return ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
}
