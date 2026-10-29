package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

const photoJSON = `{"id":"fl_1","name":"a.png","size":3,"type":"image/png","sha256":"x","uploaded_at":"t","versions":{"thumb":{"status":"ready","url":"https://cdn/t.png"},"big":{"status":"pending"}}}`

// filesServer is a fake backd with one collection main/posts and a file field "photo", and a
// fake bucket at /bucket.
type filesServer struct {
	t        *testing.T
	mu       sync.Mutex
	upload   string // "proxy" or "direct"
	multi    int64  // multipart_above
	docField string // what the document holds in "photo"
	seen     []string
	bodies   map[string]string
	declared map[string]any
	partTry  map[string]int
	failPart string // a part path that fails once with 500
	srvURL   string
}

func (s *filesServer) log(r *http.Request, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	if s.bodies == nil {
		s.bodies = map[string]string{}
	}
	s.bodies[r.Method+" "+r.URL.Path] = body
}

func (s *filesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.log(r, string(b))
	p := strings.TrimPrefix(r.URL.Path, "/v1/r")
	switch {
	case r.Method == "GET" && p == "/main/posts/_files/photo":
		fmt.Fprintf(w, `{"name":"photo","upload":%q,"download":"proxy","multiple":false,"max_files":1,"max_size":1000,"types":[],"multipart_above":%d}`, s.upload, s.multi)
	case r.Method == "GET" && p == "/main/posts/p1":
		fmt.Fprintf(w, `{"id":"p1","photo":%s}`, s.docField)
	case r.Method == "POST" && p == "/main/posts/p1/_files/photo":
		if r.Header.Get("Content-Type") != "image/png" || r.URL.Query().Get("name") != "a.png" {
			s.t.Errorf("put: %s %s", r.Header.Get("Content-Type"), r.URL)
		}
		fmt.Fprintf(w, `{"id":"p1","photo":%s}`, photoJSON)
	case r.Method == "POST" && p == "/main/posts/_files/photo/uploads":
		if s.upload == "direct" {
			s.start(w, b)
			return
		}
		fmt.Fprintf(w, `{"upload_id":"u1","upload_token":"tok","expires_at":"x","file":{"name":"a.png","size":%d,"type":"image/png","sha256":"%s"}}`, len(b), sum(string(b)))
	case r.Method == "POST" && p == "/main/posts/p1/_files/photo/uploads":
		s.start(w, b)
	case r.Method == "POST" && strings.HasSuffix(p, "/uploads/u1/complete"):
		if !strings.Contains(string(b), `"upload_token":"tok"`) {
			s.t.Errorf("complete body %s", b)
		}
		fmt.Fprintf(w, `{"id":"p1","photo":%s,"file":{"name":"a.png","size":5,"type":"image/png","sha256":"s"}}`, photoJSON)
	case strings.HasPrefix(p, "/main/posts/p1/_files/photo/fl_1/versions/"):
		fmt.Fprint(w, `{"status":"pending"}`)
	case r.Method == "GET" && p == "/main/posts/p1/_files/photo/fl_1":
		if r.URL.Query().Get("link") == "json" {
			if r.URL.Query().Get("version") == "nope" {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"code":"version_unavailable","message":"not ready"}}`)
				return
			}
			fmt.Fprint(w, `{"url":"https://signed/x","expires_at":"2030-01-01T00:00:00Z"}`)
			return
		}
		io.WriteString(w, "PNGDATA:"+r.URL.Query().Get("version"))
	case r.Method == "DELETE" && strings.HasPrefix(p, "/main/posts/p1/_files/photo"):
		fmt.Fprint(w, `{"id":"p1"}`)
	case strings.HasPrefix(r.URL.Path, "/bucket/") && r.Method == "PUT":
		s.bucket(w, r, b)
	default:
		http.Error(w, `{"error":{"code":"not_found","message":"no route `+r.Method+" "+p+`"}}`, 404)
	}
}

func (s *filesServer) start(w http.ResponseWriter, b []byte) {
	var d map[string]any
	json.Unmarshal(b, &d)
	s.mu.Lock()
	s.declared = d
	s.mu.Unlock()
	if _, multi := d["part_sha256"]; multi {
		fmt.Fprintf(w, `{"upload_id":"u1","upload_token":"tok","multipart":true,"part_size":4,"parts":[
			{"url":"%[1]s/bucket/p1","headers":{"X-Part":"1"},"size":4},{"url":"%[1]s/bucket/p2","headers":{"X-Part":"2"},"size":4},{"url":"%[1]s/bucket/p3","headers":{"X-Part":"3"},"size":2}]}`, s.srvURL)
		return
	}
	fmt.Fprintf(w, `{"upload_id":"u1","upload_token":"tok","url":"%s/bucket/one","headers":{"X-Amz":"sig"}}`, s.srvURL)
}

func (s *filesServer) bucket(w http.ResponseWriter, r *http.Request, b []byte) {
	if r.Header.Get("Authorization") != "" {
		s.t.Errorf("the storage got backd's credentials: %q", r.Header.Get("Authorization"))
	}
	s.mu.Lock()
	if s.partTry == nil {
		s.partTry = map[string]int{}
	}
	s.partTry[r.URL.Path]++
	n := s.partTry[r.URL.Path]
	fail := s.failPart == r.URL.Path && n == 1
	s.mu.Unlock()
	if fail {
		w.WriteHeader(500)
		return
	}
	w.WriteHeader(200)
}

func newFilesClient(t *testing.T, s *filesServer) *Client {
	s.t = t
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.srvURL = srv.URL
	c, _ := New(Options{URL: srv.URL, Realm: "r", APIKey: "bdk_x"})
	t.Cleanup(func() { c.Close() })
	return c
}

func TestFilesListGetLinkDelete(t *testing.T) {
	s := &filesServer{docField: photoJSON}
	c := newFilesClient(t, s)
	files := c.DB("main").Collection("posts").Files("p1", "photo")

	list, err := files.List(bg)
	if err != nil || len(list) != 1 || list[0].ID != "fl_1" || list[0].Versions["thumb"].URL != "https://cdn/t.png" {
		t.Fatalf("%v %+v", err, list)
	}
	content, err := files.Get(bg, "", GetFileOptions{Version: "thumb"}) // the only file of the field
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := content.Text(); text != "PNGDATA:thumb" || content.File == nil || content.File.ID != "fl_1" {
		t.Errorf("%q %+v", text, content.File)
	}
	l, err := files.Link(bg, "fl_1", GetFileOptions{})
	if err != nil || l.URL != "https://signed/x" || *l.ExpiresAt != "2030-01-01T00:00:00Z" {
		t.Errorf("%v %+v", err, l)
	}
	if doc, err := files.Delete(bg, "fl_1", 3); err != nil || doc.ID() != "p1" {
		t.Errorf("%v %v", err, doc)
	}
	if doc, err := files.Delete(bg, "", nil); err != nil || doc.ID() != "p1" {
		t.Errorf("clearing the field: %v %v", err, doc)
	}
	last := s.seen[len(s.seen)-1]
	if last != "DELETE /v1/r/main/posts/p1/_files/photo?" {
		t.Errorf("clear = %s", last)
	}
}

func TestWhichFileErrors(t *testing.T) {
	s := &filesServer{docField: `null`}
	c := newFilesClient(t, s)
	files := c.DB("main").Collection("posts").Files("p1", "photo")
	if _, err := files.Link(bg, "", GetFileOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty field: %v", err)
	}
	s.docField = `[` + photoJSON + `,` + photoJSON + `]`
	if _, err := files.Get(bg, "", GetFileOptions{}); err == nil || !strings.Contains(err.Error(), "several files") {
		t.Errorf("several files: %v", err)
	}
	l, err := files.List(bg)
	if err != nil || len(l) != 2 {
		t.Errorf("%v %v", err, l)
	}
}

func TestVersions(t *testing.T) {
	s := &filesServer{docField: photoJSON}
	c := newFilesClient(t, s)
	v := c.DB("main").Collection("posts").Files("p1", "photo").Version("thumb", "fl_1")
	if st, err := v.Regenerate(bg); err != nil || st.Status != "pending" {
		t.Errorf("%v %+v", err, st)
	}
	if _, err := v.Generate(bg, VersionParams{MaxWidth: 100, Fit: "cover"}); err != nil {
		t.Error(err)
	}
	if body := s.bodies["POST /v1/r/main/posts/p1/_files/photo/fl_1/versions/thumb"]; body != `{"max_width":100,"fit":"cover"}` {
		t.Errorf("generate sent %s", body)
	}
	if _, err := v.Delete(bg); err != nil {
		t.Error(err)
	}
}

func TestProxyUploadAndProgress(t *testing.T) {
	s := &filesServer{upload: "proxy"}
	c := newFilesClient(t, s)
	posts := c.DB("main").Collection("posts")
	var last UploadProgress
	doc, err := posts.UploadFile(bg, "p1", "photo", FileFromBytes([]byte("abc"), "a.png", "image/png"), UploadOptions{OnProgress: func(p UploadProgress) { last = p }})
	if err != nil || doc.ID() != "p1" || s.bodies["POST /v1/r/main/posts/p1/_files/photo"] != "abc" || last != (UploadProgress{3, 3}) {
		t.Errorf("%v %v %q %+v", err, doc, s.bodies["POST /v1/r/main/posts/p1/_files/photo"], last)
	}
	prep, err := posts.PrepareUpload(bg, "photo", FileFromBytes([]byte("abc"), "a.png", "image/png"), UploadOptions{})
	if err != nil || prep.ID != "u1" || prep.Ref["token"] != "tok" || prep.File.Size != 3 || prep.File.SHA256 != sum("abc") {
		t.Errorf("%v %+v", err, prep)
	}
}

func TestDirectUpload(t *testing.T) {
	s := &filesServer{upload: "direct"}
	c := newFilesClient(t, s)
	posts := c.DB("main").Collection("posts")
	var last UploadProgress
	doc, err := posts.UploadFile(bg, "p1", "photo", FileFromBytes([]byte("hello"), "a.png", "image/png"),
		UploadOptions{IfMatch: 4, OnProgress: func(p UploadProgress) { last = p }})
	if err != nil || doc.ID() != "p1" {
		t.Fatalf("%v %v", err, doc)
	}
	if s.declared["sha256"] != sum("hello") || s.declared["size"] != float64(5) || s.declared["name"] != "a.png" || s.declared["type"] != "image/png" {
		t.Errorf("declared %v", s.declared)
	}
	if s.bodies["PUT /bucket/one"] != "hello" || last != (UploadProgress{5, 5}) {
		t.Errorf("bucket got %q, progress %+v", s.bodies["PUT /bucket/one"], last)
	}
	order := strings.Join(s.seen[1:], "|")
	if !strings.HasPrefix(order, "POST /v1/r/main/posts/p1/_files/photo/uploads?|PUT /bucket/one?|POST /v1/r/main/posts/_files/photo/uploads/u1/complete?") {
		t.Errorf("order = %s", order)
	}
	prep, err := posts.PrepareUpload(bg, "photo", FileFromBytes([]byte("hello"), "a.png", "image/png"), UploadOptions{IfMatch: 4})
	if err != nil || prep.ID != "u1" || prep.File.Name != "a.png" {
		t.Errorf("%v %+v", err, prep)
	}
}

func TestMultipartUploadRetriesAPart(t *testing.T) {
	s := &filesServer{upload: "direct", multi: 3, failPart: "/bucket/p2"}
	c := newFilesClient(t, s)
	var done int64
	var mu sync.Mutex
	doc, err := c.DB("main").Collection("posts").UploadFile(bg, "p1", "photo", FileFromBytes([]byte("0123456789"), "a.png", "image/png"),
		UploadOptions{OnProgress: func(p UploadProgress) { mu.Lock(); done = max(done, p.Loaded); mu.Unlock() }})
	if err != nil || doc.ID() != "p1" {
		t.Fatalf("%v %v", err, doc)
	}
	digests, _ := s.declared["part_sha256"].([]any)
	if len(digests) != 1 || digests[0] != sum("0123456789") || s.declared["sha256"] != nil {
		t.Errorf("declared %v", s.declared)
	}
	if s.partTry["/bucket/p2"] != 2 || s.partTry["/bucket/p1"] != 1 || s.partTry["/bucket/p3"] != 1 {
		t.Errorf("tries %v", s.partTry)
	}
	if s.bodies["PUT /bucket/p1"] != "0123" || s.bodies["PUT /bucket/p2"] != "4567" || s.bodies["PUT /bucket/p3"] != "89" || done != 10 {
		t.Errorf("parts %q %q %q, progress %d", s.bodies["PUT /bucket/p1"], s.bodies["PUT /bucket/p2"], s.bodies["PUT /bucket/p3"], done)
	}
}

func TestStorageRefusalIsAnError(t *testing.T) {
	s := &filesServer{upload: "direct"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bucket/") {
			w.WriteHeader(403)
			return
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s.t, s.srvURL = t, srv.URL
	c, _ := New(Options{URL: srv.URL, Realm: "r", APIKey: "k"})
	_, err := c.DB("main").Collection("posts").UploadFile(bg, "p1", "photo", FileFromBytes([]byte("x"), "a", ""), UploadOptions{})
	var e *Error
	if !errors.As(err, &e) || e.Code != "upload_failed" || e.Status != 403 {
		t.Errorf("%#v", err)
	}
}

func TestUploadCanBeCanceled(t *testing.T) {
	s := &filesServer{upload: "direct"}
	c := newFilesClient(t, s)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err := c.DB("main").Collection("posts").UploadFile(ctx, "p1", "photo", FileFromBytes(make([]byte, 10), "a", ""), UploadOptions{})
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("%v", err)
	}
}

func TestFileFieldIsAskedOnce(t *testing.T) {
	s := &filesServer{upload: "proxy"}
	c := newFilesClient(t, s)
	posts := c.DB("main").Collection("posts")
	for i := 0; i < 3; i++ {
		if ff, err := posts.FileField(bg, "photo"); err != nil || ff.MaxSize != 1000 || ff.Upload != "proxy" {
			t.Fatalf("%v %+v", err, ff)
		}
	}
	n := 0
	for _, l := range s.seen {
		if strings.HasPrefix(l, "GET /v1/r/main/posts/_files/photo") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("asked %d times", n)
	}
	if _, err := posts.FileField(bg, "nope"); err == nil {
		t.Error("an unknown field")
	}
}

func TestFileURLDownloadDelete(t *testing.T) {
	s := &filesServer{docField: photoJSON}
	c := newFilesClient(t, s)
	posts := c.DB("main").Collection("posts")

	pub := &FileDetails{Public: true, URL: "https://cdn/p.png", Versions: map[string]VersionState{"thumb": {Status: "ready", URL: "https://cdn/t.png"}, "big": {Status: "pending"}}}
	for _, c := range []struct{ version, want string }{{"", "https://cdn/p.png"}, {"thumb", "https://cdn/t.png"}, {"big", ""}, {"nope", ""}} {
		l, err := posts.FileURL(bg, "p1", "photo", "fl_1", FileURLOptions{File: pub, Version: c.version})
		if err != nil || (c.want == "") != (l == nil) || (l != nil && (l.URL != c.want || l.ExpiresAt != nil)) {
			t.Errorf("public %q: %v %+v", c.version, err, l)
		}
	}
	if l, err := posts.FileURL(bg, "p1", "photo", "fl_1", FileURLOptions{}); err != nil || l.URL != "https://signed/x" {
		t.Errorf("%v %+v", err, l)
	}
	if l, err := posts.FileURL(bg, "p1", "photo", "fl_1", FileURLOptions{Version: "nope"}); err != nil || l != nil {
		t.Errorf("an unavailable version: %v %+v", err, l)
	}
	var buf strings.Builder
	if n, err := posts.DownloadFile(bg, "p1", "photo", "fl_1", &buf, GetFileOptions{}); err != nil || n != 8 || buf.String() != "PNGDATA:" {
		t.Errorf("%v %d %q", err, n, buf.String())
	}
	if _, err := posts.DeleteFile(bg, "p1", "photo", ""); err == nil {
		t.Error("DeleteFile needs a file id")
	}
	if _, err := posts.DeleteFile(bg, "p1", "photo", "fl_1"); err != nil {
		t.Error(err)
	}
}

func TestVersionStatusAndPartSize(t *testing.T) {
	f := &FileDetails{Versions: map[string]VersionState{"a": {Status: "ready", Width: 10, Height: 5}, "b": {Status: "failed", Reason: "bad"}}}
	if v := VersionStatus(f, "a"); !v.Ready || v.Width != 10 {
		t.Errorf("%+v", v)
	}
	if v := VersionStatus(f, "b"); v.Ready || v.Reason != "bad" || v.Status != "failed" {
		t.Errorf("%+v", v)
	}
	if v := VersionStatus(f, "c"); v.Status != "undeclared" || v.Ready || VersionStatus(nil, "a").Status != "undeclared" {
		t.Errorf("%+v", v)
	}
	if PublicURL(&FileDetails{URL: "u"}, "") != "" {
		t.Error("a private file has no public URL")
	}
	for size, want := range map[int64]int64{1: 64 * mib, 64 * 1000 * mib: 64 * mib, 100 * 1000 * mib: 112 * mib, 5 << 40: 5248 * mib} {
		if got := partSizeFor(size); got != want {
			t.Errorf("partSizeFor(%d) = %d MiB; want %d", size, got/mib, want/mib)
		}
	}
}

func TestOpenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pic.png")
	os.WriteFile(path, []byte("12345"), 0o600)
	src, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if name, typ := src.describe("", ""); src.Size != 5 || name != "pic.png" || typ != "image/png" {
		t.Errorf("%d %s %s", src.Size, name, typ)
	}
	if name, typ := FileFromBytes(nil, "", "").describe("", ""); name != "file" || typ != "application/octet-stream" {
		t.Errorf("defaults %s %s", name, typ)
	}
	if name, typ := src.describe("x.bin", "text/plain"); name != "x.bin" || typ != "text/plain" {
		t.Errorf("overrides %s %s", name, typ)
	}
	if _, err := OpenFile(path + ".missing"); err == nil {
		t.Error("a missing file")
	}
}
