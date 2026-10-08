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
	"github.com/fernandezvara/backd/internal/storage"
)

// reconcileMinAge is how old an object must be before reconcile may call it an orphan: a
// file being uploaded or attached right now is never one. Fixed, not configurable.
const reconcileMinAge = 24 * time.Hour

type reconcileOrphan struct {
	Key          string    `json:"key"`
	Database     string    `json:"database"`
	Collection   string    `json:"collection"`
	FileID       string    `json:"file_id"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// reconcileStorage answers POST /v1/{realm}/_admin/storage/reconcile with {"delete": bool}:
// it lists the realm's file objects (the only time the bucket is listed) and reports the
// ones no declared file field of any document references. With delete it removes exactly
// those. It always leaves alone objects younger than 24 hours, objects with a journal
// entry that isn't failed (uploads in flight, unexpired pending uploads, files a document
// holds) and anything outside <prefix>/<realm>/<database>/<collection>/.
func (a *adminAPI) reconcileStorage(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	var in struct {
		Delete bool `json:"delete"`
	}
	if data, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10)); len(data) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeError(w, r, http.StatusBadRequest, codeValidation, `the body must be {"delete": true|false}`)
			return
		}
	}
	obj, err := a.fns.docs.objects.For(r.Context(), realm)
	switch {
	case errors.Is(err, errNoStorage):
		writeError(w, r, http.StatusNotFound, codeNotFound, "this realm has no storage configured (storage: in realm.yaml)")
		return
	case errors.Is(err, errStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, err.Error())
		return
	case err != nil:
		adminError(w, r, err)
		return
	}
	svc := usersOf(r)
	st := a.reg.Realms[realm].Settings.Storage
	docs := a.fns.docs

	var rep struct {
		scanned, referenced, tooNew, inJournal, ignored, deleted int
		orphans                                                  []reconcileOrphan
		failed                                                   []string
	}
	prefix := st.Prefix + "/" + realm + "/"
	cutoff := svc.Clock().Add(-reconcileMinAge)
	var walkErr error
	err = obj.ListAll(r.Context(), prefix, func(o storage.ObjectInfo) bool {
		segs := strings.Split(strings.TrimPrefix(o.Key, prefix), "/")
		if strings.HasPrefix(segs[0], "_") { // _check, _logs…: not file areas
			return true
		}
		rep.scanned++
		// Only what backd names a file (<db>/<collection>/fl_…), or a version of one
		// (…/fl_…/<name>), is ever a candidate.
		if (len(segs) != 3 && len(segs) != 4) || !strings.HasPrefix(segs[2], "fl_") {
			rep.ignored++
			return true
		}
		database, collection, fileID := segs[0], segs[1], segs[2]
		versionName := ""
		if len(segs) == 4 {
			versionName = segs[3]
		}
		if o.LastModified.After(cutoff) {
			rep.tooNew++
			return true
		}
		if versionName != "" { // a version is kept while its file's details record it
			held, err := docs.versionHeld(r.Context(), realm, database, collection, fileID, versionName)
			if err != nil {
				walkErr = err
				return false
			}
			if held {
				rep.referenced++
			} else {
				rep.orphans = append(rep.orphans, reconcileOrphan{Key: o.Key, Database: database, Collection: collection, FileID: fileID, Size: o.Size, LastModified: o.LastModified.UTC()})
			}
			return true
		}
		entry, err := svc.UploadEntry(r.Context(), fileID)
		switch {
		case err == nil && entry.Status != auth.JournalFailed:
			rep.inJournal++
			return true
		case err != nil && !errors.Is(err, auth.ErrNotFound):
			walkErr = err
			return false
		}
		referenced, err := docs.fileReferenced(r.Context(), realm, database, collection, fileID)
		if err != nil {
			walkErr = err
			return false
		}
		if referenced {
			rep.referenced++
			return true
		}
		rep.orphans = append(rep.orphans, reconcileOrphan{Key: o.Key, Database: database, Collection: collection, FileID: fileID, Size: o.Size, LastModified: o.LastModified.UTC()})
		return true
	})
	if err == nil {
		err = walkErr
	}
	if err != nil {
		logger(r.Context()).Error("reconcile a realm's storage", "realm", realm, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "the storage could not be listed or the documents read: "+err.Error())
		return
	}
	if in.Delete {
		ctx := context.WithoutCancel(r.Context())
		for _, o := range rep.orphans {
			if err := obj.Delete(ctx, o.Key); err != nil {
				rep.failed = append(rep.failed, o.Key)
				continue
			}
			rep.deleted++
		}
	}
	svc.Audit(r.Context(), auth.AuditStorageReconcile, "storage", map[string]any{"delete": in.Delete, "orphans": len(rep.orphans), "deleted": rep.deleted})
	orphans := rep.orphans
	if orphans == nil {
		orphans = []reconcileOrphan{}
	}
	var bytes int64
	for _, o := range orphans {
		bytes += o.Size
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"delete": in.Delete, "scanned": rep.scanned, "referenced": rep.referenced, "skipped_recent": rep.tooNew, "skipped_in_journal": rep.inJournal,
		"ignored": rep.ignored, "orphans": orphans, "orphan_bytes": bytes, "deleted": rep.deleted, "failed": orFailed(rep.failed),
	})
}

func orFailed(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// fileReferenced reports whether any document of the collection (soft-deleted ones
// included) holds the file in one of the collection's declared file fields. A removed
// collection, or one without file fields, references nothing.
func (d *documents) fileReferenced(ctx context.Context, realm, database, collection, fileID string) (bool, error) {
	c, ok := d.reg.Collection(realm, database, collection)
	if !ok || len(c.Files) == 0 {
		return false, nil
	}
	repo := d.store.Repository(c)
	for _, name := range sortedKeys(c.Files) {
		page, err := repo.List(ctx, storage.Query{Filter: storage.Condition{Field: name + ".id", Op: storage.OpEq, Value: fileID}, Limit: 1})
		if err != nil {
			return false, err
		}
		if len(page.Items) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// versionHeld reports whether the document holding a file records the made version of
// that name, which is what keeps the version's object.
func (d *documents) versionHeld(ctx context.Context, realm, database, collection, fileID, name string) (bool, error) {
	c, ok := d.reg.Collection(realm, database, collection)
	if !ok || len(c.Files) == 0 {
		return false, nil
	}
	repo := d.store.Repository(c)
	for _, field := range sortedKeys(c.Files) {
		page, err := repo.List(ctx, storage.Query{Filter: storage.Condition{Field: field + ".id", Op: storage.OpEq, Value: fileID}, Limit: 1})
		if err != nil {
			return false, err
		}
		for _, doc := range page.Items {
			for _, f := range filesOf(c.Files[field], doc) {
				if f["id"] != fileID {
					continue
				}
				v := mapOf(mapOf(f["versions"])[name])
				return v != nil && v["status"] == versionReady, nil
			}
		}
	}
	return false, nil
}

// storageStatus answers GET /v1/{realm}/_admin/storage: how the realm's storage is
// configured (never its keys), whether it can be reached, what it holds (running totals
// for the realm and the users holding the most), and what waits to be cleaned up.
func (a *adminAPI) storageStatus(w http.ResponseWriter, r *http.Request) {
	realm := chi.URLParam(r, "realm")
	svc := usersOf(r)
	st := a.reg.Realms[realm].Settings.Storage
	if st == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	var public any
	if st.PublicEndpoint != "" {
		public = st.PublicEndpoint
	}
	out := map[string]any{
		"configured": true, "provider": st.Provider, "endpoint": st.Endpoint, "public_endpoint": public, "region": st.Region, "bucket": st.Bucket, "prefix": st.Prefix,
		"access_key": "secret:" + st.AccessKey, "secret_key": "secret:" + st.SecretKey,
		"download": st.Download, "presigned_ttl": dur(st.PresignedTTL), "pending_ttl": dur(st.PendingTTL),
	}
	// Whether it works: the keys are set, and the bucket answers.
	var keys, reach map[string]any
	obj, err := a.fns.docs.objects.For(r.Context(), realm)
	switch {
	case errors.Is(err, errStorageUnavailable):
		keys = map[string]any{"ok": false, "error": err.Error()}
		reach = map[string]any{"ok": false, "error": "not tried: the access keys aren't usable"}
	case err != nil:
		keys = map[string]any{"ok": false, "error": err.Error()}
		reach = map[string]any{"ok": false, "error": "not tried"}
	default:
		keys = map[string]any{"ok": true}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := obj.HeadBucket(ctx); err != nil {
			reach = map[string]any{"ok": false, "error": err.Error()}
		} else {
			reach = map[string]any{"ok": true}
		}
	}
	out["keys"], out["reachable"] = keys, reach

	usage, err := svc.StorageUsage(r.Context(), 20)
	if err != nil {
		adminError(w, r, err)
		return
	}
	users := make([]map[string]any, len(usage.Users))
	for i, u := range usage.Users {
		users[i] = map[string]any{"user_id": u.UserID, "bytes": u.Bytes, "files": u.Files}
	}
	out["usage"] = map[string]any{"bytes": usage.Realm.Bytes, "files": usage.Realm.Files, "users": users}
	// The limits usage is held to (null: none).
	quota := map[string]any{"realm": nil, "user": nil}
	if st.QuotaRealm > 0 {
		quota["realm"] = st.QuotaRealm
	}
	if st.QuotaUser > 0 {
		quota["user"] = st.QuotaUser
	}
	out["quota"] = quota

	q, err := svc.FileDeletionStats(r.Context())
	if err != nil {
		adminError(w, r, err)
		return
	}
	var oldest any
	if !q.Oldest.IsZero() {
		oldest = q.Oldest.UTC().Format(timeFormat)
	}
	out["deletions"] = map[string]any{"queued": q.Queued, "retrying": q.Retrying, "oldest": oldest}
	stale, err := svc.AbandonedUploads(r.Context(), 1000)
	if err != nil {
		adminError(w, r, err)
		return
	}
	out["stale_uploads"] = len(stale)
	writeJSON(w, http.StatusOK, out)
}
