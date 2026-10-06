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
	RerunOf        string // the finished job an administrator re-ran to make this one; empty otherwise
	Depth          int    // nested calls above it
	// Email is set on an email job: backd's own request to send a message, which
	// a worker renders and hands to the realm's delivery function. It holds no
	// message and no token.
	Email *EmailJob
	// Erase is set on an erase job (origin backd:account.erase).
	Erase *EraseJob
	// Check is set on a schema check's job (origin CheckOrigin).
	Check *CheckJob
	// Exclusive, when set, lets only one job of that name be unfinished at a
	// time: enqueuing another fails with ErrJobExclusive.
	Exclusive string
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
	// Steps are what the running (or last) attempt reported with ctx.step and
	// ctx.progress, the last being the current one; StepsOmitted counts those
	// left out of the middle when there were more than MaxSteps. A new attempt
	// starts with none.
	Steps        []Step
	StepsOmitted int
}

// MaxSteps is how many steps a job or an invocation record keeps: the first
// half and the latest half of what a function reported.
const MaxSteps = 100

// Step statuses.
const (
	StepRunning   = "running"
	StepDone      = "done"
	StepFailed    = "failed"
	StepTimedOut  = "timed_out"
	StepCancelled = "cancelled"
)

// Step is one named step of an attempt, with how far it got.
type Step struct {
	N          int
	Name       string
	Status     string // StepRunning, StepDone, StepFailed, StepTimedOut or StepCancelled
	StartedAt  time.Time
	EndedAt    time.Time // zero while running
	DurationMS int64
	Current    float64
	Total      *float64 // nil when the step has no known size
	Message    string
	UpdatedAt  time.Time
}

// SetJobSteps stores the steps the attempt numbered attempt reported, replacing
// what the job held. It does nothing (false) when the job is not running that
// attempt any more: finished, cancelled, or claimed again by another worker.
func (s *Users) SetJobSteps(ctx context.Context, id string, attempt int, steps []Step, omitted int) (bool, error) {
	if len(steps) > MaxSteps {
		steps = append(steps[:MaxSteps/2:MaxSteps/2], steps[len(steps)-MaxSteps/2:]...)
	}
	return s.Store.SetJobSteps(ctx, id, attempt, steps, omitted)
}

// CloseSteps ends the step still running, if any, with status and the time at.
// It returns steps itself when none is running.
func CloseSteps(steps []Step, status string, at time.Time) []Step {
	if n := len(steps); n > 0 && steps[n-1].Status == StepRunning {
		out := append([]Step(nil), steps...)
		last := &out[n-1]
		last.Status, last.EndedAt, last.UpdatedAt = status, at, at
		if d := at.Sub(last.StartedAt).Milliseconds(); d > 0 {
			last.DurationMS = d
		}
		return out
	}
	return steps
}

// EmailJob is what an email job stores: which email, for whom, and nothing
// that works as a credential.
type EmailJob struct {
	Kind       string
	UserID     string
	Locale     string
	RedirectTo string // checked when the request was made; bound to the token the worker creates
	// To says whose address the message goes to: "" the user's current one,
	// "pending" the address they asked to change to, "previous" the one
	// before the last change.
	To string
	// Notice marks a message that only informs: no token, no link.
	Notice bool
	// InvitationID is the invitation an invitation email is for (there is no
	// user yet): the worker takes the address from it.
	InvitationID string
	// Custom is set on an email a function asked for with ctx.email.send. Unlike
	// the account emails it stores what the function gave: the recipients'
	// addresses and the data for the template, for as long as jobs are kept.
	Custom *CustomEmail
}

// EraseJob is what an erase job carries: whose data to erase, the email to
// match fields that hold it (cleared when the job finishes), and what has
// been done so far.
//
// An erase job is not a function: it has no executor, a worker applies the
// collections' policies itself.
type EraseJob struct {
	UserID string
	Email  string
	Counts map[string]int64 // "<database>/<collection>/<operation>" → documents
}

// CustomEmail is what a function's email job carries.
type CustomEmail struct {
	Function string // <database>/<name> of the function that asked
	To       []string
	CC       []string
	BCC      []string
	Data     map[string]any
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

// ResultSkipped is the result status of a scheduled run that didn't happen
// because the function's previous run hadn't finished (overlap: skip).
const ResultSkipped = "skipped"

// ResultCancelled is the result status of a job an administrator cancelled.
const ResultCancelled = "cancelled"

var (
	// ErrJobFinished means the job is done: there is nothing left to cancel.
	ErrJobFinished = errors.New("the job has already finished")
	// ErrJobNotFinished means a job can't be re-run while it may still run.
	ErrJobNotFinished = errors.New("the job hasn't finished: cancel it, or wait for it to end")
	// ErrJobNotFunction means the job isn't a function call: backd's own
	// emails and erasures are not cancelled or re-run by hand.
	ErrJobNotFunction = errors.New("only a function's job can be cancelled or re-run")
)

// CancelJob ends a queued or running function job: it is done, with the
// result "cancelled", at once. A worker running it notices and stops the run
// (what the function already did stays done). ErrJobFinished if it ended
// first, ErrNotFound if there is no such job.
func (s *Users) CancelJob(ctx context.Context, id string) (Job, error) {
	j, found, err := s.Store.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !found {
		return Job{}, ErrNotFound
	}
	if j.Email != nil || j.Erase != nil {
		return Job{}, ErrJobNotFunction
	}
	if j.Status == JobDone {
		return Job{}, ErrJobFinished
	}
	now := s.now()
	cancelled, err := s.Store.CancelJob(ctx, id, JobResult{Status: ResultCancelled, Message: "cancelled by an administrator"}, CloseSteps(j.Steps, StepCancelled, now), now, now.Add(s.jobRetention()))
	if err != nil {
		return Job{}, err
	}
	if !cancelled {
		return Job{}, ErrJobFinished // it ended between the read and the update
	}
	j, _, err = s.Store.GetJob(ctx, id)
	return j, err
}

// RerunJob queues a new job for a finished function job: the same function,
// input and caller, as a job of its own (RerunOf says which). timeoutMS is the
// function's timeout now, for the new job's lease.
func (s *Users) RerunJob(ctx context.Context, id string, timeoutMS int64) (Job, error) {
	j, found, err := s.Store.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !found {
		return Job{}, ErrNotFound
	}
	if j.Email != nil || j.Erase != nil || j.Check != nil {
		return Job{}, ErrJobNotFunction
	}
	if j.Status != JobDone {
		return Job{}, ErrJobNotFinished
	}
	return s.EnqueueJob(ctx, Job{
		Database: j.Database, Function: j.Function, Input: j.Input,
		CallerActor: j.CallerActor, CallerUserID: j.CallerUserID, CallerKeyHash: j.CallerKeyHash,
		// A scheduled run acted as the function itself; its re-run isn't a cron run, but acts the same.
		ActsAsFunction: j.ActsAsFunction || j.Scheduled,
		Origin:         "admin", RerunOf: j.ID, TimeoutMS: timeoutMS,
	})
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
	Origin             string    // exact origin, such as function:main/ship; "" matches every one
	EraseUser          string    // the erase jobs of this user
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
// It returns the job, and false when the run already exists.
//
// With skipOverlap, a function that still has an unfinished job (queued,
// waiting for a retry, or running) doesn't get a new run: the scheduled time is
// recorded as a job that is done at once with the result "skipped", so the job
// list shows it and no worker claims it.
func (s *Users) EnqueueScheduledJob(ctx context.Context, j Job, scheduledAt time.Time, skipOverlap bool) (Job, bool, error) {
	now := s.now()
	j.ID = ScheduledJobID(j.Database, j.Function, scheduledAt)
	j.Status = JobQueued
	j.Scheduled = true
	j.CallerActor = "cron"
	j.CreatedAt = now
	j.ExpiresAt = now.Add(pendingJobTTL)
	if skipOverlap {
		active, err := s.activeJob(ctx, j.Database, j.Function)
		if err != nil {
			return Job{}, false, err
		}
		if active != "" {
			j.Status = JobDone
			j.CompletedAt = now
			j.ExpiresAt = now.Add(s.jobRetention())
			j.Result = &JobResult{Status: ResultSkipped, Message: "skipped: the previous run (" + active + ") hasn't finished"}
		}
	}
	err := s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExists) {
		return j, false, nil
	}
	return j, err == nil, err
}

// activeJob returns the id of a job of the function that hasn't finished, or "".
func (s *Users) activeJob(ctx context.Context, database, function string) (string, error) {
	for _, status := range []string{JobRunning, JobQueued} {
		jobs, _, err := s.Store.ListJobs(ctx, JobFilter{Database: database, Function: function, Status: status, Limit: 1})
		if err != nil {
			return "", err
		}
		if len(jobs) > 0 {
			return jobs[0].ID, nil
		}
	}
	return "", nil
}

// CompletionJobID is the deterministic id of the notification of a job's end.
func CompletionJobID(jobID string) string { return "complete_" + jobID }

// EnqueueCompletionJob queues the notification of a job's end (on_complete): a
// job of function target in the same database, with input as its input, run with
// no caller (it has whatever its own function.yaml gives it). Its id comes from
// the finished job, so a second attempt to queue it for the same job does
// nothing; it returns false then. timeoutMS is the target's timeout.
func (s *Users) EnqueueCompletionJob(ctx context.Context, finished Job, target string, timeoutMS int64, input json.RawMessage) (Job, bool, error) {
	now := s.now()
	j := Job{
		ID: CompletionJobID(finished.ID), Database: finished.Database, Function: target, Input: input,
		CallerActor: "on_complete", Origin: "on_complete:" + finished.Database + "/" + finished.Function,
		TimeoutMS: timeoutMS, RequestID: finished.RequestID, Status: JobQueued,
		CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
	}
	err := s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExists) {
		return j, false, nil
	}
	return j, err == nil, err
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
