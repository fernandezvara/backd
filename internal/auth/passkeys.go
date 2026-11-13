package auth

import (
	"context"
	"errors"
	"time"

	"github.com/fernandezvara/backd/internal/auth/passkey"
)

// The passkey package is a leaf (design/passkeys.md §14): this file is the glue that gives it what
// it needs from the realm. Nothing is exposed over HTTP yet; the routes come with the next steps.

// ErrPasskeysDisabled: the realm has not enabled passkeys (auth.passkeys.enabled).
var ErrPasskeysDisabled = errors.New("passkeys are not enabled in this realm")

// PasskeyService returns the passkey ceremonies for the realm, built from auth.passkeys, the store and
// the shared challenge table.
func (s *Users) PasskeyService() (*passkey.Service, error) {
	p := s.Settings.Passkeys
	if !p.Enabled {
		return nil, ErrPasskeysDisabled
	}
	return passkey.New(passkey.Config{RPID: p.RPID, RPName: p.RPName, Origins: p.Origins, AllowLoopbackHTTP: true}, s.Store, passkeyCeremonies{s}, s.now)
}

// passkeyCeremonies keeps the state of a ceremony in the challenge table: hash-only, five minutes,
// single use, removed by the table's TTL index.
type passkeyCeremonies struct{ s *Users }

func (c passkeyCeremonies) Put(ctx context.Context, kind string, data []byte, userID string, ttl time.Duration) (string, error) {
	token, err := randomValue()
	if err != nil {
		return "", err
	}
	now := c.s.now()
	err = c.s.Store.PutChallenge(ctx, AuthChallenge{ID: HashToken(token), Kind: kind, UserID: userID, Secret: string(data), CreatedAt: now, ExpiresAt: now.Add(ttl)})
	return token, err
}

func (c passkeyCeremonies) Claim(ctx context.Context, id, kind string) ([]byte, string, error) {
	if id == "" {
		return nil, "", passkey.ErrCeremony
	}
	ch, err := c.s.Store.ClaimChallenge(ctx, HashToken(id), c.s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && ch.Kind != kind) {
		return nil, "", passkey.ErrCeremony
	}
	if err != nil {
		return nil, "", err
	}
	return []byte(ch.Secret), ch.UserID, nil
}
