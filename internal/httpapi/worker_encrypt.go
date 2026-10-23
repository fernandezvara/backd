package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	// encryptAttempts is how many times a migration is tried before it ends as failed; it goes
	// on from the place it reached each time.
	encryptAttempts = 5
	// encryptRate is how many documents a second a migration reads and rewrites at most, so a
	// large collection is migrated without starving the requests that use it.
	encryptRate = 1000
)

// runEncrypt brings a collection's stored documents in line with its encryption configuration,
// in id order and in batches: plaintext values are sealed, values under an old key or generation
// sealed again, blind indexes built, values of fields that are no longer encrypted opened. The
// place it reached is stored after every batch, so a worker that dies, a cancelled job started
// again or a lost lease go on from there; a document is only rewritten when it differs, so
// repeating a batch changes nothing. At the end it retires the keys nothing uses.
func (w *Worker) runEncrypt(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	ej := *job.Encrypt
	c, ok := w.fns.docs.reg.Collection(realm, ej.Database, ej.Collection)
	if !ok {
		w.fail(ctx, log, svc, job, "the collection no longer exists")
		return
	}
	repo := w.fns.docs.store.Repository(c)
	m, ok := repo.(storage.Migrator)
	if !ok {
		w.fail(ctx, log, svc, job, "this storage can't migrate encrypted fields")
		return
	}
	if err := m.Prepare(ctx); err != nil {
		w.retryEncrypt(ctx, log, svc, job, err, nil)
		return
	}
	var total float64
	if sc, ok := repo.(interface {
		EstimatedCount(context.Context) (int64, error)
	}); ok {
		n, _ := sc.EstimatedCount(ctx)
		total = float64(n)
	}
	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	title := "Migrate " + ej.Database + "/" + ej.Collection
	if ej.DryRun {
		title = "Check what a migration of " + ej.Database + "/" + ej.Collection + " would change"
	}
	tr.begin(ctx, title, total, "")
	counts, after := ej.EncryptCounts, ej.After
	opts := storage.MigrateOptions{Fields: ej.Fields, DryRun: ej.DryRun}
	for {
		started := time.Now()
		res, err := m.MigrateBatch(ctx, after, auth.EncryptBatch, opts)
		if err != nil {
			counts = addEncrypt(counts, res.Counts)
			w.retryEncrypt(ctx, log, svc, job, err, tr)
			return
		}
		counts = addEncrypt(counts, res.Counts)
		done := res.Counts.Scanned < auth.EncryptBatch
		if res.Last != "" {
			after = res.Last
		}
		if err := svc.Store.SetEncryptProgress(ctx, job.ID, after, counts); err != nil {
			w.retryEncrypt(ctx, log, svc, job, err, tr)
			return
		}
		tr.progress(ctx, float64(counts.Scanned), encryptSummary(counts, ej.DryRun))
		if done {
			break
		}
		// The job is still ours? A cancelled job is done, and a lost lease is another worker's.
		if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, auth.EncryptLease); err != nil {
			w.retryEncrypt(ctx, log, svc, job, err, tr)
			return
		} else if !held {
			log.Info("the encryption migration was cancelled or taken over; stopping")
			return
		}
		// At most encryptRate documents a second.
		if pause := time.Duration(res.Counts.Scanned)*time.Second/encryptRate - time.Since(started); pause > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
	}
	var retired []string
	if !ej.DryRun && len(ej.Fields) == 0 && counts.Conflicts == 0 {
		var err error
		if retired, err = m.Retire(ctx); err != nil {
			w.retryEncrypt(ctx, log, svc, job, err, tr)
			return
		}
	}
	tr.progress(ctx, float64(counts.Scanned), encryptSummary(counts, ej.DryRun))
	tr.finish(ctx, auth.StepDone)
	result := map[string]any{"counts": counts, "dry_run": ej.DryRun, "retired_keys": len(retired)}
	output, _ := json.Marshal(result)
	if !ej.DryRun {
		svc.AuditAs(ctx, auth.ActorSystem, auth.AuditEncryptMigrate, "doc:"+ej.Database+"/"+ej.Collection, map[string]any{
			"job_id": job.ID, "scanned": counts.Scanned, "encrypted": counts.Encrypted, "reencrypted": counts.Reencrypted,
			"decrypted": counts.Decrypted, "indexed": counts.Indexed, "retired_keys": len(retired),
		})
	}
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: output}); err != nil {
		log.Error("complete the encryption migration", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "encrypt", executor.StatusOK)
	log.Info("encryption migration finished", "collection", ej.Database+"/"+ej.Collection, "dry_run", ej.DryRun, "scanned", counts.Scanned, "retired_keys", len(retired))
}

func addEncrypt(a auth.EncryptCounts, b storage.MigrateCounts) auth.EncryptCounts {
	return auth.EncryptCounts{
		Scanned: a.Scanned + b.Scanned, Encrypted: a.Encrypted + b.Encrypted, Reencrypted: a.Reencrypted + b.Reencrypted, Decrypted: a.Decrypted + b.Decrypted,
		Indexed: a.Indexed + b.Indexed, Marked: a.Marked + b.Marked, Conflicts: a.Conflicts + b.Conflicts,
	}
}

func encryptSummary(c auth.EncryptCounts, dry bool) string {
	verb := ""
	if dry {
		verb = "would be "
	}
	return fmt.Sprintf("%d documents read; values %sencrypted: %d, sealed again: %d, opened: %d; indexes %sbuilt: %d", c.Scanned, verb, c.Encrypted, c.Reencrypted, c.Decrypted, verb, c.Indexed)
}

// retryEncrypt tries the migration again after a growing wait while attempts are left; then the
// job ends as failed. What was migrated stays migrated, so a new run only does what is left.
func (w *Worker) retryEncrypt(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, err error, tr *stepTracker) {
	if tr != nil {
		tr.finish(ctx, auth.StepFailed)
	}
	if failed := job.Failures + 1; failed < encryptAttempts {
		wait := min(30*time.Second<<min(failed-1, 5), 15*time.Minute)
		log.Warn("the encryption migration failed; it will be tried again", "attempt", failed, "of", encryptAttempts, "retry_in", wait.String(), "error", err)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "encrypt")
		}
		return
	}
	log.Error("the encryption migration failed for good", "error", err)
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed encryption migration", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "encrypt", executor.StatusError)
}
