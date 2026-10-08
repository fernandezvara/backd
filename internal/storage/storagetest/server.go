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
	Requests    map[string]int
	failDeletes bool
	uploads     map[string]*upload
	started     int
}

// upload is a multipart upload in progress.
type upload struct {
	key, contentType string
	parts            map[int]*part
}

type part struct {
	data []byte
	sum  []byte // raw SHA-256
}

// New starts a server with one bucket and stops it when the test ends.
func New(t testing.TB, bucket string) *Server {
	t.Helper()
	s := &Server{Bucket: bucket, objects: map[string]*object{}, uploads: map[string]*upload{}, Requests: map[string]int{}}
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

// Put stores bytes under key, as an object a test wants to find.
func (s *Server) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := sha256.Sum256(data)
	s.objects[key] = &object{data: data, sum: base64.StdEncoding.EncodeToString(sum[:]), modified: time.Now().UTC()}
}

// PutAt is Put with the time the object was last modified, to make one look old.
func (s *Server) PutAt(key string, data []byte, modified time.Time) {
	s.Put(key, data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key].modified = modified.UTC()
}

// Delete removes an object directly, as if it were lost.
func (s *Server) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
}

// OpenUploads is the number of multipart uploads started and neither completed nor aborted.
func (s *Server) OpenUploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

// FailDeletes makes DELETE requests fail (500) while on.
func (s *Server) FailDeletes(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDeletes = on
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
	q := r.URL.Query()
	switch {
	case key != "" && r.Method == http.MethodPost && q.Has("uploads"):
		s.createUpload(w, r, key)
	case key != "" && q.Has("uploadId"):
		s.multipart(w, r, key, q.Get("uploadId"))
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
		if s.failDeletes {
			apiError(w, http.StatusInternalServerError, "InternalError", "we encountered an internal error")
			return
		}
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

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request, key string) {
	s.started++
	id := fmt.Sprintf("upload-%d", s.started)
	s.uploads[id] = &upload{key: key, contentType: r.Header.Get("Content-Type"), parts: map[int]*part{}}
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, s.Bucket, key, id)
}

func partETag(sum []byte) string { return fmt.Sprintf(`"%x"`, sum[:8]) }

// multipart serves the requests on an upload: a part, ListParts, complete and abort.
func (s *Server) multipart(w http.ResponseWriter, r *http.Request, key, id string) {
	u := s.uploads[id]
	if u == nil || u.key != key {
		apiError(w, http.StatusNotFound, "NoSuchUpload", "The specified multipart upload does not exist.")
		return
	}
	switch r.Method {
	case http.MethodPut:
		n, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
		body, err := io.ReadAll(r.Body)
		if err != nil || n < 1 || n > 10000 {
			apiError(w, http.StatusBadRequest, "InvalidPart", "bad part")
			return
		}
		sum := sha256.Sum256(body)
		want := r.Header.Get("X-Amz-Checksum-Sha256")
		if want == "" {
			want = r.URL.Query().Get("x-amz-checksum-sha256")
		}
		if want != "" && want != base64.StdEncoding.EncodeToString(sum[:]) {
			apiError(w, http.StatusBadRequest, "BadDigest", "The SHA-256 you specified did not match the calculated checksum.")
			return
		}
		u.parts[n] = &part{data: body, sum: sum[:]}
		w.Header().Set("ETag", partETag(sum[:]))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		type xpart struct {
			PartNumber     int
			ETag           string
			Size           int
			ChecksumSHA256 string
		}
		var out struct {
			XMLName              xml.Name `xml:"ListPartsResult"`
			IsTruncated          bool
			NextPartNumberMarker int
			Parts                []xpart `xml:"Part"`
		}
		marker, _ := strconv.Atoi(r.URL.Query().Get("part-number-marker"))
		max, _ := strconv.Atoi(r.URL.Query().Get("max-parts"))
		if max <= 0 || max > 1000 {
			max = 1000
		}
		var nums []int
		for n := range u.parts {
			if n > marker {
				nums = append(nums, n)
			}
		}
		sort.Ints(nums)
		if len(nums) > max {
			nums, out.IsTruncated = nums[:max], true
		}
		for _, n := range nums {
			p := u.parts[n]
			out.Parts = append(out.Parts, xpart{n, partETag(p.sum), len(p.data), base64.StdEncoding.EncodeToString(p.sum)})
			out.NextPartNumberMarker = n
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(out)
	case http.MethodDelete:
		delete(s.uploads, id)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		var in struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Parts) == 0 {
			apiError(w, http.StatusBadRequest, "MalformedXML", "bad body")
			return
		}
		var data, sums []byte
		last := 0
		for _, ip := range in.Parts {
			p := u.parts[ip.PartNumber]
			switch {
			case p == nil || ip.ETag != partETag(p.sum):
				apiError(w, http.StatusBadRequest, "InvalidPart", "One or more of the specified parts could not be found.")
				return
			case ip.PartNumber <= last:
				apiError(w, http.StatusBadRequest, "InvalidPartOrder", "The list of parts was not in ascending order.")
				return
			}
			last = ip.PartNumber
			data = append(data, p.data...)
			sums = append(sums, p.sum...)
		}
		composite := sha256.Sum256(sums)
		s.objects[key] = &object{data: data, contentType: u.contentType, sum: fmt.Sprintf("%s-%d", base64.StdEncoding.EncodeToString(composite[:]), len(in.Parts)), modified: time.Now().UTC()}
		delete(s.uploads, id)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><ETag>"fake-%d"</ETag></CompleteMultipartUploadResult>`, s.Bucket, key, len(in.Parts))
	default:
		apiError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}
