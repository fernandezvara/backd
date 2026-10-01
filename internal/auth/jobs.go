package auth

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Job statuses.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
)

// pendingJobTTL is a safety net: how long a job that's never claimed, or
// never finishes, is kept before MongoDB removes it, regardless of the
// realm's functions.job_retention (which only starts counting once a
// job is done). A stuck job (executor down, bundle missing) shouldn't
// live forever.
const pendingJobTTL = 48 * time.Hour

// leaseMargin is added to a job's own function timeout when a worker
// claims it, so an expired lease never overlaps a still-running attempt
// of the same job on a live executor: the executor kills the process at
// its deadline well before the lease would let another worker reclaim it.
const leaseMargin = 30 * time.Second

// Job is one async function call (roadmap F11), stored in the realm's
// system database until it's claimed, run, and its result read or
// expired. Unlike sync calls, whose input and output are never stored, a
// job's input and result are — there's nowhere else to keep them until
// the caller reads them back.
type Job struct {
	ID             string
	Database       string
	Function       string // name only; Database plus this is the function
	Input          json.RawMessage
	CallerActor    string // for display: "user:<id>", "key:<name>", "anonymous"
	CallerUserID   string // "" unless the caller was a signed-in user
	CallerKeyHash  string // "" unless the caller was an API key
	Scheduled      bool   // created by the function's cron schedule, acts as the function itself
	ActsAsFunction bool   // run as the function itself, with full access (a scheduled function run by hand)
	Origin         string // http, function (ctx.call), cron, admin or backd:<event>
	ParentID       string // the invocation that queued it with ctx.call; empty otherwise
	Depth          int    // nested calls above it
	// Email is set on an email job: backd's own request to send a message, which
	// a worker renders and hands to the realm's delivery function. It holds no
	// message and no token.
	Email         *EmailJob
	TimeoutMS     int64 // the function's timeout at enqueue time, for the worker's lease
	RequestID     string
	Status        string    // queued | running | done
	Attempts      int       // times a worker claimed it
	Failures      int       // attempts that ended in a failure worth retrying
	NextAttemptAt time.Time // when a retry may be claimed; zero unless one is waiting
	CreatedAt     time.Time
	CompletedAt   time.Time // zero until done
	ExpiresAt     time.Time
	Result        *JobResult // nil until done
}

// EmailJob is what an email job stores: which email, for whom, and nothing
// that works as a credential.
type EmailJob struct {
	Kind       string
	UserID     string
	Locale     string
	RedirectTo string // checked when the request was made; bound to the token the worker creates
}

// JobResult is how a job's run ended: the same vocabulary as a sync
// call's executor.Result (executor.Status*), flattened here so this
// package doesn't need to import the executor.
type JobResult struct {
	Status     string
	Output     json.RawMessage
	Code       string
	Message    string
	Details    json.RawMessage
	DurationMS int64
	// HTTPStatus is the function's own chosen status (ctx.error's first
	// argument) when Status is "function_error"; unused otherwise. A
	// job never answers HTTP itself, but an idempotent sync replay
	// (roadmap F12) needs it to reconstruct the original response.
	HTTPStatus int
}

func (s *Users) jobRetention() time.Duration {
	if s.Settings.FunctionsJobRetention > 0 {
		return s.Settings.FunctionsJobRetention
	}
	return registry.DefaultJobRetention
}

// EnqueueJob stores a new job, queued for a worker to claim, and returns
// it with its id and timestamps filled in.
func (s *Users) EnqueueJob(ctx context.Context, j Job) (Job, error) {
	now := s.now()
	j.ID = xid.New().String()
	j.Status = JobQueued
	j.CreatedAt = now
	j.ExpiresAt = now.Add(pendingJobTTL)
	if err := s.Store.EnqueueJob(ctx, j); err != nil {
		return Job{}, err
	}
	return j, nil
}

// JobFilter selects jobs to list. Zero fields match everything.
type JobFilter struct {
	Database, Function string    // Function is the name only; "" matches every function
	Status             string    // queued, running or done
	Scheduled          *bool     // nil: both; true: cron runs only; false: called ones only
	Since, Until       time.Time // on created_at: from Since, before Until
	Limit, Skip        int
}

// Jobs returns the jobs matching f, newest first, and whether more follow.
func (s *Users) Jobs(ctx context.Context, f JobFilter) ([]Job, bool, error) {
	return s.Store.ListJobs(ctx, f)
}

// ErrJobExists means a job with that id was already enqueued.
var ErrJobExists = errors.New("job already exists")

// EnqueueScheduledJob enqueues the run of a scheduled function for
// scheduledAt, under an id derived from the function and that time, so
// however many workers try, exactly one job is created (roadmap F18).
// It returns false when the run already exists.
func (s *Users) EnqueueScheduledJob(ctx context.Context, j Job, scheduledAt time.Time) (bool, error) {
	now := s.now()
	j.ID = ScheduledJobID(j.Database, j.Function, scheduledAt)
	j.Status = JobQueued
	j.Scheduled = true
	j.CallerActor = "cron"
	j.CreatedAt = now
	j.ExpiresAt = now.Add(pendingJobTTL)
	err := s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExists) {
		return false, nil
	}
	return err == nil, err
}

// ScheduledJobID is the deterministic id of a scheduled run.
func ScheduledJobID(database, function string, scheduledAt time.Time) string {
	return "cron_" + database + "_" + function + "_" + scheduledAt.UTC().Format("200601021504")
}

// ClaimJob atomically claims the oldest claimable job: never claimed, or
// whose lease has expired (a dead worker's jobs are picked up again),
// leasing it to workerID for the function's own timeout plus a margin.
func (s *Users) ClaimJob(ctx context.Context, workerID string) (Job, bool, error) {
	return s.Store.ClaimJob(ctx, workerID, s.now(), leaseMargin)
}

// CompleteJob records a job's result and starts its retention countdown.
func (s *Users) CompleteJob(ctx context.Context, id string, result JobResult) error {
	now := s.now()
	return s.Store.CompleteJob(ctx, id, result, now, now.Add(s.jobRetention()))
}

// RetryJob puts a job whose attempt failed back in the queue, to be
// claimed again once delay has passed.
func (s *Users) RetryJob(ctx context.Context, id string, delay time.Duration) error {
	return s.Store.RetryJob(ctx, id, s.now().Add(delay))
}

// GetJob returns a job by id, for its caller to poll its status.
func (s *Users) GetJob(ctx context.Context, id string) (Job, bool, error) {
	return s.Store.GetJob(ctx, id)
}
