package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
)

const (
	// passwordNoticeAttempts bounds the retries of a run, whether it failed or the email
	// limits held users back.
	passwordNoticeAttempts = 24
	// The wait before a run goes over the users the limits held back.
	minNoticeWait = time.Minute
	maxNoticeWait = time.Hour
)

// runPasswordNotice tells the users whose password an epoch expired to choose a new one: it
// walks the users with a password in id order, queues one password-expired email each
// through the normal email queue (so the email limits apply), and marks each user told, so a
// run that is repeated, cancelled and started again, or taken over by another worker, never
// sends twice. Users the limits hold back are left for another pass after a wait, not failed.
// It reports a step with the number of accounts, renews its lease after every batch and stops
// when the job was cancelled.
func (w *Worker) runPasswordNotice(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	if svc.Settings.Email == nil {
		w.fail(ctx, log, svc, job, "the realm no longer sends email")
		return
	}
	pn := *job.PasswordNotice
	total, err := svc.Store.CountPasswordIdentities(ctx)
	if err != nil {
		w.retryPasswordNotice(ctx, log, svc, job, err, nil)
		return
	}
	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	tr.begin(ctx, "Tell the users", float64(total), "")
	counts := pn.PasswordNoticeCounts
	var wait time.Duration
	after := pn.After
	for {
		batch := pn
		batch.After = after
		next, c, retryAfter, err := svc.NotifyPasswordBatch(ctx, batch)
		if err != nil {
			counts.Queued += c.Queued
			w.retryPasswordNotice(ctx, log, svc, job, err, tr)
			return
		}
		counts.Queued += c.Queued
		counts.Skipped += c.Skipped
		counts.Deferred += c.Deferred
		wait = max(wait, retryAfter)
		if next == "" {
			break
		}
		after = next
		if err := svc.Store.SetPasswordNoticeProgress(ctx, job.ID, after, counts); err != nil {
			w.retryPasswordNotice(ctx, log, svc, job, err, tr)
			return
		}
		tr.progress(ctx, float64(counts.Queued+counts.Skipped+counts.Deferred), fmt.Sprintf("%d told, %d skipped, %d held back by the email limits", counts.Queued, counts.Skipped, counts.Deferred))
		// The job is still ours? A cancelled job is done, and a lost lease is another worker's.
		if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, auth.PasswordNoticeLease); err != nil {
			w.retryPasswordNotice(ctx, log, svc, job, err, tr)
			return
		} else if !held {
			log.Info("the password notification was cancelled or taken over; stopping")
			return
		}
	}
	if counts.Deferred > 0 && job.Failures+1 < passwordNoticeAttempts {
		// Whoever the limits held back is told after a wait; those told already are skipped.
		wait = min(max(wait, minNoticeWait), maxNoticeWait)
		log.Info("the email limits held users back; the notification goes on after a wait", "deferred", counts.Deferred, "wait", wait.String())
		tr.progress(ctx, float64(counts.Queued+counts.Skipped), fmt.Sprintf("%d told; %d held back by the email limits, going on in %s", counts.Queued, counts.Deferred, wait.Round(time.Second)))
		if err := svc.Store.SetPasswordNoticeProgress(ctx, job.ID, "", auth.PasswordNoticeCounts{Queued: counts.Queued}); err != nil {
			w.retryPasswordNotice(ctx, log, svc, job, err, tr)
			return
		}
		if err := svc.RetryJob(ctx, job.ID, wait); err != nil {
			log.Error("queue the next pass", "error", err)
		}
		return
	}
	tr.progress(ctx, float64(total), fmt.Sprintf("%d told, %d skipped, %d left to a later run", counts.Queued, counts.Skipped, counts.Deferred))
	tr.finish(ctx, auth.StepDone)
	output, _ := json.Marshal(map[string]int64{"queued": counts.Queued, "skipped": counts.Skipped, "deferred": counts.Deferred})
	svc.AuditAs(ctx, auth.ActorSystem, auth.AuditPasswordNotify, "realm", map[string]any{"job_id": job.ID, "queued": counts.Queued, "skipped": counts.Skipped, "deferred": counts.Deferred})
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: output}); err != nil {
		log.Error("complete the password notification", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "password_notice", executor.StatusOK)
	log.Info("users told to choose a new password", "queued", counts.Queued, "skipped", counts.Skipped, "deferred", counts.Deferred)
}

// retryPasswordNotice tries the run again after a growing wait while attempts are left; then
// the job ends as failed. Users already told stay told, so a new run goes on from there.
func (w *Worker) retryPasswordNotice(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, err error, tr *stepTracker) {
	if tr != nil {
		tr.finish(ctx, auth.StepFailed)
	}
	if failed := job.Failures + 1; failed < passwordNoticeAttempts {
		wait := min(30*time.Second<<min(failed-1, 6), 30*time.Minute)
		log.Warn("telling the users failed; it will be tried again", "attempt", failed, "of", passwordNoticeAttempts, "retry_in", wait.String(), "error", err)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "password_notice")
		}
		return
	}
	log.Error("telling the users failed for good", "error", err)
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed password notification", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "password_notice", executor.StatusError)
}
