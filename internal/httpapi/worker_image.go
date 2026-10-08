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
	"slices"
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

// imageTarget is one version to make: its name, the parameters to make it with, and
// whether they are a function's own rather than the declared ones.
type imageTarget struct {
	name   string
	params imaging.Params
	custom bool
}

// imageParamsDoc is the JSON of parameters a function generates with.
type imageParamsDoc struct {
	MaxWidth  int    `json:"max_width"`
	MaxHeight int    `json:"max_height"`
	Fit       string `json:"fit"`
	Quality   int    `json:"quality"`
	Format    string `json:"format"`
	Upscale   bool   `json:"upscale"`
}

func (d imageParamsDoc) params() imaging.Params {
	return imaging.Params{MaxWidth: d.MaxWidth, MaxHeight: d.MaxHeight, Fit: imaging.Fit(d.Fit), Quality: d.Quality, Format: imaging.Format(d.Format), Upscale: d.Upscale}
}

// runImage makes versions of one file: by default its pending versions; a job for one
// version (a function asked) makes just that one, whatever its state, and ends with its
// outcome for the function waiting on it. A scan job (backd versions regenerate)
// goes through a whole field instead. The document is written without changing its
// version, so a client editing it is never told it conflicts. Safe to repeat: a file
// that is gone, or whose versions are no longer pending, ends the job at once.
func (w *Worker) runImage(ctx context.Context, log *slog.Logger, realm string, svc *auth.Users, job auth.Job) {
	e := job.Image
	if e.Scan {
		w.runRegenerate(ctx, log, realm, svc, job)
		return
	}
	log = log.With("file", e.FileID)
	asked := e.Version != ""
	finishWith := func(out any) {
		raw, _ := json.Marshal(out)
		if err := svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: "ok", Output: raw}); err != nil {
			log.Error("complete the image job", "error", err)
			return
		}
		w.fns.metrics.JobFinished(svc.Realm, "image", "ok")
	}
	// refuse ends a job for one version with the reason the function gets.
	refuse := func(reason string) { finishWith(map[string]any{"error": reason}) }
	finish := func(summary map[string]int) {
		if asked { // nothing was made: the file is gone, or the configuration changed
			refuse("file_gone")
			return
		}
		finishWith(summary)
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
		log.Error("attempts used up; the versions fail", "reason", reason, "error", err)
		if asked {
			refuse(reasonStorage)
			return
		}
		if rerr := w.fns.docs.updateVersions(ctx, svc, c, e, func(versions map[string]any) {
			for _, v := range f.Versions {
				if mapOf(versions[v.Name])["status"] == versionPending {
					versions[v.Name] = map[string]any{"status": versionFailed, "reason": reasonStorage}
				}
			}
		}); rerr != nil {
			log.Error("record the failed versions", "error", rerr)
		}
		finishWith(map[string]int{"failed": 1})
	}

	file, _, err := w.currentFile(ctx, c, e)
	if err != nil {
		again("database", err)
		return
	}
	var targets []imageTarget
	switch {
	case file == nil:
		finish(map[string]int{}) // replaced or deleted
		return
	case asked:
		v, declared := f.Version(e.Version)
		if !declared {
			refuse("version_undeclared")
			return
		}
		t := imageTarget{name: v.Name, params: v.Params}
		if e.Params != "" {
			var pd imageParamsDoc
			if json.Unmarshal([]byte(e.Params), &pd) != nil {
				refuse("invalid_parameters")
				return
			}
			t.params, t.custom = pd.params(), true
		}
		targets = []imageTarget{t}
	default:
		for _, v := range pendingVersions(f, file) {
			targets = append(targets, imageTarget{name: v.Name, params: v.Params})
		}
		if len(targets) == 0 {
			finish(map[string]int{}) // made already
			return
		}
	}

	out := w.makeFile(ctx, svc, c, f, e, targets, asked)
	switch {
	case ctx.Err() != nil: // the worker is stopping: the lease lapses and the job comes back
		return
	case out.transient != "":
		again(out.transient, out.err)
	case out.gone:
		finish(map[string]int{})
	case asked && out.failure != "":
		refuse(out.failure) // a version that was there is kept
	case asked:
		finishWith(map[string]any{"version": out.states[e.Version]})
		log.Info("image version made", "version", e.Version)
	default:
		finishWith(out.summary)
		log.Info("image versions made", "ready", out.summary[versionReady], "failed", out.summary[versionFailed], "skipped", out.summary[versionSkipped])
	}
}

// fileOutcome is what making versions of one file ended with.
type fileOutcome struct {
	states  map[string]map[string]any // by version name, as recorded
	summary map[string]int            // how many ended ready, failed, skipped
	failure string                    // why a version could not be made ("" when all were)
	gone    bool                      // the file (or its source object) is no longer there
	// transient is "storage" or "database" when something that may pass went wrong; err says what.
	transient string
	err       error
}

// makeFile makes the targets of one file: it reads the file's object, decodes the image
// once, makes every target, stores each beside the source under <file key>/<name> and
// records the details in the document. With keep set, a failure to make a target is not
// recorded (the copy it had stays) and only reported.
func (w *Worker) makeFile(ctx context.Context, svc *auth.Users, c *registry.Collection, f *registry.FileField, e *auth.ImageJob, targets []imageTarget, keep bool) fileOutcome {
	out := fileOutcome{states: map[string]map[string]any{}, summary: map[string]int{}}
	obj, err := w.fns.docs.objects.For(ctx, c.Realm)
	if err != nil {
		out.transient, out.err = "storage", err
		return out
	}
	src, cleanup, err := w.fetchSource(ctx, obj, w.fns.docs.objectKey(c, e.FileID), f.MaxSize)
	switch {
	case errors.Is(err, storage.ErrObjectNotFound):
		out.gone = true // nothing to make copies of
		return out
	case err != nil:
		out.transient, out.err = "storage", err
		return out
	}
	defer cleanup()

	params := make([]imaging.Params, len(targets))
	for i, t := range targets {
		params[i] = t.params
	}
	outcomes, perr := w.images.Process(ctx, src, f.MaxPixels, params)

	var made []madeVersion
	if perr != nil {
		reason := imaging.ReasonOf(perr)
		if ctx.Err() != nil {
			return out
		}
		status := versionFailed
		if reason == imaging.ReasonNotAnImage || reason == imaging.ReasonUnsupportedFormat {
			status = versionSkipped
		}
		out.failure = reason
		for _, t := range targets {
			out.states[t.name] = map[string]any{"status": status, "reason": reason}
			out.summary[status]++
		}
	} else {
		fileKey := w.fns.docs.objectKey(c, e.FileID)
		for i, t := range targets {
			o := outcomes[i]
			if o.Err != nil {
				out.failure = imaging.ReasonOf(o.Err)
				out.states[t.name] = map[string]any{"status": versionFailed, "reason": out.failure}
				out.summary[versionFailed]++
				continue
			}
			m := madeVersion{name: t.name, id: "fv_" + xid.New().String(), key: fileKey + "/" + t.name, result: o.Result, params: versionParams(t.params, o.Result)}
			if err := w.storeVersion(ctx, svc, c, e, m); err != nil {
				w.discard(ctx, svc, made)
				out.transient, out.err = "storage", err
				return out
			}
			made = append(made, m)
			out.states[t.name] = madeState(t, m)
			out.summary[versionReady]++
		}
	}
	if keep && out.failure != "" {
		w.discard(ctx, svc, made)
		return out
	}

	err = w.fns.docs.updateVersions(ctx, svc, c, e, func(versions map[string]any) {
		for name, st := range out.states {
			versions[name] = st
		}
	})
	switch {
	case errors.Is(err, errFileGone):
		w.discard(ctx, svc, made) // replaced or deleted while it was made
		out.gone = true
		return out
	case err != nil:
		w.discard(ctx, svc, made)
		out.transient, out.err = "database", err
		return out
	}
	for _, m := range made {
		_ = svc.SetUploadStatus(ctx, m.id, auth.JournalAttached, e.DocumentID)
	}
	return out
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
	obj, err := w.fns.docs.objects.For(ctx, c.Realm)
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
func versionParams(p imaging.Params, r *imaging.Result) map[string]any {
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

func madeState(t imageTarget, m madeVersion) map[string]any {
	st := map[string]any{
		"status": versionReady, "id": m.id, "size": int64(len(m.result.Data)), "type": m.result.Type,
		"width": int64(m.result.Width), "height": int64(m.result.Height), "params": m.params,
		"fingerprint": registry.ParamsFingerprint(t.params),
	}
	if t.custom {
		st["custom"] = true
	}
	return st
}

// updateVersions changes the versions recorded in a file's details in the document, with
// mutate, without changing the document's version. When a client changes the document
// meanwhile it is read again and mutate is applied to what it is now; errFileGone when
// the file is no longer there. The usage follows: a version that was ready and is not
// that any more stops counting, and one that is now ready is counted.
func (d *documents) updateVersions(ctx context.Context, svc *auth.Users, c *registry.Collection, e *auth.ImageJob, mutate func(versions map[string]any)) error {
	f := c.Files[e.Field]
	repo := d.store.Repository(c)
	for range imageWriteAttempts {
		doc, err := repo.Get(ctx, e.DocumentID)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			return errFileGone
		case err != nil:
			return err
		}
		var file map[string]any
		for _, x := range filesOf(f, doc) {
			if x["id"] == e.FileID {
				file = x
			}
		}
		if file == nil {
			return errFileGone
		}
		before := mapOf(file["versions"])
		versions := maps.Clone(before)
		if versions == nil {
			versions = map[string]any{}
		}
		mutate(versions)
		next := maps.Clone(file)
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
		meta["updated_at"] = d.timestamp()
		fields["id"] = doc["id"]
		fields["_meta"] = meta
		switch err := repo.Replace(ctx, fields, version(doc)); {
		case err == nil:
			d.countVersions(ctx, svc, docOwner(doc), before, versions)
			return nil
		case errors.Is(err, storage.ErrVersionMismatch):
			continue // a client changed the document: look again
		default:
			return err
		}
	}
	return storage.ErrVersionMismatch
}

// countVersions moves the usage from the versions a file had to the ones it has.
func (d *documents) countVersions(ctx context.Context, svc *auth.Users, owner string, before, after map[string]any) {
	ready := func(m map[string]any, name string) (string, int64, bool) {
		st := mapOf(m[name])
		if st == nil || st["status"] != versionReady {
			return "", 0, false
		}
		id, _ := st["id"].(string)
		return id, fileSize(st), true
	}
	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		oldID, oldSize, wasReady := ready(before, name)
		newID, newSize, isReady := ready(after, name)
		if wasReady && (!isReady || oldID != newID) {
			if err := svc.FileRemoved(ctx, owner, oldSize); err != nil {
				logger(ctx).Error("count a version out of the storage usage", "realm", svc.Realm, "version", name, "error", err)
			}
		}
		if isReady && (!wasReady || oldID != newID) {
			if err := svc.FileAdded(ctx, owner, newSize); err != nil {
				logger(ctx).Error("count a version in the storage usage", "realm", svc.Realm, "version", name, "error", err)
			}
		}
	}
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
