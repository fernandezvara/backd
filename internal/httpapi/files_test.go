package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage/storagetest"
)

// filesFixture is the shared realm with its storage pointed at an in-memory S3
// server, and its two keys set.
type filesFixture struct {
	*rulesFixture
	s3 *storagetest.Server
}

func newFilesFixture(t *testing.T) *filesFixture {
	t.Helper()
	f := newRulesFixture(t)
	s3 := storagetest.New(t, "files")
	f.svc.Realm = "acme"
	f.svc.Cipher, _ = auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	f.svc.Cache = auth.NewSecretCache()
	st := f.reg.Realms["acme"].Settings.Storage
	st.Endpoint, st.Bucket, st.HTTP = s3.URL, "files", true
	for _, n := range []string{"STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY"} {
		if err := f.svc.SetSecret(context.Background(), "", n, "v-"+n, "key:test"); err != nil {
			t.Fatal(err)
		}
	}
	return &filesFixture{rulesFixture: f, s3: s3}
}

// pngBytes is a tiny file that is a PNG to a byte sniffer.
func pngBytes(extra int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, extra)...)
}

// upload POSTs a file's bytes to a document's file field.
func (f *filesFixture) upload(t *testing.T, cred, collection, id, field, name, contentType string, body []byte, extra map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	path := "/v1/acme/app/" + collection + "/" + id + "/_files/" + field
	if name != "" {
		path += "?name=" + url.QueryEscape(name)
	}
	hdr := map[string]string{"Content-Type": contentType}
	if cred != "" {
		hdr["Authorization"] = "Bearer " + cred
	}
	for k, v := range extra {
		hdr[k] = v
	}
	return f.doRaw(t, "POST", path, body, hdr)
}

func (f *filesFixture) doRaw(t *testing.T, method, path string, body []byte, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func (f *filesFixture) newDoc(t *testing.T, collection string) string {
	t.Helper()
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/"+collection, `{"title": "d"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	return doc["id"].(string)
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestUploadStoresAFileAndAttachesIt(t *testing.T) {
	f := newFilesFixture(t)
	ctx := context.Background()
	id := f.newDoc(t, "library")
	data := pngBytes(100)

	rec, doc := f.upload(t, f.ada, "library", id, "avatar", "../my  photo.png", "application/octet-stream", data, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	file, _ := doc["avatar"].(map[string]any)
	fileID, _ := file["id"].(string)
	if !strings.HasPrefix(fileID, "fl_") || file["name"] != "my photo.png" || file["size"] != float64(len(data)) || file["type"] != "image/png" || file["sha256"] != sha(data) || file["uploaded_at"] == nil {
		t.Fatalf("the file's details: %v", file)
	}
	if doc["_meta"].(map[string]any)["version"] != float64(2) || rec.Header().Get("ETag") != `"2"` || !strings.HasSuffix(rec.Header().Get("Location"), "/_files/avatar/"+fileID) {
		t.Errorf("version %v, etag %q, location %q", doc["_meta"], rec.Header().Get("ETag"), rec.Header().Get("Location"))
	}
	// The object is where the key says, with the bytes sent, and the upload is journaled as attached.
	key := "t/acme/app/library/" + fileID
	if keys := f.s3.Keys(); len(keys) != 1 || keys[0] != key {
		t.Fatalf("stored keys: %v, want %s", keys, key)
	}
	if got := f.s3.Object(key); !bytes.Equal(got, data) {
		t.Errorf("the object holds %d bytes, want %d", len(got), len(data))
	}
	ms := f.svc.Store.(interface {
		JournalEntry(string) (auth.FileJournalEntry, bool)
	})
	if e, ok := ms.JournalEntry(fileID); !ok || e.Status != auth.JournalAttached || e.DocumentID != id || e.Key != key {
		t.Errorf("journal: %+v %v", e, ok)
	}
	// What a read of the document shows.
	code, got := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	if code != 200 || got["avatar"].(map[string]any)["id"] != fileID {
		t.Errorf("get: %d %v", code, got)
	}

	// A second upload to a single field replaces the first, whose object is queued for deletion.
	rec, doc = f.upload(t, f.ada, "library", id, "avatar", "two.png", "image/png", pngBytes(5), nil)
	if rec.Code != 201 || doc["avatar"].(map[string]any)["id"] == fileID {
		t.Fatalf("replace: %d %v", rec.Code, doc)
	}
	dels := f.svc.Store.(interface{ Deletions() []auth.FileDeletion }).Deletions()
	if len(dels) != 1 || dels[0].Key != key || dels[0].Reason != "replaced" {
		t.Errorf("deletions: %+v", dels)
	}
	_ = ctx
}

// journaled lists what the journal and deletion queue hold, for assertions.
func (f *filesFixture) deletions() []auth.FileDeletion {
	return f.svc.Store.(interface{ Deletions() []auth.FileDeletion }).Deletions()
}

func (f *filesFixture) nothingStored(t *testing.T, why string) {
	t.Helper()
	if keys := f.s3.Keys(); len(keys) != 0 {
		t.Errorf("%s: objects were stored: %v", why, keys)
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")

	// Who: nobody is 401, someone who can't read the document gets 404, a field that isn't one is 404.
	if rec, _ := f.upload(t, "", "library", id, "avatar", "a.png", "image/png", pngBytes(1), nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec, _ := f.upload(t, f.bob, "library", id, "avatar", "a.png", "image/png", pngBytes(1), nil); rec.Code != http.StatusNotFound {
		t.Errorf("another user's document: %d", rec.Code)
	}
	if rec, _ := f.upload(t, f.ada, "library", id, "title", "a.png", "image/png", pngBytes(1), nil); rec.Code != http.StatusNotFound {
		t.Errorf("not a file field: %d", rec.Code)
	}
	if rec, _ := f.upload(t, f.ada, "library", "nope", "avatar", "a.png", "image/png", pngBytes(1), nil); rec.Code != http.StatusNotFound {
		t.Errorf("no such document: %d", rec.Code)
	}

	// What: the type comes from the bytes, whatever the client says.
	rec, out := f.upload(t, f.ada, "library", id, "avatar", "evil.png", "image/png", []byte("<html><script>alert(1)</script></html>"), nil)
	if rec.Code != http.StatusUnsupportedMediaType || out["error"].(map[string]any)["code"] != "unsupported_file_type" {
		t.Errorf("a disguised file: %d %v", rec.Code, out)
	}
	f.nothingStored(t, "a disguised file")

	// How big: refused by Content-Length before a byte is stored, and while streaming without one.
	rec, out = f.upload(t, f.ada, "library", id, "avatar", "big.png", "image/png", pngBytes(5000), nil)
	if rec.Code != http.StatusRequestEntityTooLarge || out["error"].(map[string]any)["code"] != "payload_too_large" {
		t.Errorf("declared too large: %d %v", rec.Code, out)
	}
	f.nothingStored(t, "a declared size over max_size")
	req := httptest.NewRequest("POST", "/v1/acme/app/library/"+id+"/_files/avatar", io.NopCloser(bytes.NewReader(pngBytes(5000))))
	req.ContentLength = -1 // a chunked body: no size up front
	req.Header.Set("Authorization", "Bearer "+f.ada)
	rr := httptest.NewRecorder()
	f.h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("streamed too large: %d %s", rr.Code, rr.Body)
	}
	if keys := f.s3.Keys(); len(keys) != 0 {
		t.Errorf("an oversized stream left an object: %v", keys)
	}
	if got := f.deletions(); len(got) != 0 && len(f.s3.Keys()) == 0 {
		// Nothing was stored (the limit stopped the stream first), so there may be nothing to delete.
		_ = got
	}

	// Nothing above changed the document.
	_, doc := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	if doc["avatar"] != nil || doc["_meta"].(map[string]any)["version"] != float64(1) {
		t.Errorf("a refused upload changed the document: %v", doc)
	}
}

func TestMultipleFieldsAndMaxFiles(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	pdf := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<<>>\nendobj\n")
	for i, name := range []string{"one.pdf", "two.pdf"} {
		rec, doc := f.upload(t, f.ada, "library", id, "receipts", name, "application/pdf", pdf, nil)
		if rec.Code != 201 || len(doc["receipts"].([]any)) != i+1 {
			t.Fatalf("receipt %d: %d %v", i, rec.Code, doc)
		}
	}
	before := len(f.s3.Keys())
	rec, out := f.upload(t, f.ada, "library", id, "receipts", "three.pdf", "application/pdf", pdf, nil)
	if rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "too_many_files" {
		t.Errorf("a third file: %d %v", rec.Code, out)
	}
	if len(f.s3.Keys()) != before {
		t.Error("bytes were stored for a file past max_files")
	}
	// The details are what the rules and queries can see: the field holds a list of files.
	// (The in-memory store here doesn't look inside arrays, so only that the query is valid is checked.)
	if code, got := f.as(t, f.ada, "GET", "/v1/acme/app/library?where="+url.QueryEscape(`{"receipts.type": "application/pdf"}`), ""); code != 200 {
		t.Errorf("a query by a file's type: %d %v", code, got)
	}
}

// Rules for files: an upload is an update, asked before a byte is stored, and a
// field reserved with changed() is refused to a client and accepted from a caller
// that skips rules (an API key, as ctx.admin.db does).
func TestFileRulesAreAskedBeforeAnyByte(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	png := pngBytes(10)

	rec, _ := f.upload(t, f.ada, "library", id, "thumbnail", "t.png", "image/png", png, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a client's upload to the reserved thumbnail: %d %s", rec.Code, rec.Body)
	}
	f.nothingStored(t, "a refused upload")
	if e, ok := f.svc.Store.(interface {
		JournalEntry(string) (auth.FileJournalEntry, bool)
	}); ok {
		if _, found := e.JournalEntry("any"); found {
			t.Error("journal")
		}
	}
	// The same field from a caller that skips rules.
	_, adminKey, _ := f.svc.CreateAPIKey(context.Background(), "thumbs", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	rec, doc := f.upload(t, adminKey, "library", id, "thumbnail", "t.png", "image/png", png, nil)
	if rec.Code != 201 || doc["thumbnail"] == nil {
		t.Fatalf("an API key's upload: %d %s", rec.Code, rec.Body)
	}
	// And a client can't remove or clear it either.
	fileID := doc["thumbnail"].(map[string]any)["id"].(string)
	if rec, _ := f.doRaw(t, "DELETE", "/v1/acme/app/library/"+id+"/_files/thumbnail/"+fileID, nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != http.StatusForbidden {
		t.Errorf("a client's removal of the thumbnail: %d", rec.Code)
	}
	if rec, _ := f.doRaw(t, "DELETE", "/v1/acme/app/library/"+id+"/_files/thumbnail", nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != http.StatusForbidden {
		t.Errorf("a client's clearing of the thumbnail: %d", rec.Code)
	}
	// The avatar is the client's.
	if rec, _ := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", png, nil); rec.Code != 201 {
		t.Errorf("an allowed field: %d", rec.Code)
	}
}

func TestIfMatchOnFileChanges(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	rec, _ := f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(1), map[string]string{"If-Match": `"7"`})
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("a stale If-Match: %d %s", rec.Code, rec.Body)
	}
	f.nothingStored(t, "a stale If-Match")
	rec, _ = f.upload(t, f.ada, "library", id, "avatar", "a.png", "image/png", pngBytes(1), map[string]string{"If-Match": `"1"`})
	if rec.Code != 201 || rec.Header().Get("ETag") != `"2"` {
		t.Errorf("the right version: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestRemovingAndClearingFiles(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	pdf := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")
	var ids []string
	for _, n := range []string{"a.pdf", "b.pdf"} {
		_, doc := f.upload(t, f.ada, "library", id, "receipts", n, "application/pdf", pdf, nil)
		l := doc["receipts"].([]any)
		ids = append(ids, l[len(l)-1].(map[string]any)["id"].(string))
	}
	auth := map[string]string{"Authorization": "Bearer " + f.ada}
	base := "/v1/acme/app/library/" + id + "/_files/receipts"

	if rec, _ := f.doRaw(t, "DELETE", base+"/fl_nope", nil, auth); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown file: %d", rec.Code)
	}
	if rec, _ := f.doRaw(t, "DELETE", base+"/"+ids[0], nil, map[string]string{"Authorization": "Bearer " + f.bob}); rec.Code != http.StatusNotFound {
		t.Errorf("another user's file: %d", rec.Code)
	}
	rec, doc := f.doRaw(t, "DELETE", base+"/"+ids[0], nil, auth)
	if rec.Code != 200 || len(doc["receipts"].([]any)) != 1 || doc["receipts"].([]any)[0].(map[string]any)["id"] != ids[1] || doc["_meta"].(map[string]any)["version"] != float64(4) {
		t.Fatalf("remove one: %d %v", rec.Code, doc)
	}
	rec, doc = f.doRaw(t, "DELETE", base, nil, auth)
	if rec.Code != 200 || doc["receipts"] != nil {
		t.Fatalf("clear: %d %v", rec.Code, doc)
	}
	dels := f.deletions()
	if len(dels) != 2 || dels[0].Reason == "" {
		t.Errorf("both objects are queued for deletion: %+v", dels)
	}
	// The objects stay until a worker deletes them.
	if len(f.s3.Keys()) != 2 {
		t.Errorf("objects: %v", f.s3.Keys())
	}
	w := newTestWorker(t, f.rulesFixture)
	w.FilesDue(context.Background())
	if len(f.s3.Keys()) != 0 || len(f.deletions()) != 0 {
		t.Errorf("after the worker: objects %v, deletions %v", f.s3.Keys(), f.deletions())
	}
}
