// Package storagetest is a small in-memory S3 server for tests that need an
// object storage but not a real one: path-style addressing, the operations backd
// uses (bucket HEAD, PUT, GET with Range, HEAD, DELETE, ListObjectsV2) and the
// signed SHA-256 check a PUT carries. It ignores signatures; the real thing is
// MinIO in the test stack.
package storagetest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type object struct {
	data        []byte
	contentType string
	sum         string // base64 SHA-256
	modified    time.Time
}

// Server is the fake storage. Its Bucket exists; any other bucket is missing.
type Server struct {
	*httptest.Server
	Bucket string

	mu      sync.Mutex
	objects map[string]*object
	// Requests counts the requests the server got, by method.
	Requests map[string]int
}

// New starts a server with one bucket and stops it when the test ends.
func New(t testing.TB, bucket string) *Server {
	t.Helper()
	s := &Server{Bucket: bucket, objects: map[string]*object{}, Requests: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Object returns what is stored under key, or nil.
func (s *Server) Object(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.objects[key]; o != nil {
		return o.data
	}
	return nil
}

// Keys lists the stored keys, sorted.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, message)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests[r.Method]++
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != s.Bucket {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		apiError(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	switch {
	case key == "" && r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case key == "" && r.Method == http.MethodGet && r.URL.Query().Has("cors"):
		apiError(w, http.StatusNotFound, "NoSuchCORSConfiguration", "no CORS")
	case key == "" && r.Method == http.MethodGet && r.URL.Query().Has("encryption"):
		apiError(w, http.StatusNotFound, "ServerSideEncryptionConfigurationNotFoundError", "none")
	case key == "" && r.Method == http.MethodGet:
		s.list(w, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodPut:
		s.put(w, r, key)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		s.get(w, r, key)
	case r.Method == http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		apiError(w, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	// Payloads may arrive aws-chunked; the tests send plain bodies, and the checksum is
	// the one the client declared.
	sum := sha256.Sum256(body)
	got := base64.StdEncoding.EncodeToString(sum[:])
	want := r.Header.Get("X-Amz-Checksum-Sha256")
	if want == "" {
		want = r.URL.Query().Get("x-amz-checksum-sha256")
	}
	if want != "" && want != got {
		apiError(w, http.StatusBadRequest, "BadDigest", "The SHA-256 you specified did not match the calculated checksum.")
		return
	}
	s.objects[key] = &object{data: body, contentType: r.Header.Get("Content-Type"), sum: got, modified: time.Now().UTC()}
	w.Header().Set("ETag", `"fake"`)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, key string) {
	o, ok := s.objects[key]
	if !ok {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		apiError(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		return
	}
	data := o.data
	status := http.StatusOK
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		lo, hi, _ := strings.Cut(strings.TrimPrefix(rng, "bytes="), "-")
		from, _ := strconv.Atoi(lo)
		to, err := strconv.Atoi(hi)
		if err != nil || to >= len(data) {
			to = len(data) - 1
		}
		if from <= to && from < len(data) {
			data, status = data[from:to+1], http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(o.data)))
		}
	}
	if o.contentType != "" {
		w.Header().Set("Content-Type", o.contentType)
	}
	if d := r.URL.Query().Get("response-content-disposition"); d != "" {
		w.Header().Set("Content-Disposition", d)
	}
	w.Header().Set("ETag", `"fake"`)
	w.Header().Set("Last-Modified", o.modified.Format(http.TimeFormat))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Header.Get("X-Amz-Checksum-Mode") == "ENABLED" || r.Method == http.MethodHead {
		w.Header().Set("X-Amz-Checksum-Sha256", o.sum)
	}
	w.WriteHeader(status)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (s *Server) list(w http.ResponseWriter, prefix string) {
	type content struct {
		Key          string
		Size         int
		ETag         string
		LastModified string
	}
	var out struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		Name        string
		Prefix      string
		KeyCount    int
		IsTruncated bool
		Contents    []content
	}
	out.Name, out.Prefix = s.Bucket, prefix
	for _, k := range s.sortedKeys() {
		if strings.HasPrefix(k, prefix) {
			o := s.objects[k]
			out.Contents = append(out.Contents, content{Key: k, Size: len(o.data), ETag: `"fake"`, LastModified: o.modified.Format(time.RFC3339)})
		}
	}
	out.KeyCount = len(out.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(out)
}

func (s *Server) sortedKeys() []string {
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
