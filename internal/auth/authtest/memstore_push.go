package authtest

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

type memSend struct {
	auth.PushSend
	batchesDone map[string]bool
	unitsDone   map[string]bool
}

func (m *MemStore) DevicesByIDs(_ context.Context, ids []string) ([]auth.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Device
	for _, id := range ids {
		if d, ok := m.devices[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *MemStore) LiveDevicesOf(_ context.Context, userIDs []string, now time.Time) (map[string][]auth.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]auth.Device{}
	for _, d := range m.devices {
		if !slices.Contains(userIDs, d.UserID) {
			continue
		}
		if s, ok := m.sessions[d.SessionID]; !ok || !now.Before(s.ExpiresAt) {
			continue
		}
		out[d.UserID] = append(out[d.UserID], d)
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool { return list[i].LastSeen.After(list[j].LastSeen) })
	}
	return out, nil
}

func (m *MemStore) DeleteDeviceByID(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.devices, id)
	return nil
}

func (m *MemStore) UsersByIDs(_ context.Context, ids []string) ([]auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.User
	for _, id := range ids {
		if u, ok := m.users[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (m *MemStore) AudienceUserIDs(_ context.Context, a auth.PushAudience, after string, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, u := range m.users {
		switch {
		case id <= after || u.Disabled || !u.ErasedAt.IsZero():
			continue
		case len(a.Roles) > 0 && !slices.ContainsFunc(a.Roles, func(r string) bool { return slices.Contains(u.Roles, r) }):
			continue
		case a.EmailVerified != nil && u.EmailVerified != *a.EmailVerified:
			continue
		case a.Locale != "" && u.Locale != a.Locale:
			continue
		case !a.CreatedBefore.IsZero() && !u.CreatedAt.Before(a.CreatedBefore):
			continue
		case !a.CreatedAfter.IsZero() && !u.CreatedAt.After(a.CreatedAfter):
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (m *MemStore) CreatePushSend(_ context.Context, p auth.PushSend) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sends == nil {
		m.sends = map[string]*memSend{}
	}
	m.sends[p.ID] = &memSend{PushSend: p, batchesDone: map[string]bool{}, unitsDone: map[string]bool{}}
	return nil
}

func (m *MemStore) PushSendByID(_ context.Context, id string) (auth.PushSend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sends[id]
	if !ok {
		return auth.PushSend{}, auth.ErrPushSendNotFound
	}
	return s.PushSend, nil
}

// settled sets CompletedAt when everything of the send is done.
func (s *memSend) settle(now time.Time) {
	if s.CompletedAt.IsZero() && s.Planned && s.BatchesDone >= s.Batches && s.UnitsDone >= s.Units {
		s.CompletedAt = now
	}
}

func (m *MemStore) AddPushSend(_ context.Context, id string, d auth.PushDelta, now time.Time) (auth.PushSend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sends[id]
	if !ok {
		return auth.PushSend{}, auth.ErrPushSendNotFound
	}
	if d.DoneBatch != "" {
		if !s.batchesDone[d.DoneBatch] {
			s.batchesDone[d.DoneBatch] = true
			s.BatchesDone++
			s.Units += d.Units
			s.Queued += d.Queued
			s.Limited += d.Limited
			s.NoDevices += d.NoDevices
		}
	} else {
		s.Units += d.Units
		s.Queued += d.Queued
		s.Limited += d.Limited
		s.NoDevices += d.NoDevices
	}
	s.Sent += d.Sent
	s.Invalid += d.Invalid
	s.Failed += d.Failed
	if d.DoneUnit != "" && !s.unitsDone[d.DoneUnit] {
		s.unitsDone[d.DoneUnit] = true
		s.UnitsDone++
	}
	s.settle(now)
	return s.PushSend, nil
}

func (m *MemStore) SetPushPlanned(_ context.Context, id string, batches int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sends[id]
	if !ok {
		return auth.ErrPushSendNotFound
	}
	s.Planned, s.Batches = true, batches
	s.settle(now)
	return nil
}

func (m *MemStore) SetPushPlanPosition(_ context.Context, jobID, after string, nextBatch int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Push == nil {
		return auth.ErrNotFound
	}
	p := *j.Push
	p.After, p.NextBatch = after, nextBatch
	j.Push = &p
	m.jobs[jobID] = j
	return nil
}

func (m *MemStore) UpdatePushRecipients(_ context.Context, jobID string, recipients []auth.PushRecipient) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Push == nil {
		return auth.ErrNotFound
	}
	p := *j.Push
	p.Recipients = recipients
	j.Push = &p
	m.jobs[jobID] = j
	return nil
}

func (m *MemStore) ListPushSends(_ context.Context, f auth.PushSendFilter) ([]auth.PushSend, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []auth.PushSend
	for _, s := range m.sends {
		switch {
		case f.Kind != "" && s.Kind != f.Kind, f.Sender != "" && s.Sender != f.Sender,
			!f.Since.IsZero() && s.CreatedAt.Before(f.Since), !f.Until.IsZero() && !s.CreatedAt.Before(f.Until):
			continue
		}
		all = append(all, s.PushSend)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if f.Skip >= len(all) {
		return nil, false, nil
	}
	all = all[f.Skip:]
	if f.Limit > 0 && len(all) > f.Limit {
		return all[:f.Limit], true, nil
	}
	return all, false, nil
}

func (m *MemStore) PutPushOutbox(_ context.Context, e auth.PushOutboxEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outbox = append(m.outbox, e)
	return nil
}

func (m *MemStore) ListPushOutbox(_ context.Context, limit int) ([]auth.PushOutboxEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auth.PushOutboxEntry, 0, len(m.outbox))
	for i := len(m.outbox) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.outbox[i])
	}
	return out, nil
}
