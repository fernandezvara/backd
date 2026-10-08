package httpapi

import (
	"context"
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
	// regenerateBatch is how many documents one read of a regeneration takes.
	regenerateBatch = 100
	// regenerateLease is how long a worker holds a regeneration before another may take it;
	// it is renewed as the files go by.
	regenerateLease = 10 * time.Minute
	// regenerateAttempts is how many times a regeneration is tried before it ends as failed
	// (running it again resumes it: what is made already is skipped).
	regenerateAttempts = 3
	// DefaultRegenerateRate is how many files a second a regeneration makes when none is asked.
	DefaultRegenerateRate = 10
)

// staleTargets are the versions of a file that no longer match what the field declares and
// that a regeneration makes again: a declared version the file has no copy of, a copy made
// with other parameters (another fingerprint), and one that failed or never finished. Copies
// a function generated with its own parameters are left alone, and so are versions only
// functions make, files that aren't pictures, and pictures a version was skipped for.
// missingOnly keeps just the first kind.
func staleTargets(f *registry.FileField, only string, file map[string]any, missingOnly bool) []imageTarget {
	if ct, _ := file["type"].(string); !strings.HasPrefix(ct, "image/") {
		return nil
	}
	states := mapOf(file["versions"])
	var out []imageTarget
	for _, v := range f.Versions {
		if (only != "" && v.Name != only) || !v.HasParams() {
			continue
		}
		st := mapOf(states[v.Name])
		need := false
		switch {
		case st == nil:
			need = true
		case missingOnly || st["custom"] == true:
		case st["status"] == versionReady:
			need = st["fingerprint"] != v.Fingerprint()
		case st["status"] == versionFailed || st["status"] == versionPending:
			need = true
		}
		if need {
			out = append(out, imageTarget{name: v.Name, params: v.Params})
		}
	}
	return out
}

// runRegenerate makes again, for every file of a field, the versions that no longer match
// what the field declares (see staleTargets). It reads the collection in batches in id
// order, makes at most Rate files a second, keeps its lease and looks whether it was
// cancelled after every batch, and reports its progress as job steps. A worker that dies
// leaves a job that another resumes: the files already made match, so they are skipped.
func (w *Worker) runRegenerate(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	e := job.Image
	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	scope := job.Database + "/" + e.Collection + "." + e.Field
	fail := func(message string) {
		log.Error("regeneration of image versions failed", "scope", scope, "error", message)
		tr.finish(ctx, auth.StepFailed)
		if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: message}); err != nil {
			log.Error("complete the failed regeneration", "error", err)
			return
		}
		w.fns.metrics.JobFinished(svc.Realm, "image", executor.StatusError)
	}
	c, ok := w.reg.Collection(realm, job.Database, e.Collection)
	var f *registry.FileField
	if ok {
		f = c.Files[e.Field]
	}
	if f == nil {
		fail("the file field no longer exists")
		return
	}
	if e.Version != "" {
		if v, declared := f.Version(e.Version); !declared || !v.HasParams() {
			fail("the version " + e.Version + " is not declared with parameters of its own")
			return
		}
	}
	scanner, isScanner := w.fns.docs.store.Repository(c).(storage.Scanner)
	if !isScanner {
		fail("the storage of " + scope + " can't be scanned")
		return
	}
	retry := func(err error) {
		if failed := job.Failures + 1; failed < regenerateAttempts {
			wait := 30 * time.Second << (failed - 1)
			log.Warn("regeneration attempt failed; it will be tried again", "attempt", failed, "of", regenerateAttempts, "retry_in", wait.String(), "error", err)
			tr.finish(ctx, auth.StepFailed)
			if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
				log.Error("queue the retry", "error", rerr)
			} else {
				w.fns.metrics.JobRetried(svc.Realm, "image")
			}
			return
		}
		fail(err.Error())
	}
	estimate, err := scanner.EstimatedCount(ctx)
	if err != nil {
		retry(err)
		return
	}
	rate := e.Rate
	if rate < 1 {
		rate = DefaultRegenerateRate
	}
	pace := time.Second / time.Duration(rate)
	tr.begin(ctx, scope, 100, fmt.Sprintf("scanning about %d documents", estimate))

	var scanned, files, made, failed int64
	reported, after := 0, ""
	lastRenew := time.Now()
	for {
		docs, err := scanner.ScanAfter(ctx, after, regenerateBatch)
		if err != nil {
			retry(err)
			return
		}
		if len(docs) == 0 {
			break
		}
		for _, doc := range docs {
			if ctx.Err() != nil {
				return // the worker is stopping: the lease lapses and the job resumes elsewhere
			}
			scanned++
			after, _ = doc["id"].(string)
			for _, file := range filesOf(f, doc) {
				targets := staleTargets(f, e.Version, file, e.MissingOnly)
				if len(targets) == 0 {
					continue
				}
				files++
				fileID, _ := file["id"].(string)
				one := &auth.ImageJob{Collection: e.Collection, Field: e.Field, DocumentID: after, FileID: fileID}
				started := time.Now()
				out := w.makeFile(ctx, svc, c, f, one, targets, false)
				if ctx.Err() != nil {
					return
				}
				if out.transient != "" {
					retry(fmt.Errorf("%s: %s: %w", after, out.transient, out.err))
					return
				}
				made += int64(out.summary[versionReady])
				failed += int64(out.summary[versionFailed])
				if wait := pace - time.Since(started); wait > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(wait):
					}
				}
				if time.Since(lastRenew) > time.Minute {
					if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, regenerateLease); err != nil || !held {
						return // cancelled, or another worker has it now
					}
					lastRenew = time.Now()
				}
			}
		}
		if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, regenerateLease); err != nil {
			retry(err)
			return
		} else if !held {
			log.Info("the regeneration was cancelled or taken over; stopping", "scope", scope)
			return
		}
		lastRenew = time.Now()
		if pct, due := checkPercent(scanned, estimate, reported); due {
			reported = pct
			tr.progress(ctx, float64(pct), fmt.Sprintf("%d files made again so far, in %s", made, progressMessage(scanned, estimate)))
		}
	}
	tr.progress(ctx, 100, fmt.Sprintf("%d files looked at, %d versions made, %d failed", files, made, failed))
	tr.finish(ctx, auth.StepDone)
	out, _ := json.Marshal(map[string]int64{"documents": scanned, "files": files, "made": made, "failed": failed})
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: out}); err != nil {
		log.Error("complete the regeneration", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "image", executor.StatusOK)
	log.Info("image versions regenerated", "scope", scope, "files", files, "made", made, "failed", failed)
}

// CheckImageDeclarations looks, once at startup, for image versions whose declaration is
// new or changed since a worker last saw it, and logs how many files are affected: the
// copies made with other parameters, or no copy at all. It changes nothing but the
// fingerprints it keeps: `backd files regenerate` makes them again. A declaration
// that has not changed costs nothing; one that has makes one pass over its collection.
func (w *Worker) CheckImageDeclarations(ctx context.Context) {
	for _, realm := range w.reg.RealmNames() {
		rl := w.reg.Realms[realm]
		svc := w.fns.docs.users(realm)
		if rl == nil || rl.Settings.Storage == nil || svc == nil {
			continue
		}
		known, err := svc.ImageDeclarations(ctx)
		if err != nil {
			w.log.Warn("could not read the image versions last seen", "realm", realm, "error", err)
			continue
		}
		for _, db := range sortedKeys(rl.Databases) {
			for _, c := range rl.Databases[db].SortedCollections() {
				for _, field := range sortedKeys(c.Files) {
					f := c.Files[field]
					for _, v := range f.Versions {
						if !v.HasParams() || ctx.Err() != nil {
							continue
						}
						key := db + "/" + c.Name + "." + field + "/" + v.Name
						fp := v.Fingerprint()
						old, seen := known[key]
						if seen && old == fp {
							continue
						}
						affected, err := w.countStale(ctx, c, f, v.Name)
						if err != nil {
							w.log.Warn("could not count the files an image version affects", "realm", realm, "version", key, "error", err)
							continue
						}
						what := "new"
						if seen {
							what = "changed"
						}
						attrs := []any{"realm", realm, "version", key, "files", affected}
						switch {
						case affected > 0:
							w.log.Warn("image version "+what+": files need it made (backd files regenerate --realm "+realm+" --database "+db+" --collection "+c.Name+" --field "+field+" --version "+v.Name+")", attrs...)
						default:
							w.log.Info("image version "+what+": no file needs it made", attrs...)
						}
						if err := svc.SetImageDeclaration(ctx, key, fp); err != nil {
							w.log.Warn("could not record an image version", "realm", realm, "version", key, "error", err)
						}
					}
				}
			}
		}
	}
}

// countStale counts the files of a field that a regeneration of one version would make.
func (w *Worker) countStale(ctx context.Context, c *registry.Collection, f *registry.FileField, version string) (int64, error) {
	scanner, ok := w.fns.docs.store.Repository(c).(storage.Scanner)
	if !ok {
		return 0, fmt.Errorf("the storage of %s can't be scanned", c.Name)
	}
	var n int64
	after := ""
	for {
		docs, err := scanner.ScanAfter(ctx, after, regenerateBatch)
		if err != nil || len(docs) == 0 {
			return n, err
		}
		for _, doc := range docs {
			after, _ = doc["id"].(string)
			for _, file := range filesOf(f, doc) {
				if len(staleTargets(f, version, file, false)) > 0 {
					n++
				}
			}
		}
	}
}
