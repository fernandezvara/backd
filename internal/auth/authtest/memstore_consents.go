package authtest

import (
	"context"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func (m *MemStore) AppendConsentRecord(_ context.Context, r auth.ConsentRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consents = append(m.consents, r)
	return nil
}

func (m *MemStore) ListConsentRecords(_ context.Context, userID string) ([]auth.ConsentRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.ConsentRecord
	for _, r := range m.consents {
		if r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MemStore) SetUserConsent(_ context.Context, userID, consent, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return auth.ErrNotFound
	}
	next := map[string]string{}
	for k, v := range u.Consents {
		next[k] = v
	}
	if version == "" {
		delete(next, consent)
	} else {
		next[consent] = version
	}
	if len(next) == 0 {
		next = nil
	}
	u.Consents = next
	m.users[userID] = u
	return nil
}

func (m *MemStore) ConsentVersionsInUse(_ context.Context) ([]auth.ConsentRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[auth.ConsentRef]bool{}
	var out []auth.ConsentRef
	for _, r := range m.consents {
		ref := auth.ConsentRef{Consent: r.Consent, Version: r.Version}
		if r.Version != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out, nil
}

func (m *MemStore) ReduceConsentRecords(_ context.Context, userID, pseudonym string, deleteAt time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for i, r := range m.consents {
		if r.UserID != userID {
			continue
		}
		r.UserID, r.IP, r.UserAgent, r.DeleteAt = pseudonym, "", "", deleteAt
		m.consents[i] = r
		n++
	}
	return n, nil
}

func (m *MemStore) DeleteConsentRecordsOf(_ context.Context, userID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	kept := m.consents[:0]
	for _, r := range m.consents {
		if r.UserID == userID {
			n++
			continue
		}
		kept = append(kept, r)
	}
	m.consents = kept
	return n, nil
}

// PurgeConsentRecords removes the records whose retention ended, as the database's TTL does.
func (m *MemStore) PurgeConsentRecords(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.consents[:0]
	n := 0
	for _, r := range m.consents {
		if !r.DeleteAt.IsZero() && !now.Before(r.DeleteAt) {
			n++
			continue
		}
		kept = append(kept, r)
	}
	m.consents = kept
	return n
}
