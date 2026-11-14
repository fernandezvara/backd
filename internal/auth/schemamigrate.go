package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/xid"
)

// The job that migrates the documents of a versioned collection to its current schema version
// (design/schema-versioning.md §8): one job per collection, started by a worker at every start or
// by an administrator, at most one unfinished at a time.
const (
	SchemaMigrateOrigin = "backd:schema.migrate"
	// SchemaMigrateLease is how long a worker holds the job before another may take it; it
	// renews after every batch.
	SchemaMigrateLease = 10 * time.Minute
	// SchemaMigrateBatch and SchemaMigrateBatchBytes bound one batch: documents and bytes of fields.
	SchemaMigrateBatch      = 500
	SchemaMigrateBatchBytes = 4 << 20
	// SchemaMigrateRate is how many documents a second a job migrates at most.
	SchemaMigrateRate = 1000
	// MaxSchemaMigrateFailures is how many failed documents a job lists; past it only the count.
	MaxSchemaMigrateFailures = 1000
)

// Job endings, in SchemaMigrateJob.Ending.
const (
	MigrateComplete  = "complete"
	MigratePartial   = "partial"
	MigrateCancelled = "cancelled"
)

// Failure kinds of a document: what stopped it from migrating.
const (
	FailThrew      = "threw"      // the migration threw
	FailAsync      = "async"      // it returned a promise
	FailNotJSON    = "not_json"   // its result isn't JSON, or isn't an object
	FailNoStep     = "no_step"    // there is no migration from the document's version
	FailTimeout    = "timeout"    // it ran out of time
	FailInvalid    = "invalid"    // the result doesn't match the current schema
	FailFile       = "file_field" // it changed a file field
	FailConflict   = "conflict"   // the result breaks a unique index
	FailPlaintext  = "write"      // the write failed
	FailMemory     = "memory"
	FailExecutor   = "executor"
	FailUnreadable = "unreadable"
)

// SchemaMigrateFailure is one document that did not migrate and stays at its version. It never
// holds a value: the id, where it stopped and why.
type SchemaMigrateFailure struct {
	ID      string
	Step    string // "2 → 3"
	Kind    string // one of the Fail* kinds
	Path    string // the JSON path, for an invalid result
	Keyword string // the schema keyword, for an invalid result
}

// SchemaMigrateCounts is what a job did (or, in a dry run, would do).
type SchemaMigrateCounts struct {
	Scanned  int64
	Migrated int64
	Skipped  int64 // already at the current version when the job got to them
	Failed   int64
}

// SchemaMigrateJob is what a migration job carries: the collection, the version it migrates to,
// the place it reached, what it did so far and what failed.
type SchemaMigrateJob struct {
	Database   string
	Collection string
	Target     int  // the collection's current version when the job was queued
	DryRun     bool // runs the migrations and validates, writes nothing
	After      string
	SchemaMigrateCounts
	// Failures lists the failed documents, up to MaxSchemaMigrateFailures; Failed counts them all.
	Failures []SchemaMigrateFailure
	// Ending is set when the job ends: complete, partial or cancelled.
	Ending string
}

// SchemaMigrateLock is the exclusive name of a collection's migration: one unfinished at a time.
func SchemaMigrateLock(database, collection string) string {
	return fmt.Sprintf("schema-migrate:%s/%s", database, collection)
}

// ErrSchemaMigrateRunning means a migration of that collection is queued or running and is not
// the one asked for; JobID is its job.
type ErrSchemaMigrateRunning struct {
	JobID  string
	Target int
}

func (e *ErrSchemaMigrateRunning) Error() string {
	return fmt.Sprintf("a migration of this collection (to version %d) is already running: %s", e.Target, e.JobID)
}

// StartSchemaMigration queues the migration of a collection to j.Target. When one for the same
// target is already queued or running (another worker started it, or an administrator did), that
// job is returned and created is false: starting is idempotent. One for an earlier target is
// cancelled and replaced, because the new one starts from the first document and also migrates
// what the old one had not reached. A dry run never replaces a running job.
func (s *Users) StartSchemaMigration(ctx context.Context, j SchemaMigrateJob, requestedBy, requestID string) (job Job, created bool, err error) {
	j.After, j.SchemaMigrateCounts, j.Failures, j.Ending = "", SchemaMigrateCounts{}, nil, ""
	for range 3 {
		now := s.now()
		job = Job{
			ID: xid.New().String(), Database: j.Database, Function: j.Collection, CallerActor: requestedBy, Origin: SchemaMigrateOrigin,
			Exclusive: SchemaMigrateLock(j.Database, j.Collection), TimeoutMS: SchemaMigrateLease.Milliseconds(), RequestID: requestID,
			Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL), SchemaMigrate: &j,
		}
		err = s.Store.EnqueueJob(ctx, job)
		if !errors.Is(err, ErrJobExclusive) {
			return job, err == nil, err
		}
		running, ok := s.unfinishedSchemaMigration(ctx, j.Database, j.Collection)
		if !ok { // it ended between the two reads
			continue
		}
		if m := running.SchemaMigrate; m.Target == j.Target && m.DryRun == j.DryRun {
			return running, false, nil
		} else if m.Target > j.Target || j.DryRun {
			return Job{}, false, &ErrSchemaMigrateRunning{JobID: running.ID, Target: m.Target}
		}
		if _, err := s.CancelJob(ctx, running.ID); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrJobFinished) {
			return Job{}, false, err
		}
	}
	return Job{}, false, &ErrSchemaMigrateRunning{}
}

// unfinishedSchemaMigration is the queued or running migration of the collection.
func (s *Users) unfinishedSchemaMigration(ctx context.Context, database, collection string) (Job, bool) {
	for _, status := range []string{JobRunning, JobQueued} {
		jobs, _, err := s.Store.ListJobs(ctx, JobFilter{Origin: SchemaMigrateOrigin, Database: database, Function: collection, Status: status, Limit: 1})
		if err == nil && len(jobs) > 0 && jobs[0].SchemaMigrate != nil {
			return jobs[0], true
		}
	}
	return Job{}, false
}

// LatestSchemaMigration is the latest finished migration of the collection (dry runs included),
// or false when none is kept.
func (s *Users) LatestSchemaMigration(ctx context.Context, database, collection string) (Job, bool, error) {
	jobs, _, err := s.Store.ListJobs(ctx, JobFilter{Origin: SchemaMigrateOrigin, Database: database, Function: collection, Status: JobDone, Limit: 1})
	if err != nil || len(jobs) == 0 || jobs[0].SchemaMigrate == nil {
		return Job{}, false, err
	}
	return jobs[0], true, nil
}

// SetSchemaMigrateProgress keeps the place and counts of a running migration.
func (s *Users) SetSchemaMigrateProgress(ctx context.Context, jobID string, m SchemaMigrateJob) error {
	return s.Store.SetSchemaMigrateProgress(ctx, jobID, m)
}
