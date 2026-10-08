package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// realPNG is a w×h picture.
func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func exifSegment(orientation uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	tiff = binary.BigEndian.AppendUint16(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3)
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, orientation)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xff, 0xe1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(seg)+2))
	return append(out, seg...)
}

func versionsOf(t *testing.T, file map[string]any) map[string]any {
	t.Helper()
	v, _ := file["versions"].(map[string]any)
	if v == nil {
		t.Fatalf("no versions in %v", file)
	}
	return v
}

func TestAnImageUploadRecordsItsSizeAndTheStateOfItsVersions(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	data := realPNG(t, 20, 10) // 200 pixels, under the field's max_pixels
	rec, doc := f.upload(t, f.ada, "library", id, "picture", "p.png", "image/png", data, nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	file := doc["picture"].(map[string]any)
	if file["width"] != float64(20) || file["height"] != float64(10) {
		t.Errorf("size: %v", file)
	}
	v := versionsOf(t, file)
	// thumb has parameters (a worker makes it), big too (functions may redo it), mark only functions make.
	for name, want := range map[string]string{"thumb": "pending", "big": "pending", "mark": "empty"} {
		if got, _ := v[name].(map[string]any); got["status"] != want || got["reason"] != nil {
			t.Errorf("%s = %v, want %s", name, v[name], want)
		}
	}
	// The document with its details is valid, and a read returns them.
	code, got := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	if code != 200 || got["picture"].(map[string]any)["width"] != float64(20) {
		t.Errorf("read: %d %v", code, got)
	}
}

func TestAPictureOverTheFieldsPixelLimitFails(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	_, doc := f.upload(t, f.ada, "library", id, "picture", "p.png", "image/png", realPNG(t, 40, 30), nil) // 1200 pixels > max_pixels 1000
	file, _ := doc["picture"].(map[string]any)
	if file == nil || file["width"] != float64(40) {
		t.Fatalf("upload: %v", doc)
	}
	for name, v := range versionsOf(t, file) {
		if s := v.(map[string]any); s["status"] != "failed" || s["reason"] != "too_large" {
			t.Errorf("%s = %v", name, s)
		}
	}
}

func TestTheSizeIsTheOneAViewerSees(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	var base bytes.Buffer
	if err := jpeg.Encode(&base, image.NewNRGBA(image.Rect(0, 0, 30, 10)), nil); err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte{0xff, 0xd8}, exifSegment(6)...), base.Bytes()[2:]...)
	_, doc := f.upload(t, f.ada, "library", id, "picture", "turned.jpg", "image/jpeg", data, nil)
	file, _ := doc["picture"].(map[string]any)
	if file == nil || file["width"] != float64(10) || file["height"] != float64(30) {
		t.Fatalf("a 30×10 picture turned a quarter is 10×30: %v", doc)
	}
}

func TestAFileThatIsNotAnImageSkipsItsVersions(t *testing.T) {
	f := newFilesFixture(t)
	// forms.extras takes any type and declares a thumb.
	id, tok := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(id, tok)+`}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	did := doc["id"].(string)
	rec, out := f.upload(t, f.ada, "forms", did, "extras", "notes.txt", "text/plain", []byte("just words"), nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	file := out["extras"].([]any)[0].(map[string]any)
	if file["width"] != nil {
		t.Errorf("text with a size: %v", file)
	}
	if th := versionsOf(t, file)["thumb"].(map[string]any); th["status"] != "skipped" || th["reason"] != "not_an_image" {
		t.Errorf("thumb = %v", th)
	}
	// A picture in the same field is pending.
	rec, out = f.upload(t, f.ada, "forms", did, "extras", "p.png", "image/png", realPNG(t, 8, 8), nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	list := out["extras"].([]any)
	pic := list[len(list)-1].(map[string]any)
	if th := versionsOf(t, pic)["thumb"].(map[string]any); th["status"] != "pending" || pic["width"] != float64(8) {
		t.Errorf("picture: %v", pic)
	}
}

func TestAPendingUploadKeepsItsImageDetailsUntilAttached(t *testing.T) {
	f := newFilesFixture(t)
	data := realPNG(t, 12, 6)
	rec, out := f.doRaw(t, "POST", "/v1/acme/app/forms/_files/extras/uploads?name=p.png", data, map[string]string{"Content-Type": "image/png", "Authorization": "Bearer " + f.ada})
	if rec.Code != 201 {
		t.Fatalf("pending: %d %s", rec.Code, rec.Body)
	}
	id, tok := out["upload_id"].(string), out["upload_token"].(string)
	sid, stok := f.pending(t, f.ada, "forms", "scan", pngBytes(5))
	code, doc := f.as(t, f.ada, "POST", "/v1/acme/app/forms", `{"title": "x", "scan": `+ref(sid, stok)+`, "extras": [`+ref(id, tok)+`]}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, doc)
	}
	file := doc["extras"].([]any)[0].(map[string]any)
	if file["width"] != float64(12) || file["height"] != float64(6) {
		t.Errorf("size: %v", file)
	}
	if th := versionsOf(t, file)["thumb"].(map[string]any); th["status"] != "pending" {
		t.Errorf("thumb = %v", th)
	}
}

func TestADirectUploadRecordsTheImageDetails(t *testing.T) {
	f := newFilesFixture(t)
	id := f.videoDoc(t)
	data := realPNG(t, 16, 9)
	rec, out := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/still/uploads", declare("s.png", data, "image/png"))
	if rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	putToLink(t, out, data)
	rec, done := f.complete(t, f.ada, "videos", "still", out["upload_id"].(string), out["upload_token"].(string))
	if rec.Code != 201 {
		t.Fatalf("complete: %d %v", rec.Code, done)
	}
	file := done["still"].(map[string]any)
	if file["width"] != float64(16) || file["height"] != float64(9) {
		t.Errorf("size: %v", file)
	}
	if th := versionsOf(t, file)["thumb"].(map[string]any); th["status"] != "pending" {
		t.Errorf("thumb = %v", th)
	}
}

func TestClientsCannotWriteImageDetails(t *testing.T) {
	f := newFilesFixture(t)
	id := f.newDoc(t, "library")
	// File details named in a write (width, height and versions among them) are dropped or refused.
	code, out := f.as(t, f.ada, "PATCH", "/v1/acme/app/library/"+id, `{"picture": {"id": "fl_aaaaaaaaaaaaaaaaaaaa", "name": "x", "size": 1, "type": "image/png", "sha256": "`+sha([]byte("x"))+`", "uploaded_at": "2026-10-06T10:00:00Z", "width": 5, "height": 5, "versions": {"thumb": {"status": "ready"}}}}`)
	if code < 400 && out["picture"] != nil {
		t.Errorf("a client wrote file details: %d %v", code, out)
	}
	if _, got := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, ""); got["picture"] != nil {
		t.Errorf("stored: %v", got)
	}
}

func TestVersionViewShowsOnlyWhatIsDeclared(t *testing.T) {
	f := newFilesFixture(t)
	c, _ := f.reg.Collection("acme", "app", "library")
	pic := c.Files["picture"]
	thumb, _ := pic.Version("thumb")
	got := versionView(thumb)
	if got["max_width"] != 10 || got["fit"] != "cover" || got["format"] != "jpeg" || got["quality"] != 70 || got["writable"] != false || got["upscale"] != nil {
		t.Errorf("thumb: %v", got)
	}
	mark, _ := pic.Version("mark")
	if got := versionView(mark); len(got) != 1 || got["writable"] != true {
		t.Errorf("mark: %v", got)
	}
}

// runImageJobs lets a worker make everything that is queued.
func runImageJobs(t *testing.T, w *Worker) int {
	t.Helper()
	n := 0
	for w.RunOnce(context.Background()) {
		if n++; n > 50 {
			t.Fatal("the worker never runs out of jobs")
		}
	}
	return n
}

func (f *filesFixture) picture(t *testing.T, id string, img []byte) map[string]any {
	t.Helper()
	rec, doc := f.upload(t, f.ada, "library", id, "picture", "p.png", "image/png", img, nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	return doc
}

func (f *filesFixture) readDoc(t *testing.T, id string) map[string]any {
	t.Helper()
	code, doc := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, "")
	if code != 200 {
		t.Fatalf("read: %d %v", code, doc)
	}
	return doc
}

func TestAWorkerMakesTheVersions(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	doc := f.picture(t, id, realPNG(t, 20, 10))
	before := doc["_meta"].(map[string]any)["version"]
	fileID := doc["picture"].(map[string]any)["id"].(string)
	if th := versionsOf(t, doc["picture"].(map[string]any))["thumb"].(map[string]any); th["status"] != "pending" {
		t.Fatalf("thumb right after the upload: %v", th)
	}

	if n := runImageJobs(t, w); n != 1 {
		t.Fatalf("jobs run: %d", n)
	}
	doc = f.readDoc(t, id)
	file := doc["picture"].(map[string]any)
	v := versionsOf(t, file)
	thumb, _ := v["thumb"].(map[string]any)
	// thumb is a 10×10 cover of a 20×10 picture, as a JPEG at quality 70.
	if thumb["status"] != "ready" || thumb["width"] != float64(10) || thumb["height"] != float64(10) || thumb["type"] != "image/jpeg" ||
		thumb["size"].(float64) < 100 || !strings.HasPrefix(thumb["id"].(string), "fv_") || thumb["fingerprint"] == "" || thumb["generated_at"] != nil {
		t.Errorf("thumb = %v", thumb)
	}
	params, _ := thumb["params"].(map[string]any)
	if params["max_width"] != float64(10) || params["fit"] != "cover" || params["format"] != "jpeg" || params["quality"] != float64(70) {
		t.Errorf("thumb params = %v", params)
	}
	// big has a wider box than the picture: not enlarged, and as a PNG because the source is.
	if big, _ := v["big"].(map[string]any); big["status"] != "ready" || big["width"] != float64(20) || big["height"] != float64(10) || big["type"] != "image/png" {
		t.Errorf("big = %v", big)
	}
	if mark, _ := v["mark"].(map[string]any); mark["status"] != "empty" {
		t.Errorf("mark = %v", mark)
	}
	// The document keeps its version, so a client's If-Match from before still works.
	if doc["_meta"].(map[string]any)["version"] != before {
		t.Errorf("the version changed: %v → %v", before, doc["_meta"].(map[string]any)["version"])
	}
	hdr := bearer(f.ada)
	hdr["If-Match"] = `"` + strconv.Itoa(int(before.(float64))) + `"`
	rec, patched := f.doH(t, "PATCH", "/v1/acme/app/library/"+id, `{"title": "renamed"}`, hdr)
	if rec.Code != 200 || patched["title"] != "renamed" {
		t.Errorf("a client's write with the earlier version: %d %v", rec.Code, patched)
	}
	if got := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))["thumb"].(map[string]any); got["status"] != "ready" {
		t.Errorf("the client's write lost the versions: %v", got)
	}

	// Each copy is an object beside the source, and is a picture.
	keys := f.s3.Keys()
	slices.Sort(keys)
	base := "t/acme/app/library/" + fileID
	if !slices.Equal(keys, []string{base, base + "/big", base + "/thumb"}) {
		t.Fatalf("objects: %v", keys)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(f.s3.Object(base + "/thumb"))); err != nil {
		t.Errorf("thumb is not a JPEG: %v", err)
	}
	// Their bytes count in the usage.
	if bytes, files, _ := f.usage(t); files != 3 || bytes <= int64(len(realPNG(t, 20, 10))) {
		t.Errorf("usage: %d bytes in %d files", bytes, files)
	}
	// Nothing is left to do, and the journal has no unfinished upload.
	if runImageJobs(t, w) != 0 {
		t.Error("another job ran")
	}
	if stale, _ := f.svc.AbandonedUploads(context.Background(), 10); len(stale) != 0 {
		t.Errorf("unfinished uploads: %v", stale)
	}
}

func TestReplacingOrDeletingTheSourceDeletesItsVersions(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	first := f.picture(t, id, realPNG(t, 20, 10))["picture"].(map[string]any)["id"].(string)
	runImageJobs(t, w)
	if len(f.s3.Keys()) != 3 {
		t.Fatalf("objects: %v", f.s3.Keys())
	}

	second := f.picture(t, id, realPNG(t, 16, 8))["picture"].(map[string]any)["id"].(string)
	// The replaced file and its two copies are queued; the new file starts over.
	if got := len(f.deletions()); got != 3 {
		t.Errorf("deletions queued: %v", f.deletions())
	}
	if th := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))["thumb"].(map[string]any); th["status"] != "pending" {
		t.Errorf("the new file's thumb: %v", th)
	}
	runImageJobs(t, w)
	w.FilesDue(context.Background())
	keys := f.s3.Keys()
	slices.Sort(keys)
	base := "t/acme/app/library/" + second
	if !slices.Equal(keys, []string{base, base + "/big", base + "/thumb"}) || first == second {
		t.Fatalf("after the replacement: %v", keys)
	}
	if _, files, _ := f.usage(t); files != 3 {
		t.Errorf("usage files: %d", files)
	}

	// Deleting the document takes the copies with it.
	if code, out := f.as(t, f.ada, "DELETE", "/v1/acme/app/library/"+id, ""); code != 204 && code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	w.FilesDue(context.Background())
	if keys := f.s3.Keys(); len(keys) != 0 {
		t.Errorf("objects left: %v", keys)
	}
	if bytes, files, _ := f.usage(t); files != 0 || bytes != 0 {
		t.Errorf("usage: %d bytes in %d files", bytes, files)
	}
}

func TestAnImageThatCannotBeDecodedFails(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	good := realPNG(t, 20, 10)
	f.picture(t, id, good[:len(good)-30]) // the header is fine, the pixels are cut short
	runImageJobs(t, w)
	v := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))
	for _, name := range []string{"thumb", "big"} {
		if s := v[name].(map[string]any); s["status"] != "failed" || s["reason"] != "decode_error" {
			t.Errorf("%s = %v", name, s)
		}
	}
	if keys := f.s3.Keys(); len(keys) != 1 {
		t.Errorf("objects: %v", keys)
	}
}

func TestAKilledWorkersJobIsTakenOver(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	f.picture(t, id, realPNG(t, 20, 10))

	// Another worker claims the job and dies without a word.
	job, found, err := f.svc.ClaimJob(context.Background(), "dead-worker")
	if err != nil || !found || job.Image == nil || job.Origin != auth.ImageOrigin {
		t.Fatalf("claim: %+v %v %v", job, found, err)
	}
	if w.RunOnce(context.Background()) {
		t.Fatal("a job that is leased was run")
	}
	*f.clock = f.clock.Add(time.Hour)
	if !w.RunOnce(context.Background()) {
		t.Fatal("the lapsed job was not taken over")
	}
	if th := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))["thumb"].(map[string]any); th["status"] != "ready" {
		t.Errorf("thumb = %v", th)
	}
	// Running it again (a duplicate, or the dead worker's late retry) changes nothing.
	again := auth.Job{ID: job.ID, Database: job.Database, Function: job.Function, Image: job.Image}
	w.runImage(context.Background(), slog.New(slog.DiscardHandler), "acme", f.svc, again)
	if keys := f.s3.Keys(); len(keys) != 3 {
		t.Errorf("objects: %v", keys)
	}
}

func TestAJobForAFileThatIsGoneEndsQuietly(t *testing.T) {
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	f.picture(t, id, realPNG(t, 20, 10))
	// The file is replaced before the worker gets to it.
	f.picture(t, id, realPNG(t, 12, 12))
	runImageJobs(t, w)
	for _, name := range []string{"thumb", "big"} {
		if s := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))[name].(map[string]any); s["status"] != "ready" {
			t.Errorf("%s = %v", name, s)
		}
	}
	w.FilesDue(context.Background())
	if keys := f.s3.Keys(); len(keys) != 3 {
		t.Errorf("objects: %v", keys)
	}
}

func TestReconcileKeepsTheVersionsADocumentRecords(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	fileID := f.picture(t, id, realPNG(t, 20, 10))["picture"].(map[string]any)["id"].(string)
	runImageJobs(t, w)
	base := "t/acme/app/library/" + fileID
	old := f.clock.Add(-48 * time.Hour)
	for _, k := range []string{base, base + "/thumb", base + "/big"} {
		f.s3.PutAt(k, f.s3.Object(k), old)
	}
	f.s3.PutAt(base+"/forgotten", []byte("x"), old)                                // the file is held, this version isn't recorded
	f.s3.PutAt("t/acme/app/library/fl_gone00000000000001/thumb", []byte("x"), old) // its file is gone
	f.s3.PutAt(base+"/thumb/deeper", []byte("x"), old)                             // not a version's shape

	hdr := map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"}
	rec, rep := f.doRaw(t, "POST", "/v1/acme/_admin/storage/reconcile", []byte(`{"delete": true}`), hdr)
	if rec.Code != 200 {
		t.Fatalf("reconcile: %d %s", rec.Code, rec.Body)
	}
	if rep["referenced"] != float64(2) || rep["skipped_in_journal"] != float64(1) || rep["deleted"] != float64(2) || rep["ignored"] != float64(1) {
		t.Errorf("report: %v", rep)
	}
	keys := f.s3.Keys()
	slices.Sort(keys)
	if want := []string{base, base + "/big", base + "/thumb", base + "/thumb/deeper"}; !slices.Equal(keys, want) {
		t.Errorf("objects: %v, want %v", keys, want)
	}
}

func TestAMadeVersionThatWasNotRecordedIsCleanedUp(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	fileID := f.picture(t, id, realPNG(t, 20, 10))["picture"].(map[string]any)["id"].(string)
	runImageJobs(t, w)
	thumb := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))["thumb"].(map[string]any)["id"].(string)

	// A worker died after storing a version, before it was recorded: its journal entry
	// is left stored, and a recorded version's entry never is deleted.
	ghost := "fv_ghost00000000000001"
	key := "t/acme/app/library/" + fileID + "/ghost"
	_ = f.svc.JournalUpload(context.Background(), auth.FileJournalEntry{ID: ghost, Database: "app", Collection: "library", Field: "picture", DocumentID: id, Key: key})
	_ = f.svc.SetUploadStatus(context.Background(), ghost, auth.JournalStored, "")
	f.s3.Put(key, []byte("x"))
	// And one whose document did record it, but died before saying so.
	_ = f.svc.SetUploadStatus(context.Background(), thumb, auth.JournalStored, "")

	*f.clock = f.clock.Add(3 * time.Hour)
	w.FilesDue(context.Background())
	w.FilesDue(context.Background())
	keys := f.s3.Keys()
	slices.Sort(keys)
	base := "t/acme/app/library/" + fileID
	if !slices.Equal(keys, []string{base, base + "/big", base + "/thumb"}) {
		t.Errorf("objects: %v", keys)
	}
	if e, err := f.svc.UploadEntry(context.Background(), thumb); err != nil || e.Status != auth.JournalAttached {
		t.Errorf("a recorded version: %+v %v", e, err)
	}
}

func TestAStorageThatKeepsFailingFailsTheVersionsAfterRetries(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	f.picture(t, id, realPNG(t, 20, 10))
	good := f.reg.Realms["acme"].Settings.Storage.Endpoint
	f.reg.Realms["acme"].Settings.Storage.Endpoint = "http://127.0.0.1:1" // nothing listens
	f.svc.Cache = auth.NewSecretCache()                                   // the connection is made again

	for attempt := 1; attempt < imageAttempts; attempt++ {
		if !w.RunOnce(context.Background()) {
			t.Fatalf("attempt %d: no job to run", attempt)
		}
		if th := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))["thumb"].(map[string]any); th["status"] != "pending" {
			t.Fatalf("attempt %d: thumb = %v", attempt, th)
		}
		if w.RunOnce(context.Background()) {
			t.Fatalf("attempt %d: the retry ran at once", attempt)
		}
		*f.clock = f.clock.Add(15 * time.Minute) // past any wait
	}
	if !w.RunOnce(context.Background()) {
		t.Fatal("the last attempt did not run")
	}
	for _, name := range []string{"thumb", "big"} {
		if s := versionsOf(t, f.readDoc(t, id)["picture"].(map[string]any))[name].(map[string]any); s["status"] != "failed" || s["reason"] != "storage_error" {
			t.Errorf("%s = %v", name, s)
		}
	}
	_ = good
}

func TestAVersionOfAnotherPendingSourceIsOnlyMadeWhenPending(t *testing.T) {
	// A job for a file whose versions are no longer pending does nothing.
	f := newFilesFixture(t)
	w := newTestWorker(t, f.rulesFixture)
	id := f.newDoc(t, "library")
	doc := f.picture(t, id, realPNG(t, 20, 10))
	runImageJobs(t, w)
	before := f.readDoc(t, id)
	job := auth.Job{ID: "x", Database: "app", Function: "library", Image: &auth.ImageJob{Collection: "library", Field: "picture", DocumentID: id, FileID: doc["picture"].(map[string]any)["id"].(string)}}
	w.runImage(context.Background(), slog.New(slog.DiscardHandler), "acme", f.svc, job)
	after := f.readDoc(t, id)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the document changed:\n%v\n%v", before, after)
	}
}
