package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

const codeCheckRunning = "check_running"

// checkScope is what a check covers, as a name: <database>/<collection>,
// <database>, or "*" for the realm.
func checkScope(database, collection string) string {
	switch {
	case database == "":
		return "*"
	case collection == "":
		return database
	}
	return database + "/" + collection
}

// checkCollections lists the "<database>/<collection>" a scope covers, sorted;
// ok is false when the database or the collection doesn't exist.
func (a *adminAPI) checkCollections(realm, database, collection string) (list []string, ok bool) {
	rl := a.reg.Realms[realm]
	if rl == nil {
		return nil, false
	}
	for _, dbName := range slices.Sorted(maps.Keys(rl.Databases)) {
		if database != "" && dbName != database {
			continue
		}
		db := rl.Databases[dbName]
		for _, name := range slices.Sorted(maps.Keys(db.Collections)) {
			if collection == "" || name == collection {
				list = append(list, dbName+"/"+name)
			}
		}
	}
	return list, len(list) > 0
}

// estimatedDocuments is the size of the collections a check would read, as far
// as the storage can say cheaply; -1 when it can't.
func (a *adminAPI) estimatedDocuments(r *http.Request, realm string, keys []string) int64 {
	var total int64
	rl := a.reg.Realms[realm]
	for _, key := range keys {
		db, name, _ := cutKey(key)
		sc, ok := a.fns.docs.store.Repository(rl.Databases[db].Collections[name]).(storage.Scanner)
		if !ok {
			return -1
		}
		n, err := sc.EstimatedCount(r.Context())
		if err != nil {
			return -1
		}
		total += n
	}
	return total
}

func cutKey(key string) (database, collection string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

// startDataCheck answers POST /v1/{realm}/_admin/data-checks: it queues the check of
// a collection, a database or the whole realm, which a worker runs in the
// background (it reads every document, so on a big collection it takes a while).
// 202 with the job; 409 check_running while another check is queued or running.
func (a *adminAPI) startDataCheck(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	var in struct {
		Database   string `json:"database"`
		Collection string `json:"collection"`
		Limit      *int   `json:"limit"`
	}
	if body, err := io.ReadAll(r.Body); err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read the body")
		return
	} else if len(bytes.TrimSpace(body)) > 0 { // no body: the whole realm
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be {database?, collection?, limit?}")
			return
		}
	}
	limit := auth.DefaultCheckLimit
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > auth.MaxCheckLimit {
			writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "limit", Reason: "must be between 1 and " + strconv.Itoa(auth.MaxCheckLimit)})
			return
		}
		limit = *in.Limit
	}
	if in.Collection != "" && in.Database == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", Detail{Path: "database", Reason: "is required with collection"})
		return
	}
	keys, ok := a.checkCollections(realm, in.Database, in.Collection)
	if !ok {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such database or collection in this realm")
		return
	}
	svc := usersOf(r)
	caller, _ := callerOf(r)
	job, err := svc.StartSchemaCheck(r.Context(), auth.CheckJob{Collections: keys, Database: in.Database, Collection: in.Collection, Limit: limit}, caller.Actor(), requestID(r.Context()))
	var running *auth.ErrCheckRunning
	if errors.As(err, &running) {
		writeError(w, r, http.StatusConflict, codeCheckRunning, "a schema check is already queued or running in this realm; wait for it to finish or cancel it", Detail{Path: "job_id", Reason: running.JobID})
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	scope := checkScope(in.Database, in.Collection)
	svc.Audit(r.Context(), auth.AuditDataCheck, "data:"+scope, map[string]any{"collections": len(keys), "limit": limit, "job_id": job.ID})
	w.Header().Set("Location", "/v1/"+realm+"/_admin/jobs/"+job.ID)
	var estimated any
	if n := a.estimatedDocuments(r, realm, keys); n >= 0 {
		estimated = n
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": job.ID, "status": job.Status, "scope": scope, "collections": keys, "limit": limit,
		"estimated_documents": estimated, "created_at": formatTime(job.CreatedAt),
	})
}

// reportJSON renders a report; with documents false, without the list of them.
func reportJSON(rep auth.CheckReport, documents bool) map[string]any {
	out := map[string]any{
		"database": rep.Database, "collection": rep.Collection, "job_id": rep.JobID,
		"started_at": formatTime(rep.StartedAt), "finished_at": formatTime(rep.FinishedAt),
		"scanned": rep.Scanned, "invalid": rep.Invalid, "complete": rep.Complete, "stopped_by": nil,
		"limit": rep.Limit, "schema_hash": rep.SchemaHash,
	}
	if rep.StoppedBy != "" {
		out["stopped_by"] = rep.StoppedBy
	}
	if documents {
		docs := make([]map[string]any, len(rep.Documents))
		for i, d := range rep.Documents {
			problems := make([]map[string]any, len(d.Problems))
			for j, p := range d.Problems {
				problems[j] = map[string]any{"path": p.Path, "reason": p.Reason}
			}
			docs[i] = map[string]any{"id": d.ID, "deleted": d.Deleted, "problems": problems, "more_problems": d.MoreProblems}
		}
		out["documents"] = docs
	}
	return out
}

// listDataChecks answers GET /v1/{realm}/_admin/data-checks: the check queued or
// running now (with the step it is at), and every collection of the realm with
// its latest report summary, or null when it was never checked.
func (a *adminAPI) listDataChecks(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	svc := usersOf(r)
	reports, err := svc.CheckReports(r.Context(), false)
	if err != nil {
		adminError(w, r, err)
		return
	}
	byKey := map[string]auth.CheckReport{}
	for _, rep := range reports {
		byKey[rep.Database+"/"+rep.Collection] = rep
	}
	var running any
	for _, status := range []string{auth.JobRunning, auth.JobQueued} {
		jobs, _, err := svc.Jobs(r.Context(), auth.JobFilter{Origin: auth.CheckOrigin, Status: status, Limit: 1})
		if err != nil {
			adminError(w, r, err)
			return
		}
		if len(jobs) > 0 && running == nil {
			j := jobs[0]
			sum := jobSummaryJSON(j)
			sum["collections"], sum["limit"] = j.Check.Collections, j.Check.Limit
			running = sum
		}
	}
	keys, _ := a.checkCollections(realm, "", "")
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		db, name, _ := cutKey(key)
		item := map[string]any{"database": db, "collection": name, "report": nil}
		if rep, ok := byKey[key]; ok {
			item["report"] = reportJSON(rep, false)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": running, "items": items})
}

// getDataCheck answers GET /v1/{realm}/_admin/data-checks/{database}/{collection}:
// the collection's latest report, in full.
func (a *adminAPI) getDataCheck(w http.ResponseWriter, r *http.Request) {
	database, collection := chi.URLParam(r, "database"), chi.URLParam(r, "collection")
	rep, err := usersOf(r).CheckReport(r.Context(), database, collection)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this collection has no schema check report yet")
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reportJSON(rep, true))
}
