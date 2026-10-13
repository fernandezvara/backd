package authtest

import (
	"context"
	"sort"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// dropDevicesOf removes a user's devices; the caller holds the lock.
func (m *MemStore) dropDevicesOf(userID string) {
	for k, d := range m.devices {
		if d.UserID == userID {
			delete(m.devices, k)
		}
	}
}

// dropDevicesOfSession removes the devices a session registered; the caller holds the lock.
func (m *MemStore) dropDevicesOfSession(sessionID string) {
	for k, d := range m.devices {
		if d.SessionID == sessionID {
			delete(m.devices, k)
		}
	}
}

func (m *MemStore) PutDevice(_ context.Context, d auth.Device) (auth.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.devices == nil {
		m.devices = map[string]auth.Device{}
	}
	for k, old := range m.devices {
		if old.TokenHash != d.TokenHash {
			continue
		}
		if old.UserID == d.UserID {
			// The same owner: refreshed in place, keeping its id and creation time.
			d.ID, d.CreatedAt = old.ID, old.CreatedAt
			if d.Name == "" {
				d.Name = old.Name
			}
		}
		delete(m.devices, k)
	}
	m.devices[d.ID] = d
	return d, nil
}

func (m *MemStore) ListDevices(_ context.Context, userID string) ([]auth.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Device
	for _, d := range m.devices {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

func (m *MemStore) DeleteDevice(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok || d.UserID != userID {
		return auth.ErrNotFound
	}
	delete(m.devices, id)
	return nil
}

func (m *MemStore) DeleteOldestDevices(_ context.Context, userID string, keep int, keepID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var mine []auth.Device
	for _, d := range m.devices {
		if d.UserID == userID {
			mine = append(mine, d)
		}
	}
	if len(mine) <= keep {
		return 0, nil
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].LastSeen.After(mine[j].LastSeen) })
	room := keep - 1
	var n int64
	for _, d := range mine {
		switch {
		case d.ID == keepID:
		case room > 0:
			room--
		default:
			delete(m.devices, d.ID)
			n++
		}
	}
	return n, nil
}

func (m *MemStore) DeleteStaleDevices(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, d := range m.devices {
		if d.LastSeen.Before(before) {
			delete(m.devices, k)
			n++
		}
	}
	return n, nil
}

func (m *MemStore) DeleteOrphanedDevices(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, d := range m.devices {
		s, ok := m.sessions[d.SessionID]
		if !ok || !now.Before(s.ExpiresAt) {
			delete(m.devices, k)
			n++
		}
	}
	return n, nil
}
