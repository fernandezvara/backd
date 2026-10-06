package httpapi

import (
	"context"
	"github.com/rs/xid"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/registry"
)

// workerPollInterval is how long a worker's goroutine waits before
// trying to claim another job, after finding none to claim.
const workerPollInterval = 250 * time.Millisecond

// Worker claims and runs async jobs (roadmap F11): `backd worker`, or
// `backd serve --with-worker` in the same process. It never listens: it
// reaches the executor and backd's own API (ctx.db) exactly like a
// function's own caller would, reusing the same bundle resolution,
// callback construction and invocation recording sync calls use.
type Worker struct {
	id  string
	fns *functions
	reg *registry.Registry
	// backdURL is backd's public address, for the links in emails.
	backdURL string
	log      *slog.Logger
	// cancelPoll is how often a running job is checked for cancellation (zero:
	// cancelPollInterval); tests shorten it.
	cancelPoll time.Duration

	lastScheduled map[string]time.Time // only touched by the schedule loop
	lastPurge     map[string]time.Time // realm → last purge of unverified accounts; same
}

// NewWorker builds a worker from the same Config NewHandler uses. id
// identifies this process in job leases (so an operator can tell which
// instance claimed a stuck job).
func NewWorker(cfg Config, id string) *Worker {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	users := cfg.Users
	if users == nil {
		users = func(string) *auth.Users { return nil }
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	docs := &documents{reg: cfg.Registry, store: cfg.Store, now: now, users: users, callbackKey: cfg.CallbackKey}
	fns := &functions{docs: docs, runner: cfg.Functions, callbackURL: cfg.CallbackURL, log: log, metrics: cfg.Metrics}
	return &Worker{id: id, fns: fns, reg: cfg.Registry, backdURL: cfg.BackdURL, log: log, lastScheduled: map[string]time.Time{}, lastPurge: map[string]time.Time{}}
}

// Run claims and runs jobs until ctx is done, with concurrency
// goroutines working the queue at once.
func (w *Worker) Run(ctx context.Context, concurrency int) {
	if concurrency < 1 {
		concurrency = 1
	}
	done := make(chan struct{}, concurrency+1)
	go func() {
		w.scheduleLoop(ctx)
		done <- struct{}{}
	}()
	for i := 0; i < concurrency; i++ {
		go func() {
			w.loop(ctx)
			done <- struct{}{}
		}()
	}
	for i := 0; i < concurrency+1; i++ {
		<-done
	}
}

// scheduleInterval is how often a worker checks the schedules;
// scheduleCatchUp is how late a due run may still be created (every
// worker down for longer skips the run rather than replaying it).
const (
	scheduleInterval = 15 * time.Second
	scheduleCatchUp  = 5 * time.Minute
)

func (w *Worker) scheduleLoop(ctx context.Context) {
	for {
		w.EnqueueDue(ctx)
		w.PurgeDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(scheduleInterval):
		}
	}
}

// purgeInterval is how often a worker looks for unverified accounts to purge.
const purgeInterval = 10 * time.Minute

// PurgeDue deletes the accounts that never verified their address in the
// realms that ask for it (account.purge_unverified_after). Running it in
// every worker is harmless: deleting an account twice is not an error.
func (w *Worker) PurgeDue(ctx context.Context) {
	now := w.fns.docs.now()
	for realm, rl := range w.reg.Realms {
		if rl.Settings.Account.PurgeUnverifiedAfter <= 0 || now.Sub(w.lastPurge[realm]) < purgeInterval {
			continue
		}
		svc := w.fns.docs.users(realm)
		if svc == nil {
			continue
		}
		w.lastPurge[realm] = now
		if _, err := svc.PurgeUnverified(auth.WithAuditSource(ctx, func() auth.AuditSource { return auth.AuditSource{Actor: auth.ActorSystem} })); err != nil {
			w.log.Error("purge unverified accounts", "realm", realm, "error", err)
		}
	}
}

// EnqueueDue creates the job of every scheduled function whose latest
// scheduled time is recent enough and has no job yet (roadmap F18).
// Every worker runs it; the run's deterministic id makes the insert
// succeed for exactly one of them.
func (w *Worker) EnqueueDue(ctx context.Context) {
	now := w.fns.docs.now()
	for _, realm := range w.reg.FunctionRealms(true) {
		svc := w.fns.docs.users(realm)
		if svc == nil {
			continue
		}
		var states map[string]auth.ScheduleState // read once a tick, and only if something is scheduled
		for dbName, db := range w.reg.Realms[realm].Databases {
			if db.Functions == nil {
				continue
			}
			for name, fn := range db.Functions.Functions {
				if fn.Schedule == nil {
					continue
				}
				at, ok := fn.Schedule.Prev(now)
				if !ok || now.Sub(at) > scheduleCatchUp {
					continue
				}
				key := realm + "/" + dbName + "/" + name
				if w.lastScheduled[key].Equal(at) {
					continue
				}
				if states == nil {
					var err error
					if states, err = svc.ScheduleStates(ctx); err != nil {
						w.log.Error("read schedule states", "realm", realm, "error", err)
						break // try again next tick rather than run what may be paused
					}
				}
				// A paused function gets no run, and a run due while it was paused
				// is not made up when it is resumed.
				if st, ok := states[dbName+"/"+name]; ok && (st.Paused || at.Before(st.ChangedAt.Truncate(time.Minute))) {
					w.lastScheduled[key] = at
					continue
				}
				job := auth.Job{Database: dbName, Function: name, TimeoutMS: fn.Timeout.Milliseconds(), RequestID: "cron"}
				queued, created, err := svc.EnqueueScheduledJob(ctx, job, at, fn.Overlap == registry.OverlapSkip)
				if err != nil {
					w.log.Error("enqueue scheduled job", "function", key, "error", err)
					continue
				}
				w.lastScheduled[key] = at
				switch {
				case created && queued.Result != nil:
					w.log.Info("scheduled run skipped, the previous one hasn't finished", "function", key, "scheduled_at", at.Format(time.RFC3339), "job_id", queued.ID)
				case created:
					w.log.Info("scheduled job queued", "function", key, "scheduled_at", at.Format(time.RFC3339))
				}
			}
		}
	}
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		ran := w.RunOnce(ctx)
		if !ran {
			select {
			case <-ctx.Done():
				return
			case <-time.After(workerPollInterval):
			}
		}
	}
}

// RunOnce claims and runs at most one job, across every realm with
// functions. It returns whether it found one — callers use that to
// decide whether to poll again immediately or back off.
func (w *Worker) RunOnce(ctx context.Context) bool {
	// Every realm with users: erase jobs need no functions.
	for _, realm := range w.reg.AuthRealms() {
		svc := w.fns.docs.users(realm)
		if svc == nil {
			continue
		}
		job, found, err := svc.ClaimJob(ctx, w.id)
		if err != nil {
			w.log.Error("claim job", "realm", realm, "error", err)
			continue
		}
		if !found {
			continue
		}
		if job.Attempts > job.Failures+1 {
			w.fns.metrics.JobLeaseExpired(realm, jobKind(job)) // claimed again without a failure: its worker went away
		}
		w.runJob(ctx, realm, svc, job)
		return true
	}
	return false
}

// runJob runs one claimed job to its end and stores its result. It
// never marks a job done for a reason that should be retried (the
// executor unreachable, or busy): the lease simply expires, and this or
// another worker claims it again later, per the roadmap's at-least-once
// semantics.
func (w *Worker) runJob(ctx context.Context, realm string, svc *auth.Users, job auth.Job) {
	log := w.log.With("job_id", job.ID, "function", realm+"/"+job.Database+"/"+job.Function)
	if job.Erase != nil {
		w.runErase(ctx, log, realm, svc, job) // not a function: a worker applies the policies itself
		return
	}
	fn := w.fns.lookup(realm, job.Database, job.Function)
	if fn == nil {
		w.fail(ctx, log, svc, job, "the function no longer exists")
		return
	}
	caller := w.callerFor(ctx, realm, svc, job)
	var mask []string
	if job.Email != nil {
		// An email job holds no message: the worker makes it now, in memory.
		var ok bool
		if job.Input, mask, ok = w.prepareEmail(ctx, log, realm, svc, job); !ok {
			return
		}
		caller = auth.Caller{} // backd's own request: no user, ctx.db anonymous
	}
	w.execute(ctx, log, realm, svc, job, fn, caller, mask)
}

// execute runs the function for a claimed job (its input already in place)
// and records how it went: done, or queued again for a retry. mask lists
// values that must not show in the function's logs.
func (w *Worker) execute(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job, fn *registry.Function, caller auth.Caller, mask []string) {
	var secrets map[string]string
	if len(fn.Secrets) > 0 {
		values, missing, err := svc.ResolveSecrets(ctx, fn.Secrets, job.Database)
		if err != nil {
			log.Error("resolve secrets", "error", err)
			w.fail(ctx, log, svc, job, "could not read the function's secrets")
			return
		}
		if len(missing) > 0 {
			w.fail(ctx, log, svc, job, "a secret the function declares is not set")
			return
		}
		secrets = values
	}
	hash, built := w.fns.resolveHash(realm + "/" + job.Database + "/" + job.Function)
	if !built || w.fns.runner == nil {
		// Retryable: the executor or a fresh build may show up later.
		log.Warn("job claimed but functions aren't available; leaving it to be retried")
		return
	}
	deadline := w.fns.docs.now().Add(fn.Timeout)
	invID := xid.New().String()
	meta := callMeta{Origin: job.Origin, ParentID: job.ParentID, Depth: job.Depth}
	if job.Scheduled {
		meta.Origin = originCron
	}
	if meta.Origin == "" {
		meta.Origin = originHTTP // queued before origins were recorded
	}
	req := executor.InvokeRequest{
		Function:  realm + "/" + job.Database + "/" + job.Function,
		Bundle:    executor.Bundle{SHA256: hash, URL: strings.TrimRight(w.fns.callbackURL, "/") + "/_internal/functions/" + hash},
		TimeoutMS: fn.Timeout.Milliseconds(),
		MemoryMB:  (fn.Memory + 1<<20 - 1) >> 20,
		MaxOutput: fn.MaxOutput,
		Network:   fn.Network,
		Envelope: executor.Envelope{
			Mode:      fn.Mode,
			Input:     job.Input,
			User:      userEnvelope(caller),
			Secrets:   secrets,
			Mask:      mask,
			Callback:  w.fns.callback(realm, job.Database, job.Function, fn, caller, deadline.Add(callbackMargin), invID, job.Depth, &job),
			RequestID: job.RequestID,
		},
	}
	rctx, cancel := context.WithTimeout(ctx, fn.Timeout+10*time.Second)
	defer cancel()
	cancelled := w.watchCancellation(rctx, svc, job.ID, cancel)
	started := time.Now()
	res, err := w.fns.runner.Invoke(rctx, req)
	if cancelled.Load() {
		// An administrator cancelled the job: it is done already, with its
		// result. The run was stopped (the executor kills the process when the
		// request ends); only the attempt is recorded.
		log.Info("the job was cancelled while it ran; its run was stopped")
		cres := w.fns.settleSteps(ctx, svc, &job, false, executor.Result{Status: executor.StatusCancelled, DurationMS: time.Since(started).Milliseconds()})
		w.fns.recordInvocation(ctx, job.RequestID, realm, job.Database, job.Function, registry.ModeAsync, caller, cres, job.ID, invID, meta)
		return
	}
	w.fns.logResult(ctx, req.Function, res, err)
	if err != nil {
		log.Warn("executor unreachable; leaving the job to be retried", "error", err)
		return
	}
	if res.Status == executor.StatusBusy {
		log.Warn("executor busy; leaving the job to be retried")
		return
	}
	res = w.fns.settleSteps(ctx, svc, &job, true, res)
	w.fns.recordInvocation(ctx, job.RequestID, realm, job.Database, job.Function, registry.ModeAsync, caller, res, job.ID, invID, meta)
	// A failure that may pass (the function threw, timed out or crashed) is
	// tried again after a growing wait while attempts are left; one the
	// function chose (ctx.error) or that would only repeat (an output over
	// its limit) ends the job.
	if rp := fn.Retry; rp != nil && retryableStatus(res.Status) {
		if failed := job.Failures + 1; failed < rp.Attempts {
			wait := rp.Wait(failed)
			log.Warn("attempt failed; it will be tried again", "attempt", failed, "of", rp.Attempts, "status", res.Status, "retry_in", wait.String())
			if err := svc.RetryJob(ctx, job.ID, wait); err != nil {
				log.Error("queue the retry", "error", err)
			} else {
				w.fns.metrics.JobRetried(svc.Realm, jobKind(job))
			}
			return
		}
		log.Warn("attempts used up; the job fails", "attempts", rp.Attempts, "status", res.Status)
	}
	result := jobResultFromExecutor(res)
	if err := svc.CompleteJob(ctx, job.ID, result); err != nil {
		log.Error("complete job", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, jobKind(job), res.Status)
	w.fns.notifyCompletion(ctx, log, svc, job, result)
}

// cancelPollInterval is how often a worker looks whether the job it is running
// was cancelled meanwhile.
const cancelPollInterval = 2 * time.Second

// watchCancellation stops a run when its job is found done: an administrator
// cancelled it (nothing else finishes a job this worker is running). It calls
// cancel, which ends the executor request and so the process, and reports that
// it did. It stops when ctx ends, which is when the run does.
func (w *Worker) watchCancellation(ctx context.Context, svc *auth.Users, jobID string, cancel context.CancelFunc) *atomic.Bool {
	var cancelled atomic.Bool
	interval := w.cancelPoll
	if interval <= 0 {
		interval = cancelPollInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				j, found, err := svc.Store.GetJob(ctx, jobID)
				if err == nil && (!found || j.Status == auth.JobDone) {
					cancelled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return &cancelled
}

// jobKind is the job's kind as the metrics name it.
func jobKind(j auth.Job) string { return metrics.JobKind(j.Email != nil, j.Erase != nil, j.Scheduled) }

// retryableStatus says whether an attempt that ended this way may succeed
// when repeated: the function threw, ran out of time or resources, or its
// process died. A function's own ctx.error is an answer, not a fault, and an
// output over its limit would only be over it again.
func retryableStatus(status string) bool {
	switch status {
	case executor.StatusError, executor.StatusTimeout, executor.StatusMemory, executor.StatusCPU, executor.StatusCrash, executor.StatusBundle:
		return true
	}
	return false
}

// fail completes a job with an internal error result: not something the
// executor produced, but a condition that will never resolve by
// retrying (a removed function, a secret that will never be set).
func (w *Worker) fail(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, message string) {
	log.Error(message)
	res := auth.JobResult{Status: executor.StatusError, Message: message}
	if err := svc.CompleteJob(ctx, job.ID, res); err != nil {
		log.Error("complete failed job", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, jobKind(job), res.Status)
	w.fns.notifyCompletion(ctx, log, svc, job, res)
}

// callerFor reconstructs the caller that enqueued job, fetching the
// user or key fresh — never cached from enqueue time, the same
// freshness principle secrets already follow for async calls.
func (w *Worker) callerFor(ctx context.Context, realm string, svc *auth.Users, job auth.Job) auth.Caller {
	var c auth.Caller
	if job.Scheduled || job.ActsAsFunction {
		return auth.Caller{Func: &auth.FuncCaller{Name: realm + "/" + job.Database + "/" + job.Function, Admin: true}}
	}
	if job.CallerUserID != "" {
		if u, err := svc.UserByID(ctx, job.CallerUserID); err == nil {
			c.User = &auth.Principal{User: u}
		}
	}
	if job.CallerKeyHash != "" {
		if k, err := svc.Store.APIKeyByHash(ctx, job.CallerKeyHash); err == nil {
			c.Key = &k
		}
	}
	return c
}

// jobResultFromExecutor flattens an executor.Result into auth.JobResult,
// the same conversion recordInvocation's caller already does for the
// invocation history.
func jobResultFromExecutor(res executor.Result) auth.JobResult {
	r := auth.JobResult{Status: res.Status, Output: res.Output, DurationMS: res.DurationMS}
	if res.Status == executor.StatusFunctionError && res.FunctionError != nil {
		r.Code = res.FunctionError.Code
		r.Message = res.FunctionError.Message
		r.Details = res.FunctionError.Details
		r.HTTPStatus = res.FunctionError.Status
	}
	return r
}

// executorResultFromJobResult is jobResultFromExecutor's reverse, used
// to replay a stored idempotent outcome (roadmap F12) through the same
// respond() a fresh sync call would go through.
func executorResultFromJobResult(r *auth.JobResult) executor.Result {
	if r == nil {
		return executor.Result{Status: executor.StatusError, Message: "idempotency record has no stored result"}
	}
	res := executor.Result{Status: r.Status, Output: r.Output, DurationMS: r.DurationMS}
	if r.Status == executor.StatusFunctionError {
		res.FunctionError = &executor.FunctionError{Status: r.HTTPStatus, Code: r.Code, Message: r.Message, Details: r.Details}
	}
	return res
}
