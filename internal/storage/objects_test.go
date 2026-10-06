package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// The tests that talk to a real object storage need one: MinIO, in the test
// stack and CI, addressed by BACKD_TEST_MINIO_ENDPOINT (and _ACCESS_KEY, _SECRET_KEY).
// They skip without it. The few that need a hosted provider (R2) are in r2_test.go.

func minioConfig(t *testing.T) ObjectsConfig {
	t.Helper()
	endpoint := os.Getenv("BACKD_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("BACKD_TEST_MINIO_ENDPOINT is not set: no MinIO to test against")
	}
	return ObjectsConfig{
		Provider: "minio", Endpoint: endpoint, Region: "us-east-1", Bucket: "backd-test", Prefix: "t-" + hex.EncodeToString([]byte(t.Name()))[:8],
		AccessKey: os.Getenv("BACKD_TEST_MINIO_ACCESS_KEY"), SecretKey: os.Getenv("BACKD_TEST_MINIO_SECRET_KEY"),
	}
}

// minioObjects connects to the test MinIO and makes sure the bucket exists.
func minioObjects(t *testing.T) *Objects {
	t.Helper()
	o, err := NewObjects(minioConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	// The storage may have only just started: try for a while.
	var err2 error
	for i := 0; i < 60; i++ {
		_, err2 = o.client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(o.cfg.Bucket)})
		if err2 == nil || strings.Contains(err2.Error(), "BucketAlready") {
			return o
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("create the test bucket: %v", err2)
	return o
}

func TestObjectsOnMinIO(t *testing.T) {
	o := minioObjects(t)
	ctx := context.Background()
	key := o.Key("acme", "app", "notes", "fl_1")
	if key != o.cfg.Prefix+"/acme/app/notes/fl_1" {
		t.Errorf("key = %s", key)
	}
	body := []byte("hello, storage")
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])
	t.Cleanup(func() { _ = o.Delete(context.Background(), key) })

	if err := o.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain", sumHex); err != nil {
		t.Fatal(err)
	}
	info, err := o.Head(ctx, key)
	if err != nil || info.Size != int64(len(body)) || info.ContentType != "text/plain" || info.SHA256 != sumHex {
		t.Fatalf("head: %+v %v", info, err)
	}
	rc, got, err := o.Get(ctx, key, "bytes=7-13")
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(rc)
	rc.Close()
	if string(part) != "storage" || got.Size != 7 {
		t.Errorf("range: %q %+v", part, got)
	}
	items, err := o.List(ctx, o.cfg.Prefix+"/acme/", 10)
	if err != nil || len(items) != 1 || items[0].Key != key {
		t.Errorf("list: %+v %v", items, err)
	}
	// The storage verifies a signed SHA-256: another content is rejected.
	wrong := sha256.Sum256([]byte("other"))
	if err := o.Put(ctx, key+"-bad", bytes.NewReader(body), int64(len(body)), "text/plain", hex.EncodeToString(wrong[:])); err == nil {
		t.Error("a wrong SHA-256 was accepted")
	}
	if err := o.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Head(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if _, _, err := o.Get(ctx, key, ""); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := o.Delete(ctx, key); err != nil {
		t.Errorf("deleting a missing object: %v", err)
	}
}

func TestCheckOnMinIO(t *testing.T) {
	o := minioObjects(t)
	rep := o.Check(context.Background(), "acme")
	if !rep.OK() {
		t.Fatalf("check failed: %+v", rep.Steps)
	}
	levels := map[string]string{}
	for _, s := range rep.Steps {
		levels[s.Name] = s.Level
	}
	for _, name := range []string{"bucket", "write", "head", "read", "range", "list", "checksum", "download link", "upload link"} {
		if levels[name] != CheckOK {
			t.Errorf("step %s: %s (%+v)", name, levels[name], rep.Steps)
		}
	}
	if rep.ChecksumSHA256 != "verified" || rep.LinkHost == "" || levels["cors"] != CheckSkip || levels["encryption"] != CheckSkip {
		t.Errorf("report: %+v", rep)
	}
	// It cleaned up after itself.
	if items, _ := o.List(context.Background(), o.cfg.Prefix+"/", 10); len(items) != 0 {
		t.Errorf("left behind: %+v", items)
	}
}

func TestCheckSaysWhatIsWrong(t *testing.T) {
	cfg := minioConfig(t)
	cfg.SecretKey = "not-the-secret"
	o, _ := NewObjects(cfg)
	rep := o.Check(context.Background(), "acme")
	var told bool
	for _, s := range rep.Steps {
		if s.Level == CheckFail && strings.Contains(s.Detail, "access key") {
			told = true
		}
	}
	if rep.OK() || !told {
		t.Errorf("bad credentials: %+v", rep.Steps)
	}
	cfg = minioConfig(t)
	cfg.Bucket = "no-such-bucket-here"
	o, _ = NewObjects(cfg)
	if rep := o.Check(context.Background(), "acme"); rep.OK() || len(rep.Steps) != 1 || !strings.Contains(rep.Steps[0].Detail, "doesn't exist") {
		t.Errorf("missing bucket: %+v", rep.Steps)
	}
}

// Signed links for the public endpoint are made without connecting to it, and
// backd never connects to it.
func TestPublicEndpointIsOnlySigned(t *testing.T) {
	cfg := minioConfig(t)
	cfg.PublicEndpoint = "http://localhost:9"
	o, err := NewObjects(cfg)
	if err != nil {
		t.Fatal(err)
	}
	get, err := o.PresignGet(context.Background(), "k/obj", time.Minute, "my file.txt", "text/plain")
	if err != nil || !strings.HasPrefix(get.URL, "http://localhost:9/") || !strings.Contains(get.URL, "X-Amz-Signature=") || !strings.Contains(get.URL, "response-content-disposition=attachment") {
		t.Fatalf("get link: %+v %v", get, err)
	}
	put, err := o.PresignPut(context.Background(), "k/obj", time.Minute, 5, "text/plain", strings.Repeat("ab", 32))
	if err != nil || !strings.HasPrefix(put.URL, "http://localhost:9/") || put.Headers["X-Amz-Checksum-Sha256"] == "" {
		t.Fatalf("put link: %+v %v", put, err)
	}
	// A check does not use the links when they are for another address.
	rep := o.Check(context.Background(), "acme")
	var skipped bool
	for _, s := range rep.Steps {
		if s.Name == "download link" && s.Level == CheckSkip && strings.Contains(s.Detail, "localhost:9") {
			skipped = true
		}
	}
	if !rep.OK() || !skipped {
		t.Errorf("check with a public endpoint: %+v", rep.Steps)
	}
}

// The storage client dials only the declared endpoint (and the bucket under it
// for virtual-hosted addressing), follows no redirect and checks what a name resolves to.
func TestTransportDialsOnlyTheDeclaredEndpoint(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	declared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound) // an endpoint trying to send us elsewhere
	}))
	defer declared.Close()

	hc, err := NewHTTPClient(declared.URL, "bucket", PathStyle)
	if err != nil {
		t.Fatal(err)
	}
	// The declared endpoint is reachable, and its redirect is not followed.
	res, err := hc.Get(declared.URL)
	if err != nil || res.StatusCode != http.StatusFound {
		t.Fatalf("declared: %v %v", res, err)
	}
	res.Body.Close()
	if hits != 0 {
		t.Error("a redirect was followed to another host")
	}
	// Anything else is refused at the dial: another port, another host, a metadata address.
	for _, target := range []string{other.URL, "http://127.0.0.1:1/", "http://169.254.169.254/latest/meta-data/", "http://example.com/"} {
		if _, err := hc.Get(target); err == nil || !errors.Is(err, errNotDeclared) {
			t.Errorf("%s: %v, want a refusal", target, err)
		}
	}
	if hits != 0 {
		t.Error("the other server was reached")
	}
}

func TestVirtualHostedBucketIsAllowedUnderTheEndpoint(t *testing.T) {
	a, err := newAllowedHosts("https://abc.r2.cloudflarestorage.com", "tests-backd", VirtualHosted)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"abc.r2.cloudflarestorage.com":             true,
		"tests-backd.abc.r2.cloudflarestorage.com": true,
		"other.abc.r2.cloudflarestorage.com":       false,
		"evil.example.com":                         false,
	} {
		if a.hosts[host] != want {
			t.Errorf("%s allowed = %v, want %v", host, a.hosts[host], want)
		}
	}
	if a.port != "443" {
		t.Errorf("port %s", a.port)
	}
	p, err := newAllowedHosts("http://minio:9000", "b", PathStyle)
	if err != nil || p.port != "9000" || len(p.hosts) != 1 {
		t.Errorf("path style: %+v %v", p, err)
	}
	if blockedAddress(nil) || !blockedAddress([]byte{169, 254, 169, 254}) {
		t.Error("blockedAddress")
	}
}

// A stream of unknown length is stored in one PUT when small and in parts when
// large, and what is read back is what was sent.
func TestPutStreamOnMinIO(t *testing.T) {
	o := minioObjects(t)
	ctx := context.Background()
	for name, size := range map[string]int{"small": 100, "multipart": UploadPartSize*2 + 12345} {
		key := o.Key("acme", "app", "notes", "fl_"+name)
		t.Cleanup(func() { _ = o.Delete(context.Background(), key) })
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i * 7)
		}
		// A reader that doesn't know its length, as a request body doesn't.
		if err := o.PutStream(ctx, key, io.MultiReader(bytes.NewReader(data)), "application/octet-stream"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		info, err := o.Head(ctx, key)
		if err != nil || info.Size != int64(size) || info.ContentType != "application/octet-stream" {
			t.Fatalf("%s head: %+v %v", name, info, err)
		}
		rc, _, err := o.Get(ctx, key, "")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, data) {
			t.Errorf("%s: what was read back differs", name)
		}
	}
	// A stream that fails leaves nothing behind.
	key := o.Key("acme", "app", "notes", "fl_broken")
	broken := io.MultiReader(bytes.NewReader(make([]byte, UploadPartSize+5)), failingReader{})
	if err := o.PutStream(ctx, key, broken, ""); err == nil {
		t.Error("a failing stream succeeded")
	}
	if _, err := o.Head(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("a failed upload left an object: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the client went away") }

func TestCORSGapsForDirectUploads(t *testing.T) {
	for name, c := range map[string]struct {
		rules []CORSRule
		want  int
	}{
		"complete":         {[]CORSRule{{Origins: []string{"https://app"}, Methods: []string{"PUT", "GET"}, Headers: []string{"Content-Type", "x-amz-checksum-sha256"}}}, 0},
		"wildcard headers": {[]CORSRule{{Methods: []string{"PUT"}, Headers: []string{"*"}}}, 0},
		"reads only":       {[]CORSRule{{Methods: []string{"GET"}, Headers: []string{"*"}}}, 1},
		"no checksum":      {[]CORSRule{{Methods: []string{"PUT"}, Headers: []string{"content-type"}}}, 1},
		"no headers":       {[]CORSRule{{Methods: []string{"PUT"}}}, 2},
	} {
		if got := corsGaps(c.rules); len(got) != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A direct upload as a browser makes it: the signed link carries the length, the type
// and the checksum, so the storage refuses a body that differs from what was signed.
func TestPresignedPutIsVerifiedByMinIO(t *testing.T) {
	o := minioObjects(t)
	ctx := context.Background()
	key := o.Key("acme", "app", "notes", "fl_direct")
	t.Cleanup(func() { _ = o.Delete(context.Background(), key) })
	body := []byte("signed upload, exactly this")
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])
	l, err := o.PresignPut(ctx, key, time.Minute, int64(len(body)), "text/plain", sumHex)
	if err != nil {
		t.Fatal(err)
	}
	put := func(content []byte) int {
		req, _ := http.NewRequest(http.MethodPut, l.URL, bytes.NewReader(content))
		for k, v := range l.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	other := bytes.Clone(body)
	other[0] = 'S'
	if code := put(other); code < 400 {
		t.Errorf("other bytes of the same length: %d", code)
	}
	if code := put(body[:10]); code < 400 {
		t.Errorf("another length: %d", code)
	}
	if _, err := o.Head(ctx, key); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("a refused PUT stored something: %v", err)
	}
	if code := put(body); code != 200 {
		t.Fatalf("the signed bytes: %d", code)
	}
	info, err := o.Head(ctx, key)
	if err != nil || info.SHA256 != sumHex || info.Size != int64(len(body)) || info.ContentType != "text/plain" {
		t.Errorf("head: %+v %v", info, err)
	}
}
