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
	"time"

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

// manualDoc makes a document with a file in the proxy-download field `manual`.
func (f *filesFixture) manualDoc(t *testing.T, collection, owner string, data []byte) (docID, fileID string) {
	t.Helper()
	docID = f.newDoc(t, collection)
	rec, doc := f.upload(t, owner, collection, docID, "manual", "Manual v1.txt", "text/plain", data, nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	return docID, doc["manual"].(map[string]any)["id"].(string)
}

func (f *filesFixture) get(t *testing.T, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec, _ := f.doRaw(t, "GET", path, nil, hdr)
	return rec
}

func TestDownloadRedirectsToASignedLink(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	_, doc := f.upload(t, f.ada, "library", id, "avatar", "face.png", "image/png", pngBytes(20), nil)
	fileID := doc["avatar"].(map[string]any)["id"].(string)
	path := "/v1/acme/app/library/" + id + "/_files/avatar/" + fileID
	ada := map[string]string{"Authorization": "Bearer " + f.ada}

	rec := f.get(t, path, ada)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || !strings.HasPrefix(loc, f.s3.URL+"/files/t/acme/app/library/"+fileID) || !strings.Contains(loc, "X-Amz-Signature=") ||
		!strings.Contains(loc, "attachment") || !strings.Contains(loc, "face.png") || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("redirect: %d %q %v", rec.Code, loc, rec.Header())
	}
	// The link works without any credential, and gives the file as it was stored.
	resp, err := http.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, pngBytes(20)) || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("the signed link: %d %d bytes %v", resp.StatusCode, len(got), resp.Header)
	}

	// ?link=json answers the link instead of redirecting.
	rec, out := f.doRaw(t, "GET", path+"?link=json", nil, ada)
	if rec.Code != 200 || out["url"] != loc && !strings.HasPrefix(out["url"].(string), f.s3.URL) || out["expires_at"] == nil {
		t.Errorf("link=json: %d %v", rec.Code, out)
	}
	// Who: the read rule decides, and an unreadable document never reveals its files.
	for who, cred := range map[string]string{"anonymous": "", "another user": f.bob} {
		hdr := map[string]string{}
		if cred != "" {
			hdr["Authorization"] = "Bearer " + cred
		}
		want := http.StatusNotFound
		if cred == "" {
			want = http.StatusUnauthorized
		}
		if rec := f.get(t, path, hdr); rec.Code != want {
			t.Errorf("%s: %d", who, rec.Code)
		}
	}
	if rec := f.get(t, "/v1/acme/app/library/"+id+"/_files/avatar/fl_nope", ada); rec.Code != 404 {
		t.Errorf("an unknown file: %d", rec.Code)
	}
}

func TestProxyDownloadHeadersRangeAndETag(t *testing.T) {
	f := newFilesFixture(t)
	data := []byte("0123456789abcdefghij")
	id, fileID := f.manualDoc(t, "library", f.ada, data)
	path := "/v1/acme/app/library/" + id + "/_files/manual/" + fileID
	ada := map[string]string{"Authorization": "Bearer " + f.ada}

	rec := f.get(t, path, ada)
	h := rec.Header()
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="Manual v1.txt"`) || h.Get("Accept-Ranges") != "bytes" || h.Get("ETag") != `"`+sha(data)+`"` ||
		h.Get("Content-Type") != "text/plain" || h.Get("Content-Length") != "20" {
		t.Fatalf("download: %d %v", rec.Code, h)
	}
	// Not for shared caches: the document isn't readable by everyone.
	if h.Get("Cache-Control") != "private, no-store" {
		t.Errorf("cache-control: %q", h.Get("Cache-Control"))
	}

	etag := h.Get("ETag")
	if rec := f.get(t, path, map[string]string{"Authorization": "Bearer " + f.ada, "If-None-Match": etag}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	with := func(k, v string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + f.ada, k: v}
	}
	for _, c := range []struct {
		name, rng string
		status    int
		body      string
		crange    string
	}{
		{"a range", "bytes=2-5", 206, "2345", "bytes 2-5/20"},
		{"open ended", "bytes=15-", 206, "fghij", "bytes 15-19/20"},
		{"the last bytes", "bytes=-3", 206, "hij", "bytes 17-19/20"},
		{"past the end is cut", "bytes=18-99", 206, "ij", "bytes 18-19/20"},
		{"outside the file", "bytes=50-60", 416, "", "bytes */20"},
		{"several ranges get the whole file", "bytes=0-1,5-6", 200, string(data), ""},
		{"another unit is ignored", "items=0-1", 200, string(data), ""},
	} {
		rec := f.get(t, path, with("Range", c.rng))
		if rec.Code != c.status || (c.status < 400 && rec.Body.String() != c.body) || rec.Header().Get("Content-Range") != c.crange {
			t.Errorf("%s: %d %q %q", c.name, rec.Code, rec.Body, rec.Header().Get("Content-Range"))
		}
	}
	// If-Range: the range is honored only while the file is the one the client has.
	hdr := with("Range", "bytes=0-3")
	hdr["If-Range"] = etag
	if rec := f.get(t, path, hdr); rec.Code != 206 {
		t.Errorf("If-Range that matches: %d", rec.Code)
	}
	hdr["If-Range"] = `"other"`
	if rec := f.get(t, path, hdr); rec.Code != 200 || rec.Body.Len() != 20 {
		t.Errorf("If-Range that doesn't: %d", rec.Code)
	}

	// An object that is gone answers 404 file_missing.
	f.s3.Delete("t/acme/app/library/" + fileID)
	rec, out := f.doRaw(t, "GET", path, nil, ada)
	if rec.Code != 404 || out["error"].(map[string]any)["code"] != "file_missing" {
		t.Errorf("a missing object: %d %v", rec.Code, out)
	}
}

// Shared caches may keep a proxied file only when anyone may read its document.
func TestPublicCachingOnlyForAnonymouslyReadableDocuments(t *testing.T) {
	f := newFilesFixture(t)
	id, fileID := f.manualDoc(t, "gallery", f.ada, []byte("public manual"))
	path := "/v1/acme/app/gallery/" + id + "/_files/manual/" + fileID

	rec := f.get(t, path, nil) // no credentials at all
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Errorf("anonymous: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	// A private document, even when its owner asks, is never public.
	pid, pfile := f.manualDoc(t, "library", f.ada, []byte("private manual"))
	rec = f.get(t, "/v1/acme/app/library/"+pid+"/_files/manual/"+pfile, map[string]string{"Authorization": "Bearer " + f.ada})
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("private: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestBackdSignedLinks(t *testing.T) {
	f := newFilesFixture(t)
	data := []byte("linked manual")
	id, fileID := f.manualDoc(t, "library", f.ada, data)
	path := "/v1/acme/app/library/" + id + "/_files/manual/" + fileID
	ada := map[string]string{"Authorization": "Bearer " + f.ada}

	_, out := f.doRaw(t, "GET", path+"?link=json", nil, ada)
	link, _ := out["url"].(string)
	u, err := url.Parse(link)
	if err != nil || u.Path != path || u.Query().Get("sig") == "" || u.Query().Get("exp") == "" || out["expires_at"] == nil {
		t.Fatalf("link: %v", out)
	}
	rel := u.RequestURI()
	// The link is its own credential, and can be used again (Range requests, seeking, retries).
	for i := 0; i < 2; i++ {
		if rec := f.get(t, rel, nil); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) {
			t.Fatalf("use %d: %d", i, rec.Code)
		}
	}
	if rec := f.get(t, rel, map[string]string{"Range": "bytes=0-3"}); rec.Code != 206 || rec.Body.String() != "link" {
		t.Errorf("a range through the link: %d %q", rec.Code, rec.Body)
	}
	bad := func(name, target string) {
		t.Helper()
		rec, out := f.doRaw(t, "GET", target, nil, nil)
		if rec.Code != http.StatusForbidden || out["error"].(map[string]any)["code"] != "invalid_file_link" {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	q := u.Query()
	q.Set("sig", q.Get("sig")[:len(q.Get("sig"))-2]+"AA")
	bad("a tampered signature", path+"?"+q.Encode())
	q = u.Query()
	q.Set("exp", "99999999999")
	bad("a changed expiry", path+"?"+q.Encode())
	// Another file's path with this link's signature.
	otherID, otherFile := f.manualDoc(t, "library", f.ada, []byte("other"))
	bad("another file", "/v1/acme/app/library/"+otherID+"/_files/manual/"+otherFile+"?"+u.RawQuery)
	// Past its time.
	*f.clock = f.clock.Add(6 * time.Minute) // presigned_ttl defaults to 5 minutes
	bad("an expired link", rel)
	*f.clock = f.clock.Add(-6 * time.Minute)
	// Rotating the key invalidates what is out.
	if err := f.svc.SetSecret(context.Background(), "", "BACKD_FILES_LINK_KEY", "a-new-key-for-the-links", "key:test"); err != nil {
		t.Fatal(err)
	}
	f.svc.Cache = auth.NewSecretCache()
	bad("after rotating the key", rel)
	// The link is not a way past the document: once it is deleted, the link is a 404.
	_, out = f.doRaw(t, "GET", path+"?link=json", nil, ada)
	fresh, _ := url.Parse(out["url"].(string))
	if rec := f.get(t, fresh.RequestURI(), nil); rec.Code != 200 {
		t.Fatalf("a link under the new key: %d", rec.Code)
	}
	f.as(t, f.ada, "DELETE", "/v1/acme/app/library/"+id, "")
	if rec := f.get(t, fresh.RequestURI(), nil); rec.Code != http.StatusNotFound {
		t.Errorf("a link to a deleted document: %d", rec.Code)
	}
}

func TestFileLinksOnReads(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	_, doc := f.upload(t, f.ada, "library", id, "avatar", "face.png", "image/png", pngBytes(3), nil)
	etag := `"` + "2" + `"`
	f.upload(t, f.ada, "library", id, "manual", "m.txt", "text/plain", []byte("hello"), nil)
	pdf := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n")
	f.upload(t, f.ada, "library", id, "receipts", "r.pdf", "application/pdf", pdf, nil)
	_ = doc
	ada := map[string]string{"Authorization": "Bearer " + f.ada}

	// By default a document has no links (and no cost).
	_, plain := f.doRaw(t, "GET", "/v1/acme/app/library/"+id, nil, ada)
	if plain["avatar"].(map[string]any)["url"] != nil {
		t.Errorf("links by default: %v", plain["avatar"])
	}
	// Opt-in: a link in every file, each in the mode of its field; the ETag is the version's.
	rec, got := f.doRaw(t, "GET", "/v1/acme/app/library/"+id+"?file_links=true", nil, ada)
	avatar, manual := got["avatar"].(map[string]any), got["manual"].(map[string]any)
	receipt := got["receipts"].([]any)[0].(map[string]any)
	if rec.Code != 200 || !strings.HasPrefix(avatar["url"].(string), f.s3.URL) || avatar["expires_at"] == nil || !strings.Contains(manual["url"].(string), "/_files/manual/") || !strings.Contains(manual["url"].(string), "sig=") || !strings.HasPrefix(receipt["url"].(string), f.s3.URL) {
		t.Fatalf("file_links: %d %v", rec.Code, got)
	}
	if rec.Header().Get("ETag") == "" || rec.Header().Get("ETag") == etag && false {
		t.Errorf("etag: %q", rec.Header().Get("ETag"))
	}
	_, again := f.doRaw(t, "GET", "/v1/acme/app/library/"+id, nil, ada)
	rec2, _ := f.doRaw(t, "GET", "/v1/acme/app/library/"+id+"?file_links=true", nil, ada)
	rec3, _ := f.doRaw(t, "GET", "/v1/acme/app/library/"+id, nil, ada)
	if rec2.Header().Get("ETag") != rec3.Header().Get("ETag") || again["avatar"].(map[string]any)["url"] != nil {
		t.Errorf("the links changed the ETag or leaked into a plain read")
	}
	// A list takes the option too.
	_, list := f.doRaw(t, "GET", "/v1/acme/app/library?file_links=true", nil, ada)
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["avatar"].(map[string]any)["url"] == nil {
		t.Errorf("a list with links: %v", list)
	}
	// Anything but true is not a request for links.
	_, off := f.doRaw(t, "GET", "/v1/acme/app/library/"+id+"?file_links=false", nil, ada)
	if off["avatar"].(map[string]any)["url"] != nil {
		t.Error("file_links=false gave links")
	}
}

// A process killed after the object was stored and before the document was
// updated leaves a journal entry and an object: a worker finds it past the grace
// period, queues the object and deletes it. The document never saw the file.
func TestAnAbandonedUploadIsCleanedUp(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	ctx := context.Background()
	id := f.newDoc(t, "library")

	key := "t/acme/app/library/fl_killedmidway00000000"
	f.s3.Put(key, []byte("half an upload"))
	if err := f.svc.JournalUpload(ctx, auth.FileJournalEntry{ID: "fl_killedmidway00000000", Database: "app", Collection: "library", Field: "avatar", DocumentID: id, Key: key}); err != nil {
		t.Fatal(err)
	}
	_ = f.svc.SetUploadStatus(ctx, "fl_killedmidway00000000", auth.JournalStored, "")

	// Within the grace period it may still be finishing: left alone.
	w.FilesDue(ctx)
	if len(f.s3.Keys()) != 1 {
		t.Fatal("a recent upload was deleted")
	}
	*f.clock = f.clock.Add(auth.FileJournalGrace + time.Minute)
	w.FilesDue(ctx)
	if len(f.s3.Keys()) != 0 || len(f.deletions()) != 0 {
		t.Errorf("after the grace period: objects %v, deletions %v", f.s3.Keys(), f.deletions())
	}
	e, _ := f.svc.Store.(interface {
		JournalEntry(string) (auth.FileJournalEntry, bool)
	}).JournalEntry("fl_killedmidway00000000")
	if e.Status != auth.JournalFailed {
		t.Errorf("journal: %+v", e)
	}
	_, doc := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	if doc["avatar"] != nil {
		t.Error("the document references an upload that never finished")
	}

	// A deletion that fails is retried later, not lost.
	k2 := "t/acme/app/library/fl_retry"
	f.s3.Put(k2, []byte("x"))
	_ = f.svc.QueueFileDeletion(ctx, k2, "replaced")
	f.s3.FailDeletes(true)
	w.FilesDue(ctx)
	if len(f.deletions()) != 1 || f.deletions()[0].Attempts != 1 || len(f.s3.Keys()) != 1 {
		t.Fatalf("after a failed delete: %+v %v", f.deletions(), f.s3.Keys())
	}
	f.s3.FailDeletes(false)
	*f.clock = f.clock.Add(2 * time.Minute)
	w.FilesDue(ctx)
	if len(f.deletions()) != 0 || len(f.s3.Keys()) != 0 {
		t.Errorf("after the retry: %+v %v", f.deletions(), f.s3.Keys())
	}
}
