package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	// schemaMigrateAttempts is how many times a job is tried before it ends as failed; it goes
	// on from the place it reached each time.
	schemaMigrateAttempts = 5
	// migrateCallTimeout, migrateMemory and migrateMaxOutput bound one batch in the executor.
	migrateCallTimeout = 30 * time.Second
	migrateMemory      = 256 << 20
	migrateMaxOutput   = 32 << 20
	// migrateBusyWaits is how many times a refused (busy) batch is tried again, 2 seconds apart.
	migrateBusyWaits = 30
	// schemaMigrateReadTries is how many times one document is read and migrated again after
	// someone wrote it meanwhile.
	schemaMigrateReadTries = 2
)

// MigrationRunner runs a batch of documents through a collection's migrations: the executor
// client, or a fake in tests. The function runner implements it when it can.
type MigrationRunner interface {
	Migrate(ctx context.Context, req executor.MigrateRequest) (executor.MigrateResult, error)
}

// migrationHashKey is where the migration bundle of a collection is looked up.
func migrationHashKey(realm, database, collection string) string {
	return realm + "/" + database + "/" + collection + "@migrations"
}

// migrationRunner returns the executor client when it can run migrations.
func (w *Worker) migrationRunner() MigrationRunner {
	r, _ := w.fns.runner.(MigrationRunner)
	return r
}

// QueueSchemaMigrations is what a worker does at every start: for each versioned collection with
// migrations, it counts the documents below the current version (one indexed query), and queues a
// migration job when there are any. Queueing is idempotent, so many workers starting together
// queue one job. Without an executor, or without a built bundle, the documents are only counted:
// the warning says so and requests are unaffected.
func (w *Worker) QueueSchemaMigrations(ctx context.Context) {
	for _, realm := range w.reg.AuthRealms() {
		svc := w.fns.docs.users(realm)
		if svc == nil {
			continue
		}
		rl := w.reg.Realms[realm]
		for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
			db := rl.Databases[dbName]
			for _, c := range db.MigratingCollections() {
				if c.SchemaVersion < 2 {
					continue
				}
				w.queueSchemaMigration(ctx, realm, svc, c)
			}
		}
	}
}

func (w *Worker) queueSchemaMigration(ctx context.Context, realm string, svc *auth.Users, c *registry.Collection) {
	log := w.log.With("realm", realm, "database", c.Database, "collection", c.Name)
	mig, ok := w.fns.docs.store.Repository(c).(storage.SchemaMigrator)
	if !ok {
		return
	}
	n, err := mig.CountBelow(ctx, c.SchemaVersion)
	if err != nil {
		log.Warn("schema migration: counting the documents below the current version failed", "error", err)
		return
	}
	w.fns.metrics.SchemaBelowCurrent(realm, c.Database+"/"+c.Name, n)
	if n == 0 {
		return
	}
	if w.migrationRunner() == nil {
		log.Warn(fmt.Sprintf("schema migration: %d documents are below version %d and won't be migrated: no executor (BACKD_EXECUTOR_URL)", n, c.SchemaVersion))
		return
	}
	if _, built := w.fns.resolveHash(migrationHashKey(realm, c.Database, c.Name)); !built {
		log.Warn(fmt.Sprintf("schema migration: %d documents are below version %d and won't be migrated: the migrations are not built (run `backd-server functions build`)", n, c.SchemaVersion))
		return
	}
	job, created, err := svc.StartSchemaMigration(ctx, auth.SchemaMigrateJob{Database: c.Database, Collection: c.Name, Target: c.SchemaVersion}, auth.ActorSystem, "")
	var running *auth.ErrSchemaMigrateRunning
	switch {
	case errors.As(err, &running):
		log.Info("schema migration: a job for a later version is running; leaving it", "job_id", running.JobID, "target", running.Target)
	case err != nil:
		log.Warn("schema migration: queueing the job failed", "error", err)
	case created:
		log.Info(fmt.Sprintf("schema migration: %d documents are below version %d; migrating them in the background", n, c.SchemaVersion), "job_id", job.ID)
	}
}

// migratedDoc is a document read for a batch: what was sent, and what is needed to write it back.
type migratedDoc struct {
	doc     storage.Document
	read    int64 // _meta.version when read
	version int   // _meta.schema_version when read (1 when missing)
	fields  map[string]any
}

// runSchemaMigrate runs a schema migration job: it reads the documents below the target version
// in id order and in batches, sends each batch to the executor, validates every result against
// the current schema and writes it back conditionally on the version read. A document that
// fails stays as it was and is reported without its values. The place reached is stored after
// every batch, so a worker that dies, a cancelled job started again or a lost lease go on from
// there; a document already at the target is skipped, so repeating a batch changes nothing.
func (w *Worker) runSchemaMigrate(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	sm := *job.SchemaMigrate
	c, ok := w.reg.Collection(realm, sm.Database, sm.Collection)
	if !ok || !c.Versioned || len(c.Migrations) == 0 {
		w.fail(ctx, log, svc, job, "the collection no longer has schema migrations")
		return
	}
	repo := w.fns.docs.store.Repository(c)
	mig, ok := repo.(storage.SchemaMigrator)
	runner := w.migrationRunner()
	hash, built := w.fns.resolveHash(migrationHashKey(realm, sm.Database, sm.Collection))
	if !ok || runner == nil || !built {
		// Retryable: the executor or a fresh build may show up later.
		log.Warn("schema migration job claimed but the executor or the migration bundle isn't available; leaving it to be retried")
		return
	}
	current := c.SchemaVersion // the config this worker runs, which may be newer than when the job was queued
	url := strings.TrimRight(w.fns.callbackURL, "/") + "/_internal/functions/" + hash
	key := realm + "/" + sm.Database + "/" + sm.Collection

	var total float64
	if n, err := mig.CountBelow(ctx, current); err == nil {
		total = float64(n + sm.Scanned)
	}
	tr := &stepTracker{svc: svc, jobID: job.ID, attempt: job.Attempts}
	title := "Migrate " + sm.Database + "/" + sm.Collection + " to version " + strconv.Itoa(current)
	if sm.DryRun {
		title = "Check what migrating " + sm.Database + "/" + sm.Collection + " would do"
	}
	tr.begin(ctx, title, total, "")

	for {
		if ctx.Err() != nil { // the worker is stopping: the lease runs out and another one goes on from the stored place
			return
		}
		started := time.Now()
		docs, err := mig.ScanBelow(ctx, current, sm.After, auth.SchemaMigrateBatch)
		if err != nil {
			w.retrySchemaMigrate(ctx, log, svc, job, err, tr)
			return
		}
		if len(docs) == 0 {
			break
		}
		batch := w.readBatch(c, docs, auth.SchemaMigrateBatchBytes)
		sm.After, _ = batch[len(batch)-1].doc["id"].(string)
		m := &schemaMigration{w: w, c: c, realm: realm, repo: repo, runner: runner, key: key, url: url, hash: hash, current: current, dryRun: sm.DryRun, sm: &sm}
		if err := m.process(ctx, batch); err != nil {
			if ctx.Err() == nil {
				w.retrySchemaMigrate(ctx, log, svc, job, err, tr)
			}
			return
		}
		if err := svc.SetSchemaMigrateProgress(ctx, job.ID, sm); err != nil {
			w.retrySchemaMigrate(ctx, log, svc, job, err, tr)
			return
		}
		tr.progress(ctx, float64(sm.Scanned), schemaMigrateSummary(sm))
		// The job is still ours? A cancelled job is done, and a lost lease is another worker's.
		if held, err := svc.RenewJobLease(ctx, job.ID, job.Attempts, auth.SchemaMigrateLease); err != nil {
			w.retrySchemaMigrate(ctx, log, svc, job, err, tr)
			return
		} else if !held {
			log.Info("the schema migration was cancelled or taken over; stopping")
			return
		}
		// At most SchemaMigrateRate documents a second.
		if pause := time.Duration(len(batch))*time.Second/time.Duration(w.migrateRate()) - time.Since(started); pause > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pause):
			}
		}
	}

	sm.Ending = auth.MigrateComplete
	if sm.Failed > 0 {
		sm.Ending = auth.MigratePartial
	}
	if err := svc.SetSchemaMigrateProgress(ctx, job.ID, sm); err != nil {
		w.retrySchemaMigrate(ctx, log, svc, job, err, tr)
		return
	}
	tr.progress(ctx, float64(sm.Scanned), schemaMigrateSummary(sm))
	tr.finish(ctx, auth.StepDone)
	summary, _ := json.Marshal(map[string]any{"ending": sm.Ending, "dry_run": sm.DryRun, "scanned": sm.Scanned, "migrated": sm.Migrated, "skipped": sm.Skipped, "failed": sm.Failed})
	if !sm.DryRun {
		w.fns.metrics.SchemaBelowCurrent(realm, sm.Database+"/"+sm.Collection, sm.Failed)
		svc.AuditAs(ctx, auth.ActorSystem, auth.AuditDataMigrated, "data:"+sm.Database+"/"+sm.Collection, map[string]any{
			"database": sm.Database, "collection": sm.Collection, "from": 1, "to": current, "migrated": sm.Migrated, "failed": sm.Failed,
			"complete": sm.Failed == 0, "job_id": job.ID,
		})
	}
	if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusOK, Output: summary}); err != nil {
		log.Error("complete the schema migration", "error", err)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "schema_migrate", executor.StatusOK)
	log.Info("schema migration finished", "collection", sm.Database+"/"+sm.Collection, "to", current, "dry_run", sm.DryRun, "migrated", sm.Migrated, "skipped", sm.Skipped, "failed", sm.Failed)
}

func schemaMigrateSummary(sm auth.SchemaMigrateJob) string {
	verb := ""
	if sm.DryRun {
		verb = "would be "
	}
	return fmt.Sprintf("%d documents read; %smigrated: %d, already current: %d, failed: %d", sm.Scanned, verb, sm.Migrated, sm.Skipped, sm.Failed)
}

// readBatch turns stored documents into what a batch carries, as many as fit in maxBytes of
// fields (always at least one). The rest are read again by the next scan.
func (w *Worker) readBatch(c *registry.Collection, docs []storage.Document, maxBytes int) []migratedDoc {
	var out []migratedDoc
	used := 0
	for _, doc := range docs {
		fields := timesToStrings(userFields(doc)).(map[string]any)
		meta, _ := doc["_meta"].(map[string]any)
		size := 0
		if raw, err := json.Marshal(fields); err == nil {
			size = len(raw)
		}
		if len(out) > 0 && used+size > maxBytes {
			break
		}
		used += size
		out = append(out, migratedDoc{doc: doc, read: version(doc), version: storedSchemaVersion(meta), fields: fields})
	}
	return out
}

// storedSchemaVersion is a document's stamp: 1 when it has none.
func storedSchemaVersion(meta map[string]any) int {
	switch v := meta["schema_version"].(type) {
	case int64:
		return int(v)
	case int32:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	}
	return 1
}

// schemaMigration is the state of one batch's processing.
type schemaMigration struct {
	w       *Worker
	c       *registry.Collection
	repo    storage.Repository
	runner  MigrationRunner
	realm   string
	key     string
	url     string
	hash    string
	current int
	dryRun  bool
	sm      *auth.SchemaMigrateJob
}

// metaFor is what a migration may read of a document's meta.
func metaFor(doc storage.Document) json.RawMessage {
	meta, _ := doc["_meta"].(map[string]any)
	out := map[string]any{}
	for _, k := range []string{"created_at", "updated_at", "created_by", "updated_by", "owner", "org", "team"} {
		if v, ok := meta[k]; ok {
			out[k] = timesToStrings(v)
		}
	}
	raw, _ := json.Marshal(out)
	return raw
}

func (m *schemaMigration) request(docs []migratedDoc) executor.MigrateRequest {
	req := executor.MigrateRequest{
		Collection: m.key, Bundle: executor.Bundle{SHA256: m.hash, URL: m.url}, Current: m.current,
		TimeoutMS: migrateCallTimeout.Milliseconds(), MemoryBytes: migrateMemory, MaxOutput: migrateMaxOutput,
	}
	for _, d := range docs {
		id, _ := d.doc["id"].(string)
		fields, _ := json.Marshal(d.fields)
		req.Docs = append(req.Docs, executor.Doc{ID: id, Version: d.read, SchemaVersion: d.version, Meta: metaFor(d.doc), Fields: fields})
	}
	return req
}

// call sends docs to the executor; a refused (busy) batch waits and tries again.
func (m *schemaMigration) call(ctx context.Context, docs []migratedDoc) (executor.MigrateResult, error) {
	for range migrateBusyWaits {
		res, err := m.runner.Migrate(ctx, m.request(docs))
		if err != nil {
			return res, err
		}
		if res.Status != executor.StatusBusy {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return executor.MigrateResult{}, errors.New("the executor stayed busy")
}

// process migrates one batch. An error is the job's to retry (the executor unreachable, the
// storage failing); a document that can't be migrated is a failure of that document only.
func (m *schemaMigration) process(ctx context.Context, docs []migratedDoc) error {
	res, err := m.call(ctx, docs)
	if err != nil {
		return err
	}
	switch res.Status {
	case executor.StatusOK:
		for i, o := range res.Outcomes {
			if err := m.apply(ctx, docs[i], o); err != nil {
				return err
			}
		}
		return nil
	case executor.StatusBundle, executor.StatusError, executor.StatusCrash:
		if len(docs) == 1 && res.Status == executor.StatusCrash {
			m.fail(docs[0], auth.SchemaMigrateFailure{Kind: auth.FailExecutor})
			return nil
		}
		return fmt.Errorf("the executor answered %s: %s", res.Status, clip(res.Message, 300))
	}
	// A timeout, the memory or the CPU limit: the batch is tried again one document at a time, and
	// the one that still fails is reported. Nothing else in the batch is lost.
	if len(docs) == 1 {
		kind := auth.FailTimeout
		if res.Status == executor.StatusMemory || res.Status == executor.StatusCPU {
			kind = auth.FailMemory
		}
		m.fail(docs[0], auth.SchemaMigrateFailure{Kind: kind})
		return nil
	}
	for _, d := range docs {
		if err := m.process(ctx, []migratedDoc{d}); err != nil {
			return err
		}
	}
	return nil
}

// fail records a document that stays at its version.
func (m *schemaMigration) fail(d migratedDoc, f auth.SchemaMigrateFailure) {
	m.sm.Scanned++
	m.sm.Failed++
	f.ID, _ = d.doc["id"].(string)
	if len(m.sm.Failures) < auth.MaxSchemaMigrateFailures {
		m.sm.Failures = append(m.sm.Failures, f)
	}
	m.w.fns.metrics.SchemaMigrated(m.realm, m.c.Database+"/"+m.c.Name, "failed", 1)
}

// apply stores what one document became.
func (m *schemaMigration) apply(ctx context.Context, d migratedDoc, o executor.MigrateOutcome) error {
	step := ""
	if o.Error != nil {
		step = o.Error.Step
	}
	switch {
	case o.Skipped:
		m.sm.Scanned++
		m.sm.Skipped++
		m.w.fns.metrics.SchemaMigrated(m.realm, m.c.Database+"/"+m.c.Name, "skipped", 1)
		return nil
	case o.Error != nil:
		m.fail(d, auth.SchemaMigrateFailure{Step: step, Kind: o.Error.Kind})
		return nil
	}
	result, f := m.check(d, o.Fields)
	if f != nil {
		m.fail(d, *f)
		return nil
	}
	if m.dryRun {
		m.done(d)
		return nil
	}
	return m.write(ctx, d, result, schemaMigrateReadTries)
}

func (m *schemaMigration) done(d migratedDoc) {
	m.sm.Scanned++
	m.sm.Migrated++
	m.w.fns.metrics.SchemaMigrated(m.realm, m.c.Database+"/"+m.c.Name, "migrated", 1)
}

// check validates a migrated document the way a write is validated, and refuses a change to a
// file field. It returns the fields as stored, or the failure.
func (m *schemaMigration) check(d migratedDoc, raw json.RawMessage) (map[string]any, *auth.SchemaMigrateFailure) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, &auth.SchemaMigrateFailure{Kind: auth.FailNotJSON}
	}
	stripSystemFields(fields)
	if err := m.c.Schema.Validate(fields); err != nil {
		f := auth.SchemaMigrateFailure{Kind: auth.FailInvalid}
		if details := validationDetails(err); len(details) > 0 {
			f.Path, f.Keyword = clip(details[0].Path, maxCheckText), clip(details[0].Reason, maxCheckText)
		}
		return nil, &f
	}
	for name := range m.c.Files {
		before, _ := json.Marshal(timesToStrings(docGet(d.doc, name)))
		after, _ := json.Marshal(docGet(fields, name))
		if !bytes.Equal(before, after) {
			return nil, &auth.SchemaMigrateFailure{Kind: auth.FailFile, Path: name}
		}
	}
	return m.c.DatesToStorage(jsonnum.Normalize(fields).(map[string]any)), nil
}

// write replaces the document conditionally on the version read. The fields are the migration's;
// _meta is the stored one with its version bumped and the schema version set, and updated_at and
// updated_by kept: it is not a person's edit.
func (m *schemaMigration) write(ctx context.Context, d migratedDoc, fields map[string]any, tries int) error {
	id, _ := d.doc["id"].(string)
	stored, _ := d.doc["_meta"].(map[string]any)
	meta := maps.Clone(stored)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["version"] = d.read + 1
	meta["schema_version"] = int64(m.current)
	doc := storage.Document(fields)
	doc["id"] = id
	doc["_meta"] = meta
	err := m.repo.Replace(ctx, doc, d.read)
	switch {
	case err == nil:
		m.done(d)
		return nil
	case errors.Is(err, storage.ErrConflict):
		m.fail(d, auth.SchemaMigrateFailure{Kind: auth.FailConflict})
		return nil
	case errors.Is(err, storage.ErrNotFound):
		m.sm.Scanned++ // deleted meanwhile
		return nil
	case errors.Is(err, storage.ErrVersionMismatch) && tries > 0:
		// Someone wrote the document meanwhile. If it is now at the current version it is done;
		// if an old app wrote it, it is migrated again from its new content.
		fresh, gerr := m.repo.Get(ctx, id)
		if errors.Is(gerr, storage.ErrNotFound) {
			m.sm.Scanned++
			return nil
		}
		if gerr != nil {
			return gerr
		}
		freshMeta, _ := fresh["_meta"].(map[string]any)
		if storedSchemaVersion(freshMeta) >= m.current {
			m.sm.Scanned++
			m.sm.Skipped++
			return nil
		}
		again := migratedDoc{doc: fresh, read: version(fresh), version: storedSchemaVersion(freshMeta), fields: timesToStrings(userFields(fresh)).(map[string]any)}
		res, cerr := m.call(ctx, []migratedDoc{again})
		if cerr != nil {
			return cerr
		}
		if res.Status != executor.StatusOK || len(res.Outcomes) != 1 || res.Outcomes[0].Error != nil || res.Outcomes[0].Skipped {
			m.fail(d, auth.SchemaMigrateFailure{Kind: auth.FailThrew})
			return nil
		}
		next, f := m.check(again, res.Outcomes[0].Fields)
		if f != nil {
			m.fail(d, *f)
			return nil
		}
		return m.write(ctx, again, next, tries-1)
	case errors.Is(err, storage.ErrVersionMismatch):
		m.fail(d, auth.SchemaMigrateFailure{Kind: auth.FailPlaintext})
		return nil
	}
	return err
}

func docGet(doc map[string]any, path string) any {
	var cur any = doc
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// retrySchemaMigrate tries the job again after a growing wait while attempts are left; then the
// job ends as failed. What was migrated stays migrated, so a new run only does what is left.
func (w *Worker) retrySchemaMigrate(ctx context.Context, log *slog.Logger, svc *auth.Users, job auth.Job, err error, tr *stepTracker) {
	tr.finish(ctx, auth.StepFailed)
	if failed := job.Failures + 1; failed < schemaMigrateAttempts {
		wait := min(30*time.Second<<min(failed-1, 5), 15*time.Minute)
		log.Warn("the schema migration failed; it will be tried again", "attempt", failed, "of", schemaMigrateAttempts, "retry_in", wait.String(), "error", err)
		if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
			log.Error("queue the retry", "error", rerr)
		} else {
			w.fns.metrics.JobRetried(svc.Realm, "schema_migrate")
		}
		return
	}
	log.Error("the schema migration failed for good", "error", err)
	if cerr := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: executor.StatusError, Message: err.Error()}); cerr != nil {
		log.Error("complete the failed schema migration", "error", cerr)
		return
	}
	w.fns.metrics.JobFinished(svc.Realm, "schema_migrate", executor.StatusError)
}

// migrateRate is how many documents a second a migration job handles at most.
func (w *Worker) migrateRate() int {
	if w.migrateDocsPerSecond > 0 {
		return w.migrateDocsPerSecond
	}
	return auth.SchemaMigrateRate
}
