package httpapi

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
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
