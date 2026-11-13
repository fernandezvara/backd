// Package passkeytest has what tests of passkeys need: an in-memory store and ceremony keeper, and
// a software authenticator that plays the browser's and the authenticator's side of the ceremonies.
package passkeytest

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/auth/passkey"
)

// Store is an in-memory passkey.Store.
type Store struct {
	mu      sync.Mutex
	creds   []passkey.Credential
	handles map[string][]byte
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{handles: map[string][]byte{}} }

func (s *Store) Add(_ context.Context, c passkey.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.creds {
		if string(e.CredentialID) == string(c.CredentialID) {
			return passkey.ErrInvalid
		}
	}
	s.creds = append(s.creds, c)
	return nil
}

func (s *Store) ByCredentialID(_ context.Context, id []byte) (passkey.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.creds {
		if string(c.CredentialID) == string(id) {
			return c, nil
		}
	}
	return passkey.Credential{}, passkey.ErrUnknownCredential
}

func (s *Store) ListByUser(_ context.Context, userID string) ([]passkey.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []passkey.Credential
	for _, c := range s.creds {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) Update(_ context.Context, id []byte, u passkey.Update) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.creds {
		if string(c.CredentialID) != string(id) {
			continue
		}
		if u.SignCount != nil {
			c.SignCount = *u.SignCount
		}
		if u.LastUsedAt != nil {
			c.LastUsedAt = *u.LastUsedAt
		}
		if u.BackupState != nil {
			c.BackupState = *u.BackupState
		}
		if u.CloneSuspected != nil {
			c.CloneSuspected = *u.CloneSuspected
		}
		if u.Name != nil {
			c.Name = *u.Name
		}
		s.creds[i] = c
		return nil
	}
	return passkey.ErrUnknownCredential
}

func (s *Store) Delete(_ context.Context, userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.creds {
		if c.UserID == userID && c.ID == id {
			s.creds = append(s.creds[:i], s.creds[i+1:]...)
			return nil
		}
	}
	return passkey.ErrUnknownCredential
}

func (s *Store) CountByUser(ctx context.Context, userID string) (int, error) {
	l, _ := s.ListByUser(ctx, userID)
	return len(l), nil
}

func (s *Store) UserHandle(_ context.Context, userID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handles[userID]; ok {
		return h, nil
	}
	h := make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		return nil, err
	}
	s.handles[userID] = h
	return h, nil
}

func (s *Store) UserByHandle(_ context.Context, handle []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, h := range s.handles {
		if string(h) == string(handle) {
			return id, nil
		}
	}
	return "", passkey.ErrUnknownCredential
}

// Ceremonies is an in-memory passkey.Ceremonies.
type Ceremonies struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]ceremony
}

type ceremony struct {
	kind, userID string
	data         []byte
	expires      time.Time
}

// NewCeremonies returns an empty keeper that reads the time from now.
func NewCeremonies(now func() time.Time) *Ceremonies {
	return &Ceremonies{now: now, items: map[string]ceremony{}}
}

func (c *Ceremonies) Put(_ context.Context, kind string, data []byte, userID string, ttl time.Duration) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := string(append([]byte("c"), hexOf(b)...))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[id] = ceremony{kind: kind, userID: userID, data: data, expires: c.now().Add(ttl)}
	return id, nil
}

func (c *Ceremonies) Claim(_ context.Context, id, kind string) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[id]
	delete(c.items, id)
	if !ok || it.kind != kind || !c.now().Before(it.expires) {
		return nil, "", passkey.ErrCeremony
	}
	return it.data, it.userID, nil
}

func hexOf(b []byte) []byte {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&15])
	}
	return out
}
