package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/storage"
)

const revisionsKey = "acme__app.revisions"

// seedRevisions stores n documents of app/revisions r00001…: those with a stamp in stamped are at
// that version, the rest have none (version 1).
func seedRevisions(f *rulesFixture, n int, stamped map[int]int) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if f.store.docs == nil {
		f.store.docs = map[string]map[string]storage.Document{}
	}
	if f.store.docs[revisionsKey] == nil {
		f.store.docs[revisionsKey] = map[string]storage.Document{}
	}
	at := *f.clock
	for i := 1; i <= n; i++ {
		meta := map[string]any{"version": int64(1), "created_at": at, "updated_at": at, "owner": "u1", "created_by": "user:u1", "updated_by": "user:u1"}
		if v, ok := stamped[i]; ok {
			meta["schema_version"] = int64(v)
		}
		id := fmt.Sprintf("r%05d", i)
		f.store.docs[revisionsKey][id] = storage.Document{"id": id, "title": fmt.Sprintf("title %d", i), "_meta": meta}
	}
}

func (f *rulesFixture) revision(id string) storage.Document {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	return clone(f.store.docs[revisionsKey][id]).(map[string]any)
}

// startedJob is the unfinished migration job of app/revisions.
func startedJob(t *testing.T, f *rulesFixture) auth.Job {
	t.Helper()
	jobs, _, err := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.SchemaMigrateOrigin, Database: "app", Function: "revisions", Limit: 10})
	if err != nil || len(jobs) == 0 {
		t.Fatalf("no migration job: %v %v", jobs, err)
	}
	return jobs[0]
}

// A worker finds the documents below the current version at start, queues one job, and the job
// moves them in batches: fields from the migration, _meta.version bumped, the stamp set, and
// updated_at and updated_by kept.
func TestWorkerMigratesAtStart(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx := context.Background()
	stamped := map[int]int{}
	for i := 1101; i <= 1200; i++ {
		stamped[i] = 2 // already current
	}
	seedRevisions(f, 1200, stamped)

	w.QueueSchemaMigrations(ctx)
	w.QueueSchemaMigrations(ctx) // another worker starting: the same job
	jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Origin: auth.SchemaMigrateOrigin})
	if len(jobs) != 1 || jobs[0].SchemaMigrate.Target != 2 {
		t.Fatalf("jobs queued: %+v", jobs)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("the worker didn't claim the migration")
	}
	done, _, _ := f.svc.GetJob(ctx, jobs[0].ID)
	if done.Status != auth.JobDone || done.Result == nil || done.Result.Status != "ok" {
		t.Fatalf("the job: %+v", done)
	}
	sm := done.SchemaMigrate
	if sm.Ending != auth.MigrateComplete || sm.Scanned != 1100 || sm.Migrated != 1100 || sm.Failed != 0 || len(sm.Failures) != 0 {
		t.Fatalf("report: %+v", sm)
	}
	batches := f.runner.migrationBatches()
	sizes := []int{}
	for _, b := range batches {
		sizes = append(sizes, len(b.Docs))
		if b.Current != 2 || b.Collection != "acme/app/revisions" || len(b.Bundle.SHA256) != 64 || !strings.HasSuffix(b.Bundle.URL, b.Bundle.SHA256) {
			t.Errorf("batch request: %+v", b)
		}
	}
	if fmt.Sprint(sizes) != "[500 500 100]" {
		t.Errorf("batches of %v, want 500, 500, 100", sizes)
	}

	d := f.revision("r00001")
	meta := d["_meta"].(map[string]any)
	if d["migrated"] != true || meta["schema_version"] != int64(2) || meta["version"] != int64(2) {
		t.Errorf("migrated document: %+v", d)
	}
	if !meta["updated_at"].(time.Time).Equal(*f.clock) || meta["updated_by"] != "user:u1" || meta["owner"] != "u1" || meta["created_by"] != "user:u1" {
		t.Errorf("updated_at and updated_by should be kept: %+v", meta)
	}
	if untouched := f.revision("r01150"); untouched["migrated"] != nil || untouched["_meta"].(map[string]any)["version"] != int64(1) {
		t.Errorf("a document at the current version was written: %+v", untouched)
	}
	// What the executor was sent is the document as the API shows it, never _meta or id.
	var sent map[string]any
	_ = json.Unmarshal(batches[0].Docs[0].Fields, &sent)
	if sent["title"] != "title 1" || sent["id"] != nil || sent["_meta"] != nil {
		t.Errorf("fields sent: %v", sent)
	}
	var sentMeta map[string]any
	_ = json.Unmarshal(batches[0].Docs[0].Meta, &sentMeta)
	if sentMeta["owner"] != "u1" || sentMeta["created_by"] != "user:u1" || sentMeta["version"] != nil {
		t.Errorf("meta sent: %v", sentMeta)
	}
	if batches[0].Docs[0].ID != "r00001" || batches[0].Docs[0].Version != 1 || batches[0].Docs[0].SchemaVersion != 1 {
		t.Errorf("doc header: %+v", batches[0].Docs[0])
	}

	// The audit trail says it ended, with counts and no values; the next start finds nothing to do.
	recs, _, _ := f.svc.Store.ListAudit(ctx, auth.AuditFilter{Action: auth.AuditDataMigrated})
	if len(recs) != 1 || recs[0].Details["migrated"] != int64(1100) {
		t.Errorf("audit: %+v", recs)
	}
	before := len(f.runner.migrationBatches())
	w.QueueSchemaMigrations(ctx)
	if jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Origin: auth.SchemaMigrateOrigin, Status: auth.JobQueued}); len(jobs) != 0 || len(f.runner.migrationBatches()) != before {
		t.Errorf("nothing was due, but a job was queued: %+v", jobs)
	}
}

// A document whose migration throws, whose result breaks the schema or that times out stays as
// it was and is reported without a value; the rest of the batch migrates.
func TestSchemaMigrationFailures(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx := context.Background()
	seedRevisions(f, 6, nil)
	f.runner.migrate = func(req executor.MigrateRequest) (executor.MigrateResult, error) {
		res := executor.MigrateResult{Status: executor.StatusOK}
		for _, d := range req.Docs {
			switch d.ID {
			case "r00002":
				res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Error: &executor.MigrateError{Kind: "threw", Step: "1 → 2", Message: "TypeError: secret-value is not a function"}})
			case "r00003":
				res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: json.RawMessage(`{"title": 42}`)}) // the schema says string
			case "r00004":
				res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: json.RawMessage(`{"title": "x", "extra": "secret-value"}`)}) // additionalProperties: false
			default:
				res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: json.RawMessage(`{"title": "ok", "migrated": true}`)})
			}
		}
		return res, nil
	}
	w.QueueSchemaMigrations(ctx)
	job := startedJob(t, f)
	w.RunOnce(ctx)
	done, _, _ := f.svc.GetJob(ctx, job.ID)
	sm := done.SchemaMigrate
	if done.Result == nil || done.Result.Status != "ok" || sm.Ending != auth.MigratePartial || sm.Migrated != 3 || sm.Failed != 3 || sm.Scanned != 6 {
		t.Fatalf("job: %+v %+v", done.Result, sm)
	}
	got := map[string]auth.SchemaMigrateFailure{}
	for _, fl := range sm.Failures {
		got[fl.ID] = fl
	}
	if fl := got["r00002"]; fl.Kind != auth.FailThrew || fl.Step != "1 → 2" {
		t.Errorf("a throw: %+v", fl)
	}
	if fl := got["r00003"]; fl.Kind != auth.FailInvalid || fl.Path != "title" {
		t.Errorf("a wrong type: %+v", fl)
	}
	if fl := got["r00004"]; fl.Kind != auth.FailInvalid || fl.Path != "extra" {
		t.Errorf("an extra field: %+v", fl)
	}
	raw, _ := json.Marshal(done)
	if strings.Contains(string(raw), "secret-value") {
		t.Errorf("a value reached the job: %s", raw)
	}
	for _, id := range []string{"r00002", "r00003", "r00004"} {
		d := f.revision(id)
		meta := d["_meta"].(map[string]any)
		if meta["schema_version"] != nil || meta["version"] != int64(1) || d["migrated"] != nil {
			t.Errorf("%s should stay as it was: %+v", id, d)
		}
	}
	if d := f.revision("r00001"); d["_meta"].(map[string]any)["schema_version"] != int64(2) || d["title"] != "ok" {
		t.Errorf("a good one: %+v", d)
	}
	// Tried again at the next start, because they're still below the current version.
	w.QueueSchemaMigrations(ctx)
	if again := startedJob(t, f); again.ID == job.ID || again.Status != auth.JobQueued {
		t.Errorf("a new job should be queued for the failed documents: %+v", again)
	}
}

// A batch that times out is tried again one document at a time; the one that times out alone is
// reported and nothing else is lost.
func TestSchemaMigrationTimeoutSplitsTheBatch(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx := context.Background()
	seedRevisions(f, 4, nil)
	f.runner.migrate = func(req executor.MigrateRequest) (executor.MigrateResult, error) {
		res := executor.MigrateResult{Status: executor.StatusOK}
		for _, d := range req.Docs {
			if d.ID == "r00003" {
				return executor.MigrateResult{Status: executor.StatusTimeout, Message: "stopped after 30s"}, nil
			}
			res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: json.RawMessage(`{"title": "t", "migrated": true}`)})
		}
		return res, nil
	}
	w.QueueSchemaMigrations(ctx)
	job := startedJob(t, f)
	w.RunOnce(ctx)
	sm := func() *auth.SchemaMigrateJob { d, _, _ := f.svc.GetJob(ctx, job.ID); return d.SchemaMigrate }()
	if sm.Migrated != 3 || sm.Failed != 1 || len(sm.Failures) != 1 || sm.Failures[0].ID != "r00003" || sm.Failures[0].Kind != auth.FailTimeout {
		t.Fatalf("report: %+v", sm)
	}
	// One batch of four, then one call per document (the one that failed alone is not asked twice).
	if n := len(f.runner.migrationBatches()); n != 5 {
		t.Errorf("%d calls, want 1 + 4", n)
	}
}

// Someone writes a document while the job has it: if they wrote the current version it is
// skipped; if an old app wrote it, it is migrated again from its new content.
func TestSchemaMigrationConcurrentWrite(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx := context.Background()
	seedRevisions(f, 3, nil)
	calls := 0
	f.runner.migrate = func(req executor.MigrateRequest) (executor.MigrateResult, error) {
		calls++
		if calls == 1 {
			f.store.mu.Lock()
			docs := f.store.docs[revisionsKey]
			// r00001: a new app wrote it, at version 2. r00002: an old app wrote it, still version 1.
			m1 := docs["r00001"]["_meta"].(map[string]any)
			m1["version"], m1["schema_version"] = int64(2), int64(2)
			docs["r00001"]["title"] = "written by a new app"
			m2 := docs["r00002"]["_meta"].(map[string]any)
			m2["version"] = int64(2)
			docs["r00002"]["title"] = "written by an old app"
			f.store.mu.Unlock()
		}
		res := executor.MigrateResult{Status: executor.StatusOK}
		for _, d := range req.Docs {
			var fields map[string]any
			_ = json.Unmarshal(d.Fields, &fields)
			fields["migrated"] = true
			raw, _ := json.Marshal(fields)
			res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: raw})
		}
		return res, nil
	}
	w.QueueSchemaMigrations(ctx)
	job := startedJob(t, f)
	w.RunOnce(ctx)
	done, _, _ := f.svc.GetJob(ctx, job.ID)
	sm := done.SchemaMigrate
	if sm.Failed != 0 || sm.Migrated != 2 || sm.Skipped != 1 || sm.Scanned != 3 {
		t.Fatalf("report: %+v", sm)
	}
	if d := f.revision("r00001"); d["title"] != "written by a new app" || d["migrated"] != nil || d["_meta"].(map[string]any)["version"] != int64(2) {
		t.Errorf("the new app's write must stand untouched: %+v", d)
	}
	if d := f.revision("r00002"); d["title"] != "written by an old app" || d["migrated"] != true || d["_meta"].(map[string]any)["schema_version"] != int64(2) || d["_meta"].(map[string]any)["version"] != int64(3) {
		t.Errorf("the old app's write must be migrated from its content: %+v", d)
	}
}

// A job that stops half way (the worker is killed) is claimed again after its lease and goes on
// where it was: every document is sent to the executor once.
func TestSchemaMigrationResumesAfterAKill(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	f.svc.Now = func() time.Time { return *f.clock } // leases follow the test's clock
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx, kill := context.WithCancel(context.Background())
	defer kill()
	seedRevisions(f, 10_000, nil)
	sent := map[string]int{}
	batches := 0
	f.runner.migrate = func(req executor.MigrateRequest) (executor.MigrateResult, error) {
		batches++
		if batches == 8 {
			kill() // the worker dies while this batch is in flight
		}
		res := executor.MigrateResult{Status: executor.StatusOK}
		for _, d := range req.Docs {
			sent[d.ID]++
			res.Outcomes = append(res.Outcomes, executor.MigrateOutcome{ID: d.ID, Fields: json.RawMessage(`{"title": "t", "migrated": true}`)})
		}
		return res, nil
	}
	w.QueueSchemaMigrations(ctx)
	job := startedJob(t, f)
	w.RunOnce(ctx)
	stopped, _, _ := f.svc.GetJob(context.Background(), job.ID)
	if stopped.Status != auth.JobRunning || stopped.SchemaMigrate.Scanned == 0 || stopped.SchemaMigrate.Scanned >= 10_000 || stopped.SchemaMigrate.After == "" {
		t.Fatalf("the job should be running with progress stored: %+v", stopped)
	}

	// Another worker, after the lease: it goes on from the stored place.
	*f.clock = f.clock.Add(auth.SchemaMigrateLease + time.Minute)
	w2 := newTestWorker(t, f)
	w2.migrateDocsPerSecond = 1_000_000
	if !w2.RunOnce(context.Background()) {
		t.Fatal("the second worker didn't claim the job")
	}
	done, _, _ := f.svc.GetJob(context.Background(), job.ID)
	if done.Status != auth.JobDone || done.SchemaMigrate.Ending != auth.MigrateComplete || done.SchemaMigrate.Scanned != 10_000 || done.SchemaMigrate.Migrated != 10_000 {
		t.Fatalf("job after the resume: %+v", done.SchemaMigrate)
	}
	for id, n := range sent {
		if n != 1 {
			t.Fatalf("%s was sent %d times", id, n)
		}
	}
	if len(sent) != 10_000 {
		t.Errorf("%d documents sent, want 10000", len(sent))
	}
}

// A dry run writes nothing and says what would happen; a start for a later version replaces a
// running job for an earlier one, and a dry run never replaces a real one.
func TestSchemaMigrationDryRunAndReplace(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	w.migrateDocsPerSecond = 1_000_000
	ctx := context.Background()
	seedRevisions(f, 5, nil)
	job, created, err := f.svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: "app", Collection: "revisions", Target: 2, DryRun: true}, "key:ops", "")
	if err != nil || !created {
		t.Fatal(created, err)
	}
	w.RunOnce(ctx)
	done, _, _ := f.svc.GetJob(ctx, job.ID)
	if sm := done.SchemaMigrate; sm.Migrated != 5 || sm.Ending != auth.MigrateComplete || !sm.DryRun {
		t.Errorf("dry run report: %+v", sm)
	}
	if d := f.revision("r00001"); d["migrated"] != nil || d["_meta"].(map[string]any)["schema_version"] != nil {
		t.Errorf("a dry run wrote: %+v", d)
	}
	if latest, ok, _ := f.svc.LatestSchemaMigration(ctx, "app", "revisions"); !ok || latest.ID != job.ID {
		t.Errorf("latest: %+v %v", latest, ok)
	}

	real, created, err := f.svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: "app", Collection: "revisions", Target: 2}, "key:ops", "")
	if err != nil || !created {
		t.Fatal(created, err)
	}
	if same, created, err := f.svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: "app", Collection: "revisions", Target: 2}, "system", ""); err != nil || created || same.ID != real.ID {
		t.Errorf("a second start for the same target: %+v %v %v", same.ID, created, err)
	}
	if _, _, err := f.svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: "app", Collection: "revisions", Target: 2, DryRun: true}, "key:ops", ""); err == nil {
		t.Error("a dry run started over a real migration")
	}
	next, created, err := f.svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: "app", Collection: "revisions", Target: 3}, "system", "")
	if err != nil || !created || next.ID == real.ID {
		t.Fatalf("a later target should replace the earlier job: %v %v", created, err)
	}
	if old, _, _ := f.svc.GetJob(ctx, real.ID); old.Status != auth.JobDone || old.Result.Status != auth.ResultCancelled {
		t.Errorf("the earlier job should be cancelled: %+v", old)
	}
	if _, _, err := f.svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: "app", Collection: "revisions", Target: 2}, "system", ""); err == nil {
		t.Error("an earlier target replaced a later job")
	}
}

// Without an executor, or without a build, the documents are only counted: a warning, no job.
func TestSchemaMigrationWithoutExecutor(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	var logs strings.Builder
	w := newTestWorker(t, f)
	w.log = slog.New(slog.NewTextHandler(&logs, nil))
	w.fns.runner = onlyInvoke{f.runner} // a runner that can't migrate
	seedRevisions(f, 3, nil)
	w.QueueSchemaMigrations(context.Background())
	if jobs, _, _ := f.svc.Jobs(context.Background(), auth.JobFilter{Origin: auth.SchemaMigrateOrigin}); len(jobs) != 0 {
		t.Errorf("jobs: %+v", jobs)
	}
	if !strings.Contains(logs.String(), "3 documents are below version 2 and won't be migrated: no executor") {
		t.Errorf("log: %s", logs.String())
	}
}

type onlyInvoke struct{ r *fakeRunner }

func (o onlyInvoke) Invoke(ctx context.Context, req executor.InvokeRequest) (executor.Result, error) {
	return o.r.Invoke(ctx, req)
}
