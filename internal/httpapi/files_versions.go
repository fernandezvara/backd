package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// What a function asks of a file's versions: make one again, make it with other
// parameters, or delete it. They are updates of the document as far as access goes
// (the `update` rule is asked with the document as it is; an API key or ctx.admin.db
// skips it), but never change the document's version.

const (
	codeVersionNotWritable = "version_not_writable"
)

// versionWaitMax is the longest a request waits for a worker to make a version.
var versionWaitMax = 5 * time.Minute

// versionRequest is what a request about a version has looked up and been allowed.
type versionRequest struct {
	c       *registry.Collection
	f       *registry.FileField
	svc     *auth.Users
	docID   string
	file    map[string]any
	version registry.Version
	doc     map[string]any
}

// versionRequestOf checks the request names a document the caller may update, one of its
// files and a version the field declares; it answers when it doesn't.
func (d *documents) versionRequestOf(w http.ResponseWriter, r *http.Request) (versionRequest, bool) {
	c, repo := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return versionRequest{}, false
	}
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return versionRequest{}, false
	}
	if _, ok := d.objectsFor(w, r, c.Realm); !ok {
		return versionRequest{}, false
	}
	a := d.accessFor(r)
	filter, ok := readFilter(w, r, a, c)
	if !ok {
		return versionRequest{}, false
	}
	docID, fileID, name := chi.URLParam(r, "id"), chi.URLParam(r, "fileID"), chi.URLParam(r, "version")
	current, err := storage.Fetch(r.Context(), repo, docID, filter)
	if err != nil {
		storageError(w, r, err)
		return versionRequest{}, false
	}
	var file map[string]any
	for _, e := range filesOf(f, current) {
		if id, _ := e["id"].(string); id == fileID {
			file = e
		}
	}
	if file == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "file not found")
		return versionRequest{}, false
	}
	v, declared := f.Version(name)
	if !declared {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this field declares no version "+name)
		return versionRequest{}, false
	}
	// Changing a version is an update that changes nothing the rules look at: they are
	// asked with the document as it is.
	if !allowWrite(w, r, a, c, rules.Update, current, userFields(current)) {
		return versionRequest{}, false
	}
	return versionRequest{c: c, f: f, svc: svc, docID: docID, file: file, version: v, doc: current}, true
}

// makeVersion handles POST …/{id}/_files/{field}/{file id}/versions/{version}. An empty
// body makes the version again with the parameters the field declares; parameters in the
// body generate it with those instead, which only a `writable` version allows. The
// request waits for a worker to make it and answers the version's new details.
func (d *documents) makeVersion(w http.ResponseWriter, r *http.Request) {
	q, ok := d.versionRequestOf(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body could not be read")
		return
	}
	custom := ""
	if len(strings.TrimSpace(string(raw))) > 0 {
		var keys map[string]json.RawMessage
		if json.Unmarshal(raw, &keys) != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalidJSON, "the body must be a JSON object of parameters: max_width, max_height, fit, quality, format, upscale")
			return
		}
		if len(keys) > 0 {
			var pd imageParamsDoc
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&pd); err != nil {
				writeError(w, r, http.StatusBadRequest, codeValidation, "the parameters are not valid: "+err.Error())
				return
			}
			if !q.version.Writable {
				writeError(w, r, http.StatusForbidden, codeVersionNotWritable, "the version "+q.version.Name+" is not writable: declare writable: true to generate it with other parameters")
				return
			}
			if problems := registry.ValidateVersionParams(pd.params()); len(problems) > 0 {
				details := make([]Detail, len(problems))
				for i, p := range problems {
					field, reason, _ := strings.Cut(p, ": ")
					details[i] = Detail{Path: field, Reason: reason}
				}
				writeError(w, r, http.StatusBadRequest, codeValidation, "the parameters are not valid", details...)
				return
			}
			custom = string(raw)
		}
	} else if !q.version.HasParams() {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the version "+q.version.Name+" has no parameters of its own: generate it with max_width or max_height")
		return
	}

	job := auth.ImageJob{Collection: q.c.Name, Field: q.f.Name, DocumentID: q.docID, FileID: chi.URLParam(r, "fileID"), Version: q.version.Name, Params: custom}
	// The wait outlasts the deadline the data routes put on a request's storage work: it is
	// a worker's time, bounded by versionWaitMax.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), versionWaitMax)
	defer cancel()
	deadline := time.Now().Add(versionWaitMax)
	var queued auth.Job
	for wait := 25 * time.Millisecond; ; wait = min(wait*2, 500*time.Millisecond) {
		queued, err = q.svc.EnqueueImageOp(ctx, q.c.Database, job, requestID(ctx))
		if !errors.Is(err, auth.ErrJobExclusive) {
			break
		}
		// Another job of this file is queued or running: ours comes after it.
		if clientGone(r) {
			return
		}
		if !sleepUntil(ctx, wait, deadline) {
			writeError(w, r, http.StatusGatewayTimeout, "version_timeout", "no worker made the version in time: is a worker running?")
			return
		}
	}
	if err != nil {
		storageError(w, r, err)
		return
	}
	for wait := 25 * time.Millisecond; ; wait = min(wait*2, 500*time.Millisecond) {
		done, found, err := q.svc.GetJob(ctx, queued.ID)
		if err != nil {
			storageError(w, r, err)
			return
		}
		if found && done.Status == auth.JobDone {
			d.versionAnswer(w, r, q, done)
			return
		}
		if clientGone(r) {
			return
		}
		if !sleepUntil(ctx, wait, deadline) {
			writeError(w, r, http.StatusGatewayTimeout, "version_timeout", "no worker made the version in time: is a worker running?")
			return
		}
	}
}

// sleepUntil waits for wait, or the context, or the deadline; false when it should give up.
func sleepUntil(ctx context.Context, wait time.Duration, deadline time.Time) bool {
	if time.Now().Add(wait).After(deadline) {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wait):
		return true
	}
}

// clientGone reports whether the caller hung up (not merely that the data routes'
// deadline passed, which a wait for a worker outlasts).
func clientGone(r *http.Request) bool { return errors.Is(r.Context().Err(), context.Canceled) }

// versionAnswer turns the outcome a worker recorded on its job into the response.
func (d *documents) versionAnswer(w http.ResponseWriter, r *http.Request, q versionRequest, job auth.Job) {
	var out struct {
		Version map[string]any `json:"version"`
		Error   string         `json:"error"`
	}
	if job.Result == nil || job.Result.Status != "ok" || json.Unmarshal(job.Result.Output, &out) != nil {
		writeError(w, r, http.StatusBadGateway, codeStorageUnavailable, "the version could not be made")
		return
	}
	switch out.Error {
	case "":
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out.Version)
	case "file_gone":
		writeError(w, r, http.StatusNotFound, codeNotFound, "the file is no longer in the document")
	case "version_undeclared":
		writeError(w, r, http.StatusNotFound, codeNotFound, "this field declares no version "+q.version.Name)
	case reasonStorage:
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
	default:
		// The image can't be made into this version: too_large, decode_error, timeout,
		// not_an_image, unsupported_format, invalid_parameters.
		writeError(w, r, http.StatusUnprocessableEntity, out.Error, "the version could not be made: "+out.Error)
	}
}

// deleteVersion handles DELETE …/{id}/_files/{field}/{file id}/versions/{version}: the
// made copy is dropped and the version goes back to what a new file's is: pending
// (a worker makes it again) when it has parameters, empty when only functions make it.
func (d *documents) deleteVersion(w http.ResponseWriter, r *http.Request) {
	q, ok := d.versionRequestOf(w, r)
	if !ok {
		return
	}
	name := q.version.Name
	e := &auth.ImageJob{Collection: q.c.Name, Field: q.f.Name, DocumentID: q.docID, FileID: chi.URLParam(r, "fileID")}
	var was map[string]any
	var now map[string]any
	err := d.updateVersions(r.Context(), q.svc, q.c, e, func(versions map[string]any) {
		was = mapOf(versions[name])
		if q.version.HasParams() {
			now = map[string]any{"status": versionPending}
		} else {
			now = map[string]any{"status": versionEmpty}
		}
		versions[name] = now
	})
	switch {
	case errors.Is(err, errFileGone):
		writeError(w, r, http.StatusNotFound, codeNotFound, "the file is no longer in the document")
		return
	case err != nil:
		storageError(w, r, err)
		return
	}
	// A version made again lands on the same object, so only one nobody remakes is deleted.
	if was["status"] == versionReady && !q.version.HasParams() {
		if err := q.svc.QueueFileDeletion(r.Context(), d.objectKey(q.c, e.FileID)+"/"+name, "removed"); err != nil {
			logger(r.Context()).Error("queue a version's object for deletion", "realm", q.c.Realm, "file", e.FileID, "version", name, "error", err)
		}
	}
	if q.version.HasParams() {
		if err := q.svc.EnqueueImageJob(r.Context(), q.c.Database, *e, requestID(r.Context())); err != nil {
			logger(r.Context()).Error("queue the making of a version", "realm", q.c.Realm, "file", e.FileID, "error", err)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, now)
}
