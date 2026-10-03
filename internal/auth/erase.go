package auth

import (
	"context"
	"errors"
	"time"
)

// What an erase job is called in the job list and in the audit trail.
const (
	EraseOrigin   = "backd:account.erase"
	eraseDatabase = "_backd" // names can't start with _, so no function has it
	eraseFunction = "erase"
	// eraseLease is how long a worker holds an erase job before another may
	// take it over (the operations are safe to repeat, so a takeover only
	// costs duplicated work).
	eraseLease = 10 * time.Minute
)

var (
	// ErrAlreadyErased means the user was erased and nothing is left to do.
	ErrAlreadyErased = errors.New("this user was already erased")
	// ErrUserErased means an operation that needs a user was asked of an
	// erased one: all that is left is a tombstone.
	ErrUserErased = errors.New("this user was erased")
)

// ErasedEmail is the unique, undeliverable address a tombstone gets: the
// .invalid domain is reserved, so it can't receive mail, and the id makes it
// unique. The real address is freed for a new registration.
func ErasedEmail(id string) string { return "erased-" + id + "@erased.invalid" }

// DeactivateAccount handles a user deleting their own account: they must give
// their password, and the account is disabled and its sessions end. Nothing is
// erased: that is an administrator's action (Erase).
func (s *Users) DeactivateAccount(ctx context.Context, p Principal, password string) error {
	if err := s.checkPassword(ctx, p.User, password); err != nil {
		return err
	}
	disabled := true
	if err := s.Store.UpdateUser(ctx, p.User.ID, UserUpdate{Disabled: &disabled}, s.now()); err != nil {
		return err
	}
	if err := s.endSessions(ctx, p.User.ID, "deactivated"); err != nil {
		return err
	}
	s.Audit(ctx, AuditAccountDelete, userTarget(p.User.ID), nil)
	return nil
}

// EraseJobFor returns the latest erase job of a user.
func (s *Users) EraseJobFor(ctx context.Context, userID string) (Job, bool, error) {
	jobs, _, err := s.Store.ListJobs(ctx, JobFilter{Origin: EraseOrigin, EraseUser: userID, Limit: 1})
	if err != nil || len(jobs) == 0 {
		return Job{}, false, err
	}
	return jobs[0], true, nil
}

// Erase starts erasing a user, as an administrator: it queues the erase job
// (which holds the id and the email the policies match by), then turns the
// user into a tombstone and deletes their email tokens and email jobs at once.
// A worker applies the collections' policies. Repeating it for a user whose
// erase job failed queues that job again; for one whose job is still going it
// returns the job; otherwise it is ErrAlreadyErased.
func (s *Users) Erase(ctx context.Context, id, requestID string) (Job, error) {
	u, err := s.Store.UserByID(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !u.ErasedAt.IsZero() {
		job, found, err := s.EraseJobFor(ctx, id)
		switch {
		case err != nil:
			return Job{}, err
		case !found:
			return Job{}, ErrAlreadyErased
		case job.Status != JobDone:
			return job, nil
		case job.Result != nil && job.Result.Status != "ok":
			if reopened, err := s.Store.ReopenJob(ctx, job.ID, s.now().Add(pendingJobTTL)); err != nil {
				return Job{}, err
			} else if reopened {
				job.Status = JobQueued
				return job, nil
			}
		}
		return Job{}, ErrAlreadyErased
	}
	job, err := s.EnqueueJob(ctx, Job{
		Database: eraseDatabase, Function: eraseFunction, CallerActor: "admin", Origin: EraseOrigin,
		TimeoutMS: eraseLease.Milliseconds(), RequestID: requestID, Erase: &EraseJob{UserID: id, Email: u.Email},
	})
	if err != nil {
		return Job{}, err
	}
	s.countSessions(ctx, id, "erased")
	if err := s.Store.EraseUser(ctx, id, ErasedEmail(id), s.now()); err != nil {
		return Job{}, err
	}
	if err := s.Store.DeleteEmailTokensOfUser(ctx, id); err != nil {
		return Job{}, err
	}
	if err := s.Store.DeleteEmailJobsOfUser(ctx, id); err != nil {
		return Job{}, err
	}
	s.Audit(ctx, AuditUserDelete, userTarget(id), map[string]any{"job_id": job.ID})
	return job, nil
}
