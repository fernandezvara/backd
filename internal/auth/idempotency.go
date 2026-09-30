package auth

import (
	"context"
	"time"
)

// Idempotency record statuses.
const (
	IdempotencyRunning = "running"
	IdempotencyDone    = "done"
)

// idempotencyRetention is how long an Idempotency-Key's outcome is kept
// (roadmap F12): fixed, not configurable — calls are fast (a sync
// function's own timeout caps at 60s; an async enqueue is one insert),
// unlike async job results (F11), which can wait on a function running
// up to 24h and so need their own configurable retention.
const idempotencyRetention = 24 * time.Hour

// IdempotencyRecord ties one Idempotency-Key to its outcome, scoped to
// the function and the caller: two different callers can never collide
// on the same client-chosen key, even if they happen to pick the same
// string. Never stores input or output for keyless calls — this is the
// one deliberate exception, for calls that declare a key.
type IdempotencyRecord struct {
	ID          string // see IdempotencyID
	Function    string // <database>/<name>, for display
	CallerActor string
	InputHash   string     // sha256 of the exact input bytes
	Mode        string     // sync | async
	Status      string     // running | done
	Result      *JobResult // nil until done; a sync call's outcome
	JobID       string     // set once done, for async; "" for sync
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// IdempotencyID builds a record's id: unique per function, per caller
// (Caller.Subject(), so ownership can never cross callers), per key.
func IdempotencyID(database, name, callerSubject, key string) string {
	return database + "/" + name + "\x00" + callerSubject + "\x00" + key
}

// ClaimIdempotency atomically claims a key for a new call, or returns
// the existing record (running or done) if one already exists.
func (s *Users) ClaimIdempotency(ctx context.Context, rec IdempotencyRecord) (IdempotencyRecord, bool, error) {
	now := s.now()
	rec.Status = IdempotencyRunning
	rec.CreatedAt = now
	rec.ExpiresAt = now.Add(idempotencyRetention)
	return s.Store.ClaimIdempotency(ctx, rec)
}

// CompleteIdempotency stores a claimed key's outcome.
func (s *Users) CompleteIdempotency(ctx context.Context, id string, result *JobResult, jobID string) error {
	now := s.now()
	return s.Store.CompleteIdempotency(ctx, id, result, jobID, now, now.Add(idempotencyRetention))
}

// ReleaseIdempotency removes a claim that shouldn't be remembered (the
// executor was unreachable or busy — nothing about the call's own
// outcome to replay), so a retry can go through cleanly.
func (s *Users) ReleaseIdempotency(ctx context.Context, id string) error {
	return s.Store.ReleaseIdempotency(ctx, id)
}
