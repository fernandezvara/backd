// Package authtest provides an in-memory auth.Store for tests.
package authtest

import (
	"cmp"
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// MemStore is an in-memory auth.Store. The zero value is not usable; use
// NewMemStore.
type MemStore struct {
	mu          sync.Mutex
	users       map[string]auth.User
	identities  map[string]auth.Identity // provider/subject → identity
	sessions    map[string]auth.Session  // id → session
	keys        map[string]auth.APIKey   // hash → key
	attempts    map[string]memAttempts
	loginLocks  map[string]memLock
	invites     map[string]auth.Invitation // id → invitation
	audit       []auth.AuditRecord
	journal     map[string]auth.FileJournalEntry
	deletions   map[string]auth.FileDeletion
	checks      map[string]auth.CheckReport // "database/collection" → latest report
	secrets     map[string]auth.Secret      // "database\x00name" → secret
	schedules   map[string]auth.ScheduleState
	invocations []auth.InvocationRecord
	emailTokens map[string]auth.EmailToken
	jobs        map[string]memJob                 // id → job
	idempotency map[string]auth.IdempotencyRecord // id → record
	// FailPutIdentity, when set, is returned by PutIdentity.
	FailPutIdentity error
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{users: map[string]auth.User{}, identities: map[string]auth.Identity{}, sessions: map[string]auth.Session{}, keys: map[string]auth.APIKey{}, attempts: map[string]memAttempts{}, invites: map[string]auth.Invitation{}, secrets: map[string]auth.Secret{}, schedules: map[string]auth.ScheduleState{}, checks: map[string]auth.CheckReport{}, journal: map[string]auth.FileJournalEntry{}, deletions: map[string]auth.FileDeletion{}, jobs: map[string]memJob{}, idempotency: map[string]auth.IdempotencyRecord{}}
}

func secretKey(database, name string) string { return database + "\x00" + name }

func (m *MemStore) UpsertSecret(_ context.Context, s auth.Secret) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.secrets[secretKey(s.Database, s.Name)]; ok {
		s.CreatedAt = existing.CreatedAt
	}
	m.secrets[secretKey(s.Database, s.Name)] = s
	return nil
}

func (m *MemStore) SecretByScope(_ context.Context, database, name string) (auth.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.secrets[secretKey(database, name)]
	if !ok {
		return auth.Secret{}, auth.ErrSecretNotFound
	}
	return s, nil
}

func (m *MemStore) ListSecrets(context.Context) ([]auth.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auth.Secret, 0, len(m.secrets))
	for _, s := range m.secrets {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b auth.Secret) int {
		if c := strings.Compare(a.Database, b.Database); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

func (m *MemStore) DeleteSecret(_ context.Context, database, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := secretKey(database, name)
	if _, ok := m.secrets[k]; !ok {
		return auth.ErrSecretNotFound
	}
	delete(m.secrets, k)
	return nil
}

// SessionCount returns how many sessions the user has, expired or not.
func (m *MemStore) SessionCount(userID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.UserID == userID {
			n++
		}
	}
	return n
}

// Session returns a stored session by id.
func (m *MemStore) Session(id string) (auth.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *MemStore) CreateUser(_ context.Context, u auth.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.users {
		if x.Email == u.Email {
			return auth.ErrEmailTaken
		}
	}
	m.users[u.ID] = u
	return nil
}

func (m *MemStore) UserByEmail(_ context.Context, email string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Email == email {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

func (m *MemStore) UserByID(_ context.Context, id string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}

func (m *MemStore) ListUsers(context.Context) ([]auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.User
	for _, u := range m.users {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b auth.User) int { return strings.Compare(a.Email, b.Email) })
	return out, nil
}

func (m *MemStore) ListUsersPage(ctx context.Context, contains, after string, skip, limit int) ([]auth.User, bool, error) {
	all, _ := m.ListUsers(ctx)
	var rest []auth.User
	for _, u := range all {
		if contains != "" && !strings.Contains(u.Email, contains) {
			continue
		}
		if after == "" || u.Email > after {
			rest = append(rest, u)
		}
	}
	rest = rest[min(skip, len(rest)):]
	hasMore := len(rest) > limit
	return rest[:min(limit, len(rest))], hasMore, nil
}

func (m *MemStore) UpdateUser(_ context.Context, id string, upd auth.UserUpdate, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.ErrNotFound
	}
	if upd.Email != nil {
		for _, x := range m.users {
			if x.ID != id && x.Email == *upd.Email {
				return auth.ErrEmailTaken
			}
		}
		u.Email = *upd.Email
	}
	if upd.PendingEmail != nil {
		u.PendingEmail = *upd.PendingEmail
	}
	if upd.PreviousEmail != nil {
		u.PreviousEmail = *upd.PreviousEmail
	}
	if upd.EmailVerified != nil {
		u.EmailVerified = *upd.EmailVerified
	}
	if upd.Disabled != nil {
		u.Disabled = *upd.Disabled
	}
	if upd.Locale != nil {
		u.Locale = *upd.Locale
	}
	if upd.AdminNetworks != nil {
		u.AdminNetworks = *upd.AdminNetworks
	}
	if upd.LoginNetworks != nil {
		u.LoginNetworks = *upd.LoginNetworks
	}
	u.UpdatedAt = now
	m.users[id] = u
	return nil
}

func (m *MemStore) ListUnverifiedUsers(_ context.Context, before time.Time, limit int) ([]auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.User
	for _, u := range m.users {
		if !u.EmailVerified && u.CreatedAt.Before(before) {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) DeleteUser(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[id]; !ok {
		return auth.ErrNotFound
	}
	delete(m.users, id)
	for k, s := range m.sessions {
		if s.UserID == id {
			delete(m.sessions, k)
		}
	}
	for k, i := range m.identities {
		if i.UserID == id {
			delete(m.identities, k)
		}
	}
	return nil
}

func (m *MemStore) PutIdentity(_ context.Context, id auth.Identity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPutIdentity != nil {
		return m.FailPutIdentity
	}
	k := id.Provider + "/" + id.Subject
	if cur, ok := m.identities[k]; ok {
		cur.PasswordHash, cur.UpdatedAt = id.PasswordHash, id.UpdatedAt
		m.identities[k] = cur
		return nil
	}
	m.identities[k] = id
	return nil
}

func (m *MemStore) Identity(_ context.Context, provider, subject string) (auth.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, ok := m.identities[provider+"/"+subject]
	if !ok {
		return auth.Identity{}, auth.ErrNotFound
	}
	return i, nil
}

func (m *MemStore) DeleteSessions(_ context.Context, userID string) (int64, error) {
	return m.DeleteOtherSessions(context.Background(), userID, "")
}

func (m *MemStore) CreateSession(_ context.Context, s auth.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	return nil
}

func (m *MemStore) SessionByTokenHash(_ context.Context, hash string) (auth.Session, auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.TokenHash == hash {
			u, ok := m.users[s.UserID]
			if !ok {
				return auth.Session{}, auth.User{}, auth.ErrNotFound
			}
			return s, u, nil
		}
	}
	return auth.Session{}, auth.User{}, auth.ErrNotFound
}

func (m *MemStore) TouchSession(_ context.Context, id string, lastUsed, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return auth.ErrNotFound
	}
	s.LastUsedAt, s.ExpiresAt = lastUsed, expires
	m.sessions[id] = s
	return nil
}

func (m *MemStore) ListSessions(_ context.Context, userID string) ([]auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Session
	for _, s := range m.sessions {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b auth.Session) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (m *MemStore) DeleteSession(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.UserID != userID {
		return auth.ErrNotFound
	}
	delete(m.sessions, id)
	return nil
}

func (m *MemStore) DeleteOtherSessions(_ context.Context, userID, keepID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, s := range m.sessions {
		if s.UserID == userID && k != keepID {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}

func (m *MemStore) CreateAPIKey(_ context.Context, k auth.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.keys {
		if x.Name == k.Name {
			return auth.ErrKeyNameTaken
		}
	}
	m.keys[k.Hash] = k
	return nil
}

func (m *MemStore) APIKeyByHash(_ context.Context, hash string) (auth.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[hash]
	if !ok {
		return auth.APIKey{}, auth.ErrKeyNotFound
	}
	return k, nil
}

func (m *MemStore) ListAPIKeys(context.Context) ([]auth.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.APIKey
	for _, k := range m.keys {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b auth.APIKey) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (m *MemStore) DeleteAPIKey(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, k := range m.keys {
		if k.Name == name {
			delete(m.keys, h)
			return nil
		}
	}
	return auth.ErrKeyNotFound
}

func (m *MemStore) TouchAPIKey(_ context.Context, hash string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[hash]
	if !ok {
		return auth.ErrKeyNotFound
	}
	k.LastUsedAt = &at
	m.keys[hash] = k
	return nil
}

type memLock struct {
	owner string
	until time.Time
}

type memAttempts struct {
	auth.Attempts
	expires time.Time
}

// LoginAttempts returns the counter even past its expiry, like MongoDB
// before its TTL monitor deletes it; a failure after expiry restarts it.
func (m *MemStore) LoginAttempts(_ context.Context, key string) (auth.Attempts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts[key].Attempts, nil
}

func (m *MemStore) RecordLoginFailure(_ context.Context, key string, at, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.attempts[key]
	if !at.Before(a.expires) {
		a = memAttempts{}
	}
	a.Failures++
	a.LastFailureAt, a.expires = at, expires
	m.attempts[key] = a
	return nil
}

// AcquireLoginLock and ReleaseLoginLock keep the locks apart from the counters.
func (m *MemStore) AcquireLoginLock(_ context.Context, key, owner string, now, until time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, held := m.loginLocks[key]; held && now.Before(l.until) {
		return false, nil
	}
	if m.loginLocks == nil {
		m.loginLocks = map[string]memLock{}
	}
	m.loginLocks[key] = memLock{owner: owner, until: until}
	return true, nil
}

func (m *MemStore) ReleaseLoginLock(_ context.Context, key, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loginLocks[key].owner == owner {
		delete(m.loginLocks, key)
	}
	return nil
}

func (m *MemStore) ClearLoginAttempts(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attempts, key)
	return nil
}

// IncrementCounter is a fixed window, unlike RecordLoginFailure's
// sliding cleanup deadline: expires (and so the reset) only moves once
// the current window has actually ended, or a steady stream of calls
// would push the deadline forward forever and never reset the count.
func (m *MemStore) IncrementCounter(_ context.Context, key string, at, expires time.Time) (int, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.attempts[key]
	if !at.Before(a.expires) {
		a = memAttempts{expires: expires}
	}
	a.Failures++
	a.LastFailureAt = at
	m.attempts[key] = a
	return a.Failures, a.expires, nil
}

func (m *MemStore) AddRoles(_ context.Context, userID string, roles []string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return auth.ErrNotFound
	}
	for _, r := range roles {
		if !slices.Contains(u.Roles, r) {
			u.Roles = append(slices.Clone(u.Roles), r)
		}
	}
	u.UpdatedAt = now
	m.users[userID] = u
	return nil
}

func (m *MemStore) RemoveRole(_ context.Context, userID, role string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return auth.ErrNotFound
	}
	u.Roles = slices.DeleteFunc(slices.Clone(u.Roles), func(r string) bool { return r == role })
	u.UpdatedAt = now
	m.users[userID] = u
	return nil
}

func (m *MemStore) CreateInvitation(_ context.Context, inv auth.Invitation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invites[inv.ID] = inv
	return nil
}

func (m *MemStore) ClaimInvitation(_ context.Context, hash string, now time.Time) (auth.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, inv := range m.invites {
		if inv.TokenHash == hash && now.Before(inv.ExpiresAt) {
			delete(m.invites, id)
			return inv, nil
		}
	}
	return auth.Invitation{}, auth.ErrNotFound
}

func (m *MemStore) ClaimInvitationByID(_ context.Context, id string, now time.Time) (auth.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv, ok := m.invites[id]; ok && now.Before(inv.ExpiresAt) {
		delete(m.invites, id)
		return inv, nil
	}
	return auth.Invitation{}, auth.ErrNotFound
}

func (m *MemStore) ListInvitations(context.Context) ([]auth.Invitation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Invitation
	for _, inv := range m.invites {
		out = append(out, inv)
	}
	slices.SortFunc(out, func(a, b auth.Invitation) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID))
	})
	return out, nil
}

func (m *MemStore) DeleteInvitation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.invites[id]; !ok {
		return auth.ErrNotFound
	}
	delete(m.invites, id)
	return nil
}

func (m *MemStore) AppendAudit(_ context.Context, rec auth.AuditRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, rec)
	return nil
}

// ListAudit filters like the MongoDB store, newest first. Expired records
// are kept, as before MongoDB's TTL monitor removes them.
func (m *MemStore) ListAudit(_ context.Context, f auth.AuditFilter) ([]auth.AuditRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.AuditRecord
	for _, r := range m.audit {
		if (f.Action == "" || r.Action == f.Action) && (f.Actor == "" || r.Actor == f.Actor) &&
			(f.Target == "" || r.Target == f.Target) && (f.Since.IsZero() || !r.At.Before(f.Since)) &&
			(f.Until.IsZero() || r.At.Before(f.Until)) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b auth.AuditRecord) int {
		return cmp.Or(b.At.Compare(a.At), strings.Compare(b.ID, a.ID))
	})
	out = out[min(f.Skip, len(out)):]
	if f.Limit > 0 && len(out) > f.Limit {
		return out[:f.Limit], true, nil
	}
	return out, false, nil
}

// AuditRecords returns every stored audit record, oldest first.
func (m *MemStore) AuditRecords() []auth.AuditRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.audit)
}

func (m *MemStore) RecordInvocation(_ context.Context, r auth.InvocationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invocations = append(m.invocations, r)
	return nil
}

// ListInvocations filters like the MongoDB store, newest first. Expired
// records are kept, as before MongoDB's TTL monitor removes them.
func (m *MemStore) ListInvocations(_ context.Context, f auth.InvocationFilter) ([]auth.InvocationRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.InvocationRecord
	for _, r := range m.invocations {
		if (f.Function == "" || r.Function == f.Function) && (f.RequestID == "" || r.RequestID == f.RequestID) &&
			(f.Since.IsZero() || !r.At.Before(f.Since)) && (f.Until.IsZero() || r.At.Before(f.Until)) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b auth.InvocationRecord) int {
		return cmp.Or(b.At.Compare(a.At), strings.Compare(b.ID, a.ID))
	})
	out = out[min(f.Skip, len(out)):]
	if f.Limit > 0 && len(out) > f.Limit {
		return out[:f.Limit], true, nil
	}
	return out, false, nil
}

// ListJobs filters like the MongoDB store, newest first.
func (m *MemStore) ListJobs(_ context.Context, f auth.JobFilter) ([]auth.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Job
	for _, j := range m.jobs {
		if (f.Database == "" || j.Database == f.Database) && (f.Function == "" || j.Function == f.Function) &&
			(f.Status == "" || j.Status == f.Status) && (f.Origin == "" || j.Origin == f.Origin) && (f.EraseUser == "" || (j.Erase != nil && j.Erase.UserID == f.EraseUser)) && (f.Scheduled == nil || j.Scheduled == *f.Scheduled) &&
			(f.Since.IsZero() || !j.CreatedAt.Before(f.Since)) && (f.Until.IsZero() || j.CreatedAt.Before(f.Until)) {
			out = append(out, j.Job)
		}
	}
	slices.SortStableFunc(out, func(a, b auth.Job) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID))
	})
	out = out[min(f.Skip, len(out)):]
	if f.Limit > 0 && len(out) > f.Limit {
		return out[:f.Limit], true, nil
	}
	return out, false, nil
}

// memJob wraps auth.Job with the lease deadline MongoDB tracks in
// lease_expires but auth.Job doesn't expose (callers never see it).
type memJob struct {
	auth.Job
	leaseExpires time.Time
}

func (m *MemStore) EnqueueJob(_ context.Context, j auth.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; ok {
		return auth.ErrJobExists
	}
	if j.Exclusive != "" {
		for _, other := range m.jobs {
			if other.Exclusive == j.Exclusive && other.Status != auth.JobDone {
				return auth.ErrJobExclusive
			}
		}
	}
	m.jobs[j.ID] = memJob{Job: j}
	return nil
}

// ClaimJob picks the oldest job that's never been claimed, or whose
// lease has expired, the same rule the MongoDB store's query applies.
func (m *MemStore) ClaimJob(_ context.Context, workerID string, at time.Time, margin time.Duration) (auth.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var bestID string
	var best memJob
	for id, j := range m.jobs {
		if j.Status == auth.JobDone || (!j.leaseExpires.IsZero() && j.leaseExpires.After(at)) {
			continue
		}
		if bestID == "" || j.CreatedAt.Before(best.CreatedAt) {
			bestID, best = id, j
		}
	}
	if bestID == "" {
		return auth.Job{}, false, nil
	}
	best.Status = auth.JobRunning
	best.Attempts++
	best.NextAttemptAt = time.Time{}
	best.Steps, best.StepsOmitted = nil, 0
	best.leaseExpires = at.Add(time.Duration(best.TimeoutMS)*time.Millisecond + margin)
	m.jobs[bestID] = best
	return best.Job, true, nil
}

func (m *MemStore) RetryJob(_ context.Context, id string, notBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return auth.ErrNotFound
	}
	if j.Status == auth.JobDone {
		return nil
	}
	j.Status = auth.JobQueued
	j.Failures++
	j.NextAttemptAt = notBefore
	j.leaseExpires = notBefore
	m.jobs[id] = j
	return nil
}

func (m *MemStore) CreateEmailToken(_ context.Context, t auth.EmailToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.emailTokens == nil {
		m.emailTokens = map[string]auth.EmailToken{}
	}
	m.emailTokens[t.Hash] = t
	return nil
}

func (m *MemStore) GetEmailToken(_ context.Context, hash string) (auth.EmailToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.emailTokens[hash]
	if !ok {
		return auth.EmailToken{}, auth.ErrInvalidToken
	}
	return t, nil
}

func (m *MemStore) RedeemEmailToken(_ context.Context, hash, purpose string, now time.Time) (auth.EmailToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.emailTokens[hash]
	if !ok || t.Purpose != purpose || !t.UsedAt.IsZero() || !now.Before(t.ExpiresAt) {
		return auth.EmailToken{}, auth.ErrInvalidToken
	}
	t.UsedAt = now
	m.emailTokens[hash] = t
	return t, nil
}

func (m *MemStore) InvalidateEmailTokens(_ context.Context, userID, purpose string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, t := range m.emailTokens {
		if t.UserID == userID && t.Purpose == purpose && t.UsedAt.IsZero() {
			t.UsedAt = now
			m.emailTokens[h] = t
		}
	}
	return nil
}

// CounterKeys returns the keys of every counter, for tests.
func (m *MemStore) CounterKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.attempts))
	for k := range m.attempts {
		out = append(out, k)
	}
	return out
}

// EmailTokens returns every stored token record, for tests.
func (m *MemStore) EmailTokens() []auth.EmailToken {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auth.EmailToken, 0, len(m.emailTokens))
	for _, t := range m.emailTokens {
		out = append(out, t)
	}
	return out
}

func (m *MemStore) CompleteJob(_ context.Context, id string, result auth.JobResult, completedAt, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return auth.ErrNotFound
	}
	if j.Status == auth.JobDone {
		return nil
	}
	j.Status = auth.JobDone
	j.CompletedAt = completedAt
	j.ExpiresAt = expiresAt
	r := result
	j.Result = &r
	m.jobs[id] = j
	return nil
}

func (m *MemStore) CancelJob(_ context.Context, id string, result auth.JobResult, steps []auth.Step, completedAt, expiresAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.Status == auth.JobDone {
		return false, nil
	}
	j.Status = auth.JobDone
	j.CompletedAt = completedAt
	j.ExpiresAt = expiresAt
	j.leaseExpires = time.Time{}
	r := result
	j.Result = &r
	j.Steps = append([]auth.Step(nil), steps...)
	m.jobs[id] = j
	return true, nil
}

func (m *MemStore) GetJob(_ context.Context, id string) (auth.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return auth.Job{}, false, nil
	}
	return j.Job, true, nil
}

// ClaimIdempotency inserts rec if its id isn't already used, mirroring
// the MongoDB store's insert-or-fetch-existing atomicity (a single
// mutex makes this trivially atomic here).
func (m *MemStore) ClaimIdempotency(_ context.Context, rec auth.IdempotencyRecord) (auth.IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.idempotency[rec.ID]; ok {
		return existing, false, nil
	}
	m.idempotency[rec.ID] = rec
	return rec, true, nil
}

func (m *MemStore) CompleteIdempotency(_ context.Context, id string, result *auth.JobResult, jobID string, completedAt, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.idempotency[id]
	if !ok {
		return auth.ErrNotFound
	}
	rec.Status = auth.IdempotencyDone
	rec.Result = result
	rec.JobID = jobID
	rec.ExpiresAt = expiresAt
	m.idempotency[id] = rec
	return nil
}

func (m *MemStore) ReleaseIdempotency(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.idempotency, id)
	return nil
}

func (m *MemStore) EraseUser(_ context.Context, id, placeholderEmail string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.ErrNotFound
	}
	for k, sess := range m.sessions {
		if sess.UserID == id {
			delete(m.sessions, k)
		}
	}
	for k, i := range m.identities {
		if i.UserID == id {
			delete(m.identities, k)
		}
	}
	u.Email, u.ErasedAt, u.Disabled, u.EmailVerified, u.Roles, u.UpdatedAt = placeholderEmail, now, true, true, []string{}, now
	u.Locale, u.PendingEmail, u.PreviousEmail, u.AdminNetworks, u.LoginNetworks = "", "", "", nil, nil
	m.users[id] = u
	return nil
}

func (m *MemStore) DeleteEmailTokensOfUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, t := range m.emailTokens {
		if t.UserID == userID {
			delete(m.emailTokens, h)
		}
	}
	return nil
}

func (m *MemStore) DeleteEmailJobsOfUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, j := range m.jobs {
		if j.Email != nil && j.Email.UserID == userID {
			delete(m.jobs, id)
		}
	}
	return nil
}

func (m *MemStore) AddEraseCount(_ context.Context, jobID, key string, n int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Erase == nil {
		return auth.ErrNotFound
	}
	e := *j.Erase
	e.Counts = map[string]int64{}
	for k, v := range j.Erase.Counts {
		e.Counts[k] = v
	}
	e.Counts[key] += n
	j.Erase = &e
	m.jobs[jobID] = j
	return nil
}

func (m *MemStore) ClearEraseEmail(_ context.Context, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok || j.Erase == nil {
		return auth.ErrNotFound
	}
	e := *j.Erase
	e.Email = ""
	j.Erase = &e
	m.jobs[jobID] = j
	return nil
}

func (m *MemStore) ReopenJob(_ context.Context, id string, expiresAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.Status != auth.JobDone || j.Result == nil || j.Result.Status == "ok" {
		return false, nil
	}
	j.Status, j.Attempts, j.Failures, j.ExpiresAt = auth.JobQueued, 0, 0, expiresAt
	j.Result, j.CompletedAt, j.NextAttemptAt, j.leaseExpires = nil, time.Time{}, time.Time{}, time.Time{}
	m.jobs[id] = j
	return true, nil
}

func (m *MemStore) SetScheduleState(_ context.Context, st auth.ScheduleState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedules[st.Database+"/"+st.Function] = st
	return nil
}

func (m *MemStore) ListScheduleStates(context.Context) ([]auth.ScheduleState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auth.ScheduleState, 0, len(m.schedules))
	for _, st := range m.schedules {
		out = append(out, st)
	}
	return out, nil
}

func (m *MemStore) SetJobSteps(_ context.Context, id string, attempt int, steps []auth.Step, omitted int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.Status != auth.JobRunning || j.Attempts != attempt {
		return false, nil
	}
	j.Steps, j.StepsOmitted = append([]auth.Step(nil), steps...), omitted
	m.jobs[id] = j
	return true, nil
}

func (m *MemStore) RenewJobLease(_ context.Context, id string, attempt int, until time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.Status != auth.JobRunning || j.Attempts != attempt {
		return false, nil
	}
	j.leaseExpires = until
	m.jobs[id] = j
	return true, nil
}

func (m *MemStore) SetCheckReport(_ context.Context, r auth.CheckReport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checks[r.Database+"/"+r.Collection] = r
	return nil
}

func (m *MemStore) ListCheckReports(_ context.Context, documents bool) ([]auth.CheckReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auth.CheckReport, 0, len(m.checks))
	for _, r := range m.checks {
		if !documents {
			r.Documents = nil
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *MemStore) GetCheckReport(_ context.Context, database, collection string) (auth.CheckReport, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.checks[database+"/"+collection]
	return r, ok, nil
}

func (m *MemStore) JournalFile(_ context.Context, e auth.FileJournalEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.journal[e.ID] = e
	return nil
}

func (m *MemStore) SetFileJournalStatus(_ context.Context, id, status, documentID string, at, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.journal[id]
	if !ok {
		return auth.ErrNotFound
	}
	e.Status, e.UpdatedAt, e.ExpiresAt = status, at, expiresAt
	if documentID != "" {
		e.DocumentID = documentID
	}
	m.journal[id] = e
	return nil
}

func (m *MemStore) StaleFileJournal(_ context.Context, before, now time.Time, limit int) ([]auth.FileJournalEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.FileJournalEntry
	for _, e := range m.journal {
		open := e.Status == auth.JournalWriting || e.Status == auth.JournalStored || e.Status == auth.JournalAttaching
		switch {
		case !open:
		case e.Pending && e.Status == auth.JournalStored:
			if e.PendingUntil.Before(now) {
				out = append(out, e)
			}
		case e.UpdatedAt.Before(before):
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b auth.FileJournalEntry) int { return a.UpdatedAt.Compare(b.UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) FileJournalEntry(_ context.Context, id string) (auth.FileJournalEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.journal[id]
	if !ok {
		return e, auth.ErrNotFound
	}
	return e, nil
}

func (m *MemStore) CompletePendingUpload(_ context.Context, id, name, contentType, sha256sum string, size int64, uploadedAt, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.journal[id]
	if !ok {
		return auth.ErrNotFound
	}
	e.Name, e.Type, e.SHA256, e.Size, e.UploadedAt, e.Status, e.UpdatedAt = name, contentType, sha256sum, size, uploadedAt, auth.JournalStored, at
	m.journal[id] = e
	return nil
}

func (m *MemStore) ClaimPendingUpload(_ context.Context, id, tokenHash, documentID string, now time.Time) (auth.FileJournalEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.journal[id]
	if !ok || !e.Pending || e.Status != auth.JournalStored || e.TokenHash != tokenHash || !e.PendingUntil.After(now) {
		return auth.FileJournalEntry{}, auth.ErrNotFound
	}
	e.Status, e.DocumentID, e.UpdatedAt = auth.JournalAttaching, documentID, now
	m.journal[id] = e
	return e, nil
}

func (m *MemStore) ReleasePendingUpload(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.journal[id]; ok && e.Status == auth.JournalAttaching {
		e.Status, e.DocumentID, e.UpdatedAt = auth.JournalStored, "", at
		m.journal[id] = e
	}
	return nil
}

func (m *MemStore) CountOpenPendingUploads(_ context.Context, callerKey string, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.journal {
		if e.Pending && e.CallerKey == callerKey && (e.Status == auth.JournalWriting || e.Status == auth.JournalStored) && e.PendingUntil.After(now) {
			n++
		}
	}
	return n, nil
}

// JournalEntry returns an upload's record, for tests.
func (m *MemStore) JournalEntry(id string) (auth.FileJournalEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.journal[id]
	return e, ok
}

// Deletions returns the queued deletions, for tests.
func (m *MemStore) Deletions() []auth.FileDeletion {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.FileDeletion
	for _, d := range m.deletions {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b auth.FileDeletion) int { return strings.Compare(a.Key, b.Key) })
	return out
}

func (m *MemStore) QueueFileDeletion(_ context.Context, d auth.FileDeletion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.deletions[d.Key]; !ok {
		m.deletions[d.Key] = d
	}
	return nil
}

func (m *MemStore) ClaimFileDeletions(_ context.Context, now, lease time.Time, limit int) ([]auth.FileDeletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.FileDeletion
	for k, d := range m.deletions {
		if len(out) == limit {
			break
		}
		if !d.NotBefore.After(now) {
			out = append(out, d)
			d.NotBefore = lease
			m.deletions[k] = d
		}
	}
	return out, nil
}

func (m *MemStore) CompleteFileDeletion(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.deletions, key)
	return nil
}

func (m *MemStore) RetryFileDeletion(_ context.Context, key string, notBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.deletions[key]; ok {
		d.Attempts++
		d.NotBefore = notBefore
		m.deletions[key] = d
	}
	return nil
}
