package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

// smallParts makes a few kilobytes "above one PUT", so a multipart upload can be tested.
func smallParts(t *testing.T) {
	t.Helper()
	above, min, step, ttl := multipartAbove, multipartPartMin, multipartPartStep, multipartMinTTL
	multipartAbove, multipartPartMin, multipartPartStep, multipartMinTTL = 1000, 1024, 512, time.Hour
	t.Cleanup(func() { multipartAbove, multipartPartMin, multipartPartStep, multipartMinTTL = above, min, step, ttl })
}

func partDigests(data []byte) []string {
	size := int(partSizeFor(int64(len(data))))
	var out []string
	for i := 0; i < len(data); i += size {
		s := sha256.Sum256(data[i:min(i+size, len(data))])
		out = append(out, hex.EncodeToString(s[:]))
	}
	return out
}

func declareParts(name string, data []byte, typ string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "size": len(data), "type": typ, "part_sha256": partDigests(data)})
	return string(b)
}

func putPart(t *testing.T, part map[string]any, data []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, part["url"].(string), bytes.NewReader(data))
	for k, v := range part["headers"].(map[string]any) {
		req.Header.Set(k, v.(string))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestMultipartDirectUpload(t *testing.T) {
	smallParts(t)
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 190) // 3040 bytes: parts of 1024, 1024 and 992
	path := "/v1/acme/app/videos/" + id + "/_files/clips/uploads"

	rec, out := f.startOn(t, f.ada, path, declareParts("big.bin", data, "application/octet-stream"))
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	parts, _ := out["parts"].([]any)
	if out["multipart"] != true || out["part_size"] != float64(1024) || len(parts) != 3 || out["url"] != nil || out["url_expires_at"] == nil {
		t.Fatalf("start reply: %v", out)
	}
	fileID, token := out["upload_id"].(string), out["upload_token"].(string)
	composite, wantHex, _ := storage.CompositeSHA256(partDigests(data))
	_ = composite
	if out["sha256"] != wantHex {
		t.Errorf("sha256 %v, want %s", out["sha256"], wantHex)
	}
	for i, p := range parts {
		p := p.(map[string]any)
		want := float64(1024)
		if i == 2 {
			want = 992
		}
		if p["number"] != float64(i+1) || p["size"] != want || !strings.Contains(p["url"].(string), "partNumber="+string(rune('1'+i))) || p["headers"].(map[string]any) == nil {
			t.Errorf("part %d: %v", i+1, p)
		}
	}
	if e := f.journal(fileID); e.MultipartID == "" || e.PartSize != 1024 || len(e.PartSHA256) != 3 || e.SHA256 != wantHex || e.Status != auth.JournalWriting {
		t.Errorf("journal: %+v", e)
	}
	if f.s3.OpenUploads() != 1 {
		t.Errorf("open uploads: %d", f.s3.OpenUploads())
	}

	// Nothing uploaded, then part of it: completing is a 409 that can be retried.
	rec, out2 := f.complete(t, f.ada, "videos", "clips", fileID, token)
	if rec.Code != 409 || out2["error"].(map[string]any)["code"] != "file_not_uploaded" {
		t.Fatalf("complete before any part: %d %v", rec.Code, out2)
	}
	// A part that isn't what was declared is refused by storage.
	if resp := putPart(t, parts[0].(map[string]any), data[1:1025]); resp.StatusCode < 400 {
		t.Errorf("another part: %d", resp.StatusCode)
	}
	if resp := putPart(t, parts[0].(map[string]any), data[:1024]); resp.StatusCode != 200 {
		t.Fatalf("part 1: %d", resp.StatusCode)
	}
	rec, out2 = f.complete(t, f.ada, "videos", "clips", fileID, token)
	if rec.Code != 409 || !strings.Contains(out2["error"].(map[string]any)["message"].(string), "2 of 3 parts") {
		t.Fatalf("complete with one part: %d %v", rec.Code, out2)
	}
	// The parts may arrive in any order.
	for _, i := range []int{2, 1} {
		if resp := putPart(t, parts[i].(map[string]any), data[i*1024:min((i+1)*1024, len(data))]); resp.StatusCode != 200 {
			t.Fatalf("part %d: %d", i+1, resp.StatusCode)
		}
	}
	rec, doc := f.complete(t, f.ada, "videos", "clips", fileID, token)
	files, _ := doc["clips"].([]any)
	if rec.Code != 201 || len(files) != 1 {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body)
	}
	file := files[0].(map[string]any)
	if file["id"] != fileID || file["sha256"] != wantHex || file["size"] != float64(len(data)) || file["type"] != "text/plain" { // sniffed from the content
		t.Errorf("details: %v", file)
	}
	if !bytes.Equal(f.s3.Object("t/acme/app/videos/"+fileID), data) || f.s3.OpenUploads() != 0 {
		t.Errorf("object stored: %v, open uploads %d", f.s3.Keys(), f.s3.OpenUploads())
	}
	if e := f.journal(fileID); e.Status != auth.JournalAttached {
		t.Errorf("journal after: %+v", e)
	}
	if rec, _ := f.doRaw(t, "GET", rec.Header().Get("Location")+"?link=json", nil, map[string]string{"Authorization": "Bearer " + f.ada}); rec.Code != 200 {
		t.Errorf("download: %d", rec.Code)
	}
}

func TestMultipartStartRefusals(t *testing.T) {
	smallParts(t)
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	data := bytes.Repeat([]byte("x"), 3000)
	path := "/v1/acme/app/videos/" + id + "/_files/clips/uploads"
	digests := partDigests(data)
	body := func(m map[string]any) string {
		m["name"], m["size"], m["type"] = "a.bin", len(data), "application/octet-stream"
		b, _ := json.Marshal(m)
		return string(b)
	}
	small := bytes.Repeat([]byte("y"), 500)
	for name, c := range map[string]struct{ body, reason string }{
		"no part digests":        {body(map[string]any{}), "3 digests"},
		"too few digests":        {body(map[string]any{"part_sha256": digests[:2]}), "3 digests"},
		"too many digests":       {body(map[string]any{"part_sha256": append(digests, digests[0])}), "3 digests"},
		"a digest that isn't":    {body(map[string]any{"part_sha256": []string{digests[0], "abc", digests[2]}}), "64 lower-case hex"},
		"only the whole sha":     {body(map[string]any{"sha256": sha(data)}), "3 digests"},
		"parts for a small file": {`{"name": "s", "size": 500, "type": "application/octet-stream", "sha256": "` + sha(small) + `", "part_sha256": ["` + sha(small) + `"]}`, "sent in one piece"},
	} {
		rec, out := f.startOn(t, f.ada, path, c.body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.reason) {
			t.Errorf("%s: %d %v", name, rec.Code, out)
		}
	}
	if f.s3.OpenUploads() != 0 {
		t.Errorf("a refused start left %d uploads open", f.s3.OpenUploads())
	}
	// A file under the limit still goes in one piece.
	if rec, out := f.startOn(t, f.ada, path, declare("s.bin", small, "application/octet-stream")); rec.Code != 201 || out["multipart"] != nil || out["url"] == nil {
		t.Errorf("a small file: %d %v", rec.Code, out)
	}
}

// An upload that is never completed is aborted by the worker, parts and all.
func TestAbandonedMultipartUploadIsAborted(t *testing.T) {
	smallParts(t)
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	id := f.videoDoc(t)
	data := bytes.Repeat([]byte("z"), 2500)
	rec, out := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/clips/uploads", declareParts("a.bin", data, "application/octet-stream"))
	if rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if resp := putPart(t, out["parts"].([]any)[0].(map[string]any), data[:1024]); resp.StatusCode != 200 {
		t.Fatalf("part: %d", resp.StatusCode)
	}
	if f.s3.OpenUploads() != 1 {
		t.Fatalf("open uploads: %d", f.s3.OpenUploads())
	}
	*f.clock = f.clock.Add(48 * time.Hour)
	newTestWorker(t, f.rulesFixture).FilesDue(t.Context())
	if f.s3.OpenUploads() != 0 {
		t.Errorf("the abandoned upload is still open")
	}
	if e := f.journal(out["upload_id"].(string)); e.Status != auth.JournalFailed {
		t.Errorf("journal: %+v", e)
	}
}
