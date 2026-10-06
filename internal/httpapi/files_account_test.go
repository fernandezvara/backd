package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func (f *filesFixture) usage(t *testing.T) (bytes, files int64, users map[string][2]int64) {
	t.Helper()
	u, err := f.svc.StorageUsage(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	users = map[string][2]int64{}
	for _, x := range u.Users {
		users[x.UserID] = [2]int64{x.Bytes, x.Files}
	}
	return u.Realm.Bytes, u.Realm.Files, users
}

func (f *filesFixture) queued(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range f.svc.Store.(interface{ Deletions() []auth.FileDeletion }).Deletions() {
		out[d.Key] = d.Reason
	}
	return out
}

// Usage follows what documents reference: it grows with an upload, shrinks when a file is
// replaced, removed or cleared, and when its document is deleted for good.
func TestStorageUsageFollowsTheDocuments(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	_, doc := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(100), nil)
	a1 := doc["avatar"].(map[string]any)
	size1 := int64(a1["size"].(float64))
	if b, n, users := f.usage(t); b != size1 || n != 1 || users[f.adaID] != [2]int64{size1, 1} {
		t.Fatalf("after one upload: %d bytes, %d files, %v", b, n, users)
	}
	_, doc = f.upload(t, f.ada, "library", id, "receipts", "r.pdf", "application/pdf", []byte("%PDF-1.4 receipt"), nil)
	rsize := int64(doc["receipts"].([]any)[0].(map[string]any)["size"].(float64))
	// Replacing a single field's file swaps what it counts.
	_, doc = f.upload(t, f.ada, "library", id, "avatar", "b.png", "image/png", pngBytes(10), nil)
	size2 := int64(doc["avatar"].(map[string]any)["size"].(float64))
	if b, n, _ := f.usage(t); b != size2+rsize || n != 2 {
		t.Errorf("after replacing: %d bytes, %d files, want %d and 2", b, n, size2+rsize)
	}
	if q := f.queued(t); len(q) != 1 || q["t/acme/app/library/"+a1["id"].(string)] != "replaced" {
		t.Errorf("queued: %v", q)
	}
	// Removing one file, then clearing a field.
	rid := doc["receipts"].([]any)[0].(map[string]any)["id"].(string)
	if rec, _ := f.doRaw(t, "DELETE", "/v1/acme/app/library/"+id+"/_files/receipts/"+rid, nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if b, n, _ := f.usage(t); b != size2 || n != 1 {
		t.Errorf("after removing a file: %d, %d", b, n)
	}
	// Deleting the document takes the rest with it.
	if code, _ := f.as(t, f.ada, "DELETE", "/v1/acme/app/library/"+id, ""); code != 204 {
		t.Fatal(code)
	}
	b, n, users := f.usage(t)
	if b != 0 || n != 0 || users[f.adaID] != [2]int64{0, 0} {
		t.Errorf("after deleting the document: %d, %d, %v", b, n, users)
	}
	q := f.queued(t)
	last := "t/acme/app/library/" + doc["avatar"].(map[string]any)["id"].(string)
	if q[last] != "deleted" || q["t/acme/app/library/"+rid] != "removed" || len(q) != 3 {
		t.Errorf("queued: %v", q)
	}
}

// Every way of removing a document for good queues its files: DELETE with and without
// If-Match, a batch, and a purge from the trash; soft-deleting keeps them.
func TestDeletingDocumentsQueuesTheirFiles(t *testing.T) {
	f := newFilesFixture(t)
	make1 := func(collection string) (string, string) {
		id := f.newDoc(t, collection)
		_, doc := f.upload(t, f.ada, collection, id, "avatar", "a.png", "image/png", pngBytes(5), nil)
		return id, doc["avatar"].(map[string]any)["id"].(string)
	}
	id, fid := make1("library")
	if code, _ := f.as(t, f.key, "DELETE", "/v1/acme/app/library/"+id, ""); code != 204 { // an API key: no rules, no If-Match
		t.Fatal(code)
	}
	if f.queued(t)["t/acme/app/library/"+fid] != "deleted" {
		t.Errorf("a key's delete: %v", f.queued(t))
	}
	id, fid = make1("library")
	code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_batch", `{"operations": [{"op": "delete", "collection": "library", "id": "`+id+`"}]}`)
	if code != 200 {
		t.Fatalf("batch: %d %v", code, out)
	}
	if f.queued(t)["t/acme/app/library/"+fid] != "deleted" {
		t.Errorf("a batch delete: %v", f.queued(t))
	}
	if b, n, _ := f.usage(t); b != 0 || n != 0 {
		t.Errorf("usage: %d, %d", b, n)
	}
}

// Erasing a user: delete takes every file field of their documents, anonymize the file
// fields it removes, and a collection with no policy keeps everything.
func TestErasingAUserQueuesTheirFiles(t *testing.T) {
	f := newFilesFixture(t)
	ctx := context.Background()
	w := newTestWorker(t, f.rulesFixture)
	type held struct{ avatar, receipt string }
	mk := func(collection string) (id string, h held) {
		id = f.newDoc(t, collection)
		_, doc := f.upload(t, f.ada, collection, id, "avatar", "a.png", "image/png", pngBytes(5), nil)
		h.avatar = doc["avatar"].(map[string]any)["id"].(string)
		_, doc = f.upload(t, f.ada, collection, id, "receipts", "r.pdf", "application/pdf", []byte("%PDF-1.4 r"), nil)
		h.receipt = doc["receipts"].([]any)[0].(map[string]any)["id"].(string)
		return id, h
	}
	_, vault := mk("vault")
	profID, prof := mk("profiles")
	_, kept := mk("library")
	bobID := f.newDocAs(t, f.bob, "vault")
	_, doc := f.upload(t, f.bob, "vault", bobID, "avatar", "b.png", "image/png", pngBytes(6), nil)
	bobFile := doc["avatar"].(map[string]any)["id"].(string)

	if code, out := f.as(t, f.key, "DELETE", "/v1/acme/_admin/users/"+f.adaID, ""); code != 202 {
		t.Fatalf("erase: %d %v", code, out)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("no erase job")
	}
	q := f.queued(t)
	for _, id := range []string{vault.avatar, vault.receipt} {
		if q["t/acme/app/vault/"+id] != "erased" {
			t.Errorf("delete didn't queue %s: %v", id, q)
		}
	}
	if q["t/acme/app/profiles/"+prof.avatar] != "erased" {
		t.Errorf("anonymize didn't queue the avatar: %v", q)
	}
	for _, key := range []string{"t/acme/app/profiles/" + prof.receipt, "t/acme/app/library/" + kept.avatar, "t/acme/app/library/" + kept.receipt, "t/acme/app/vault/" + bobFile} {
		if _, has := q[key]; has {
			t.Errorf("%s was queued", key)
		}
	}
	// The anonymized document keeps its receipts and lost its avatar.
	_, got := f.as(t, f.key, "GET", "/v1/acme/app/profiles/"+profID, "")
	if got["avatar"] != nil || len(got["receipts"].([]any)) != 1 {
		t.Errorf("anonymized document: %v", got)
	}
	// Usage: only what is still referenced is counted, and Ada's share is what she kept.
	_, _, users := f.usage(t)
	if users[f.bobID][1] != 1 {
		t.Errorf("bob's files: %v", users)
	}
	if users[f.adaID][1] != 3 {
		t.Errorf("ada's files after the erase: %v (the profile's receipt and the library's two)", users[f.adaID])
	}
	// The worker deletes the queued objects.
	w.FilesDue(ctx)
	for _, k := range f.s3.Keys() {
		if strings.Contains(k, vault.avatar) || strings.Contains(k, prof.avatar) {
			t.Errorf("%s was not deleted", k)
		}
	}
}

func (f *filesFixture) newDocAs(t *testing.T, cred, collection string) string {
	t.Helper()
	code, doc := f.as(t, cred, "POST", "/v1/acme/app/"+collection, `{"title": "d"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	return doc["id"].(string)
}

func TestReconcileReportsAndDeletesOrphans(t *testing.T) {
	f := newFilesFixture(t)
	ctx := context.Background()
	old := f.clock.Add(-48 * time.Hour)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.newDoc(t, "library")
	_, doc := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(5), nil)
	held := "t/acme/app/library/" + doc["avatar"].(map[string]any)["id"].(string)
	f.s3.PutAt(held, pngBytes(5), old) // referenced
	put := func(key string, age time.Time) { f.s3.PutAt(key, []byte("x"), age) }
	// A document restored from a backup holds a file nobody journaled here: referenced.
	restored := f.newDoc(t, "library")
	f.seedFile(t, "library", restored, "avatar", fileDetails("fl_restored00000000001", "r.png"))
	put("t/acme/app/library/fl_restored00000000001", old)
	put("t/acme/app/library/fl_orphan0000000000001", old)     // nothing references it: an orphan
	put("t/acme/app/gallery/fl_orphan0000000000002", old)     // another collection's
	put("t/acme/app/removed/fl_orphan0000000000003", old)     // a collection that no longer exists
	put("t/acme/app/library/fl_recent000000000001", *f.clock) // younger than 24 hours
	put("t/acme/_logs/2026/10/x.log", old)                    // not a file area
	put("t/acme/app/library/strange", old)                    // not shaped like a file
	put("t/other/app/library/fl_otherrealm000000001", old)    // another realm's
	// Journaled and not failed: in flight, or a pending upload waiting to be used.
	inflight := "fl_inflight00000000001"
	if err := f.svc.JournalUpload(ctx, auth.FileJournalEntry{ID: inflight, Database: "app", Collection: "library", Field: "avatar", Key: "t/acme/app/library/" + inflight}); err != nil {
		t.Fatal(err)
	}
	put("t/acme/app/library/"+inflight, old)
	// One that failed is fair game.
	failed := "fl_failed000000000001"
	_ = f.svc.JournalUpload(ctx, auth.FileJournalEntry{ID: failed, Database: "app", Collection: "library", Field: "avatar", Key: "t/acme/app/library/" + failed})
	_ = f.svc.SetUploadStatus(ctx, failed, auth.JournalFailed, "")
	put("t/acme/app/library/"+failed, old)

	body := func(del bool) string {
		if del {
			return `{"delete": true}`
		}
		return `{}`
	}
	rec, rep := f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(body(false)), map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"})
	if rec.Code != 200 {
		t.Fatalf("reconcile: %d %s", rec.Code, rec.Body)
	}
	orphans := map[string]bool{}
	for _, o := range rep["orphans"].([]any) {
		orphans[o.(map[string]any)["key"].(string)] = true
	}
	want := []string{"t/acme/app/library/fl_orphan0000000000001", "t/acme/app/gallery/fl_orphan0000000000002", "t/acme/app/removed/fl_orphan0000000000003", "t/acme/app/library/" + failed}
	if len(orphans) != len(want) {
		t.Errorf("orphans: %v, want %v", orphans, want)
	}
	for _, k := range want {
		if !orphans[k] {
			t.Errorf("%s isn't reported", k)
		}
	}
	if rep["referenced"] != float64(1) || rep["skipped_recent"] != float64(1) || rep["skipped_in_journal"] != float64(2) || rep["ignored"] != float64(1) || rep["deleted"] != float64(0) || rep["delete"] != false {
		t.Errorf("report: %v", rep)
	}
	if len(f.s3.Keys()) != 11 { // a report removes nothing
		t.Errorf("a dry run changed the bucket: %d objects", len(f.s3.Keys()))
	}

	// With delete: exactly what the report listed, nothing else.
	rec, rep = f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(body(true)), map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"})
	if rec.Code != 200 || rep["deleted"] != float64(len(want)) || len(rep["failed"].([]any)) != 0 {
		t.Fatalf("reconcile --delete: %d %v", rec.Code, rep)
	}
	left := map[string]bool{}
	for _, k := range f.s3.Keys() {
		left[k] = true
	}
	for _, k := range want {
		if left[k] {
			t.Errorf("%s was not deleted", k)
		}
	}
	for _, k := range []string{held, "t/acme/app/library/fl_restored00000000001", "t/acme/app/library/fl_recent000000000001", "t/acme/_logs/2026/10/x.log", "t/acme/app/library/strange", "t/other/app/library/fl_otherrealm000000001", "t/acme/app/library/" + inflight} {
		if !left[k] {
			t.Errorf("%s was deleted", k)
		}
	}
	// The document still has its file, and a second run finds nothing.
	if _, rep = f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(body(false)), map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}); len(rep["orphans"].([]any)) != 0 {
		t.Errorf("a second run: %v", rep["orphans"])
	}
	// Rights and audit.
	if rec, _ := f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(`{}`), map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Errorf("a user: %d", rec.Code)
	}
	if recs, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditStorageReconcile}); len(recs) != 3 {
		t.Errorf("audit: %+v", recs)
	}
}

func TestStorageStatus(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	key := map[string]string{"Authorization": "Bearer " + f.key}
	id := f.newDoc(t, "library")
	_, doc := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(5), nil)
	size := doc["avatar"].(map[string]any)["size"].(float64)
	f.upload(t, f.ada, "library", id, "avatar", "b.png", "image/png", pngBytes(6), nil) // queues the first
	_ = f.svc.JournalUpload(context.Background(), auth.FileJournalEntry{ID: "fl_stuck", Database: "app", Collection: "library", Field: "avatar", Key: "k"})
	*f.clock = f.clock.Add(2 * time.Hour)

	rec, out := f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, key)
	if rec.Code != 200 || out["configured"] != true || out["provider"] != "minio" || out["bucket"] != "files" || out["prefix"] != "t" || out["access_key"] != "secret:STORAGE_ACCESS_KEY" {
		t.Fatalf("status: %d %v", rec.Code, out)
	}
	if strings.Contains(rec.Body.String(), "v-STORAGE") {
		t.Error("a key's value is in the answer")
	}
	if out["keys"].(map[string]any)["ok"] != true || out["reachable"].(map[string]any)["ok"] != true {
		t.Errorf("keys and reachability: %v %v", out["keys"], out["reachable"])
	}
	usage := out["usage"].(map[string]any)
	users := usage["users"].([]any)
	if usage["files"] != float64(1) || usage["bytes"] != size+1 && usage["bytes"].(float64) <= 0 || len(users) != 1 || users[0].(map[string]any)["user_id"] != f.adaID {
		t.Errorf("usage: %v", usage)
	}
	if d := out["deletions"].(map[string]any); d["queued"] != float64(1) || d["oldest"] == nil {
		t.Errorf("deletions: %v", d)
	}
	if out["stale_uploads"] != float64(1) {
		t.Errorf("stale uploads: %v", out["stale_uploads"])
	}
	// Keys that aren't set: said, and the bucket isn't tried.
	st := f.reg.Realms["acme"].Settings.Storage
	saved := *st
	st.AccessKey = "NOT_SET_ANYWHERE"
	_, out = f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, key)
	if out["keys"].(map[string]any)["ok"] != false || out["reachable"].(map[string]any)["ok"] != false {
		t.Errorf("missing keys: %v", out)
	}
	*st = saved
	// An unreachable bucket.
	f.s3.Close()
	_, out = f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, key)
	if out["reachable"].(map[string]any)["ok"] != false || out["reachable"].(map[string]any)["error"] == nil {
		t.Errorf("unreachable: %v", out["reachable"])
	}
	// No storage: not configured, still 200. And only administrators ask.
	f.reg.Realms["acme"].Settings.Storage = nil
	if rec, out := f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, key); rec.Code != 200 || out["configured"] != false {
		t.Errorf("no storage: %d %v", rec.Code, out)
	}
	if rec, _ := f.doRaw(t, "GET", "/v1/acme/_admin/storage", nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != 403 {
		t.Errorf("a user: %d", rec.Code)
	}
}
