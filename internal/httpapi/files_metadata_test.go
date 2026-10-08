package httpapi

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/imaging"
)

// dirtyJPEG is a w×h picture with the things a phone leaves in it: EXIF with an orientation
// (turned a quarter) and a position, a comment and XMP.
func dirtyJPEG(t *testing.T, w, h int) (dirty, clean []byte) {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewNRGBA(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	clean = b.Bytes()
	seg := func(code byte, body string) []byte {
		out := []byte{0xff, code}
		out = binary.BigEndian.AppendUint16(out, uint16(len(body)+2))
		return append(out, body...)
	}
	dirty = append([]byte{0xff, 0xd8}, exifSegment(6)...)
	dirty = append(dirty, seg(0xe1, "http://ns.adobe.com/xap/1.0/\x00<x>SECRET-GPS-POSITION 48.85,2.35</x>")...)
	dirty = append(dirty, seg(0xfe, "SECRET-COMMENT at home")...)
	dirty = append(dirty, clean[2:]...)
	return dirty, clean
}

func pngWithText(t *testing.T) []byte {
	t.Helper()
	base := realPNG(t, 12, 8)
	chunk := func(kind, data string) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
		out = append(out, kind...)
		out = append(out, data...)
		return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE([]byte(kind+data)))
	}
	cut := 8 + 25
	return append(append(append([]byte{}, base[:cut]...), append(chunk("tEXt", "Comment\x00SECRET-GPS-POSITION"), chunk("tIME", "\x07\xea\x0a\x08\x0c\x00\x00")...)...), base[cut:]...)
}

func pixelsOf(t *testing.T, data []byte) []byte {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	n := image.NewNRGBA(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			n.Set(x, y, img.At(x, y))
		}
	}
	return n.Pix
}

func (f *filesFixture) onlyObject(t *testing.T) []byte {
	t.Helper()
	keys := f.s3.Keys()
	if len(keys) != 1 {
		t.Fatalf("objects: %v", keys)
	}
	return f.s3.Object(keys[0])
}

func TestAProxyUploadLosesItsMetadata(t *testing.T) {
	f := newFilesFixture(t)
	dirty, clean := dirtyJPEG(t, 30, 10)
	id := f.newDoc(t, "library")
	rec, doc := f.upload(t, f.ada, "library", id, "shot", "holiday.jpg", "image/jpeg", dirty, nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	stored := f.onlyObject(t)
	if bytes.Contains(stored, []byte("SECRET")) || bytes.Contains(stored, []byte("xap")) {
		t.Error("the stored picture still carries its metadata")
	}
	// It still shows upright, and not a pixel changed.
	info, err := imaging.Inspect(bytes.NewReader(stored))
	if err != nil || info.Orientation != 6 || info.Width != 30 || info.Height != 10 {
		t.Errorf("stored picture: %+v %v", info, err)
	}
	if !reflect.DeepEqual(pixelsOf(t, stored), pixelsOf(t, clean)) {
		t.Error("the pixels changed")
	}
	// The details describe what is stored, not what was sent.
	file := doc["shot"].(map[string]any)
	if file["size"] != float64(len(stored)) || file["sha256"] != sha(stored) || file["sha256"] == sha(dirty) || len(stored) >= len(dirty) {
		t.Errorf("details: %v (stored %d bytes, sent %d)", file, len(stored), len(dirty))
	}
	// The size a viewer sees has the orientation applied.
	if file["width"] != float64(10) || file["height"] != float64(30) {
		t.Errorf("size: %v", file)
	}
	// And it downloads as what was stored.
	got := f.doRawGet(t, "/v1/acme/app/library/"+id+"/_files/shot/"+file["id"].(string)+"?link=json")
	if got == "" {
		t.Error("no link")
	}
}

func (f *filesFixture) doRawGet(t *testing.T, path string) string {
	t.Helper()
	rec, out := f.doRaw(t, "GET", path, nil, map[string]string{"Authorization": "Bearer " + f.ada})
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	u, _ := out["url"].(string)
	return u
}

func TestAFieldMayKeepMetadata(t *testing.T) {
	f := newFilesFixture(t)
	dirty, _ := dirtyJPEG(t, 30, 10)
	id := f.newDoc(t, "library")
	rec, doc := f.upload(t, f.ada, "library", id, "raw", "holiday.jpg", "image/jpeg", dirty, nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(f.onlyObject(t), dirty) {
		t.Error("keep_metadata: true changed the file")
	}
	if doc["raw"].(map[string]any)["sha256"] != sha(dirty) {
		t.Errorf("sha: %v", doc["raw"])
	}
}

func TestPNGTextAndTimeAreRemoved(t *testing.T) {
	f := newFilesFixture(t)
	dirty := pngWithText(t)
	id := f.newDoc(t, "library")
	rec, doc := f.upload(t, f.ada, "library", id, "shot", "a.png", "image/png", dirty, nil)
	if rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	stored := f.onlyObject(t)
	if bytes.Contains(stored, []byte("SECRET")) || bytes.Contains(stored, []byte("tIME")) || bytes.Contains(stored, []byte("tEXt")) {
		t.Error("the PNG kept its text or time")
	}
	if !bytes.Equal(stored, realPNG(t, 12, 8)) {
		t.Error("what is left isn't the original picture")
	}
	if doc["shot"].(map[string]any)["size"] != float64(len(stored)) {
		t.Errorf("size: %v", doc["shot"])
	}
}

func TestOtherFilesAreStoredAsSent(t *testing.T) {
	f := newFilesFixture(t)
	pdf := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\nSECRET-AUTHOR\n")
	id := f.newDoc(t, "library")
	if rec, _ := f.upload(t, f.ada, "library", id, "shot", "a.pdf", "application/pdf", pdf, nil); rec.Code != 201 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(f.onlyObject(t), pdf) {
		t.Error("a PDF was changed")
	}
}

func TestADamagedPictureIsRefusedNotStoredWithItsMetadata(t *testing.T) {
	f := newFilesFixture(t)
	dirty, _ := dirtyJPEG(t, 30, 10)
	id := f.newDoc(t, "library")
	rec, out := f.upload(t, f.ada, "library", id, "shot", "cut.jpg", "image/jpeg", dirty[:len(dirty)-40], nil)
	if rec.Code != 422 || out["error"].(map[string]any)["code"] != "invalid_image" {
		t.Fatalf("a cut JPEG: %d %s", rec.Code, rec.Body)
	}
	// Nothing is left behind for a worker to find later, and the document holds no file.
	w := newTestWorker(t, f.rulesFixture)
	w.FilesDue(t.Context())
	if keys := f.s3.Keys(); len(keys) != 0 {
		t.Errorf("objects after the refusal: %v", keys)
	}
	if _, got := f.as(t, f.ada, "GET", "/v1/acme/app/library/"+id, ""); got["shot"] != nil {
		t.Errorf("the document holds a file: %v", got["shot"])
	}
	// The same file goes in when the field keeps metadata.
	if rec, _ := f.upload(t, f.ada, "library", id, "raw", "cut.jpg", "image/jpeg", dirty[:len(dirty)-40], nil); rec.Code != 201 {
		t.Errorf("keep_metadata: %d", rec.Code)
	}
}

func TestAPendingUploadLosesItsMetadataToo(t *testing.T) {
	f := newFilesFixture(t)
	dirty, _ := dirtyJPEG(t, 20, 20)
	rec, out := f.doRaw(t, "POST", "/v1/acme/app/library/_files/shot/uploads?name=p.jpg", dirty, map[string]string{"Content-Type": "image/jpeg", "Authorization": "Bearer " + f.ada})
	if rec.Code != 201 {
		t.Fatalf("pending: %d %s", rec.Code, rec.Body)
	}
	stored := f.onlyObject(t)
	file := out["file"].(map[string]any)
	if bytes.Contains(stored, []byte("SECRET")) || file["size"] != float64(len(stored)) || file["sha256"] != sha(stored) {
		t.Errorf("pending upload: stored %d bytes, details %v", len(stored), file)
	}
	_, doc := f.as(t, f.ada, "POST", "/v1/acme/app/library", `{"title": "with a photo", "shot": `+ref(out["upload_id"].(string), out["upload_token"].(string))+`}`)
	if doc["shot"].(map[string]any)["sha256"] != sha(stored) {
		t.Errorf("attached: %v", doc["shot"])
	}
}

func TestADirectUploadIsCleanedWhenItIsCompleted(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	dirty, clean := dirtyJPEG(t, 30, 10)
	for field, want := range map[string]bool{"snap": true, "rawsnap": false} {
		id := f.videoDoc(t)
		rec, out := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/"+field+"/uploads", declare("p.jpg", dirty, "image/jpeg"))
		if rec.Code != 201 {
			t.Fatalf("%s start: %d %s", field, rec.Code, rec.Body)
		}
		putToLink(t, out, dirty)
		rec, done := f.complete(t, f.ada, "videos", field, out["upload_id"].(string), out["upload_token"].(string))
		if rec.Code != 201 {
			t.Fatalf("%s complete: %d %v", field, rec.Code, done)
		}
		file := done[field].(map[string]any)
		key := "t/acme/app/videos/" + file["id"].(string)
		stored := f.s3.Object(key)
		if want {
			// Cleaned in place, and the details are those of what is stored.
			if bytes.Contains(stored, []byte("SECRET")) || len(stored) >= len(dirty) || file["size"] != float64(len(stored)) || file["sha256"] != sha(stored) {
				t.Errorf("%s: stored %d of %d bytes, details %v", field, len(stored), len(dirty), file)
			}
			if !reflect.DeepEqual(pixelsOf(t, stored), pixelsOf(t, clean)) {
				t.Errorf("%s: the pixels changed", field)
			}
			if info, err := imaging.Inspect(bytes.NewReader(stored)); err != nil || info.Orientation != 6 {
				t.Errorf("%s: %+v %v", field, info, err)
			}
		} else if !bytes.Equal(stored, dirty) || file["sha256"] != sha(dirty) {
			t.Errorf("%s: a field that keeps metadata was changed", field)
		}
	}
}

func TestADamagedDirectUploadIsRefusedAndDeleted(t *testing.T) {
	f := newFilesFixture(t)
	f.svc.Now = func() time.Time { return *f.clock }
	dirty, _ := dirtyJPEG(t, 30, 10)
	cut := dirty[:len(dirty)-40]
	id := f.videoDoc(t)
	before := len(f.s3.Keys())
	rec, out := f.startOn(t, f.ada, "/v1/acme/app/videos/"+id+"/_files/snap/uploads", declare("p.jpg", cut, "image/jpeg"))
	if rec.Code != 201 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	putToLink(t, out, cut)
	rec, done := f.complete(t, f.ada, "videos", "snap", out["upload_id"].(string), out["upload_token"].(string))
	if rec.Code != 422 || done["error"].(map[string]any)["code"] != "invalid_image" {
		t.Fatalf("complete: %d %v", rec.Code, done)
	}
	if len(f.s3.Keys()) != before {
		t.Errorf("the refused object is still stored: %v", f.s3.Keys())
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Error("the answer leaks")
	}
}

func TestWhatFollowsAPictureStillCountsAgainstTheSizeLimit(t *testing.T) {
	f := newFilesFixture(t)
	// shot takes 64 KiB: a tiny picture followed by more than that is too large,
	// although the copy without the trailer would fit.
	big := append(realPNG(t, 4, 4), bytes.Repeat([]byte{0}, 70<<10)...)
	id := f.newDoc(t, "library")
	rec, out := f.upload(t, f.ada, "library", id, "shot", "big.png", "image/png", big, nil)
	if rec.Code != 413 {
		t.Fatalf("a trailer over max_size: %d %v", rec.Code, out)
	}
	if keys := f.s3.Keys(); len(keys) > 1 {
		t.Errorf("objects: %v", keys)
	}
	// Under the limit, the trailer is dropped.
	small := append(realPNG(t, 4, 4), []byte("trailer-secret")...)
	rec, doc := f.upload(t, f.ada, "library", id, "shot", "small.png", "image/png", small, nil)
	if rec.Code != 201 || doc["shot"].(map[string]any)["size"] != float64(len(realPNG(t, 4, 4))) {
		t.Errorf("small: %d %v", rec.Code, doc["shot"])
	}
}
