package httpapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/imaging"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// Error codes of the file endpoints.
const (
	codeTooManyFiles       = "too_many_files"
	codeUploadModeMismatch = "upload_mode_mismatch"
	codeUnsupportedFile    = "unsupported_file_type"
	codeFileMissing        = "file_missing"
	codeVersionUnavailable = "version_unavailable"
	codeInvalidImage       = "invalid_image"
	codeInvalidFileLink    = "invalid_file_link"
)

// uploadTimeout bounds one upload or proxied download: a big file takes longer than
// the storage operations of an ordinary request.
const uploadTimeout = time.Hour

// mountFiles registers the file endpoints of a collection: the same shapes for
// single and multiple fields, so a client needs no field's cardinality to download
// or remove a file. There is no PUT.
func (d *documents) mountFiles(r chi.Router) {
	r.Get("/_files/{field}", d.fileFieldInfo)
	r.Post("/_files/{field}/uploads", d.uploadPending)
	r.Post("/_files/{field}/uploads/{uploadID}/complete", d.completeDirect)
	r.Post("/{id}/_files/{field}/uploads", d.startDirectForDocument)
	r.Post("/{id}/_files/{field}", d.uploadFile)
	r.Delete("/{id}/_files/{field}", d.clearFiles)
	r.Get("/{id}/_files/{field}/{fileID}", d.downloadFile)
	r.Delete("/{id}/_files/{field}/{fileID}", d.deleteFile)
	r.Post("/{id}/_files/{field}/{fileID}/versions/{version}", d.makeVersion)
	r.Delete("/{id}/_files/{field}/{fileID}/versions/{version}", d.deleteVersion)
}

// fileField looks up the request's file field, or answers 404.
func fileField(w http.ResponseWriter, r *http.Request, c *registry.Collection) *registry.FileField {
	f := c.Files[chi.URLParam(r, "field")]
	if f == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "this collection has no such file field")
	}
	return f
}

// fileFieldInfo answers GET …/_files/{field}: how the field takes files, so an app can
// choose between a proxy and a direct upload and check a file before sending it. It is
// the field's configuration, nothing about any document.
func (d *documents) fileFieldInfo(w http.ResponseWriter, r *http.Request) {
	c, _ := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return
	}
	st := d.reg.Realms[c.Realm].Settings.Storage
	if st == nil {
		notFound(w, r)
		return
	}
	maxFiles := 1
	if f.Multiple {
		maxFiles = f.MaxFiles
	}
	maxSize := f.MaxSize
	if f.Upload == registry.UploadProxy {
		maxSize = min(maxSize, d.maxUpload)
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{
		"name": f.Name, "upload": f.Upload, "download": downloadMode(f, st), "multiple": f.Multiple,
		"max_files": maxFiles, "max_size": maxSize, "types": orStrings(f.Types),
	})
}

// objectsFor returns the realm's storage connection or answers for it: 503
// storage_unavailable when the keys can't be used.
func (d *documents) objectsFor(w http.ResponseWriter, r *http.Request, realm string) (*storage.Objects, bool) {
	obj, err := d.objects.For(r.Context(), realm)
	switch {
	case err == nil:
		return obj, true
	case errors.Is(err, errNoStorage):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no storage configured")
	case errors.Is(err, errStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		logger(r.Context()).Error("file storage unavailable", "realm", realm, "error", err)
	default:
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		logger(r.Context()).Error("file storage", "realm", realm, "error", err)
	}
	return nil, false
}

// filesOf returns the files a stored document holds in a field: one for a single
// field, any number for a multiple one.
func filesOf(f *registry.FileField, doc map[string]any) []map[string]any {
	switch v := doc[f.Name].(type) {
	case map[string]any:
		return []map[string]any{v}
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// withFiles is doc's user fields with the field holding files: the data an
// operation would leave, which the update rule is asked about.
func withFiles(f *registry.FileField, doc map[string]any, files []map[string]any) map[string]any {
	out := userFields(doc)
	switch {
	case len(files) == 0:
		delete(out, f.Name)
	case f.Multiple:
		list := make([]any, len(files))
		for i, e := range files {
			list[i] = e
		}
		out[f.Name] = list
	default:
		out[f.Name] = files[0]
	}
	return out
}

// uploadState is what an upload looked at before it stored anything.
type uploadPlan struct {
	name  string
	id    string
	known map[string]any // what is known before the bytes: id, name, size (Content-Length) and nothing else
	quota quota          // how many bytes the owner's quota still leaves (room -1: no quota)
}

// uploadFile handles POST …/{id}/_files/{field}: it adds a file (replacing the one
// of a single field). The update rule runs before a single byte is stored, with
// `data` the document as it will be; the body is then streamed to storage, its size
// checked as it passes and its type detected from the first bytes, and the document
// is updated conditionally, so a concurrent change never loses either.
func (d *documents) uploadFile(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return
	}
	if f.Upload != registry.UploadProxy {
		writeError(w, r, http.StatusConflict, codeUploadModeMismatch, "this field takes direct uploads: start one with POST …/{id}/_files/"+f.Name+"/uploads")
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
	plan := uploadPlan{name: sanitizeFileName(requestedFileName(r)), id: "fl_" + xid.New().String()}
	plan.known = map[string]any{"id": plan.id, "name": plan.name, "size": nil, "type": nil, "sha256": nil, "uploaded_at": nil}
	if r.ContentLength >= 0 {
		plan.known["size"] = r.ContentLength
	}
	limit := min(f.MaxSize, d.maxUpload)

	// Before any byte: the document is read (a document the caller can't read is a
	// 404), its version checked, and the update rule asked about the result.
	check := func() (current map[string]any, ok bool) {
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
		var next []map[string]any
		if f.Multiple {
			next = append(slices.Clone(existing), plan.known)
		} else {
			next = []map[string]any{plan.known}
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
	current, ok := check()
	if !ok {
		return
	}
	if r.ContentLength > limit {
		tooLarge(w, r, limit)
		return
	}
	// The quota of whoever owns the document; a single field's old file is freed by the replace.
	var freed int64
	if !f.Multiple {
		freed = fileSizes(filesOf(f, current))
	}
	if plan.quota, ok = d.quotaPlan(w, r, c, docOwner(current), freed, r.ContentLength); !ok {
		return
	}

	up, ok := d.storeUpload(w, r, c, f, svc, obj, plan, auth.FileJournalEntry{DocumentID: docID})
	if !ok {
		return
	}
	defer up.cancel()
	ctx, meta, abandon := up.ctx, up.meta, up.abandon

	d.attachFile(w, r, attachJob{c: c, f: f, repo: repo, svc: svc, obj: obj, docID: docID, cond: cond, meta: meta, check: check, abandon: abandon, ctx: ctx,
		location: r.URL.JoinPath(url.PathEscape(plan.id)).Path})
}

// attachJob is a stored file waiting to be attached to a document.
type attachJob struct {
	c       *registry.Collection
	f       *registry.FileField
	repo    storage.Repository
	svc     *auth.Users
	obj     *storage.Objects
	docID   string
	cond    ifMatch
	meta    map[string]any                           // the file's details
	check   func() (current map[string]any, ok bool) // reads the document and asks the rules; answers when it refuses
	abandon func()                                   // the file will not be attached: queue its object for deletion
	ctx     context.Context
	// location is the file's URL, answered with the 201.
	location string
}

// attachFile updates the document with the file, conditionally on the version
// read; a concurrent change retries (with the rules asked again), up to three
// times. It answers the request; on any failure the file is abandoned.
func (d *documents) attachFile(w http.ResponseWriter, r *http.Request, j attachJob) {
	c, f, repo, svc, docID, cond, meta, check, abandon, ctx := j.c, j.f, j.repo, j.svc, j.docID, j.cond, j.meta, j.check, j.abandon, j.ctx
	plan := struct{ id string }{id: meta["id"].(string)}
	// Attach it: the document is updated conditionally on the version read; a
	// concurrent change retries (with the rule asked again), up to three times.
	for attempt := range maxWriteAttempts {
		if attempt > 0 && !backoff(ctx, attempt) {
			abandon()
			storageError(w, r, ctx.Err())
			return
		}
		current, ok := check()
		if !ok {
			abandon()
			return
		}
		read := version(current)
		existing := filesOf(f, current)
		var next []map[string]any
		if f.Multiple {
			next = append(slices.Clone(existing), meta)
		} else {
			next = []map[string]any{meta}
		}
		fields := withFiles(f, current, next)
		stored, _ := current["_meta"].(map[string]any)
		docMeta := map[string]any{"created_at": stored["created_at"], "updated_at": d.timestamp(), "version": read + 1}
		stampUpdate(r, stored, docMeta)
		fields["id"] = current["id"]
		fields["_meta"] = docMeta
		err := repo.Replace(ctx, fields, read)
		switch {
		case err == nil:
			_ = svc.SetUploadStatus(ctx, plan.id, auth.JournalAttached, docID)
			d.fileAdded(ctx, c, docID, f.Name, docOwner(current), meta)
			if !f.Multiple { // the file this one replaced
				d.filesGone(ctx, c, docOwner(current), existing, "replaced")
			}
			d.auditData(r, auth.AuditDataUpdate, c, docID)
			w.Header().Set("Location", j.location)
			writeDocument(w, http.StatusCreated, fields)
			return
		case errors.Is(err, storage.ErrVersionMismatch) && cond.specific():
			abandon()
			writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; If-Match no longer matches")
			return
		case errors.Is(err, storage.ErrVersionMismatch):
			logger(r.Context()).Debug("write conflict attaching a file, retrying", "id", docID, "version", read)
		default:
			abandon()
			storageError(w, r, err)
			return
		}
	}
	abandon()
	writeError(w, r, http.StatusConflict, codeWriteConflict, "document is being modified concurrently; retry the request")
}

type countingReader struct {
	r io.Reader
	n *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// removeFiles takes files out of a document: one by id, or (fileID "") every one of
// the field. The update rule is asked about the result; the objects are queued for
// deletion once the document no longer references them.
func (d *documents) removeFiles(w http.ResponseWriter, r *http.Request, fileID string) {
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
	if _, ok := d.objectsFor(w, r, c.Realm); !ok {
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
	for attempt := range maxWriteAttempts {
		if attempt > 0 && !backoff(r.Context(), attempt) {
			storageError(w, r, r.Context().Err())
			return
		}
		current, err := storage.Fetch(r.Context(), repo, docID, filter)
		if err != nil {
			storageError(w, r, err)
			return
		}
		read := version(current)
		if !cond.matches(read) {
			versionMismatch(w, r, read)
			return
		}
		existing := filesOf(f, current)
		var kept, removed []map[string]any
		for _, e := range existing {
			if id, _ := e["id"].(string); fileID == "" || id == fileID {
				removed = append(removed, e)
			} else {
				kept = append(kept, e)
			}
		}
		if fileID != "" && len(removed) == 0 {
			writeError(w, r, http.StatusNotFound, codeNotFound, "file not found")
			return
		}
		fields := withFiles(f, current, kept)
		if !allowWrite(w, r, a, c, rules.Update, current, fields) {
			return
		}
		stored, _ := current["_meta"].(map[string]any)
		docMeta := map[string]any{"created_at": stored["created_at"], "updated_at": d.timestamp(), "version": read + 1}
		stampUpdate(r, stored, docMeta)
		fields["id"] = current["id"]
		fields["_meta"] = docMeta
		err = repo.Replace(r.Context(), fields, read)
		switch {
		case err == nil:
			reason := "removed"
			if fileID == "" {
				reason = "cleared"
			}
			d.filesGone(r.Context(), c, docOwner(current), removed, reason)
			d.auditData(r, auth.AuditDataUpdate, c, docID)
			writeDocument(w, http.StatusOK, fields)
			return
		case errors.Is(err, storage.ErrVersionMismatch) && cond.specific():
			writeError(w, r, http.StatusPreconditionFailed, codeVersionMismatch, "document was modified concurrently; If-Match no longer matches")
			return
		case errors.Is(err, storage.ErrVersionMismatch):
		default:
			storageError(w, r, err)
			return
		}
	}
	writeError(w, r, http.StatusConflict, codeWriteConflict, "document is being modified concurrently; retry the request")
}

func (d *documents) deleteFile(w http.ResponseWriter, r *http.Request) {
	d.removeFiles(w, r, chi.URLParam(r, "fileID"))
}

func (d *documents) clearFiles(w http.ResponseWriter, r *http.Request) { d.removeFiles(w, r, "") }

// linkKeySecret is the realm secret holding the key that signs backd's own links to
// files. backd makes it the first time it is needed; rotating the key (setting or
// deleting the secret) invalidates every outstanding link.
const linkKeySecret = registry.FilesLinkKeySecret

// linkKey returns the realm's link-signing key, making it when the realm has none.
func (d *documents) linkKey(ctx context.Context, realm string) ([]byte, error) {
	svc := d.users(realm)
	if svc == nil {
		return nil, errNoStorage
	}
	ref := []registry.SecretRef{{Realm: true, Name: linkKeySecret}}
	values, missing, err := svc.ResolveSecrets(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		if err := svc.SetSecret(ctx, "", linkKeySecret, hex.EncodeToString(raw), "backd:files"); err != nil {
			return nil, err
		}
		// Read it back: another instance may have made one at the same time.
		if values, missing, err = svc.ResolveSecrets(ctx, ref, ""); err != nil || len(missing) > 0 {
			return nil, errors.Join(err, errors.New("the files link key could not be read back"))
		}
	}
	return []byte(values["realm."+linkKeySecret]), nil
}

// downloadMode is how a field's files are delivered: its own setting, else the realm's.
func downloadMode(f *registry.FileField, st *registry.StorageSettings) string {
	return firstNonEmpty(f.Download, st.Download)
}

func linkTTL(f *registry.FileField, st *registry.StorageSettings) time.Duration {
	if f.PresignedTTL > 0 {
		return f.PresignedTTL
	}
	return st.PresignedTTL
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// errNoPublicURL: a backd-signed link made on the internal listener (for a function)
// has no address clients could use unless BACKD_URL says which.
var errNoPublicURL = errors.New("backd's public address is unknown: set BACKD_URL")

// linkError answers a link that couldn't be made.
func linkError(w http.ResponseWriter, r *http.Request, err error) {
	logger(r.Context()).Error("make a file link", "error", err)
	message := "file storage is not available"
	if errors.Is(err, errNoPublicURL) {
		message = "this link is made for an address clients can reach, which isn't known: set BACKD_URL"
	}
	writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, message)
}

// publicBase is the address of backd as clients reach it, for the links backd signs.
func (d *documents) publicBase(r *http.Request) string {
	if d.baseURL != "" {
		return strings.TrimSuffix(d.baseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// fileLink makes a link to download a file without credentials, that works until it
// expires: a storage-signed link in presigned mode, a backd-signed one in proxy mode.
func (d *documents) fileLink(ctx context.Context, r *http.Request, obj *storage.Objects, c *registry.Collection, f *registry.FileField, docID string, tg fileTarget) (link string, expires time.Time, err error) {
	st := d.reg.Realms[c.Realm].Settings.Storage
	ttl := linkTTL(f, st)
	if downloadMode(f, st) == registry.DownloadPresigned {
		l, err := obj.PresignGet(ctx, tg.key, ttl, tg.name, tg.contentType)
		return l.URL, l.ExpiresAt, err
	}
	key, err := d.linkKey(ctx, c.Realm)
	if err != nil {
		return "", time.Time{}, err
	}
	if d.internal && d.baseURL == "" {
		return "", time.Time{}, errNoPublicURL
	}
	expires = d.now().Add(ttl)
	sig := fileLinkMAC(key, c.Realm, c.Database, c.Name, docID, f.Name, tg.macID(), expires.Unix())
	path := "/v1/" + url.PathEscape(c.Realm) + "/" + url.PathEscape(c.Database) + "/" + url.PathEscape(c.Name) + "/" + url.PathEscape(docID) + "/_files/" + url.PathEscape(f.Name) + "/" + url.PathEscape(tg.fileID)
	link = d.publicBase(r) + path + "?exp=" + strconv.FormatInt(expires.Unix(), 10) + "&sig=" + sig
	if tg.version != "" {
		link += "&version=" + url.QueryEscape(tg.version)
	}
	return link, expires, nil
}

// fileTarget is what a download serves: a file's original, or one of its made versions.
type fileTarget struct {
	fileID      string
	version     string // "" for the original
	key         string // the object's key
	name        string // the name a download is saved under
	contentType string
	etag        string
	size        int64
}

// macID is what a backd-signed link names as its file: a version's link is bound to
// that version, so changing ?version= on it invalidates the signature.
func (t fileTarget) macID() string {
	if t.version == "" {
		return t.fileID
	}
	return t.fileID + "/" + t.version
}

// sourceTarget is the original of a file.
func (d *documents) sourceTarget(c *registry.Collection, file map[string]any) fileTarget {
	fileID, _ := file["id"].(string)
	name, _ := file["name"].(string)
	ct, _ := file["type"].(string)
	sha, _ := file["sha256"].(string)
	return fileTarget{fileID: fileID, key: d.objectKey(c, fileID), name: name, contentType: ct, etag: `"` + sha + `"`, size: fileSize(file)}
}

// versionTarget is a made version of a file, or false with the reason it can't be served.
func (d *documents) versionTarget(c *registry.Collection, f *registry.FileField, file map[string]any, version string) (fileTarget, map[string]any, bool) {
	src := d.sourceTarget(c, file)
	if _, declared := f.Version(version); !declared {
		return src, map[string]any{"status": "undeclared", "reason": "the field declares no version " + version}, false
	}
	state := mapOf(mapOf(file["versions"])[version])
	if state == nil || state["status"] != versionReady {
		out := map[string]any{"status": "unknown"}
		if state != nil {
			out["status"] = state["status"]
			if reason, ok := state["reason"]; ok {
				out["reason"] = reason
			}
		}
		return src, out, false
	}
	id, _ := state["id"].(string)
	ct, _ := state["type"].(string)
	return fileTarget{
		fileID: src.fileID, version: version, key: src.key + "/" + version, contentType: ct, etag: `"` + id + `"`, size: fileSize(state),
		name: versionFileName(src.name, version, ct),
	}, nil, true
}

// versionDetails say why a version can't be served: its status and, when it has one, the reason.
func versionDetails(state map[string]any) []Detail {
	out := []Detail{{Path: "status", Reason: fmt.Sprint(state["status"])}}
	if reason, ok := state["reason"]; ok {
		out = append(out, Detail{Path: "reason", Reason: fmt.Sprint(reason)})
	}
	return out
}

// versionFileName is the name a version is saved under: the original's, with the
// version's name and the extension of its type.
func versionFileName(name, version, contentType string) string {
	stem := strings.TrimSuffix(name, path.Ext(name))
	ext := ".jpg"
	if contentType == "image/png" {
		ext = ".png"
	}
	return stem + "-" + version + ext
}

// anonymousReadable reports whether the read rule lets anyone, signed in or not,
// read the document: only then may a proxied download be cached by shared caches.
func (d *documents) anonymousReadable(c *registry.Collection, doc map[string]any) bool {
	rule := c.Rules.For(rules.Read)
	if rule == nil {
		return false
	}
	ok, err := rule.Allow(rules.Values{Now: d.now().UTC(), Document: doc})
	return err == nil && ok
}

// downloadFile handles GET …/{id}/_files/{field}/{file_id}. By default it redirects
// to a storage-signed link; with download: proxy it streams the file. A backd-signed
// link (?exp&sig) is its own credential. ?link=json answers the link instead.
func (d *documents) downloadFile(w http.ResponseWriter, r *http.Request) {
	c, repo := d.collection(r)
	f := fileField(w, r, c)
	if f == nil {
		return
	}
	st := d.reg.Realms[c.Realm].Settings.Storage
	if st == nil {
		notFound(w, r)
		return
	}
	docID, fileID := chi.URLParam(r, "id"), chi.URLParam(r, "fileID")
	q := r.URL.Query()
	signed := q.Has("sig") || q.Has("exp")
	version := q.Get("version")
	macID := fileID
	if version != "" {
		macID += "/" + version
	}

	var doc map[string]any
	if signed {
		key, err := d.linkKey(r.Context(), c.Realm)
		if err != nil {
			logger(r.Context()).Error("files link key", "realm", c.Realm, "error", err)
			writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
			return
		}
		if !verifyFileLink(key, c.Realm, c.Database, c.Name, docID, f.Name, macID, q.Get("exp"), q.Get("sig"), d.now()) {
			writeError(w, r, http.StatusForbidden, codeInvalidFileLink, "this link is invalid or has expired: ask for a new one")
			return
		}
		// The read rule was checked when the link was made; the document must still be there.
		got, err := repo.Get(r.Context(), docID)
		if err != nil {
			storageError(w, r, err)
			return
		}
		if meta, _ := got["_meta"].(map[string]any); c.SoftDelete != nil && meta["deleted_at"] != nil {
			notFound(w, r)
			return
		}
		doc = got
	} else {
		a := d.accessFor(r)
		filter, ok := readFilter(w, r, a, c)
		if !ok {
			return
		}
		got, err := storage.Fetch(r.Context(), repo, docID, filter)
		if err != nil {
			storageError(w, r, err) // a document the caller can't read is a 404, never revealing its files
			return
		}
		doc = got
	}
	var file map[string]any
	for _, e := range filesOf(f, doc) {
		if id, _ := e["id"].(string); id == fileID {
			file = e
		}
	}
	if file == nil {
		writeError(w, r, http.StatusNotFound, codeNotFound, "file not found")
		return
	}
	tg := d.sourceTarget(c, file)
	if version != "" {
		var why map[string]any
		var ok bool
		if tg, why, ok = d.versionTarget(c, f, file, version); !ok {
			writeError(w, r, http.StatusNotFound, codeVersionUnavailable, "this version of the file is not available", versionDetails(why)...)
			return
		}
	}
	obj, ok := d.objectsFor(w, r, c.Realm)
	if !ok {
		return
	}

	if q.Get("link") == "json" && !signed {
		l, exp, err := d.fileLink(r.Context(), r, obj, c, f, docID, tg)
		if err != nil {
			linkError(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"url": l, "expires_at": exp.UTC().Format(timeFormat)})
		return
	}
	// A function reads the bytes through backd: it may not reach the bucket.
	if !signed && !d.internal && downloadMode(f, st) == registry.DownloadPresigned {
		l, _, err := d.fileLink(r.Context(), r, obj, c, f, docID, tg)
		if err != nil {
			logger(r.Context()).Error("make a file link", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Location", l)
		w.WriteHeader(http.StatusFound)
		return
	}
	d.proxyFile(w, r, obj, c, f, doc, tg)
}

// proxyFile streams a file from storage with its own headers: a download (never
// shown inline), not sniffed, sandboxed; strong ETag from the SHA-256 (304 on a
// match); and single-range requests answered with 206. Shared caches may keep it
// only when anyone may read the document.
func (d *documents) proxyFile(w http.ResponseWriter, r *http.Request, obj *storage.Objects, c *registry.Collection, f *registry.FileField, doc map[string]any, tg fileTarget) {
	fileID, name, ct, size, etag := tg.fileID, tg.name, tg.contentType, tg.size, tg.etag
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", attachment(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", etag)
	if f.Cache > 0 && d.anonymousReadable(c, doc) {
		h.Set("Cache-Control", "public, max-age="+strconv.Itoa(int(f.Cache.Seconds())))
	} else {
		h.Set("Cache-Control", "private, no-store")
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rng := ""
	status := http.StatusOK
	length := size
	if hv := r.Header.Get("Range"); hv != "" && (r.Header.Get("If-Range") == "" || r.Header.Get("If-Range") == etag) {
		switch start, end, kind := parseRange(hv, size); kind {
		case rangeOK:
			rng = "bytes=" + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10)
			status, length = http.StatusPartialContent, end-start+1
			h.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
		case rangeUnsatisfiable:
			h.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			writeError(w, r, http.StatusRequestedRangeNotSatisfiable, "range_not_satisfiable", "the requested range is outside the file")
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), uploadTimeout)
	defer cancel()
	body, _, err := obj.Get(ctx, tg.key, rng)
	if errors.Is(err, storage.ErrObjectNotFound) {
		h.Del("Content-Range")
		writeError(w, r, http.StatusNotFound, codeFileMissing, "the file is no longer in storage")
		logger(r.Context()).Error("a referenced file is missing from storage", "realm", c.Realm, "collection", c.Name, "file", fileID)
		return
	}
	if err != nil {
		logger(r.Context()).Error("read a file", "error", err)
		writeError(w, r, http.StatusBadGateway, codeStorageUnavailable, "the file could not be read")
		return
	}
	defer body.Close()
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(status)
	_, _ = io.Copy(w, body)
}

func etagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(p)
		if p == "*" || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}

type rangeKind int

const (
	rangeIgnored rangeKind = iota // not a single range: the whole file is sent
	rangeOK
	rangeUnsatisfiable
)

// parseRange reads a single-range Range header ("bytes=a-b", "a-", "-n") against a
// file of size bytes. Anything else (several ranges, another unit) is ignored.
func parseRange(h string, size int64) (start, end int64, kind rangeKind) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, 0, rangeIgnored
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, rangeIgnored
	}
	switch {
	case a == "" && b != "": // the last n bytes
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n < 1 {
			return 0, 0, rangeIgnored
		}
		if size == 0 {
			return 0, 0, rangeUnsatisfiable
		}
		return max(size-n, 0), size - 1, rangeOK
	case a != "":
		s, err := strconv.ParseInt(a, 10, 64)
		if err != nil || s < 0 {
			return 0, 0, rangeIgnored
		}
		e := size - 1
		if b != "" {
			if e2, err := strconv.ParseInt(b, 10, 64); err != nil || e2 < s {
				return 0, 0, rangeIgnored
			} else {
				e = min(e2, size-1)
			}
		}
		if s >= size {
			return 0, 0, rangeUnsatisfiable
		}
		return s, e, rangeOK
	}
	return 0, 0, rangeIgnored
}

// attachment is a Content-Disposition that makes a browser save the file under its name.
func attachment(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// addFileLinks puts a link (`url`, `expires_at`) in every file of the given rendered
// documents when the request asks for them (?file_links=true). A link is made per
// response, never stored, and has no part in the document's ETag; making one is a
// local calculation, so a list carries them cheaply. It answers the request and
// returns false when it can't. When links can't be made (the storage's keys aren't set,
// or the public address isn't known) the documents are answered without them, and
// the reason is logged: reading a document never fails because its files' storage is
// down; a download then says why.
func (d *documents) addFileLinks(w http.ResponseWriter, r *http.Request, c *registry.Collection, docs []map[string]any) bool {
	if r.URL.Query().Get("file_links") != "true" || len(c.Files) == 0 {
		return true
	}
	obj, err := d.objects.For(r.Context(), c.Realm)
	if err != nil {
		logger(r.Context()).Warn("file links left out: the realm's storage is not available", "realm", c.Realm, "error", err)
		return true
	}
	for _, doc := range docs {
		docID, _ := doc["id"].(string)
		for _, f := range c.Files {
			files := filesOf(f, doc)
			if len(files) == 0 {
				continue
			}
			linked := make([]map[string]any, len(files))
			for i, file := range files {
				cp := make(map[string]any, len(file)+2)
				for k, v := range file {
					cp[k] = v
				}
				l, exp, err := d.fileLink(r.Context(), r, obj, c, f, docID, d.sourceTarget(c, file))
				if err != nil {
					logger(r.Context()).Warn("file links left out", "realm", c.Realm, "error", err)
					return true
				}
				cp["url"], cp["expires_at"] = l, exp.UTC().Format(timeFormat)
				// The versions made of an image get a link too, to show right away.
				if versions := mapOf(file["versions"]); versions != nil {
					linkedVersions := make(map[string]any, len(versions))
					for name, v := range versions {
						state := maps.Clone(mapOf(v))
						if vt, _, ok := d.versionTarget(c, f, file, name); ok && state != nil {
							vl, vexp, err := d.fileLink(r.Context(), r, obj, c, f, docID, vt)
							if err != nil {
								logger(r.Context()).Warn("file links left out", "realm", c.Realm, "error", err)
								return true
							}
							state["url"], state["expires_at"] = vl, vexp.UTC().Format(timeFormat)
						}
						linkedVersions[name] = state
					}
					cp["versions"] = linkedVersions
				}
				linked[i] = cp
			}
			if f.Multiple {
				list := make([]any, len(linked))
				for i, e := range linked {
					list[i] = e
				}
				doc[f.Name] = list
			} else {
				doc[f.Name] = linked[0]
			}
		}
	}
	return true
}

// storedUpload is a file that was streamed to storage and not yet attached to a
// document: its details, and what to do if it never is.
type storedUpload struct {
	ctx     context.Context // outlives the request: the work that follows must finish
	cancel  context.CancelFunc
	key     string
	meta    map[string]any // the file's details, as a document holds them
	abandon func()         // the upload failed or is not used: queue the object for deletion
}

// storeUpload streams the request's body to the field's storage: the first bytes
// decide the type (refused before anything is stored), the size is checked as the
// bytes pass, and the upload is journaled before the first one is stored. template
// carries what the caller knows about the journal entry (the document, or the
// pending upload's token). It answers and returns false when it can't.
func (d *documents) storeUpload(w http.ResponseWriter, r *http.Request, c *registry.Collection, f *registry.FileField, svc *auth.Users, obj *storage.Objects, plan uploadPlan, template auth.FileJournalEntry) (*storedUpload, bool) {
	limit := min(f.MaxSize, d.maxUpload)
	// A quota with less room than the field allows is the limit, and what passes it is a
	// quota error rather than a size one.
	quotaLimited := plan.quota.room >= 0 && plan.quota.room < limit
	if quotaLimited {
		limit = plan.quota.room
	}
	over := func() {
		if quotaLimited {
			d.quotaExceeded(w, r, c, plan.quota.which)
			return
		}
		tooLarge(w, r, limit)
	}

	// The first bytes decide the type, before anything is stored.
	body := http.MaxBytesReader(w, r.Body, limit)
	headSize := sniffBytes
	bufSize := 4096
	if readsImages(f) {
		headSize, bufSize = imageHeadBytes, imageHeadBytes // the start of a picture tells its size
	}
	br := bufio.NewReaderSize(body, bufSize)
	head, peekErr := br.Peek(headSize)
	var tooBig *http.MaxBytesError
	if errors.As(peekErr, &tooBig) {
		over()
		return nil, false
	}
	contentType := detectContentType(head[:min(len(head), sniffBytes)], r.Header.Get("Content-Type"))
	if !f.Allows(contentType) {
		writeError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedFile, "this field doesn't accept "+contentType+" (detected from the file's content)")
		return nil, false
	}
	var image map[string]any
	if len(f.Versions) > 0 || strings.HasPrefix(contentType, "image/") {
		image = imageDetails(f, head, d.imageMaxPixels)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), uploadTimeout)
	key := obj.Key(c.Realm, c.Database, c.Name, plan.id)
	caller, _ := callerOf(r)
	entry := template
	entry.ID, entry.Database, entry.Collection, entry.Field, entry.Key, entry.Caller, entry.Size = plan.id, c.Database, c.Name, f.Name, key, caller.Subject(), max(r.ContentLength, 0)
	var err error
	if entry.Pending {
		err = svc.JournalPendingUpload(ctx, entry, d.pendingTTL(c))
	} else {
		err = svc.JournalUpload(ctx, entry)
	}
	if err != nil {
		cancel()
		storageError(w, r, err)
		return nil, false
	}
	// From here an object may exist: any failure queues it for deletion.
	abandon := func() {
		_ = svc.SetUploadStatus(ctx, plan.id, auth.JournalFailed, "")
		_ = svc.QueueFileDeletion(ctx, key, "abandoned")
	}

	// A JPEG or PNG is stored without its metadata (the GPS position of a photograph, the
	// camera, comments), copied byte for byte otherwise; what is hashed and counted is what
	// is stored.
	var source io.Reader = br
	var stripped chan error
	closeSource := func() {}
	if format := metadataFormat(contentType); format != "" && !f.KeepMetadata {
		pr, pw := io.Pipe()
		stripped = make(chan error, 1)
		go func() {
			err := imaging.Strip(pw, br, format)
			pw.CloseWithError(err)
			if err == nil { // what follows the end of the picture is dropped, but counts against the size limit
				_, err = io.Copy(io.Discard, br)
			}
			stripped <- err
		}()
		closeSource = func() { _ = pr.Close() }
		defer closeSource()
		source = pr
	}
	hasher := sha256.New()
	var size int64
	stream := io.TeeReader(&countingReader{r: source, n: &size}, hasher)
	if err := obj.PutStream(ctx, key, stream, contentType); err != nil {
		abandon()
		cancel()
		if stripped != nil {
			closeSource() // the copy may be blocked on a reader that has gone
			if serr := <-stripped; errors.Is(serr, imaging.ErrUnreadable) {
				writeError(w, r, http.StatusUnprocessableEntity, codeInvalidImage, "the image can't be read to remove its metadata: it is damaged (or set keep_metadata: true for this field)")
				return nil, false
			}
		}
		if errors.As(err, &tooBig) || size > limit {
			over()
			return nil, false
		}
		logger(r.Context()).Error("store a file", "realm", c.Realm, "collection", c.Name, "field", f.Name, "error", err)
		writeError(w, r, http.StatusBadGateway, codeStorageUnavailable, "the file could not be stored")
		return nil, false
	}
	if stripped != nil {
		if serr := <-stripped; serr != nil { // the rest of the body could not be read to its end
			abandon()
			cancel()
			if errors.As(serr, &tooBig) {
				over()
				return nil, false
			}
			writeError(w, r, http.StatusBadGateway, codeStorageUnavailable, "the file could not be read")
			return nil, false
		}
	}
	if size > limit {
		abandon()
		cancel()
		over()
		return nil, false
	}
	uploadedAt := d.timestamp().UTC()
	sum := hex.EncodeToString(hasher.Sum(nil))
	if entry.Pending {
		_ = svc.CompletePendingUpload(ctx, plan.id, plan.name, contentType, sum, size, uploadedAt, imageJSON(image))
	} else {
		_ = svc.SetUploadStatus(ctx, plan.id, auth.JournalStored, "")
	}

	meta := map[string]any{"id": plan.id, "name": plan.name, "size": size, "type": contentType, "sha256": sum, "uploaded_at": uploadedAt.Format(timeFormat)}
	maps.Copy(meta, image)
	return &storedUpload{ctx: ctx, cancel: cancel, key: key, meta: meta, abandon: abandon}, true
}
