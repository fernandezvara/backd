package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func declare(name string, data []byte, typ string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "size": len(data), "type": typ, "sha256": sha(data)})
	return string(b)
}

func (f *filesFixture) startOn(t *testing.T, cred, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	hdr := map[string]string{"Content-Type": "application/json"}
	if cred != "" {
		hdr["Authorization"] = "Bearer " + cred
	}
	return f.doRaw(t, "POST", path, []byte(body), hdr)
}

// putToLink does what a browser does with a started upload: PUT the bytes to the signed
// link with the headers it names.
func putToLink(t *testing.T, start map[string]any, data []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, start["url"].(string), bytes.NewReader(data))
	for k, v := range start["headers"].(map[string]any) {
		req.Header.Set(k, v.(string))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (f *filesFixture) complete(t *testing.T, cred, collection, field, id, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return f.startOn(t, cred, "/v1/acme/app/"+collection+"/_files/"+field+"/uploads/"+id+"/complete", `{"upload_token": "`+token+`"}`)
}

func (f *filesFixture) videoDoc(t *testing.T) string {
	t.Helper()
	// A required file field: the document is made with an upload, as any is.
	id, tok := f.pendingDirect(t, f.ada, "clip", pngBytes(10))
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/videos", `{"title": "v", "clip": `+ref(id, tok)+`}`)
	if code != 201 {
		t.Fatalf("create video: %d %v", code, doc)
	}
	return doc["id"].(string)
}

func (f *filesFixture) pendingDirect(t *testing.T, cred, field string, data []byte) (id, token string) {
	t.Helper()
	rec, out := f.startOn(t, cred, "/v1/acme/app/videos/_files/"+field+"/uploads", declare("c.png", data, "image/png"))
	if rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if resp := putToLink(t, out, data); resp.StatusCode != 200 {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	id, token = out["upload_id"].(string), out["upload_token"].(string)
	if rec, out := f.complete(t, cred, "videos", field, id, token); rec.Code != 200 {
		t.Fatalf("complete: %d %v", rec.Code, out)
	}
	return id, token
}

func TestDirectUploadToADocument(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	data := pngBytes(200)
	path := "/v1/acme/app/videos/" + id + "/_files/clips/uploads"

	rec, out := f.startOn(t, f.ada, path, declare("../my clip.png", data, "image/png"))
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	fileID, token := out["upload_id"].(string), out["upload_token"].(string)
	link, _ := out["url"].(string)
	headers, _ := out["headers"].(map[string]any)
	lower := map[string]string{}
	for k, v := range headers {
		lower[strings.ToLower(k)] = v.(string)
	}
	// The link is for the bucket, signed, with type and checksum signed in.
	if !strings.HasPrefix(link, f.s3.URL+"/files/t/acme/app/videos/"+fileID) || !strings.Contains(link, "X-Amz-Signature=") || out["method"] != "PUT" ||
		lower["content-type"] != "image/png" || lower["x-amz-checksum-sha256"] == "" || !strings.HasPrefix(fileID, "fl_") || !strings.HasPrefix(token, "fut_") || out["url_expires_at"] == nil {
		t.Fatalf("start reply: %v", out)
	}
	if keys := f.s3.Keys(); len(keys) != 1 { // the clip made with the document
		t.Errorf("the start stored something: %v", keys)
	}
	e := f.journal(fileID)
	if !e.Direct || e.Pending || e.Status != auth.JournalWriting || e.DocumentID != id || e.TokenHash == "" || e.TokenHash == token {
		t.Errorf("journal: %+v", e)
	}
	// Not uploaded yet: completing is a 409 that can be retried.
	rec, out2 := f.complete(t, f.ada, "videos", "clips", fileID, token)
	if rec.Code != 409 || out2["error"].(map[string]any)["code"] != "file_not_uploaded" {
		t.Fatalf("complete before the PUT: %d %v", rec.Code, out2)
	}
	if resp := putToLink(t, out, data); resp.StatusCode != 200 {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	rec, doc := f.complete(t, f.ada, "videos", "clips", fileID, token)
	files, _ := doc["clips"].([]any)
	if rec.Code != 201 || len(files) != 1 {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body)
	}
	file := files[0].(map[string]any)
	if file["id"] != fileID || file["name"] != "my clip.png" || file["type"] != "image/png" || file["sha256"] != sha(data) || file["size"] != float64(len(data)) {
		t.Errorf("details: %v", file)
	}
	if doc["_meta"].(map[string]any)["version"] != float64(2) || rec.Header().Get("Location") != "/v1/acme/app/videos/"+id+"/_files/clips/"+fileID || rec.Header().Get("ETag") != `"2"` {
		t.Errorf("version %v, location %q", doc["_meta"], rec.Header().Get("Location"))
	}
	if e := f.journal(fileID); e.Status != auth.JournalAttached {
		t.Errorf("journal after: %+v", e)
	}
	// It is the document's file like any other, and the upload can't be completed twice.
	if rec, _ := f.doRaw(t, "GET", rec.Header().Get("Location")+"?link=json", nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != 200 {
		t.Errorf("download: %d", rec.Code)
	}
	if rec, _ := f.complete(t, f.ada, "videos", "clips", fileID, token); rec.Code != 400 {
		t.Errorf("completed twice: %d", rec.Code)
	}
}

func TestDirectStartRefusals(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	data := pngBytes(20)
	path := "/v1/acme/app/videos/" + id + "/_files/clips/uploads"
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"nothing declared":     {`{}`, 400},
		"not JSON":             {`nope`, 400},
		"no name":              {`{"size": 5, "type": "image/png", "sha256": "` + sha(data) + `"}`, 400},
		"no size":              {`{"name": "a", "type": "image/png", "sha256": "` + sha(data) + `"}`, 400},
		"an empty file":        {`{"name": "a", "size": 0, "type": "image/png", "sha256": "` + sha(data) + `"}`, 400},
		"a sha that isn't one": {`{"name": "a", "size": 5, "type": "image/png", "sha256": "abc"}`, 400},
		"no type":              {`{"name": "a", "size": 5, "sha256": "` + sha(data) + `"}`, 400},
		"over max_size":        {`{"name": "a", "size": 5000, "type": "image/png", "sha256": "` + sha(data) + `"}`, 400},
	} {
		if rec, out := f.startOn(t, f.ada, path, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if rec, _ := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/clip/uploads", declare("a", data, "text/plain")); rec.Code != 415 {
		t.Errorf("a type the field refuses: %d", rec.Code)
	}
	// A proxy field takes its bytes through backd.
	rec, out := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/photo/uploads", declare("a", data, "image/png"))
	if rec.Code != 409 || out["error"].(map[string]any)["code"] != "upload_mode_mismatch" {
		t.Errorf("a direct start on a proxy field: %d %v", rec.Code, out)
	}
	// And the other way round: a proxy upload to a direct field.
	rec, out = f.upload(t, f.ada, "videos", id, "clip", "a.png", "image/png", data, nil)
	if rec.Code != 409 || out["error"].(map[string]any)["code"] != "upload_mode_mismatch" {
		t.Errorf("a proxy upload to a direct field: %d %v", rec.Code, out)
	}
	// The document isn't another user's to change, and If-Match applies.
	if rec, _ := f.startOn(t, f.bob, path, declare("a", data, "image/png")); rec.Code != 404 {
		t.Errorf("another user's document: %d", rec.Code)
	}
	if rec, _ := f.startOn(t, "", path, declare("a", data, "image/png")); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
	hdr := map[string]string{"Authorization": "Bearer " + f.ada, "Content-Type": "application/json", "If-Match": `"99"`}
	if rec, _ := f.doRaw(t, "POST", path, []byte(declare("a", data, "image/png")), hdr); rec.Code != 412 {
		t.Errorf("stale If-Match: %d", rec.Code)
	}
	// max_files is answered at the start, before anything is signed.
	for i := 0; i < 2; i++ {
		rec, out := f.startOn(t, f.ada, path, declare("a.png", pngBytes(i+1), "image/png"))
		if rec.Code != 201 {
			t.Fatal(rec.Code)
		}
		d := pngBytes(i + 1)
		putToLink(t, out, d)
		if rec, o := f.complete(t, f.ada, "videos", "clips", out["upload_id"].(string), out["upload_token"].(string)); rec.Code != 201 {
			t.Fatalf("complete %d: %d %v", i, rec.Code, o)
		}
	}
	rec, out = f.startOn(t, f.ada, path, declare("a.png", data, "image/png"))
	if rec.Code != 409 || out["error"].(map[string]any)["code"] != "too_many_files" {
		t.Errorf("past max_files: %d %v", rec.Code, out)
	}
}

func TestDirectCompletionVerifiesWhatWasUploaded(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	path := "/v1/acme/app/videos/" + id + "/_files/clips/uploads"
	start := func(declared []byte, typ string) (map[string]any, string, string) {
		t.Helper()
		rec, out := f.startOn(t, f.ada, path, declare("c", declared, typ))
		if rec.Code != 201 {
			t.Fatalf("start: %d %s", rec.Code, rec.Body)
		}
		return out, out["upload_id"].(string), out["upload_token"].(string)
	}
	rejected := func(name string, rec *httptest.ResponseRecorder, want int, fileID string) {
		t.Helper()
		if rec.Code != want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		for _, k := range f.s3.Keys() {
			if strings.HasSuffix(k, fileID) {
				t.Errorf("%s: the object is still stored", name)
			}
		}
		if e := f.journal(fileID); e.Status != auth.JournalFailed {
			t.Errorf("%s: journal %+v", name, e)
		}
	}
	declared := pngBytes(30)

	// Another size than declared.
	_, fid, tok := start(declared, "image/png")
	f.s3.Put("t/acme/app/videos/"+fid, pngBytes(31))
	rec, _ := f.complete(t, f.ada, "videos", "clips", fid, tok)
	rejected("a different size", rec, 422, fid)
	// The same size, other bytes: storage computed another checksum.
	other := pngBytes(30)
	other[len(other)-1] = 9
	_, fid, tok = start(declared, "image/png")
	f.s3.Put("t/acme/app/videos/"+fid, other)
	rec, out := f.complete(t, f.ada, "videos", "clips", fid, tok)
	rejected("a different checksum", rec, 422, fid)
	if out["error"].(map[string]any)["code"] != "upload_mismatch" {
		t.Errorf("code: %v", out)
	}
	// A rejected upload can't be completed again.
	if rec, _ := f.complete(t, f.ada, "videos", "clips", fid, tok); rec.Code != 400 {
		t.Errorf("after a rejection: %d", rec.Code)
	}
	// Bytes that aren't what the declared type says: the type comes from the content.
	text := []byte(strings.Repeat("plain words ", 10))
	rec, o := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/clip/uploads", declare("c", text, "image/png"))
	fid, tok = o["upload_id"].(string), o["upload_token"].(string)
	putToLink(t, o, text)
	rec, _ = f.complete(t, f.ada, "videos", "clip", fid, tok)
	rejected("a type that isn't the declared one", rec, 415, fid)

	// The storage itself refuses a PUT whose checksum isn't the signed one.
	o, _, _ = start(declared, "image/png")
	if resp := putToLink(t, o, other); resp.StatusCode == 200 {
		t.Error("storage accepted bytes that differ from the signed checksum")
	}
	// The document was never touched.
	_, doc := f.as(t, f.ada, "GET", "/v1/acme/app/videos/"+id, "")
	if doc["clips"] != nil || doc["_meta"].(map[string]any)["version"] != float64(1) {
		t.Errorf("document: %v", doc)
	}
}

func TestDirectCompletionNeedsTheToken(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	data := pngBytes(15)
	rec, out := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/clips/uploads", declare("c", data, "image/png"))
	fid, tok := out["upload_id"].(string), out["upload_token"].(string)
	if rec.Code != 201 {
		t.Fatal(rec.Code)
	}
	putToLink(t, out, data)
	reasons := map[string]string{}
	refuse := func(name string, rec *httptest.ResponseRecorder, out map[string]any) {
		t.Helper()
		if rec.Code != 400 {
			t.Errorf("%s: %d %v", name, rec.Code, out)
			return
		}
		if d, _ := out["error"].(map[string]any)["details"].([]any); len(d) == 1 {
			reasons[name] = d[0].(map[string]any)["reason"].(string)
		}
	}
	r1, o1 := f.complete(t, f.ada, "videos", "clips", fid, "fut_wrong")
	refuse("wrong token", r1, o1)
	r2, o2 := f.complete(t, f.ada, "videos", "clips", fid, "")
	refuse("empty token", r2, o2)
	r3, o3 := f.complete(t, f.ada, "videos", "clips", "fl_nothing", tok)
	refuse("unknown upload", r3, o3)
	r4, o4 := f.complete(t, f.bob, "videos", "clips", fid, tok)
	refuse("another user", r4, o4)
	r5, o5 := f.complete(t, f.ada, "videos", "clip", fid, tok)
	refuse("another field", r5, o5)
	for _, n := range []string{"wrong token", "unknown upload", "another user", "another field"} {
		if reasons[n] != uploadInvalid {
			t.Errorf("%s: %q", n, reasons[n])
		}
	}
	if e := f.journal(fid); e.Status != auth.JournalWriting {
		t.Fatalf("a refused completion claimed the upload: %+v", e)
	}
	// It expires.
	*f.clock = f.clock.Add(2 * time.Hour)
	if rec, _ := f.complete(t, f.ada, "videos", "clips", fid, tok); rec.Code != 400 {
		t.Errorf("expired: %d", rec.Code)
	}
	// And a worker removes what was put there.
	w := newTestWorker(t, f.rulesFixture)
	w.FilesDue(t.Context())
	for _, k := range f.s3.Keys() {
		if strings.HasSuffix(k, fid) {
			t.Errorf("the abandoned upload's object remains: %s", k)
		}
	}
}

func TestDirectUploadBeforeTheDocumentExists(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	data := pngBytes(40)
	// Anonymous callers are admitted by the create rule (true).
	rec, out := f.startOn(t, "", "/v1/acme/app/videos/_files/clip/uploads", declare("a.png", data, "image/png"))
	if rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	fid, tok := out["upload_id"].(string), out["upload_token"].(string)
	e := f.journal(fid)
	if !e.Pending || !e.Direct || e.DocumentID != "" {
		t.Errorf("journal: %+v", e)
	}
	// Not usable until it is complete.
	if code, _ := f.as(t, f.ada, "POST", "/v1/acme/app/videos", `{"title": "t", "clip": `+ref(fid, tok)+`}`); code != 400 {
		t.Errorf("an upload still waiting for its bytes: %d", code)
	}
	putToLink(t, out, data)
	rec, done := f.complete(t, "", "videos", "clip", fid, tok)
	file, _ := done["file"].(map[string]any)
	if rec.Code != 200 || done["upload_id"] != fid || file["sha256"] != sha(data) || file["type"] != "image/png" {
		t.Fatalf("complete: %d %v", rec.Code, done)
	}
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/videos", `{"title": "t", "clip": `+ref(fid, tok)+`}`)
	if code != 201 || doc["clip"].(map[string]any)["id"] != fid {
		t.Fatalf("create: %d %v", code, doc)
	}
	if e := f.journal(fid); e.Status != auth.JournalAttached {
		t.Errorf("journal after: %+v", e)
	}
	// Direct uploads count among a caller's open uploads.
	for i := 0; i < auth.MaxOpenPendingUploads; i++ {
		if rec, _ := f.startOn(t, f.bob, "/v1/acme/app/videos/_files/clip/uploads", declare("a", pngBytes(i), "image/png")); rec.Code != 201 {
			t.Fatalf("start %d: %d", i, rec.Code)
		}
	}
	if rec, _ := f.startOn(t, f.bob, "/v1/acme/app/videos/_files/clip/uploads", declare("a", data, "image/png")); rec.Code != 429 {
		t.Errorf("past the open limit: %d", rec.Code)
	}
}

func TestFileUploadBodiesAreNotCapped(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/r/d/c/_files/f/uploads":               true,
		"/v1/r/d/c/doc1/_files/f/uploads":          true,
		"/v1/r/d/c/doc1/_files/f":                  true,
		"/v1/r/d/c/_files/f/uploads/fl_1/complete": false,
		"/v1/r/d/c/doc1/_files/f/fl_1":             false,
		"/v1/r/d/c":                                false,
	} {
		r := httptest.NewRequest("POST", path, io.NopCloser(strings.NewReader("")))
		if isFileUpload(r) != want {
			t.Errorf("%s: %v, want %v", path, !want, want)
		}
	}
}
