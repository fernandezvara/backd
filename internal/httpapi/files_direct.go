package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// Direct uploads (`upload: direct`): the client sends the file's bytes straight to
// the bucket with a signed link, and backd verifies them afterwards. Starting one
// declares what the file is (name, size, type, SHA-256) and signs exactly that into
// the PUT, so storage itself refuses a body that differs. Completing one is a HEAD
// (the size and the checksum storage computed must be the declared ones) and a range
// read of the first bytes, whatever the file's size, to detect its type: any
// mismatch deletes the object. The SHA-256 in a file's details is therefore always
// one storage verified, never one the client claimed.

const (
	codeUploadMismatch    = "upload_mismatch"
	codeFileNotUploaded   = "file_not_uploaded"
	maxStartBody          = 8 << 10
	directStartDescribing = "name, size, type and sha256 are required"
)

var sha256Hex = func(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// directStart is the body of a start call.
type directStart struct {
	Name   string `json:"name"`
	Size   *int64 `json:"size"`
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
}

// readDirectStart reads and validates a start call against the field. It answers and
// returns false when it can't be used.
func readDirectStart(w http.ResponseWriter, r *http.Request, f *registry.FileField) (directStart, bool) {
	var in directStart
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStartBody))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		tooLarge(w, r, maxStartBody)
		return in, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err != nil || dec.Decode(&in) != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidJSON, `the body must be a JSON object: {"name", "size", "type", "sha256"}`)
		return in, false
	}
	var details []Detail
	if strings.TrimSpace(in.Name) == "" {
		details = append(details, Detail{Path: "name", Reason: "is required"})
	}
	switch {
	case in.Size == nil:
		details = append(details, Detail{Path: "size", Reason: "is required: the file's size in bytes"})
	case *in.Size < 1:
		details = append(details, Detail{Path: "size", Reason: "must be at least 1 byte"})
	case *in.Size > f.MaxSize:
		details = append(details, Detail{Path: "size", Reason: "is over this field's max_size (" + strconv.FormatInt(f.MaxSize, 10) + " bytes)"})
	}
	if _, _, err := mime.ParseMediaType(in.Type); err != nil || strings.TrimSpace(in.Type) == "" {
		details = append(details, Detail{Path: "type", Reason: "is required: the file's content type"})
	}
	if !sha256Hex(in.SHA256) {
		details = append(details, Detail{Path: "sha256", Reason: "is required: the file's SHA-256, 64 lower-case hex characters"})
	}
	if len(details) > 0 {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the upload isn't described: "+directStartDescribing, details...)
		return in, false
	}
	if !f.Allows(in.Type) {
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedFile, "this field doesn't accept "+in.Type)
		return in, false
	}
	return in, true
}

// startReply answers a started direct upload: where to PUT, with which headers, and
// the id and token to complete it.
func (d *documents) startReply(w http.ResponseWriter, r *http.Request, c *registry.Collection, f *registry.FileField, obj *storage.Objects, id, token string, in directStart, ttl time.Duration) {
	key := obj.Key(c.Realm, c.Database, c.Name, id)
	link, err := obj.PresignPut(r.Context(), key, linkTTL(f, d.reg.Realms[c.Realm].Settings.Storage), *in.Size, in.Type, in.SHA256)
	if err != nil {
		logger(r.Context()).Error("sign a direct upload", "realm", c.Realm, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"upload_id": id, "upload_token": token, "expires_at": d.timestamp().Add(ttl).UTC().Format(timeFormat),
		"method": http.MethodPut, "url": link.URL, "headers": link.Headers, "url_expires_at": link.ExpiresAt.UTC().Format(timeFormat),
	})
}

// startDirectForDocument handles POST …/{id}/_files/{field}/uploads on a direct
// field: the update rule is asked, as for an upload, about the document with the new
// file's known details (what the client declared), before anything is signed.
func (d *documents) startDirectForDocument(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return
	}
	if f.Upload != registry.UploadDirect {
		writeError(w, r, http.StatusConflict, codeUploadModeMismatch, "this field takes proxy uploads: POST the file to …/_files/"+f.Name)
		return
	}
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return
	}
	obj, ok := d.objectsFor(w, r, c.Realm)
	if !ok {
		return
	}
	in, ok := readDirectStart(w, r, f)
	if !ok {
		return
	}
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	a := d.accessFor(r)
	filter, ok := readFilter(w, r, a, c)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")
	id := "fl_" + xid.New().String()
	known := map[string]any{"id": id, "name": sanitizeFileName(in.Name), "size": *in.Size, "type": in.Type, "sha256": in.SHA256, "uploaded_at": nil}
	current, ok := d.checkFileChange(w, r, repo, a, c, f, docID, filter, cond, known)
	if !ok {
		return
	}
	var freed int64
	if !f.Multiple {
		freed = fileSizes(filesOf(f, current))
	}
	if !d.quotaAllows(w, r, c, docOwner(current), *in.Size, freed) {
		return
	}
	callerKey, ok := d.uploadGate(w, r, svc)
	if !ok {
		return
	}
	token, hash := auth.NewUploadToken()
	ttl := d.pendingTTL(c)
	caller, _ := callerOf(r)
	entry := auth.FileJournalEntry{ID: id, Database: c.Database, Collection: c.Name, Field: f.Name, DocumentID: docID, Key: obj.Key(c.Realm, c.Database, c.Name, id),
		Caller: caller.Subject(), Size: *in.Size, TokenHash: hash, CallerKey: callerKey, Owner: ownerOf(r), Name: sanitizeFileName(in.Name), Type: in.Type, SHA256: in.SHA256}
	if err := svc.JournalDirectUpload(r.Context(), entry, ttl); err != nil {
		storageError(w, r, err)
		return
	}
	d.startReply(w, r, c, f, obj, id, token, in, ttl)
}

// checkFileChange is the pre-flight of a change to a document's files: the document
// is read (404 when the caller can't), If-Match is checked, the update rule is asked
// about the document with file added (replacing a single field's), and max_files.
func (d *documents) checkFileChange(w http.ResponseWriter, r *http.Request, repo storage.Repository, a access, c *registry.Collection, f *registry.FileField, docID string, filter storage.Filter, cond ifMatch, file map[string]any) (map[string]any, bool) {
	current, err := storage.Fetch(r.Context(), repo, docID, filter)
	if err != nil {
		storageError(w, r, err)
		return nil, false
	}
	if read := version(current); !cond.matches(read) {
		versionMismatch(w, r, read)
		return nil, false
	}
	existing := filesOf(f, current)
	next := []map[string]any{file}
	if f.Multiple {
		next = append(append([]map[string]any{}, existing...), file)
	}
	if !allowWrite(w, r, a, c, rules.Update, current, withFiles(f, current, next)) {
		return nil, false
	}
	if f.Multiple && len(existing) >= f.MaxFiles {
		writeError(w, r, http.StatusConflict, codeTooManyFiles, "this field already holds "+strconv.Itoa(f.MaxFiles)+" files (max_files)")
		return nil, false
	}
	return current, true
}

// ownerOf is the user a pending or direct upload is bound to, "" for anyone.
func ownerOf(r *http.Request) string {
	if cl, ok := callerOf(r); ok && cl.User != nil && !adminData(r) {
		return cl.User.User.ID
	}
	return ""
}

// startDirectPending handles the start of a pending upload on a direct field: the
// body of POST …/_files/{field}/uploads is the declaration, not the file.
func (d *documents) startDirectPending(w http.ResponseWriter, r *http.Request, c *registry.Collection, f *registry.FileField, svc *auth.Users, obj *storage.Objects) {
	in, ok := readDirectStart(w, r, f)
	if !ok {
		return
	}
	id := "fl_" + xid.New().String()
	known := map[string]any{"id": id, "name": sanitizeFileName(in.Name), "size": *in.Size, "type": in.Type, "sha256": in.SHA256, "uploaded_at": nil}
	if !d.pendingGate(w, r, c, f, known) || !d.quotaAllows(w, r, c, ownerOf(r), *in.Size, 0) {
		return
	}
	callerKey, ok := d.uploadGate(w, r, svc)
	if !ok {
		return
	}
	token, hash := auth.NewUploadToken()
	ttl := d.pendingTTL(c)
	caller, _ := callerOf(r)
	entry := auth.FileJournalEntry{ID: id, Database: c.Database, Collection: c.Name, Field: f.Name, Key: obj.Key(c.Realm, c.Database, c.Name, id),
		Caller: caller.Subject(), Size: *in.Size, Pending: true, TokenHash: hash, CallerKey: callerKey, Owner: ownerOf(r), Name: sanitizeFileName(in.Name), Type: in.Type, SHA256: in.SHA256}
	if err := svc.JournalDirectUpload(r.Context(), entry, ttl); err != nil {
		storageError(w, r, err)
		return
	}
	d.startReply(w, r, c, f, obj, id, token, in, ttl)
}

// completeDirect handles POST …/_files/{field}/uploads/{upload_id}/complete with
// {"upload_token": …}: it verifies what the client put in the bucket, and attaches
// it (to the document it was started for) or makes the pending upload ready.
func (d *documents) completeDirect(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return
	}
	svc := d.users(c.Realm)
	if svc == nil {
		notFound(w, r)
		return
	}
	obj, ok := d.objectsFor(w, r, c.Realm)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"upload_token"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStartBody))
	if err != nil || json.Unmarshal(data, &in) != nil || in.Token == "" {
		writeError(w, r, http.StatusBadRequest, codeValidation, "the body must be {\"upload_token\": …}", Detail{Path: "upload_token", Reason: "is required"})
		return
	}
	id := chi.URLParam(r, "uploadID")
	invalid := func() {
		writeError(w, r, http.StatusBadRequest, codeValidation, "this upload can't be completed", Detail{Path: "upload_id", Reason: uploadInvalid})
	}
	// Who: the token, and the user when a signed-in one started it.
	e, err := svc.UploadEntry(r.Context(), id)
	caller, _ := callerOf(r)
	if err != nil || !e.Direct || e.Database != c.Database || e.Collection != c.Name || e.Field != f.Name ||
		(e.Owner != "" && (caller.User == nil || caller.User.User.ID != e.Owner)) {
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			storageError(w, r, err)
			return
		}
		invalid()
		return
	}
	// An upload to a document answers who may touch the document before anything is
	// claimed or read from the bucket.
	a := d.accessFor(r)
	var filter storage.Filter
	if !e.Pending {
		if filter, ok = readFilter(w, r, a, c); !ok {
			return
		}
	}
	claimed, err := svc.ClaimDirectUpload(r.Context(), id, in.Token)
	if errors.Is(err, auth.ErrNotFound) {
		invalid()
		return
	}
	if err != nil {
		storageError(w, r, err)
		return
	}
	ctx, cancel := contextForUpload(r)
	defer cancel()
	key := claimed.Key
	// Until the file is verified the upload can be tried again; once it is refused the
	// object goes.
	retry := func() { _ = svc.ReleaseDirectUpload(ctx, id) }
	reject := func(status int, code, message string) {
		_ = svc.SetUploadStatus(ctx, id, auth.JournalFailed, "")
		if err := obj.Delete(ctx, key); err != nil {
			_ = svc.QueueFileDeletion(ctx, key, "rejected")
		}
		writeError(w, r, status, code, message)
	}

	info, err := obj.Head(ctx, key)
	switch {
	case errors.Is(err, storage.ErrObjectNotFound):
		retry()
		writeError(w, r, http.StatusConflict, codeFileNotUploaded, "no file has been uploaded to the link yet: PUT it, then complete")
		return
	case err != nil:
		retry()
		logger(r.Context()).Error("verify a direct upload", "realm", c.Realm, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		return
	}
	if info.Size != claimed.Size {
		reject(http.StatusUnprocessableEntity, codeUploadMismatch, "the uploaded file is "+strconv.FormatInt(info.Size, 10)+" bytes, not the declared "+strconv.FormatInt(claimed.Size, 10))
		return
	}
	// Only a checksum storage computed counts: a missing one is not a pass.
	if info.SHA256 == "" || info.SHA256 != claimed.SHA256 {
		reject(http.StatusUnprocessableEntity, codeUploadMismatch, "the uploaded file's SHA-256 is not the declared one")
		return
	}
	rc, _, err := obj.Get(ctx, key, "bytes=0-"+strconv.Itoa(sniffBytes-1))
	if err != nil {
		retry()
		logger(r.Context()).Error("read the start of a direct upload", "realm", c.Realm, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		return
	}
	head, _ := io.ReadAll(io.LimitReader(rc, sniffBytes))
	rc.Close()
	contentType := detectContentType(head, claimed.Type)
	if !f.Allows(contentType) {
		reject(http.StatusUnsupportedMediaType, codeUnsupportedFile, "this field doesn't accept "+contentType+" (detected from the file's content)")
		return
	}
	uploadedAt := d.timestamp().UTC()
	meta := map[string]any{"id": id, "name": claimed.Name, "size": claimed.Size, "type": contentType, "sha256": claimed.SHA256, "uploaded_at": uploadedAt.Format(timeFormat)}

	if claimed.Pending {
		if err := svc.CompletePendingUpload(ctx, id, claimed.Name, contentType, claimed.SHA256, claimed.Size, uploadedAt); err != nil {
			retry()
			storageError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"upload_id": id, "file": map[string]any{"name": claimed.Name, "size": claimed.Size, "type": contentType, "sha256": claimed.SHA256}})
		return
	}

	// An upload to a document: attach it, asking the rules about the real details.
	docID := claimed.DocumentID
	cond, err := parseIfMatch(r.Header.Values("If-Match"))
	if err != nil {
		retry()
		writeError(w, r, http.StatusBadRequest, codeInvalidHeader, err.Error())
		return
	}
	check := func() (map[string]any, bool) {
		return d.checkFileChange(w, r, repo, a, c, f, docID, filter, cond, meta)
	}
	abandon := func() {
		_ = svc.SetUploadStatus(ctx, id, auth.JournalFailed, "")
		_ = svc.QueueFileDeletion(ctx, key, "abandoned")
	}
	d.attachFile(w, r, attachJob{c: c, f: f, repo: repo, svc: svc, obj: obj, docID: docID, cond: cond, meta: meta, check: check, abandon: abandon, ctx: ctx,
		location: fileURL(r, docID, f.Name, id)})
}

// fileURL is the URL of a file of the document the request is about, built from the
// request's own path (which ends …/_files/{field}/uploads/{id}/complete).
func fileURL(r *http.Request, docID, field, fileID string) string {
	p := r.URL.Path
	if i := strings.Index(p, "/_files/"); i >= 0 {
		p = p[:i]
	}
	return p + "/" + url.PathEscape(docID) + "/_files/" + url.PathEscape(field) + "/" + url.PathEscape(fileID)
}
