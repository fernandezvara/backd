package auth

import (
	"context"
	"errors"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/email"
)

// PasswordNoticeOrigin is the origin of the job that tells the users with an expired
// password to choose a new one (design/breach-response.md §4); PasswordNoticeLock lets only
// one run at a time.
const (
	PasswordNoticeOrigin = "backd:password.notify"
	PasswordNoticeLock   = "password-notice"
	// PasswordNoticeLease is how long a worker holds the job before another may take it; the
	// worker renews it after every batch.
	PasswordNoticeLease = 10 * time.Minute
	// PasswordNoticeBatch is how many users a batch looks at.
	PasswordNoticeBatch = 100
)

// ErrNoPasswordEpoch means no password was ever expired for the whole realm: there is nobody
// to notify.
var ErrNoPasswordEpoch = errors.New("no password epoch is set: expire the passwords first")

// PasswordNoticeJob is what the notification job carries: the epoch it notifies for, the
// operator's reason, the id it has reached and what it did so far.
type PasswordNoticeJob struct {
	Epoch  time.Time
	Reason string
	After  string
	PasswordNoticeCounts
}

// PasswordNoticeCounts is what a notification run did.
type PasswordNoticeCounts struct {
	// Queued emails; Skipped users it didn't write to (disabled, erased, unverified address,
	// already told, or a new password since); Deferred users the email limits held back.
	Queued, Skipped, Deferred int64
}

// ErrPasswordNoticeRunning means a notification run is queued or running; JobID is its job.
type ErrPasswordNoticeRunning struct{ JobID string }

func (e *ErrPasswordNoticeRunning) Error() string {
	return "a notification of the users is already running: " + e.JobID
}

// StartPasswordNotice queues the job that emails every user whose password the realm's epoch
// expired, a reset link each. It is safe to start again after a cancel or a failure: users
// already told are skipped. ErrEmailNotConfigured in a realm without email,
// ErrNoPasswordEpoch before any expiry, and *ErrPasswordNoticeRunning when one is running.
func (s *Users) StartPasswordNotice(ctx context.Context, requestedBy, requestID string) (Job, error) {
	if s.Settings.Email == nil {
		return Job{}, ErrEmailNotConfigured
	}
	epoch, err := s.Store.PasswordEpoch(ctx)
	if err != nil {
		return Job{}, err
	}
	if epoch.ValidAfter.IsZero() {
		return Job{}, ErrNoPasswordEpoch
	}
	now := s.now()
	j := Job{
		ID: xid.New().String(), CallerActor: requestedBy, Origin: PasswordNoticeOrigin, Exclusive: PasswordNoticeLock,
		TimeoutMS: PasswordNoticeLease.Milliseconds(), RequestID: requestID, Status: JobQueued, CreatedAt: now, ExpiresAt: now.Add(pendingJobTTL),
		PasswordNotice: &PasswordNoticeJob{Epoch: epoch.ValidAfter, Reason: epoch.Reason},
	}
	err = s.Store.EnqueueJob(ctx, j)
	if errors.Is(err, ErrJobExclusive) {
		running := ""
		for _, status := range []string{JobRunning, JobQueued} {
			if jobs, _, lerr := s.Store.ListJobs(ctx, JobFilter{Origin: PasswordNoticeOrigin, Status: status, Limit: 1}); lerr == nil && len(jobs) > 0 {
				running = jobs[0].ID
				break
			}
		}
		return Job{}, &ErrPasswordNoticeRunning{JobID: running}
	}
	if err != nil {
		return Job{}, err
	}
	return j, nil
}

// NotifyPasswordBatch looks at the next users of a notification run, in id order, and writes
// to those who are due: it returns the id it reached ("" when the users are done) and what it
// did. A user is skipped when disabled, erased, without a verified address, or told after the
// epoch already; one the email limits hold back is counted as deferred and left for a later
// pass. The epoch's expiry is what selects the users, so an account that chose a new password
// since drops out by itself.
func (s *Users) NotifyPasswordBatch(ctx context.Context, pj PasswordNoticeJob) (after string, c PasswordNoticeCounts, retryAfter time.Duration, err error) {
	ids, err := s.Store.PasswordUserIDs(ctx, pj.Epoch, pj.After, PasswordNoticeBatch)
	if err != nil || len(ids) == 0 {
		return "", c, 0, err
	}
	users, err := s.Store.UsersByIDs(ctx, ids)
	if err != nil {
		return "", c, 0, err
	}
	byID := make(map[string]User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	for _, id := range ids {
		after = id
		u, ok := byID[id]
		if !ok || u.Disabled || !u.ErasedAt.IsZero() || !u.EmailVerified || (!u.PasswordNotifiedAt.IsZero() && !u.PasswordNotifiedAt.Before(pj.Epoch)) {
			c.Skipped++
			continue
		}
		_, qerr := s.QueueEmail(ctx, EmailRequest{Kind: email.PasswordExpired, UserID: u.ID, Address: u.Email, Locale: s.LocaleOf(u), Event: pj.Reason})
		var limited *EmailLimitedError
		switch {
		case errors.As(qerr, &limited):
			c.Deferred++
			retryAfter = max(retryAfter, limited.RetryAfter)
			continue
		case qerr != nil:
			return "", c, 0, qerr
		}
		now := s.now()
		if err := s.Store.UpdateUser(ctx, u.ID, UserUpdate{PasswordNotifiedAt: &now}, now); err != nil {
			return "", c, 0, err
		}
		c.Queued++
	}
	return after, c, retryAfter, nil
}
