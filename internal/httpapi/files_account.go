package httpapi

import (
	"context"
	"encoding/json"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// What a document holding files costs the realm's storage, and what is queued when
// it stops holding them. A file counts in the realm's usage (and its owner's) once a
// document references it, and stops counting the moment none does: its object is then
// queued for deletion, which a worker carries out.

// objectKey is the key of a file's object, which needs no connection to the storage.
func (d *documents) objectKey(c *registry.Collection, fileID string) string {
	prefix := ""
	if st := d.reg.Realms[c.Realm].Settings.Storage; st != nil {
		prefix = st.Prefix
	}
	return storage.ObjectKey(prefix, c.Realm, c.Database, c.Name, fileID)
}

// docOwner is the user a stored document belongs to, "" for none.
func docOwner(doc map[string]any) string {
	meta, _ := doc["_meta"].(map[string]any)
	owner, _ := meta["owner"].(string)
	return owner
}

// fileSize reads the size of a file's details, however its number was decoded.
func fileSize(meta map[string]any) int64 {
	switch n := meta["size"].(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		v, _ := n.Int64()
		return v
	}
	return 0
}

// allFiles lists the files a document holds in every file field.
func allFiles(c *registry.Collection, doc map[string]any) []map[string]any {
	var out []map[string]any
	for _, name := range sortedKeys(c.Files) {
		out = append(out, filesOf(c.Files[name], doc)...)
	}
	return out
}

// fileAdded counts a file a document now references, and queues the making of its
// image versions when it has some waiting.
func (d *documents) fileAdded(ctx context.Context, c *registry.Collection, docID, field, owner string, meta map[string]any) {
	svc := d.users(c.Realm)
	if svc == nil {
		return
	}
	if err := svc.FileAdded(ctx, owner, fileSize(meta)); err != nil {
		logger(ctx).Error("count a file in the storage usage", "realm", c.Realm, "error", err)
	}
	if !hasPendingVersions(meta) {
		return
	}
	id, _ := meta["id"].(string)
	job := auth.ImageJob{Collection: c.Name, Field: field, DocumentID: docID, FileID: id}
	// A failure is logged and the versions stay pending: nothing else queues them yet.
	if err := svc.EnqueueImageJob(ctx, c.Database, job, requestID(ctx)); err != nil {
		logger(ctx).Error("queue the making of a file's image versions", "realm", c.Realm, "file", id, "error", err)
	}
}

// hasPendingVersions reports whether a file's details name a version a worker is to make.
func hasPendingVersions(meta map[string]any) bool {
	versions, _ := meta["versions"].(map[string]any)
	for _, v := range versions {
		if s, _ := v.(map[string]any); s != nil && s["status"] == versionPending {
			return true
		}
	}
	return false
}

// filesGone queues the objects of files no document references any more, and stops
// counting them.
func (d *documents) filesGone(ctx context.Context, c *registry.Collection, owner string, files []map[string]any, reason string) {
	svc := d.users(c.Realm)
	if svc == nil {
		return
	}
	for _, f := range files {
		id, _ := f["id"].(string)
		if id == "" {
			continue
		}
		if err := svc.QueueFileDeletion(ctx, d.objectKey(c, id), reason); err != nil {
			logger(ctx).Error("queue a file's object for deletion", "realm", c.Realm, "file", id, "error", err)
			continue
		}
		if err := svc.FileRemoved(ctx, owner, fileSize(f)); err != nil {
			logger(ctx).Error("count a file out of the storage usage", "realm", c.Realm, "error", err)
		}
		// The copies made of an image go with it.
		for _, name := range readyVersions(f) {
			v, _ := f["versions"].(map[string]any)[name].(map[string]any)
			if err := svc.QueueFileDeletion(ctx, d.objectKey(c, id)+"/"+name, reason); err != nil {
				logger(ctx).Error("queue a file's version for deletion", "realm", c.Realm, "file", id, "version", name, "error", err)
				continue
			}
			if err := svc.FileRemoved(ctx, owner, fileSize(v)); err != nil {
				logger(ctx).Error("count a version out of the storage usage", "realm", c.Realm, "error", err)
			}
		}
	}
}

// readyVersions names, in order, the versions of a file that were made (and so have an object).
func readyVersions(file map[string]any) []string {
	versions, _ := file["versions"].(map[string]any)
	var out []string
	for _, name := range sortedKeys(versions) {
		if s, _ := versions[name].(map[string]any); s != nil && s["status"] == versionReady {
			out = append(out, name)
		}
	}
	return out
}

// documentGone is filesGone for a document that was removed for good.
func (d *documents) documentGone(ctx context.Context, c *registry.Collection, doc map[string]any) {
	if len(c.Files) > 0 {
		d.filesGone(ctx, c, docOwner(doc), allFiles(c, doc), "deleted")
	}
}
