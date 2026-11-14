package httpapi

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	codeMigrationRunning     = "migration_running"
	codeMigrationUnavailable = "migration_unavailable"
)

// migratingCollection finds a collection with schema migrations, answering 404 or 422 itself.
func (a *adminAPI) migratingCollection(w http.ResponseWriter, r *http.Request, realm, database, collection string) (*registry.Collection, bool) {
	c, ok := a.reg.Collection(realm, database, collection)
	if !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such database or collection in this realm")
		return nil, false
	}
	if !c.Versioned || len(c.Migrations) == 0 {
		writeError(w, r, http.StatusUnprocessableEntity, codeValidation, "this collection has no schema migrations", Detail{Path: "collection", Reason: "needs schema.v<N>.json and schema.v<N>.ts files"})
		return nil, false
	}
	return c, true
}

// startDataMigration answers POST /v1/{realm}/_admin/data-migrations: it queues the migration of a
// collection's documents to its current schema version, which a worker runs in the background.
// With dry_run it runs the migrations and validates the results and writes nothing. 202 with the
// job (the one already running for that version when there is one); 409 migration_running while
// a migration to another version is, and 409 migration_unavailable without an executor or a build.
func (a *adminAPI) startDataMigration(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	var in struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		DryRun     bool   `json:"dry_run"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(body)) == 0 || strictDecode(body, &in) != nil || in.Database == "" || in.Collection == "" {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be {database, collection, dry_run?}")
		return
	}
	c, ok := a.migratingCollection(w, r, realm, in.Database, in.Collection)
	if !ok {
		return
	}
	if a.fns == nil || a.fns.docs == nil {
		writeError(w, r, http.StatusConflict, codeMigrationUnavailable, "schema migrations need the worker's functions support")
		return
	}
	if _, isRunner := a.fns.runner.(MigrationRunner); !isRunner {
		writeError(w, r, http.StatusConflict, codeMigrationUnavailable, "schema migrations run in the executor, which this instance has none (BACKD_EXECUTOR_URL)")
		return
	}
	if _, built := a.fns.resolveHash(migrationHashKey(realm, c.Database, c.Name)); !built {
		writeError(w, r, http.StatusConflict, codeMigrationUnavailable, "the migrations of this collection are not built: run `backd-server functions build`")
		return
	}
	svc := usersOf(r)
	caller, _ := callerOf(r)
	job, created, err := svc.StartSchemaMigration(r.Context(), auth.SchemaMigrateJob{Database: c.Database, Collection: c.Name, Target: c.SchemaVersion, DryRun: in.DryRun}, caller.Actor(), requestID(r.Context()))
	if running, ok := errors.AsType[*auth.ErrSchemaMigrateRunning](err); ok {
		writeError(w, r, http.StatusConflict, codeMigrationRunning, "a migration of this collection is already queued or running; wait for it to finish or cancel it", Detail{Path: "job_id", Reason: running.JobID})
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	if created {
		svc.Audit(r.Context(), auth.AuditDataMigrate, "data:"+c.Database+"/"+c.Name, map[string]any{"dry_run": in.DryRun, "job_id": job.ID})
	}
	w.Header().Set("Location", "/v1/"+realm+"/_admin/jobs/"+job.ID)
	var below any
	if n, ok := a.documentsBelow(r, c); ok {
		below = n
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": job.ID, "status": job.Status, "database": c.Database, "collection": c.Name, "target": c.SchemaVersion,
		"dry_run": job.SchemaMigrate.DryRun, "already_running": !created, "documents_below_current": below, "created_at": formatTime(job.CreatedAt),
	})
}

// migrationEnding says how a finished migration job ended.
func migrationEnding(j auth.Job) string {
	switch {
	case j.Result == nil:
		return ""
	case j.Result.Status == auth.ResultCancelled:
		return auth.MigrateCancelled
	case j.SchemaMigrate.Ending != "":
		return j.SchemaMigrate.Ending
	}
	return "failed"
}

// migrationJSON renders a migration job's report. With failures false, without the list of them.
func migrationJSON(j auth.Job, failures bool) map[string]any {
	m := j.SchemaMigrate
	out := map[string]any{
		"job_id": j.ID, "database": m.Database, "collection": m.Collection, "target": m.Target, "dry_run": m.DryRun,
		"status": j.Status, "ending": nil, "created_at": formatTime(j.CreatedAt), "finished_at": nil,
		"scanned": m.Scanned, "migrated": m.Migrated, "skipped": m.Skipped, "failed": m.Failed,
	}
	if e := migrationEnding(j); e != "" {
		out["ending"] = e
	}
	if !j.CompletedAt.IsZero() {
		out["finished_at"] = formatTime(j.CompletedAt)
	}
	if failures {
		list := make([]map[string]any, len(m.Failures))
		for i, f := range m.Failures {
			list[i] = map[string]any{"id": f.ID, "step": f.Step, "kind": f.Kind, "path": f.Path, "keyword": f.Keyword}
		}
		out["failures"] = list
		out["failures_omitted"] = max(0, int(m.Failed)-len(m.Failures))
	}
	return out
}

// listDataMigrations answers GET /v1/{realm}/_admin/data-migrations: the migrations queued or
// running now, and every collection with migrations with the documents still below its current
// version and the summary of its latest finished migration, or null when none is kept.
func (a *adminAPI) listDataMigrations(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	svc := usersOf(r)
	running := []map[string]any{}
	for _, status := range []string{auth.JobRunning, auth.JobQueued} {
		jobs, _, err := svc.Jobs(r.Context(), auth.JobFilter{Origin: auth.SchemaMigrateOrigin, Status: status, Limit: 100})
		if err != nil {
			adminError(w, r, err)
			return
		}
		for _, j := range jobs {
			if j.SchemaMigrate == nil {
				continue
			}
			running = append(running, jobSummaryJSON(j))
		}
	}
	items := []map[string]any{}
	if rl := a.reg.Realms[realm]; rl != nil {
		for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
			for _, c := range rl.Databases[dbName].MigratingCollections() {
				item := map[string]any{"database": c.Database, "collection": c.Name, "current_version": c.SchemaVersion, "documents_below_current": nil, "report": nil}
				if n, ok := a.documentsBelow(r, c); ok {
					item["documents_below_current"] = n
				}
				if j, ok, err := svc.LatestSchemaMigration(r.Context(), c.Database, c.Name); err != nil {
					adminError(w, r, err)
					return
				} else if ok {
					item["report"] = migrationJSON(j, false)
				}
				items = append(items, item)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": running, "items": items})
}

// getDataMigration answers GET /v1/{realm}/_admin/data-migrations/{database}/{collection}: the
// collection's latest finished migration, in full.
func (a *adminAPI) getDataMigration(w http.ResponseWriter, r *http.Request) {
	database, collection := chi.URLParam(r, "database"), chi.URLParam(r, "collection")
	j, ok, err := usersOf(r).LatestSchemaMigration(r.Context(), database, collection)
	if err != nil {
		adminError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this collection has no finished schema migration kept")
		return
	}
	writeJSON(w, http.StatusOK, migrationJSON(j, true))
}

// documentsBelow counts the documents of c below its current schema version, when the storage can.
func (a *adminAPI) documentsBelow(r *http.Request, c *registry.Collection) (int64, bool) {
	if a.fns == nil || a.fns.docs == nil || a.fns.docs.store == nil {
		return 0, false
	}
	mig, ok := a.fns.docs.store.Repository(c).(storage.SchemaMigrator)
	if !ok {
		return 0, false
	}
	n, err := mig.CountBelow(r.Context(), c.SchemaVersion)
	return n, err == nil
}
