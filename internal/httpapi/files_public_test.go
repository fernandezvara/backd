package httpapi

import (
	"context"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// A file records which of the realm's two storages it is in, and everything that reaches its
// object follows that record, not the field's setting. These tests put a public file in a
// document by hand (uploading to a public field comes with the next step) and follow it through
// the places that touch storage.

// publicFile gives the document a public file in `photo`: its object in the public storage, under the
// key that ends with the file's name, and a ready thumbnail beside it.
func (f *filesFixture) publicFile(t *testing.T) (c *registry.Collection, docID string, file map[string]any) {
	t.Helper()
	c, _ = f.reg.Collection("acme", "app", "pictures")
	docID = f.newDoc(t, "pictures")
	file = map[string]any{
		"id": "fl_pub00000000000000001", "name": "Holiday photo.png", "key_name": "Holiday photo.png", "public": true, "type": "image/png", "size": int64(9),
		"sha256": sha([]byte("PNGBYTES!")), "uploaded_at": time.Now().UTC().Format(timeFormat),
		"versions": map[string]any{"thumb": map[string]any{"status": versionReady, "id": "fv_pub000000000000001", "type": "image/png", "size": int64(3)}},
	}
	f.pub.Put("t/acme/app/pictures/fl_pub00000000000000001/Holiday photo.png", []byte("PNGBYTES!"))
	f.pub.Put("t/acme/app/pictures/fl_pub00000000000000001/thumb/Holiday photo.png", []byte("T!!"))
	repo := f.store.Repository(c)
	doc, err := repo.Get(context.Background(), docID)
	if err != nil {
		t.Fatal(err)
	}
	fields := userFields(doc)
	fields["photo"] = file
	fields["id"], fields["_meta"] = doc["id"], doc["_meta"]
	if err := repo.Replace(context.Background(), fields, version(doc)); err != nil {
		t.Fatal(err)
	}
	return c, docID, file
}

func TestPublicFileKeys(t *testing.T) {
	f := newFilesFixture(t)
	d := &documents{reg: f.reg}
	c, _ := f.reg.Collection("acme", "app", "pictures")
	private := fileLoc{id: "fl_1"}
	public := fileLoc{id: "fl_1", public: true, keyName: "a b.png"}
	for _, tc := range []struct{ got, want string }{
		{d.fileKey(c, private), "t/acme/app/pictures/fl_1"},
		{d.versionKey(c, private, "thumb", nil), "t/acme/app/pictures/fl_1/thumb"},
		{d.fileKey(c, public), "t/acme/app/pictures/fl_1/a b.png"},
		{d.versionKey(c, public, "thumb", map[string]any{"type": "image/jpeg"}), "t/acme/app/pictures/fl_1/thumb/a b.jpg"},
		{d.versionKey(c, public, "thumb", map[string]any{"type": "image/png"}), "t/acme/app/pictures/fl_1/thumb/a b.png"},
	} {
		if tc.got != tc.want {
			t.Errorf("key = %q, want %q", tc.got, tc.want)
		}
	}
	if got := locOf(map[string]any{"id": "fl_1", "public": true, "key_name": "x.png"}); got != (fileLoc{id: "fl_1", public: true, keyName: "x.png"}) {
		t.Errorf("locOf = %+v", got)
	}
	if got := locOf(map[string]any{"id": "fl_1"}); got.public {
		t.Errorf("a file with no record is private: %+v", got)
	}
}

// A public file is read through the private route, from the public storage, by proxy.
func TestPublicFileDownloadsFromThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	_, docID, file := f.publicFile(t)
	hdr := map[string]string{"Authorization": "Bearer " + f.ada}
	path := "/v1/acme/app/pictures/" + docID + "/_files/photo/" + file["id"].(string)
	rec := f.get(t, path, hdr)
	if rec.Code != http.StatusOK || rec.Body.String() != "PNGBYTES!" {
		t.Fatalf("original: %d %q", rec.Code, rec.Body)
	}
	rec = f.get(t, path+"?version=thumb", hdr)
	if rec.Code != http.StatusOK || rec.Body.String() != "T!!" {
		t.Fatalf("version: %d %q", rec.Code, rec.Body)
	}
	if reads := f.s3.Requests["GET"]; reads != 0 {
		t.Errorf("the private storage was read %d times", reads)
	}
}

// Deleting the document queues its public objects in the public storage, and the worker deletes
// them there, not from the private storage where objects of the same ids might exist.
func TestPublicFilesAreDeletedFromThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	ctx := context.Background()
	c, docID, file := f.publicFile(t)
	f.s3.Put("t/acme/app/pictures/fl_pub00000000000000001", []byte("a private file with the same id"))

	d := w.fns.docs
	d.filesGone(ctx, c, "", []map[string]any{file}, "removed")
	queued := f.deletions()
	if len(queued) != 2 {
		t.Fatalf("queued: %+v", queued)
	}
	for _, q := range queued {
		if !q.Public {
			t.Errorf("queued as private: %+v", q)
		}
	}
	w.FilesDue(ctx)
	if keys := f.pub.Keys(); len(keys) != 0 {
		t.Errorf("left in the public storage: %v", keys)
	}
	if keys := f.s3.Keys(); len(keys) != 1 {
		t.Errorf("the private storage lost its object: %v", keys)
	}
	_ = docID
}

// An abandoned upload is cleaned from the storage it was journaled in.
func TestAbandonedPublicUploadIsCleanedFromThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	ctx := context.Background()
	id := f.newDoc(t, "pictures")

	key := "t/acme/app/pictures/fl_killedmidway00000001/photo.png"
	f.pub.Put(key, []byte("half an upload"))
	if err := f.svc.JournalUpload(ctx, auth.FileJournalEntry{ID: "fl_killedmidway00000001", Database: "app", Collection: "pictures", Field: "photo", DocumentID: id, Key: key, Public: true}); err != nil {
		t.Fatal(err)
	}
	_ = f.svc.SetUploadStatus(ctx, "fl_killedmidway00000001", auth.JournalStored, "")
	*f.clock = f.clock.Add(auth.FileJournalGrace + time.Minute)
	w.FilesDue(ctx)
	w.FilesDue(ctx) // the first pass queues the object, the second deletes it
	if len(f.pub.Keys()) != 0 || len(f.deletions()) != 0 {
		t.Errorf("after the grace period: public %v, deletions %v", f.pub.Keys(), f.deletions())
	}
}

// Erasing a user takes their public files out of the public storage too.
func TestErasedPublicFilesAreQueuedInThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	c, _ := f.reg.Collection("acme", "app", "pictures")
	w.eraseFiles(context.Background(), f.svc, c, "u1", []storage.ErasedFile{{
		ID: "fl_1", Size: 5, Public: true, KeyName: "a.png", Versions: map[string]int64{"thumb": 2}, VersionTypes: map[string]string{"thumb": "image/jpeg"},
	}})
	got := map[string]bool{}
	for _, q := range f.deletions() {
		if !q.Public {
			t.Errorf("queued as private: %+v", q)
		}
		got[q.Key] = true
	}
	want := map[string]bool{"t/acme/app/pictures/fl_1/a.png": true, "t/acme/app/pictures/fl_1/thumb/a.jpg": true}
	if !maps.Equal(got, want) {
		t.Errorf("queued %v, want %v", got, want)
	}
}

// Reconcile of the public storage understands its keys and leaves the private one alone.
func TestReconcileOfThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	_, _, file := f.publicFile(t)
	old := f.clock.Add(-48 * time.Hour)
	f.pub.PutAt("t/acme/app/pictures/fl_orphan00000000000001/gone.png", []byte("orphan"), old)
	f.pub.PutAt("t/acme/app/pictures/"+file["id"].(string)+"/Holiday photo.png", []byte("PNGBYTES!"), old)
	f.pub.PutAt("t/acme/app/pictures/"+file["id"].(string)+"/thumb/Holiday photo.png", []byte("T!!"), old)
	f.pub.PutAt("t/acme/app/pictures/"+file["id"].(string)+"/thumb/old.png", []byte("T!!"), old) // not recorded
	f.s3.PutAt("t/acme/app/library/fl_privateorphan0000001", []byte("private orphan"), old)

	_, key, _ := f.svc.CreateAPIKey(context.Background(), "recon", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	rec, out := f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(`{"storage":"public"}`), map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"})
	if rec.Code != http.StatusOK || out["storage"] != "public" {
		t.Fatalf("reconcile: %d %v", rec.Code, out)
	}
	orphans, _ := out["orphans"].([]any)
	var keys []string
	for _, o := range orphans {
		keys = append(keys, o.(map[string]any)["key"].(string))
	}
	if len(keys) != 2 || keys[0] != "t/acme/app/pictures/fl_orphan00000000000001/gone.png" && keys[1] != "t/acme/app/pictures/fl_orphan00000000000001/gone.png" {
		t.Errorf("orphans: %v", keys)
	}
	if out["referenced"] != float64(2) {
		t.Errorf("referenced: %v", out["referenced"])
	}
	// And the private one is reported on its own.
	_, out = f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(`{}`), map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"})
	if out["storage"] != "private" || len(out["orphans"].([]any)) != 1 {
		t.Errorf("private reconcile: %v", out)
	}
}

func TestUploadsToAPublicFieldWaitForTheNextStep(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "pictures")
	rec, _ := f.upload(t, f.ada, "pictures", id, "photo", "a.png", "image/png", pngBytes(5), nil)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("upload: %d %s", rec.Code, rec.Body)
	}
	rec, _ = f.doRaw(t, "POST", "/v1/acme/app/pictures/_files/photo/uploads", pngBytes(5), map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "image/png"})
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("pending upload: %d %s", rec.Code, rec.Body)
	}
	if len(f.pub.Keys()) != 0 || len(f.s3.Keys()) != 0 {
		t.Errorf("something was stored: %v %v", f.pub.Keys(), f.s3.Keys())
	}
	// A private field of the same collection still takes files.
	rec, _ = f.upload(t, f.ada, "pictures", id, "secret", "a.png", "image/png", pngBytes(5), nil)
	if rec.Code != http.StatusCreated {
		t.Errorf("private upload: %d %s", rec.Code, rec.Body)
	}
}
