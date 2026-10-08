package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	// checkBatch is how many documents one read of a schema check takes.
	checkBatch = 500
	// checkAttempts is how many times a check is tried before it ends as failed.
	checkAttempts = 3
	// maxCheckText clips the path and the rule of a problem.
	maxCheckText = 200
)

// schemaHash identifies a collection's schema, for the report.
func schemaHash(c *registry.Collection) string {
	data, _ := json.Marshal(c.RawSchema) // map keys are sorted: the same schema, the same bytes
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// checkDocument validates a stored document the way the API validates a write
// (without its system fields) and returns its problems, at most
// auth.MaxCheckProblems of them, and how many more there were. Never a value.
func checkDocument(c *registry.Collection, doc storage.Document) (problems []auth.CheckProblem, more int) {
	body := timesToStrings(userFields(doc)).(map[string]any)
	err := c.Schema.Validate(body)
	if err == nil {
		return nil, 0
	}
	details := validationDetails(err)
	for i, d := range details {
		if i == auth.MaxCheckProblems {
			return problems, len(details) - i
		}
		problems = append(problems, auth.CheckProblem{Path: clip(d.Path, maxCheckText), Reason: clip(d.Reason, maxCheckText)})
	}
	return problems, 0
}

func isDeleted(doc storage.Document) bool {
	meta, _ := doc["_meta"].(map[string]any)
	_, ok := meta["deleted_at"]
	return ok
}

// stepTracker keeps the steps of a job a worker runs itself and stores them on it
// as they change, as a function's runner does for ctx.step.
type stepTracker struct {
	svc     *auth.Users
	jobID   string
	attempt int
	steps   []auth.Step
}

func (t *stepTracker) flush(ctx context.Context) {
	steps, omitted := t.steps, 0
	if len(steps) > auth.MaxSteps {
		omitted = len(steps) - auth.MaxSteps
	}
	// A failed write is only a stale progress bar.
	_, _ = t.svc.SetJobSteps(ctx, t.jobID, t.attempt, steps, omitted)
}

// begin starts a step, closing the one running as done.
func (t *stepTracker) begin(ctx context.Context, name string, total float64, message string) {
	now := t.svc.Clock()
	t.steps = auth.CloseSteps(t.steps, auth.StepDone, now)
	n := 1
	if len(t.steps) > 0 {
		n = t.steps[len(t.steps)-1].N + 1
	}
	t.steps = append(t.steps, auth.Step{N: n, Name: name, Status: auth.StepRunning, StartedAt: now, Total: &total, Message: message, UpdatedAt: now})
	t.flush(ctx)
}

// progress sets how far the current step got.
func (t *stepTracker) progress(ctx context.Context, current float64, message string) {
	if len(t.steps) == 0 {
		return
	}
	last := &t.steps[len(t.steps)-1]
	last.Current, last.Message, last.UpdatedAt = current, message, t.svc.Clock()
	t.flush(ctx)
}

// finish closes the step running with status.
func (t *stepTracker) finish(ctx context.Context, status string) {
	t.steps = auth.CloseSteps(t.steps, status, t.svc.Clock())
	t.flush(ctx)
}

// checkSummary is what a finished check's job keeps as its output: no documents.
type checkSummary struct {
	Collections []checkSummaryEntry `json:"collections"`
}

type checkSummaryEntry struct {
	Database   string `json:"database"`
	Collection string `json:"collection"`
	Scanned    int64  `json:"scanned"`
	Invalid    int64  `json:"invalid"`
	Complete   bool   `json:"complete"`
}

// runCheck runs a schema check job: it reads every document of the collections in
// batches, validates it against the collection's current schema and stores each
// collection's report when it is done. It reports one step per collection with
// a total of 100, advancing every 5 percent; renews its lease and looks whether
// the job was cancelled after every batch; and never writes a document.
func (w *Worker) runCheck(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	cj := job.Check
	deadline := svc.Clock().Add(auth.CheckMaxDuration)
	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	var summary checkSummary
	for _, key := range cj.Collections {
		database, name, _ := strings.Cut(key, "/")
		var c *registry.Collection
		if rl := w.reg.Realms[realm]; rl != nil && rl.Databases[database] != nil {
			c = rl.Databases[database].Collections[name]
		}
		if c == nil { // the config changed since the check started
			tr.begin(ctx, key, 100, "the collection no longer exists")
			tr.finish(ctx, auth.StepFailed)
			continue
		}
		report, ok, err := w.checkCollection(ctx, svc, job, tr, c, key, cj.Limit, deadline)
		if err != nil {
			w.checkFailed(ctx, log, svc, job, tr, err)
			return
		}
		if !ok { // cancelled, or another worker holds the job now: the job is no longer ours
			log.Info("the schema check was cancelled or taken over; stopping")
			return
		}
		if err := svc.SaveCheckReport(ctx, report); err != nil {
			w.checkFailed(ctx, log, svc, job, tr, err)
			return
		}
		summary.Collections = append(summary.Collections, checkSummaryEntry{Database: database, Collection: name, Scanned: report.Scanned, Invalid: report.Invalid, Complete: report.Complete})
	}
	tr.finish(ctx, auth.StepDone)
	output, _ := json.Marshal(summary)
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: output}); err != nil {
		log.Error("complete the schema check", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "check", executor.StatusOK)
	log.Info("schema check finished", "collections", len(summary.Collections))
}

// checkCollection scans one collection. ok is false when the job is no longer this
// worker's (cancelled, or its lease went to another).
func (w *Worker) checkCollection(ctx context.Context, svc *auth.Users, job auth.Job, tr *stepTracker, c *registry.Collection, key string, limit int, deadline time.Time) (report auth.CheckReport, ok bool, err error) {
	scanner, isScanner := w.fns.docs.store.Repository(c).(storage.Scanner)
	if !isScanner {
		return report, false, fmt.Errorf("the storage of %s can't be scanned", key)
	}
	estimate, err := scanner.EstimatedCount(ctx)
	if err != nil {
		return report, false, fmt.Errorf("%s: %w", key, err)
	}
	report = auth.CheckReport{
		Database: strings.SplitN(key, "/", 2)[0], Collection: c.Name, JobID: job.ID, StartedAt: svc.Clock(),
		Limit: limit, SchemaHash: schemaHash(c), Complete: true,
	}
	tr.begin(ctx, key, 100, fmt.Sprintf("scanning about %d documents", estimate))
	reported := 0 // the last percentage reported, a multiple of 5
	after := ""
scan:
	for {
		if svc.Clock().After(deadline) {
			report.Complete, report.StoppedBy = false, "time"
			break
		}
		docs, err := scanner.ScanAfter(ctx, after, checkBatch)
		if err != nil {
			return report, false, fmt.Errorf("%s: %w", key, err)
		}
		if len(docs) == 0 {
			break
		}
		for i, doc := range docs {
			report.Scanned++
			after, _ = doc["id"].(string)
			problems, more := checkDocument(c, doc)
			if len(problems) == 0 {
				continue
			}
			report.Invalid++
			report.Documents = append(report.Documents, auth.CheckedDocument{ID: after, Deleted: isDeleted(doc), Problems: problems, MoreProblems: more})
			if report.Invalid >= int64(limit) && (len(docs) >= checkBatch || i != len(docs)-1) {
				report.Complete, report.StoppedBy = false, "limit"
				break scan
			}
		}
		// The job is still ours? A cancelled job is done, and a lost lease is another worker's.
		if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, auth.CheckLease); err != nil {
			return report, false, err
		} else if !held {
			return report, false, nil
		}
		if pct, due := checkPercent(report.Scanned, estimate, reported); due {
			reported = pct
			tr.progress(ctx, float64(pct), progressMessage(report.Scanned, estimate))
		}
	}
	report.FinishedAt = svc.Clock()
	message := fmt.Sprintf("scanned %d documents, %d invalid", report.Scanned, report.Invalid)
	if !report.Complete {
		message += " (stopped: " + report.StoppedBy + ")"
	}
	tr.progress(ctx, 100, message)
	return report, true, nil
}

// checkPercent says whether a check that scanned documents of about estimate
// has a new percentage to report: every 5, from 5 to 95; the end reports 100.
// reported is the last one sent.
func checkPercent(scanned, estimate int64, reported int) (pct int, due bool) {
	if estimate <= 0 {
		return reported, false
	}
	pct = min(int(scanned*100/estimate)/5*5, 95)
	return pct, pct >= reported+5
}

func progressMessage(scanned, estimate int64) string {
	if scanned > estimate {
		return fmt.Sprintf("scanned %d documents", scanned)
	}
	return fmt.Sprintf("scanned %d of about %d documents", scanned, estimate)
}

// checkFailed tries a check again after a growing wait while attempts are left;
// then it ends as failed (the previous reports stay as they were).
func (w *Worker) checkFailed(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, tr *stepTracker, err error) {
	if failed := job.Failures + 1; failed < checkAttempts {
		wait := 30 * time.Second << (failed - 1)
		log.Warn("schema check attempt failed; it will be tried again", "attempt", failed, "of", checkAttempts, "retry_in", wait.String(), "error", err)
		tr.finish(ctx, auth.StepFailed)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "check")
		}
		return
	}
	log.Error("schema check attempts used up; the job fails", "error", err)
	tr.finish(ctx, auth.StepFailed)
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed schema check", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "check", executor.StatusError)
}
