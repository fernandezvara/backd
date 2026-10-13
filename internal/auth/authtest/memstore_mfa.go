package authtest

import (
	"context"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// dropSecondFactors forgets a user's second factors and pending steps. m.mu is held.
func (m *MemStore) dropSecondFactors(userID string) {
	delete(m.mfa, userID)
	for k, c := range m.challenges {
		if c.UserID == userID {
			delete(m.challenges, k)
		}
	}
}

func (m *MemStore) PutChallenge(_ context.Context, c auth.AuthChallenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.challenges == nil {
		m.challenges = map[string]auth.AuthChallenge{}
	}
	m.challenges[c.ID] = c
	return nil
}

func (m *MemStore) GetChallenge(_ context.Context, id string, now time.Time) (auth.AuthChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[id]
	if !ok || !c.ExpiresAt.After(now) {
		return auth.AuthChallenge{}, auth.ErrNotFound
	}
	return c, nil
}

func (m *MemStore) ClaimChallenge(_ context.Context, id string, now time.Time) (auth.AuthChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[id]
	delete(m.challenges, id)
	if !ok || !c.ExpiresAt.After(now) {
		return auth.AuthChallenge{}, auth.ErrNotFound
	}
	return c, nil
}

func (m *MemStore) CountChallengeAttempt(_ context.Context, id string, now time.Time) (auth.AuthChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[id]
	if !ok || !c.ExpiresAt.After(now) {
		return auth.AuthChallenge{}, auth.ErrNotFound
	}
	c.Attempts++
	m.challenges[id] = c
	return c, nil
}

func (m *MemStore) DeleteChallenge(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.challenges, id)
	return nil
}

func (m *MemStore) DeleteChallengesOfUser(_ context.Context, userID string, kinds ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.challenges {
		if c.UserID == userID && (len(kinds) == 0 || slices.Contains(kinds, c.Kind)) {
			delete(m.challenges, k)
		}
	}
	return nil
}

func (m *MemStore) MFAOf(_ context.Context, userID string) (auth.MFARecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.mfa[userID]
	if !ok {
		return auth.MFARecord{}, auth.ErrNotFound
	}
	rec.RecoveryCodes = slices.Clone(rec.RecoveryCodes)
	return rec, nil
}

func (m *MemStore) PutMFA(_ context.Context, rec auth.MFARecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mfa == nil {
		m.mfa = map[string]auth.MFARecord{}
	}
	rec.RecoveryCodes = slices.Clone(rec.RecoveryCodes)
	m.mfa[rec.UserID] = rec
	return nil
}

func (m *MemStore) DeleteMFA(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.mfa, userID)
	return nil
}

func (m *MemStore) MFAUserIDs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.mfa))
	for id := range m.mfa {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

func (m *MemStore) AdvanceMFAStep(_ context.Context, userID string, step int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.mfa[userID]
	if !ok || step <= rec.LastStep {
		return false, nil
	}
	rec.LastStep = step
	m.mfa[userID] = rec
	return true, nil
}

func (m *MemStore) UseRecoveryCode(_ context.Context, userID, hash string) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.mfa[userID]
	if !ok {
		return 0, false, nil
	}
	i := slices.Index(rec.RecoveryCodes, hash)
	if i < 0 {
		return len(rec.RecoveryCodes), false, nil
	}
	rec.RecoveryCodes = slices.Delete(slices.Clone(rec.RecoveryCodes), i, i+1)
	m.mfa[userID] = rec
	return len(rec.RecoveryCodes), true, nil
}
