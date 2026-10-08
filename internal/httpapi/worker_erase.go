package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	// eraseBatch is how many documents one step of an erase touches.
	eraseBatch = 200
	// eraseAttempts is how many times an erase job is tried before it ends as
	// failed and needs an administrator (repeating the delete resumes it).
	eraseAttempts = 5
)

// runErase applies the policies of the realm's collections for an erase job:
// the documents the user owns (deleted or anonymized), and the references to
// them in every document (pulled from arrays, cleared from fields). It works
// in batches that only touch documents that still match, so a job that was
// interrupted, retried or taken over by another worker converges to the same
// result. Every batch's count is added to the job as it finishes; at the end the
// job forgets the email, records the counts in the audit trail and completes.
func (w *Worker) runErase(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	e := job.Erase
	if err := w.applyErasePolicies(ctx, realm, svc, job); err != nil {
		w.eraseFailed(ctx, log, svc, job, err)
		return
	}
	if err := svc.Store.ClearEraseEmail(ctx, job.ID); err != nil {
		w.eraseFailed(ctx, log, svc, job, err)
		return
	}
	done, _, err := svc.GetJob(ctx, job.ID)
	if err != nil {
		w.eraseFailed(ctx, log, svc, job, err)
		return
	}
	counts := map[string]int64{}
	if done.Erase != nil {
		counts = maps.Clone(done.Erase.Counts)
	}
	output, _ := json.Marshal(counts)
	svc.AuditAs(ctx, auth.ActorSystem, auth.AuditUserErased, "user:"+e.UserID, map[string]any{"job_id": job.ID, "counts": counts})
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: "ok", Output: output}); err != nil {
		log.Error("complete the erase job", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "erase", "ok")
	log.Info("user erased", "user_id", e.UserID)
}

func (w *Worker) applyErasePolicies(ctx context.Context, realm string, svc *auth.Users, job auth.Job) error {
	e := job.Erase
	rl := w.reg.Realms[realm]
	if rl == nil {
		return errors.New("the realm no longer exists")
	}
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		db := rl.Databases[dbName]
		for _, name := range slices.Sorted(maps.Keys(db.Collections)) {
			c := db.Collections[name]
			p := c.Erasure
			if p == nil {
				continue
			}
			er, ok := w.fns.docs.store.Repository(c).(storage.Eraser)
			if !ok {
				return fmt.Errorf("the storage of %s.%s can't erase", dbName, name)
			}
			now := svc.Clock()
			fileFields := sortedKeys(c.Files)
			// step repeats one operation until nothing matches any more, adding what each
			// batch did to the job's counts.
			step := func(op string, run func() (int64, error)) error {
				for {
					n, err := run()
					if err != nil {
						return fmt.Errorf("%s.%s: %w", dbName, name, err)
					}
					if n == 0 {
						return nil
					}
					if err := svc.Store.AddEraseCount(ctx, job.ID, dbName+"/"+name+"/"+op, n); err != nil {
						return err
					}
				}
			}
			switch p.Action {
			case registry.ErasureDelete:
				if err := step("deleted", func() (int64, error) {
					n, files, err := er.DeleteOwned(ctx, e.UserID, fileFields, eraseBatch)
					w.eraseFiles(ctx, svc, c, e.UserID, files)
					return n, err
				}); err != nil {
					return err
				}
			case registry.ErasureAnonymize:
				var removedFiles []string
				for _, f := range fileFields {
					if slices.Contains(p.Remove, f) {
						removedFiles = append(removedFiles, f)
					}
				}
				if err := step("anonymized", func() (int64, error) {
					n, files, err := er.AnonymizeOwned(ctx, e.UserID, p.Remove, p.Replace, removedFiles, eraseBatch, now)
					w.eraseFiles(ctx, svc, c, e.UserID, files)
					return n, err
				}); err != nil {
					return err
				}
			}
			for _, kind := range []struct {
				op     string
				fields map[string]string
				run    func(field, value string) (int64, error)
			}{
				{"pulled", p.Pull, func(f, v string) (int64, error) { return er.PullReference(ctx, f, v, eraseBatch, now) }},
				{"cleared", p.Unset, func(f, v string) (int64, error) { return er.ClearReference(ctx, f, v, eraseBatch, now) }},
			} {
				for _, field := range slices.Sorted(maps.Keys(kind.fields)) {
					value := e.UserID
					if kind.fields[field] == registry.MatchEmail {
						if value = e.Email; value == "" {
							return fmt.Errorf("%s.%s: %s is matched by email, which this job no longer holds", dbName, name, field)
						}
					}
					if err := step(kind.op, func() (int64, error) { return kind.run(field, value) }); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// eraseFailed tries the job again after a growing wait while attempts are
// left; then it ends as failed, visibly (the audit trail says it needs
// attention) and repeating the delete resumes it. The email stays in the job
// for that.
func (w *Worker) eraseFailed(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, err error) {
	if failed := job.Failures + 1; failed < eraseAttempts {
		wait := 30 * time.Second << (failed - 1)
		log.Warn("erase attempt failed; it will be tried again", "attempt", failed, "of", eraseAttempts, "retry_in", wait.String(), "error", err)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "erase")
		}
		return
	}
	log.Error("erase attempts used up; the job fails", "error", err)
	svc.AuditAs(ctx, auth.ActorSystem, auth.AuditUserErased, "user:"+job.Erase.UserID,
		map[string]any{"job_id": job.ID, "needs_attention": true, "error": err.Error()})
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed erase job", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "erase", executor.StatusError)
}

// eraseFiles queues the objects of the files an erase took out of documents, and stops
// counting them: `delete` takes every file field of the user's documents, `anonymize`
// the file fields it removes.
func (w *Worker) eraseFiles(ctx context.Context, svc *auth.Users, c *registry.Collection, userID string, files []storage.ErasedFile) {
	d := w.fns.docs
	for _, f := range files {
		if err := svc.QueueFileDeletion(ctx, d.objectKey(c, f.ID), "erased"); err != nil {
			w.log.Error("queue an erased file's object for deletion", "realm", svc.Realm, "file", f.ID, "error", err)
			continue
		}
		if err := svc.FileRemoved(ctx, userID, f.Size); err != nil {
			w.log.Error("count an erased file out of the storage usage", "realm", svc.Realm, "error", err)
		}
		for _, name := range slices.Sorted(maps.Keys(f.Versions)) {
			if err := svc.QueueFileDeletion(ctx, d.objectKey(c, f.ID)+"/"+name, "erased"); err != nil {
				w.log.Error("queue an erased file's version for deletion", "realm", svc.Realm, "file", f.ID, "version", name, "error", err)
				continue
			}
			if err := svc.FileRemoved(ctx, userID, f.Versions[name]); err != nil {
				w.log.Error("count an erased version out of the storage usage", "realm", svc.Realm, "error", err)
			}
		}
	}
}
