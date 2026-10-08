package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

const (
	codeRegenerateRunning = "regenerate_running"
	// maxRegenerateRate bounds the files a second one regeneration may ask for.
	maxRegenerateRate = 1000
)

// regenerateVersions answers POST /v1/{realm}/_admin/files/versions/regenerate with
// {database, collection, field, version?, missing_only?, rate?}: it queues the job that
// makes again, for every file of the field, the versions that no longer match what the
// field declares (see staleTargets). 202 with the job, which the jobs routes follow and
// cancel; 409 while one runs for the field.
func (a *adminAPI) regenerateVersions(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	var in struct {
		Database    string `json:"database"`
		Collection  string `json:"collection"`
		Field       string `json:"field"`
		Version     string `json:"version"`
		MissingOnly bool   `json:"missing_only"`
		Rate        *int   `json:"rate"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&in) != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, `the body must be {"database", "collection", "field", "version"?, "missing_only"?, "rate"?}`)
		return
	}
	var details []Detail
	for path, v := range map[string]string{"database": in.Database, "collection": in.Collection, "field": in.Field} {
		if v == "" {
			details = append(details, Detail{Path: path, Reason: "is required"})
		}
	}
	rate := 0
	if in.Rate != nil {
		if *in.Rate < 1 || *in.Rate > maxRegenerateRate {
			details = append(details, Detail{Path: "rate", Reason: "must be between 1 and " + strconv.Itoa(maxRegenerateRate) + " files a second"})
		}
		rate = *in.Rate
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "invalid request body", details...)
		return
	}
	c, ok := a.reg.Collection(realm, in.Database, in.Collection)
	var f *registry.FileField
	if ok {
		f = c.Files[in.Field]
	}
	if f == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such file field in this realm")
		return
	}
	if len(f.Versions) == 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "this field declares no versions")
		return
	}
	if in.Version != "" {
		if v, declared := f.Version(in.Version); !declared {
			writeError(w, r, http.StatusNotFound, codeNotFound, "this field declares no version "+in.Version)
			return
		} else if !v.HasParams() {
			writeError(w, r, http.StatusBadRequest, codeValidation, "the version "+in.Version+" has no parameters of its own: only functions make it")
			return
		}
	}
	svc := usersOf(r)
	caller, _ := callerOf(r)
	job, err := svc.StartRegeneration(r.Context(), in.Database, auth.ImageJob{Collection: in.Collection, Field: in.Field, Version: in.Version, MissingOnly: in.MissingOnly, Rate: rate}, caller.Actor(), requestID(r.Context()))
	if errors.Is(err, auth.ErrRegenerateRunning) {
		writeError(w, r, http.StatusConflict, codeRegenerateRunning, "a regeneration of this field's versions is already queued or running; wait for it to finish or cancel it")
		return
	}
	if err != nil {
		adminError(w, r, err)
		return
	}
	svc.Audit(r.Context(), auth.AuditVersionsRegenerate, "files:"+in.Database+"/"+in.Collection+"."+in.Field, map[string]any{"version": in.Version, "missing_only": in.MissingOnly, "job_id": job.ID})
	w.Header().Set("Location", "/v1/"+realm+"/_admin/jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": job.ID, "status": job.Status, "database": in.Database, "collection": in.Collection, "field": in.Field,
		"version": nilIfEmpty(in.Version), "missing_only": in.MissingOnly, "created_at": formatTime(job.CreatedAt),
	})
}
