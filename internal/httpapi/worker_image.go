package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/imaging"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

const (
	// imageAttempts is how many times the making of a file's versions is tried when the
	// storage or the database fail, before its pending versions are marked failed.
	imageAttempts = 5
	// imageWriteAttempts is how many times the document is re-read when a client changes
	// it while the versions are being recorded.
	imageWriteAttempts = 5
	// reasonStorage is why versions failed when the storage kept failing.
	reasonStorage = "storage_error"
)

// A version this worker made and stored, waiting to be recorded in the document.
type madeVersion struct {
	name   string
	id     string // fv_…
	key    string
	result *imaging.Result
	params map[string]any
}

// runImage makes the pending versions of one file: it reads the document and the file's
// object, decodes the image once, makes every pending version, stores each beside the
// source under <file key>/<version name> and records the details in the document. It
// writes the document without changing its version, so a client editing it is never
// told it conflicts. Safe to repeat: a file that is gone, or whose versions are no
// longer pending, ends the job at once.
func (w *Worker) runImage(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	e := job.Image
	log = log.With("file", e.FileID)
	finish := func(summary map[string]int) {
		out, _ := json.Marshal(summary)
		if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: "ok", Output: out}); err != nil {
			log.Error("complete the image job", "error", err)
			return
		}
		w.fns.metrics.JobFinished(svc.Realm, "image", "ok")
	}
	c, ok := w.reg.Collection(realm, job.Database, e.Collection)
	f := (*registry.FileField)(nil)
	if ok {
		f = c.Files[e.Field]
	}
	if f == nil { // the configuration changed since the job was queued
		finish(map[string]int{})
		return
	}
	again := func(reason string, err error) {
		failed := job.Failures + 1
		if failed < imageAttempts {
			wait := min(30*time.Second<<(failed-1), 10*time.Minute)
			log.Warn("making image versions failed; it will be tried again", "attempt", failed, "of", imageAttempts, "retry_in", wait.String(), "reason", reason, "error", err)
			if rerr := svc.RetryJob(ctx, job.ID, wait); rerr != nil {
				log.Error("queue the retry", "error", rerr)
			} else {
				w.fns.metrics.JobRetried(svc.Realm, "image")
			}
			return
		}
		log.Error("attempts used up; the pending versions fail", "reason", reason, "error", err)
		if rerr := w.recordVersions(ctx, svc, c, e, nil, func(registry.Version) map[string]any {
			return map[string]any{"status": versionFailed, "reason": reasonStorage}
		}); rerr != nil {
			log.Error("record the failed versions", "error", rerr)
		}
		finish(map[string]int{"failed": 1})
	}

	file, _, err := w.currentFile(ctx, c, e)
	switch {
	case err != nil:
		again("database", err)
		return
	case file == nil || len(pendingVersions(f, file)) == 0:
		finish(map[string]int{}) // replaced, deleted, or made already
		return
	}
	declared := pendingVersions(f, file)

	obj, err := w.fns.docs.objects.For(ctx, realm)
	if err != nil {
		again("storage", err)
		return
	}
	src, cleanup, err := w.fetchSource(ctx, obj, w.fns.docs.objectKey(c, e.FileID), f.MaxSize)
	switch {
	case errors.Is(err, storage.ErrObjectNotFound):
		finish(map[string]int{}) // nothing to make copies of
		return
	case err != nil:
		again("storage", err)
		return
	}
	defer cleanup()

	params := make([]imaging.Params, len(declared))
	for i, v := range declared {
		params[i] = v.Params
	}
	outcomes, perr := w.images.Process(ctx, src, f.MaxPixels, params)

	states := map[string]map[string]any{}
	var made []madeVersion
	summary := map[string]int{}
	if perr != nil {
		reason := imaging.ReasonOf(perr)
		if ctx.Err() != nil { // the worker is stopping: the lease lapses and the job comes back
			return
		}
		status := versionFailed
		if reason == imaging.ReasonNotAnImage || reason == imaging.ReasonUnsupportedFormat {
			status = versionSkipped
		}
		for _, v := range declared {
			states[v.Name] = map[string]any{"status": status, "reason": reason}
			summary[status]++
		}
	} else {
		fileKey := w.fns.docs.objectKey(c, e.FileID)
		for i, v := range declared {
			o := outcomes[i]
			if o.Err != nil {
				states[v.Name] = map[string]any{"status": versionFailed, "reason": imaging.ReasonOf(o.Err)}
				summary[versionFailed]++
				continue
			}
			m := madeVersion{name: v.Name, id: "fv_" + xid.New().String(), key: fileKey + "/" + v.Name, result: o.Result, params: versionParams(v, o.Result)}
			if err := w.storeVersion(ctx, svc, c, e, m); err != nil {
				w.discard(ctx, svc, made)
				again("storage", err)
				return
			}
			made = append(made, m)
			states[v.Name] = madeState(v, m)
			summary[versionReady]++
		}
	}

	err = w.recordVersions(ctx, svc, c, e, states, nil)
	switch {
	case errors.Is(err, errFileGone):
		w.discard(ctx, svc, made) // replaced or deleted while it was made
		finish(map[string]int{})
		return
	case err != nil:
		w.discard(ctx, svc, made)
		again("database", err)
		return
	}
	for _, m := range made {
		_ = svc.SetUploadStatus(ctx, m.id, auth.JournalAttached, e.DocumentID)
	}
	finish(summary)
	log.Info("image versions made", "ready", summary[versionReady], "failed", summary[versionFailed], "skipped", summary[versionSkipped])
}

var errFileGone = errors.New("the file is no longer in the document")

// currentFile reads the document and finds the file the job is for; nil when either is gone.
func (w *Worker) currentFile(ctx context.Context, c *registry.Collection, e *auth.ImageJob) (map[string]any, storage.Document, error) {
	doc, err := w.fns.docs.store.Repository(c).Get(ctx, e.DocumentID)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil, nil
	case err != nil:
		return nil, nil, err
	}
	for _, f := range filesOf(c.Files[e.Field], doc) {
		if f["id"] == e.FileID {
			return f, doc, nil
		}
	}
	return nil, doc, nil
}

// pendingVersions are the declared versions of a field a worker is to make for a file:
// the ones its details mark pending.
func pendingVersions(f *registry.FileField, file map[string]any) []registry.Version {
	states, _ := file["versions"].(map[string]any)
	var out []registry.Version
	for _, v := range f.Versions {
		if s, _ := states[v.Name].(map[string]any); s != nil && s["status"] == versionPending && v.HasParams() {
			out = append(out, v)
		}
	}
	return out
}

// fetchSource copies the file's object to a temporary file the engine can read from
// and seek in, refusing more bytes than the field allows.
func (w *Worker) fetchSource(ctx context.Context, obj *storage.Objects, key string, limit int64) (io.ReadSeeker, func(), error) {
	rc, _, err := obj.Get(ctx, key, "")
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp("", "backd-image-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }
	n, err := io.Copy(tmp, io.LimitReader(rc, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("the object is larger than the field's max_size")
	}
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return tmp, cleanup, nil
}

// storeVersion writes a made version to the storage, journaled like an upload so that a
// crash leaves nothing a worker won't clean up.
func (w *Worker) storeVersion(ctx context.Context, svc *auth.Users, c *registry.Collection, e *auth.ImageJob, m madeVersion) error {
	entry := auth.FileJournalEntry{ID: m.id, Database: c.Database, Collection: c.Name, Field: e.Field, DocumentID: e.DocumentID, Key: m.key, Caller: "system", Size: int64(len(m.result.Data))}
	if err := svc.JournalUpload(ctx, entry); err != nil {
		return err
	}
	obj, err := w.fns.docs.objects.For(ctx, svc.Realm)
	if err == nil {
		err = obj.PutStream(ctx, m.key, bytes.NewReader(m.result.Data), m.result.Type)
	}
	if err != nil {
		_ = svc.SetUploadStatus(ctx, m.id, auth.JournalFailed, "")
		_ = svc.QueueFileDeletion(ctx, m.key, "abandoned")
		return err
	}
	return svc.SetUploadStatus(ctx, m.id, auth.JournalStored, "")
}

// discard queues the objects of versions that won't be recorded.
func (w *Worker) discard(ctx context.Context, svc *auth.Users, made []madeVersion) {
	for _, m := range made {
		_ = svc.SetUploadStatus(ctx, m.id, auth.JournalFailed, "")
		_ = svc.QueueFileDeletion(ctx, m.key, "abandoned")
	}
}

// versionParams are the parameters a copy was made with, as the document records them.
func versionParams(v registry.Version, r *imaging.Result) map[string]any {
	p := v.Params
	out := map[string]any{}
	if p.MaxWidth > 0 {
		out["max_width"] = int64(p.MaxWidth)
	}
	if p.MaxHeight > 0 {
		out["max_height"] = int64(p.MaxHeight)
	}
	fit := string(p.Fit)
	if fit == "" {
		fit = string(imaging.Contain)
	}
	out["fit"] = fit
	if r.Type == "image/jpeg" {
		q := p.Quality
		if q == 0 {
			q = imaging.DefaultQuality
		}
		out["quality"] = int64(q)
		out["format"] = "jpeg"
	} else {
		out["format"] = "png"
	}
	if p.Upscale {
		out["upscale"] = true
	}
	return out
}

func madeState(v registry.Version, m madeVersion) map[string]any {
	return map[string]any{
		"status": versionReady, "id": m.id, "size": int64(len(m.result.Data)), "type": m.result.Type,
		"width": int64(m.result.Width), "height": int64(m.result.Height), "params": m.params,
		"fingerprint": v.Fingerprint(),
	}
}

// recordVersions writes the versions' new states into the file's details in the document,
// without changing the document's version. states are by version name; when states is
// nil, failed gives the state of each version still pending. When a client changes the
// document meanwhile it is read again and the states are applied to what it is now;
// errFileGone when the file is no longer there.
func (w *Worker) recordVersions(ctx context.Context, svc *auth.Users, c *registry.Collection, e *auth.ImageJob, states map[string]map[string]any, failed func(registry.Version) map[string]any) error {
	f := c.Files[e.Field]
	repo := w.fns.docs.store.Repository(c)
	for range imageWriteAttempts {
		file, doc, err := w.currentFile(ctx, c, e)
		if err != nil {
			return err
		}
		if file == nil {
			return errFileGone
		}
		next := maps.Clone(file)
		versions := maps.Clone(mapOf(file["versions"]))
		if versions == nil {
			versions = map[string]any{}
		}
		for _, v := range f.Versions {
			cur := mapOf(versions[v.Name])
			switch {
			case states != nil && states[v.Name] != nil:
				versions[v.Name] = states[v.Name]
			case states == nil && failed != nil && cur["status"] == versionPending:
				versions[v.Name] = failed(v)
			}
		}
		next["versions"] = versions
		var files []map[string]any
		for _, x := range filesOf(f, doc) {
			if x["id"] == e.FileID {
				x = next
			}
			files = append(files, x)
		}
		fields := withFiles(f, doc, files)
		stored, _ := doc["_meta"].(map[string]any)
		meta := maps.Clone(stored)
		meta["updated_at"] = w.fns.docs.timestamp()
		fields["id"] = doc["id"]
		fields["_meta"] = meta
		switch err := repo.Replace(ctx, fields, version(doc)); {
		case err == nil:
			// The usage counts what the document now holds, versions included.
			for name, st := range states {
				if st["status"] == versionReady {
					if err := svc.FileAdded(ctx, docOwner(doc), fileSize(st)); err != nil {
						w.log.Error("count a version in the storage usage", "realm", svc.Realm, "file", e.FileID, "version", name, "error", err)
					}
				}
			}
			return nil
		case errors.Is(err, storage.ErrVersionMismatch):
			continue // a client changed the document: look again
		default:
			return err
		}
	}
	return storage.ErrVersionMismatch
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
