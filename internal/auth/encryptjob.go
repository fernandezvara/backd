package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/xid"
)

// The job that brings the stored documents of a collection in line with its encryption
// configuration (design/field-encryption.md §8).
const (
	EncryptOrigin = "backd:encrypt.migrate"
	// EncryptLease is how long a worker holds the job before another may take it; it renews after
	// every batch.
	EncryptLease = 10 * time.Minute
	// EncryptBatch is how many documents a batch reads.
	EncryptBatch = 200
)

// EncryptCounts is what a migration did (or, in a dry run, would do).
type EncryptCounts struct {
	Scanned     int64 `json:"scanned"`
	Encrypted   int64 `json:"encrypted"`
	Reencrypted int64 `json:"reencrypted"`
	Decrypted   int64 `json:"decrypted"`
	Indexed     int64 `json:"indexed"`
	Marked      int64 `json:"marked"`
	Conflicts   int64 `json:"conflicts"`
}

// EncryptJob is what a migration job carries: the collection, the fields asked for (none: all),
// whether it only counts, and the place it reached with what it did so far.
type EncryptJob struct {
	Database   string
	Collection string
	Fields     []string
	DryRun     bool
	After      string
	EncryptCounts
}

// ErrEncryptRunning means a migration of that collection is queued or running; JobID is its job.
type ErrEncryptRunning struct{ JobID string }

func (e *ErrEncryptRunning) Error() string {
	return "a migration of this collection is already running: " + e.JobID
}

// EncryptLock is the exclusive key of a collection's migration.
func EncryptLock(database, collection string) string {
	return fmt.Sprintf("encrypt:%s/%s", database, collection)
}

// StartEncryptMigration queues the migration of a collection. It is safe to start again after a
// cancel or a failure: it goes on from the beginning, and what is already migrated is skipped.
func (s *Users) StartEncryptMigration(ctx context.Context, ej EncryptJob, requestedBy, requestID string) (Job, error) {
	now := s.now()
	ej.After, ej.EncryptCounts = "", EncryptCounts{}
	j := Job{
		ID: xid.New().String(), CallerActor: requestedBy, Origin: EncryptOrigin, Exclusive: EncryptLock(ej.Database, ej.Collection),
		TimeoutMS: EncryptLease.Milliseconds(), RequestID: requestID, Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
		Encrypt: &ej,
	}
	err := s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExclusive) {
		running := ""
		for _, status := range []string{JobRunning, JobQueued} {
			if jobs, _, lerr := s.Store.ListJobs(ctx, JobFilter{Origin: EncryptOrigin, Status: status, Limit: 20}); lerr == nil {
				for _, c := range jobs {
					if c.Encrypt != nil && c.Encrypt.Database == ej.Database && c.Encrypt.Collection == ej.Collection {
						running = c.ID
					}
				}
			}
			if running != "" {
				break
			}
		}
		return Job{}, &ErrEncryptRunning{JobID: running}
	}
	if err != nil {
		return Job{}, err
	}
	return j, nil
}
