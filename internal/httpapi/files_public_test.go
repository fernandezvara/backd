package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestSanitizePublicName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"photo.jpg", "photo.jpg", true},
		{"../a b.PNG", "a b.PNG", true},
		{`C:\Users\me\Holiday  photo.jpeg`, "Holiday photo.jpeg", true},
		{"a*b?<c>.png", "a_b__c_.png", true},
		{"ünï cødé 写真.png", "ünï cødé 写真.png", true},
		{"cafe\u0301.png", "café.png", true},       // NFC
		{"tab\there\x00.jpg", "tabhere.jpg", true}, // control characters go
		{"  ..hidden.png", "hidden.png", true},
		{"my (copy)+1_final-2.png", "my (copy)+1_final-2.png", true},
		{"photo", "photo", true}, // no extension: the type check refuses it, not the name
		{"", "", false},
		{".png", "", false},
		{"...", "", false},
		{"..", "", false},
		{"   .jpg", "", false},
		{"/", "", false},
	} {
		got, ok := sanitizePublicName(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("sanitizePublicName(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	long := strings.Repeat("é", 100) + ".jpg" // 200 bytes of stem
	got, ok := sanitizePublicName(long)
	if !ok || len(got) > maxPublicNameBytes || !strings.HasSuffix(got, ".jpg") || !utf8.ValidString(got) {
		t.Errorf("a long name = %q (%d bytes), %v", got, len(got), ok)
	}
	if got, ok := sanitizePublicName(strings.Repeat("a", 300) + ".png"); !ok || len(got) != maxPublicNameBytes || !strings.HasSuffix(got, "a.png") {
		t.Errorf("an ASCII name = %d bytes %v", len(got), ok)
	}
}

// publicUpload uploads body as name to the public field of a new document in `pictures`.
func (f *filesFixture) publicUpload(t *testing.T, name string, body []byte) (docID string, rec *httptest.ResponseRecorder, doc map[string]any) {
	t.Helper()
	docID = f.newDoc(t, "pictures")
	rec, doc = f.upload(t, f.ada, "pictures", docID, "photo", name, "application/octet-stream", body, nil)
	return docID, rec, doc
}

func (f *filesFixture) publicObject(t *testing.T, key string) *http.Response {
	t.Helper()
	res, err := http.Get(f.pub.URL + "/public/" + key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestUploadToAPublicFieldStoresItInThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	dirty, clean := dirtyJPEG(t, 16, 8)
	docID, rec, doc := f.publicUpload(t, "Holiday photo.JPG", dirty)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	file, _ := doc["photo"].(map[string]any)
	id, _ := file["id"].(string)
	key := "t/acme/app/pictures/" + id + "/Holiday photo.JPG"
	if file["public"] != true || file["key_name"] != "Holiday photo.JPG" || file["name"] != "Holiday photo.JPG" || file["type"] != "image/jpeg" {
		t.Fatalf("file = %v", file)
	}
	if want := "https://cdn.acme.example/t/acme/app/pictures/" + id + "/Holiday%20photo.JPG"; file["url"] != want {
		t.Errorf("url = %v, want %v", file["url"], want)
	}
	if keys := f.pub.Keys(); len(keys) != 1 || keys[0] != key {
		t.Errorf("public storage: %v", keys)
	}
	if len(f.s3.Keys()) != 0 {
		t.Errorf("the private storage got %v", f.s3.Keys())
	}
	// Stored with the headers a CDN needs, and without the metadata.
	res := f.publicObject(t, key)
	stored, _ := io.ReadAll(res.Body)
	if res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || res.Header.Get("Content-Disposition") != "inline" || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("headers: %v", res.Header)
	}
	if bytes.Contains(stored, []byte("SECRET")) || !bytes.Equal(pixelsOf(t, stored), pixelsOf(t, clean)) {
		t.Error("the public file kept its metadata")
	}
	// The journal says where it is.
	e, _ := f.svc.Store.(interface {
		JournalEntry(string) (auth.FileJournalEntry, bool)
	}).JournalEntry(id)
	if !e.Public || e.KeyName != "Holiday photo.JPG" || e.Key != key {
		t.Errorf("journal: %+v", e)
	}
	// A read carries the address too, in a get and in a list, for anyone who may read the document.
	_, got := f.as(t, f.ada, "GET", "/v1/acme/app/pictures/"+docID, "")
	if got["photo"].(map[string]any)["url"] != file["url"] {
		t.Errorf("get: %v", got["photo"])
	}
	rec2, list := f.doRaw(t, "GET", "/v1/acme/app/pictures", nil, nil)
	items, _ := list["items"].([]any)
	if rec2.Code != 200 || len(items) != 1 || items[0].(map[string]any)["photo"].(map[string]any)["url"] != file["url"] {
		t.Errorf("anonymous list: %d %v", rec2.Code, list)
	}
	// Downloading through the API still works and is the same bytes.
	if r := f.get(t, "/v1/acme/app/pictures/"+docID+"/_files/photo/"+id, map[string]string{"Authorization": "Bearer " + f.ada}); r.Code != 200 || !bytes.Equal(r.Body.Bytes(), stored) {
		t.Errorf("download: %d", r.Code)
	}
}

func TestPublicUploadRefusesWhatIsNotAnImageWithItsExtension(t *testing.T) {
	f := newFilesFixture(t)
	png := realPNG(t, 8, 8)
	_, jpg := dirtyJPEG(t, 8, 8)
	for _, tc := range []struct {
		name string
		body []byte
		code int
		want string
	}{
		{"", png, 400, "invalid_file_name"},
		{".png", png, 400, "invalid_file_name"},
		{"photo", png, 400, "file_extension_mismatch"},
		{"photo.txt", png, 400, "file_extension_mismatch"},
		{"photo.jpg", png, 400, "file_extension_mismatch"}, // not renamed: a PNG called .jpg is refused
		{"photo.png", jpg, 400, "file_extension_mismatch"},
		{"photo.gif", []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), 415, "unsupported_file_type"},
		{"notes.png", []byte("just text, not a picture"), 415, "unsupported_file_type"},
		{"vector.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 415, "unsupported_file_type"},
		{"page.html", []byte("<html><body>hi</body></html>"), 415, "unsupported_file_type"},
	} {
		_, rec, _ := f.publicUpload(t, tc.name, tc.body)
		var out struct{ Error struct{ Code string } }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != tc.code || out.Error.Code != tc.want {
			t.Errorf("%q: %d %s, want %d %s", tc.name, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
	if len(f.pub.Keys()) != 0 || len(f.s3.Keys()) != 0 {
		t.Errorf("something was stored: %v %v", f.pub.Keys(), f.s3.Keys())
	}
	// A damaged picture is refused rather than published with what it carries.
	if _, rec, _ := f.publicUpload(t, "cut.png", pngBytes(5)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a cut picture: %d %s", rec.Code, rec.Body)
	}
	// The jpg and the jpeg spellings both fit a JPEG.
	for _, name := range []string{"a.jpg", "b.JPEG"} {
		if _, rec, _ := f.publicUpload(t, name, jpg); rec.Code != http.StatusCreated {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestPublicFilesChangeTheDocumentsValidator(t *testing.T) {
	f := newFilesFixture(t)
	png := realPNG(t, 8, 8)
	docID, rec, _ := f.publicUpload(t, "a.png", png)
	if rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	plain := f.newDoc(t, "pictures")
	path := "/v1/acme/app/pictures/" + docID
	r1 := f.get(t, path, nil)
	tag := r1.Header().Get("ETag")
	if !strings.Contains(tag, ".r") {
		t.Fatalf("a document with a public file has a plain tag: %s", tag)
	}
	if r := f.get(t, path, map[string]string{"If-None-Match": tag}); r.Code != http.StatusNotModified {
		t.Errorf("same url: %d", r.Code)
	}
	// Another address for the same bytes is another validator, so clients refetch.
	f.reg.Realms["acme"].Settings.PublicStorage.URL = "https://img.acme.example"
	r2 := f.get(t, path, map[string]string{"If-None-Match": tag})
	if r2.Code != http.StatusOK || r2.Header().Get("ETag") == tag {
		t.Errorf("new url: %d %s", r2.Code, r2.Header().Get("ETag"))
	}
	var doc map[string]any
	_ = json.Unmarshal(r2.Body.Bytes(), &doc)
	if u, _ := doc["photo"].(map[string]any)["url"].(string); !strings.HasPrefix(u, "https://img.acme.example/") {
		t.Errorf("url = %v", u)
	}
	// A document without public files keeps the plain tag; If-Match still takes the plain one.
	if r := f.get(t, "/v1/acme/app/pictures/"+plain, nil); strings.Contains(r.Header().Get("ETag"), ".r") {
		t.Errorf("plain document: %s", r.Header().Get("ETag"))
	}
	if r := f.get(t, path, nil); r.Header().Get("ETag") == tag {
		t.Error("the tag did not change with the url")
	}
	// The list's validator follows too.
	l1 := f.get(t, "/v1/acme/app/pictures", nil)
	f.reg.Realms["acme"].Settings.PublicStorage.URL = "https://other.acme.example"
	l2 := f.get(t, "/v1/acme/app/pictures", map[string]string{"If-None-Match": l1.Header().Get("ETag")})
	if l2.Code != http.StatusOK {
		t.Errorf("list after a url change: %d", l2.Code)
	}
}

func TestPublicFilesInPendingUploadsReplacementsAndLinks(t *testing.T) {
	f := newFilesFixture(t)
	png := realPNG(t, 8, 8)
	// A pending upload, then a document that names it.
	hdr := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "image/png"}
	rec, up := f.doRaw(t, "POST", "/v1/acme/app/pictures/_files/photo/uploads?name=Cover.png", png, hdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("pending: %d %s", rec.Code, rec.Body)
	}
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/pictures", `{"title": "p", "photo": {"upload": "`+up["upload_id"].(string)+`", "token": "`+up["upload_token"].(string)+`"}}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	first := doc["photo"].(map[string]any)
	id := first["id"].(string)
	if first["public"] != true || first["key_name"] != "Cover.png" || first["url"] != "https://cdn.acme.example/t/acme/app/pictures/"+id+"/Cover.png" {
		t.Errorf("attached file = %v", first)
	}
	if keys := f.pub.Keys(); len(keys) != 1 {
		t.Fatalf("public keys: %v", keys)
	}
	docID := doc["id"].(string)
	// ?file_links=true leaves a public file at its own address.
	_, linked := f.doRaw(t, "GET", "/v1/acme/app/pictures/"+docID+"?file_links=true", nil, map[string]string{"Authorization": "Bearer " + f.ada})
	if lf := linked["photo"].(map[string]any); lf["url"] != first["url"] || lf["expires_at"] != nil {
		t.Errorf("file_links changed a public file: %v", lf)
	}
	// Replacing the single field's file queues the old object in the public storage.
	w := newTestWorker(t, f.rulesFixture)
	f.svc.Now = func() time.Time { return *f.clock }
	rec, doc2 := f.upload(t, f.ada, "pictures", docID, "photo", "Second.png", "image/png", realPNG(t, 9, 9), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if q := f.deletions(); len(q) != 1 || !q[0].Public || !strings.HasSuffix(q[0].Key, id+"/Cover.png") {
		t.Fatalf("queued: %+v", q)
	}
	w.FilesDue(context.Background())
	if keys := f.pub.Keys(); len(keys) != 1 || !strings.HasSuffix(keys[0], "/Second.png") {
		t.Errorf("after the worker: %v", keys)
	}
	// Removing the file queues the rest.
	newID := doc2["photo"].(map[string]any)["id"].(string)
	if r, _ := f.doRaw(t, "DELETE", "/v1/acme/app/pictures/"+docID+"/_files/photo/"+newID, nil, map[string]string{"Authorization": "Bearer " + f.ada}); r.Code != http.StatusOK {
		t.Fatalf("delete file: %d", r.Code)
	}
	w.FilesDue(context.Background())
	if len(f.pub.Keys()) != 0 || len(f.deletions()) != 0 {
		t.Errorf("after delete: %v %v", f.pub.Keys(), f.deletions())
	}
}

func TestPublicVersionsAreMadeInThePublicStorage(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	docID, rec, doc := f.publicUpload(t, "Big view.png", realPNG(t, 40, 20))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	file := doc["photo"].(map[string]any)
	id := file["id"].(string)
	if th := versionsOf(t, file)["thumb"].(map[string]any); th["status"] != "pending" || th["url"] != nil {
		t.Fatalf("thumb right after the upload: %v", th)
	}
	runImageJobs(t, w)
	_, got := f.as(t, f.ada, "GET", "/v1/acme/app/pictures/"+docID, "")
	th := versionsOf(t, got["photo"].(map[string]any))["thumb"].(map[string]any)
	if th["status"] != "ready" {
		t.Fatalf("thumb: %v", th)
	}
	vkey := "t/acme/app/pictures/" + id + "/thumb/Big view.png"
	if want := "https://cdn.acme.example/t/acme/app/pictures/" + id + "/thumb/Big%20view.png"; th["url"] != want {
		t.Errorf("version url = %v, want %v", th["url"], want)
	}
	if keys := f.pub.Keys(); len(keys) != 2 {
		t.Fatalf("public keys: %v", keys)
	}
	res := f.publicObject(t, vkey)
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "public, max-age=86400" {
		t.Errorf("version object: %d %v", res.StatusCode, res.Header)
	}
	if len(f.s3.Keys()) != 0 {
		t.Errorf("the private storage got %v", f.s3.Keys())
	}
	// Deleting the document removes the original and the version from the public storage.
	if r, _ := f.doRaw(t, "DELETE", "/v1/acme/app/pictures/"+docID, nil, map[string]string{"Authorization": "Bearer " + f.ada}); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", r.Code)
	}
	w.FilesDue(context.Background())
	if len(f.pub.Keys()) != 0 {
		t.Errorf("left behind: %v", f.pub.Keys())
	}
}

// A private field of the collection still takes files, and a public file is not affected by a
// field that stopped being public: where it is stays recorded on the file.
func TestPrivateFieldsAndFilesThatOutliveTheSetting(t *testing.T) {
	f := newFilesFixture(t)
	docID := f.newDoc(t, "pictures")
	if rec, _ := f.upload(t, f.ada, "pictures", docID, "secret", "a.png", "image/png", pngBytes(5), nil); rec.Code != http.StatusCreated {
		t.Errorf("private upload: %d %s", rec.Code, rec.Body)
	}
	if len(f.s3.Keys()) != 1 || len(f.pub.Keys()) != 0 {
		t.Errorf("keys: %v %v", f.s3.Keys(), f.pub.Keys())
	}
	rec, doc := f.upload(t, f.ada, "pictures", docID, "photo", "a.png", "image/png", realPNG(t, 8, 8), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("public upload: %d %s", rec.Code, rec.Body)
	}
	id := doc["photo"].(map[string]any)["id"].(string)
	// The field is made private in the configuration: the file stays public and is still read.
	f.reg.Realms["acme"].Databases["app"].Collections["pictures"].Files["photo"].Public = false
	_, got := f.as(t, f.ada, "GET", "/v1/acme/app/pictures/"+docID, "")
	if got["photo"].(map[string]any)["url"] == nil {
		t.Errorf("the file lost its address: %v", got["photo"])
	}
	if r := f.get(t, "/v1/acme/app/pictures/"+docID+"/_files/photo/"+id, map[string]string{"Authorization": "Bearer " + f.ada}); r.Code != http.StatusOK {
		t.Errorf("download: %d", r.Code)
	}
}

// Functions remake, generate and remove versions of a public file in the public storage, with the
// extension of what was made, and a regeneration rewrites them there.
func TestFunctionsAndRegenerationWorkOnPublicVersions(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	docID, rec, doc := f.publicUpload(t, "Sunset.png", realPNG(t, 40, 20))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	id := doc["photo"].(map[string]any)["id"].(string)
	runImageJobs(t, w)
	runWorker(t, w) // the function routes wait for it
	prefix := "t/acme/app/pictures/" + id + "/"
	base := "/v1/acme/app/pictures/" + docID + "/_files/photo/" + id + "/versions/"
	ada := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json"}
	state := func(name string) map[string]any {
		return versionsOf(t, f.readDoc2(t, docID)["photo"].(map[string]any))[name].(map[string]any)
	}
	if keys := f.pub.Keys(); len(keys) != 2 {
		t.Fatalf("keys: %v", keys)
	}

	// The declared version again: the same place, a new copy.
	oldThumb := state("thumb")["id"]
	if rec, out := f.doRaw(t, "POST", base+"thumb", nil, ada); rec.Code != 200 || out["id"] == oldThumb || out["status"] != "ready" {
		t.Fatalf("regenerate: %d %v", rec.Code, out)
	}
	if keys := f.pub.Keys(); len(keys) != 2 {
		t.Errorf("a copy made again lands on the same object: %v", keys)
	}
	// A version made with other parameters, in another format: its key has that extension.
	if rec, out := f.doRaw(t, "POST", base+"mark", []byte(`{"max_width": 8, "format": "jpeg"}`), ada); rec.Code != 200 || out["type"] != "image/jpeg" {
		t.Fatalf("generate: %d %v", rec.Code, out)
	}
	res := f.publicObject(t, prefix+"mark/Sunset.jpg")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || res.Header.Get("Cache-Control") != "public, max-age=86400" {
		t.Errorf("generated copy: %d %v", res.StatusCode, res.Header)
	}
	if u, _ := state("mark")["url"].(string); u != "https://cdn.acme.example/t/acme/app/pictures/"+id+"/mark/Sunset.jpg" {
		t.Errorf("url of the generated copy = %q", u)
	}
	// Removing it queues its object in the public storage.
	if rec, _ := f.doRaw(t, "DELETE", base+"mark", nil, ada); rec.Code != 200 {
		t.Fatalf("delete version: %d", rec.Code)
	}
	if q := f.deletions(); len(q) != 1 || !q[0].Public || q[0].Key != prefix+"mark/Sunset.jpg" {
		t.Fatalf("queued: %+v", q)
	}
	w.FilesDue(context.Background())
	if keys := f.pub.Keys(); len(keys) != 2 {
		t.Errorf("after the worker: %v", keys)
	}

	// A changed declaration, regenerated by the scan job, rewrites the copy in the public storage.
	c, _ := f.reg.Collection("acme", "app", "pictures")
	for i := range c.Files["photo"].Versions {
		if c.Files["photo"].Versions[i].Name == "thumb" {
			c.Files["photo"].Versions[i].Params.MaxWidth, c.Files["photo"].Versions[i].Params.MaxHeight = 6, 6
		}
	}
	code, out := f.regenerate(t, f.key, `{"database": "app", "collection": "pictures", "field": "photo", "version": "thumb"}`)
	if code != 202 {
		t.Fatalf("regenerate job: %d %v", code, out)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if j, _, _ := f.svc.GetJob(context.Background(), out["id"].(string)); j.Status == auth.JobDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := state("thumb"); got["width"] != float64(6) || got["status"] != "ready" {
		t.Errorf("thumb after the scan: %v", got)
	}
	if keys := f.pub.Keys(); len(keys) != 2 || len(f.s3.Keys()) != 0 {
		t.Errorf("keys: public %v, private %v", keys, f.s3.Keys())
	}
}

func (f *filesFixture) readDoc2(t *testing.T, id string) map[string]any {
	t.Helper()
	code, doc := f.as(t, f.ada, "GET", "/v1/acme/app/pictures/"+id, "")
	if code != 200 {
		t.Fatalf("read: %d %v", code, doc)
	}
	return doc
}
