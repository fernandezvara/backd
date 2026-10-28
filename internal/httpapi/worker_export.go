package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	// exportAttempts is how many times an export is tried before it ends as failed.
	exportAttempts = 3
	// exportRenewEvery is how often the building of an export renews the lease of its job.
	exportRenewEvery = time.Minute
	// exportSweepBatch is how many expired packages one sweep hands to the file deletion queue.
	exportSweepBatch = 50
)

// exportObjectKey is where a user's package is stored: under the realm's prefix, in a folder
// (`_exports`) that the storage reconciliation leaves alone.
func exportObjectKey(prefix, realm, userID, exportID string) string {
	return storage.ObjectKey(prefix, realm, "_exports", userID, exportID+".zip")
}

// runExport builds a user's personal data export and stores it: the package is streamed to the
// realm's file storage as it is written, so nothing of it waits in memory. The record of the
// export follows the job (running, ready or failed); a user who asked for their own is emailed
// when it is ready, and an administrator's is theirs to download.
func (w *Worker) runExport(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	ej := job.Export
	rec, err := svc.Store.UserExportByID(ctx, ej.ExportID)
	if err != nil {
		w.fail(ctx, log, svc, job, "the export's record is gone")
		return
	}
	if rec.Status == auth.ExportReady {
		// A retry after the work was done: only the job is left to close.
		w.completeExport(ctx, log, svc, job, rec)
		return
	}
	rl := w.reg.Realms[realm]
	if rl == nil || !rl.Settings.Compliance.Export || rl.Settings.Storage == nil {
		w.exportFailedForGood(ctx, log, svc, job, rec, errors.New("this realm no longer exports its users' data (compliance.export)"))
		return
	}
	d := w.fns.docs
	obj, err := d.objects.For(ctx, realm, false)
	if err != nil {
		w.retryExport(ctx, log, svc, job, rec, nil, err)
		return
	}
	now := svc.Clock()
	rec.Status, rec.StartedAt, rec.Error = auth.ExportRunning, now, ""
	rec.Key = exportObjectKey(rl.Settings.Storage.Prefix, realm, rec.UserID, rec.ID)
	if err := svc.Store.ReplaceUserExport(ctx, rec); err != nil {
		w.retryExport(ctx, log, svc, job, rec, nil, err)
		return
	}

	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	build, cancel := context.WithCancel(ctx)
	defer cancel()
	lastRenew := time.Now()
	held := true
	progress := func(step string) {
		tr.begin(build, "Export: "+step, 0, "")
		if time.Since(lastRenew) < exportRenewEvery {
			return
		}
		lastRenew = time.Now()
		// A cancelled job is done, and a lost lease is another worker's: stop building.
		if ok, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, auth.ExportLease); err == nil && !ok {
			held = false
			cancel()
		}
	}
	pr, pw := io.Pipe()
	type built struct {
		report *personalExportReport
		err    error
	}
	done := make(chan built, 1)
	go func() {
		report, err := d.buildPersonalExport(build, personalExportParams{
			Realm: realm, Svc: svc, UserID: rec.UserID, Operator: exportOperator(rec),
			Retention: rl.Settings.Compliance.ExportKeep(), Objects: obj, Version: w.version, Fingerprint: w.fingerprint,
			Progress: progress,
		}, pw)
		_ = pw.CloseWithError(err) // ends the upload: cleanly after the last byte, or with the error
		done <- built{report, err}
	}()
	putErr := obj.PutStream(build, rec.Key, pr, "application/zip")
	_ = pr.CloseWithError(putErr) // an upload that stopped early must not leave the builder writing
	res := <-done
	if !held {
		log.Info("the export was cancelled or taken over; stopping")
		_ = svc.QueueFileDeletion(context.WithoutCancel(ctx), rec.Key, "export_abandoned")
		return
	}
	if err := errors.Join(res.err, putErr); err != nil {
		tr.finish(ctx, auth.StepFailed)
		_ = svc.QueueFileDeletion(context.WithoutCancel(ctx), rec.Key, "export_failed") // whatever was stored of it
		w.retryExport(ctx, log, svc, job, rec, tr, err)
		return
	}
	tr.finish(ctx, auth.StepDone)

	finished := svc.Clock()
	rec.Status, rec.FinishedAt, rec.ExpiresAt, rec.Size = auth.ExportReady, finished, finished.Add(rl.Settings.Compliance.ExportKeep()), res.report.Bytes
	rec.Counts = exportCounts(res.report)
	if err := svc.Store.ReplaceUserExport(ctx, rec); err != nil {
		w.retryExport(ctx, log, svc, job, rec, nil, err)
		return
	}
	svc.AuditAs(ctx, auth.ActorSystem, auth.AuditDataExported, "user:"+rec.UserID, exportAuditDetails(rec))
	if !rec.ByAdmin() {
		// The user asked for their own: tell them where to get it.
		if _, err := svc.QueueEmail(ctx, auth.EmailRequest{Kind: email.DataExportReady, UserID: rec.UserID, Locale: rec.Locale, Notice: true, ExportID: rec.ID, RequestID: job.RequestID}); err != nil {
			// The package is ready and listed; the user can still find it from the app.
			log.Warn("could not queue the email that says an export is ready", "export", rec.ID, "error", err)
		}
	}
	w.completeExport(ctx, log, svc, job, rec)
}

// exportOperator is who the manifest says made the export.
func exportOperator(rec auth.UserExport) string {
	if rec.ByAdmin() {
		return rec.Admin
	}
	return "user:" + rec.UserID
}

// exportCounts is what the record shows of the package before it is downloaded.
func exportCounts(r *personalExportReport) map[string]int64 {
	out := map[string]int64{"files": r.Files}
	for _, c := range r.Collections {
		out[c.Part] = c.Documents
	}
	for name, n := range r.Account {
		out[accountPartsDir+"/"+name] = int64(n)
	}
	return out
}

func exportAuditDetails(rec auth.UserExport) map[string]any {
	d := map[string]any{"export": rec.ID, "size": rec.Size, "by": "user"}
	if rec.ByAdmin() {
		d["by"] = rec.Admin
		if rec.Ticket != "" {
			d["ticket"] = rec.Ticket
		}
	}
	return d
}

func (w *Worker) completeExport(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, rec auth.UserExport) {
	output, _ := json.Marshal(map[string]any{"export": rec.ID, "size": rec.Size, "expires_at": rec.ExpiresAt})
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: output}); err != nil {
		log.Error("complete the export job", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "export", executor.StatusOK)
	log.Info("personal data export ready", "export", rec.ID, "user_id", rec.UserID, "bytes", rec.Size)
}

// retryExport tries the export again after a growing wait while attempts are left; then it ends
// as failed. Nothing that was stored of the failed attempt is kept.
func (w *Worker) retryExport(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, rec auth.UserExport, tr *stepTracker, err error) {
	if tr != nil {
		tr.finish(ctx, auth.StepFailed)
	}
	if failed := job.Failures + 1; failed < exportAttempts {
		wait := min(time.Minute<<min(failed-1, 4), 15*time.Minute)
		log.Warn("the export failed; it will be tried again", "attempt", failed, "of", exportAttempts, "retry_in", wait.String(), "error", err)
		rec.Status, rec.Error = auth.ExportQueued, err.Error()
		_ = svc.Store.ReplaceUserExport(ctx, rec)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "export")
		}
		return
	}
	w.exportFailedForGood(ctx, log, svc, job, rec, err)
}

func (w *Worker) exportFailedForGood(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, rec auth.UserExport, err error) {
	log.Error("the export failed for good", "export", rec.ID, "error", err)
	rec.Status, rec.Error, rec.FinishedAt, rec.Key = auth.ExportFailed, err.Error(), svc.Clock(), ""
	if rerr := svc.Store.ReplaceUserExport(ctx, rec); rerr != nil {
		log.Error("record the failed export", "error", rerr)
	}
	svc.AuditAs(ctx, auth.ActorSystem, auth.AuditDataExportFailed, "user:"+rec.UserID, map[string]any{"export": rec.ID, "error": err.Error()})
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed export job", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "export", executor.StatusError)
}

// ExportsDue deletes the packages whose retention ended, in the realms that export: it hands
// each object to the file deletion queue (which retries until it is gone) and marks the record
// expired. Running it in every worker is harmless.
func (w *Worker) ExportsDue(ctx context.Context) {
	now := w.fns.docs.now()
	for realm, rl := range w.reg.Realms {
		if !rl.Settings.Compliance.Export {
			continue
		}
		svc := w.fns.docs.users(realm)
		if svc == nil {
			continue
		}
		due, err := svc.Store.ListExpiredUserExports(ctx, now, exportSweepBatch)
		if err != nil {
			w.log.Error("find expired exports", "realm", realm, "error", err)
			continue
		}
		for _, rec := range due {
			if err := svc.QueueFileDeletion(ctx, rec.Key, "export_expired"); err != nil {
				w.log.Error("queue an expired export's package for deletion", "realm", realm, "export", rec.ID, "error", err)
				continue
			}
			rec.Status, rec.Key = auth.ExportExpired, ""
			if err := svc.Store.ReplaceUserExport(ctx, rec); err != nil {
				w.log.Error("mark an export expired", "realm", realm, "export", rec.ID, "error", err)
				continue
			}
			svc.AuditAs(ctx, auth.ActorSystem, auth.AuditDataExportExpired, "user:"+rec.UserID, map[string]any{"export": rec.ID})
		}
	}
}
