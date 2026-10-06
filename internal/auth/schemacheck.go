package auth

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/rs/xid"
)

// A schema check (roadmap #26) reads every stored document of some collections
// and reports those that no longer match the collection's schema. It is a job
// backd runs itself (origin CheckOrigin), at most one per realm at a time.

const (
	// CheckOrigin is the origin of a schema check's job.
	CheckOrigin = "backd:schema.check"
	// CheckLock is the exclusive name of a schema check's job: one at a time.
	CheckLock = "schema-check"
	// CheckLease is how long a worker holds a check before another may claim it; the
	// worker renews it after every batch.
	CheckLease = 10 * time.Minute
	// CheckMaxDuration is the longest a check attempt runs before it stops and
	// says its report is incomplete.
	CheckMaxDuration = 6 * time.Hour
	// DefaultCheckLimit and MaxCheckLimit bound how many invalid documents a
	// report lists per collection.
	DefaultCheckLimit = 100
	MaxCheckLimit     = 1000
	// MaxCheckProblems is how many problems a report lists per document.
	MaxCheckProblems = 5
)

// CheckJob is what a schema check's job holds: what to check and how much to list.
type CheckJob struct {
	Collections []string // "<database>/<collection>", resolved when the check started
	Database    string   // the scope asked for: "" every database
	Collection  string   // "" every collection of Database
	Limit       int      // invalid documents listed per collection
}

// ErrCheckRunning means a schema check is already queued or running in the realm;
// JobID is its job.
type ErrCheckRunning struct{ JobID string }

func (e *ErrCheckRunning) Error() string { return "a schema check is already running: " + e.JobID }

// ErrJobExclusive means a job with the same exclusive name is not finished yet.
var ErrJobExclusive = errors.New("an exclusive job is not finished yet")

// CheckProblem is one way a document fails its schema: a JSON path and the rule
// that failed, never the value.
type CheckProblem struct {
	Path   string
	Reason string
}

// CheckedDocument is a document that failed its collection's schema.
type CheckedDocument struct {
	ID       string
	Deleted  bool // soft-deleted (in the trash)
	Problems []CheckProblem
	// MoreProblems counts the problems left out past MaxCheckProblems.
	MoreProblems int
}

// CheckReport is the latest finished schema check of one collection.
type CheckReport struct {
	Database, Collection string
	JobID                string
	StartedAt            time.Time
	FinishedAt           time.Time
	Scanned              int64
	Invalid              int64
	// Complete is false when the scan stopped before the end: StoppedBy says why
	// ("limit" when Limit invalid documents were found, "time" for CheckMaxDuration).
	Complete  bool
	StoppedBy string
	Limit     int
	// SchemaHash identifies the schema the documents were checked against.
	SchemaHash string
	Documents  []CheckedDocument // up to Limit
}

// StartSchemaCheck queues a schema check of collections (each "<database>/<collection>"),
// or *ErrCheckRunning when one is already queued or running in the realm. scope
// is what was asked for (database and collection, either may be "").
func (s *Users) StartSchemaCheck(ctx context.Context, scope CheckJob, requestedBy, requestID string) (Job, error) {
	if scope.Limit <= 0 {
		scope.Limit = DefaultCheckLimit
	}
	now := s.now()
	j := Job{
		ID: xid.New().String(), Database: scope.Database, Function: scope.Collection, CallerActor: requestedBy,
		Origin: CheckOrigin, Exclusive: CheckLock, TimeoutMS: CheckLease.Milliseconds(), RequestID: requestID,
		Check: &scope, Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
	}
	err := s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExclusive) {
		return Job{}, &ErrCheckRunning{JobID: s.runningCheck(ctx)}
	}
	if err != nil {
		return Job{}, err
	}
	return j, nil
}

// runningCheck is the id of the realm's queued or running check, or "".
func (s *Users) runningCheck(ctx context.Context) string {
	for _, status := range []string{JobRunning, JobQueued} {
		jobs, _, err := s.Store.ListJobs(ctx, JobFilter{Origin: CheckOrigin, Status: status, Limit: 1})
		if err == nil && len(jobs) > 0 {
			return jobs[0].ID
		}
	}
	return ""
}

// RenewJobLease keeps a running job's lease for another d, for the worker's
// attempt; false when it no longer runs that attempt.
func (s *Users) RenewJobLease(ctx context.Context, id string, attempt int, d time.Duration) (bool, error) {
	return s.Store.RenewJobLease(ctx, id, attempt, s.now().Add(d))
}

// SaveCheckReport stores r as the collection's latest report, replacing the last.
func (s *Users) SaveCheckReport(ctx context.Context, r CheckReport) error {
	return s.Store.SetCheckReport(ctx, r)
}

// CheckReports returns the latest report of every collection checked, sorted
// by database and collection. With documents false the lists of invalid
// documents are left out.
func (s *Users) CheckReports(ctx context.Context, documents bool) ([]CheckReport, error) {
	out, err := s.Store.ListCheckReports(ctx, documents)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b CheckReport) int {
		if c := compareStrings(a.Database, b.Database); c != 0 {
			return c
		}
		return compareStrings(a.Collection, b.Collection)
	})
	return out, nil
}

// CheckReport returns one collection's latest report; ErrNotFound if it was never checked.
func (s *Users) CheckReport(ctx context.Context, database, collection string) (CheckReport, error) {
	r, found, err := s.Store.GetCheckReport(ctx, database, collection)
	if err != nil {
		return CheckReport{}, err
	}
	if !found {
		return CheckReport{}, ErrNotFound
	}
	return r, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
