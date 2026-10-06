package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
