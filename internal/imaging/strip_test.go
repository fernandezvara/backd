package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	hcrc "hash/crc32"
	"image"
	"image/png"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func segment(code byte, body []byte) []byte {
	out := []byte{0xff, code}
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)+2))
	return append(out, body...)
}

// withSegments inserts segments right after the SOI of a JPEG.
func withSegments(jpg []byte, segs ...[]byte) []byte {
	out := []byte{0xff, 0xd8}
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

func exifWithGPS(orientation uint16) []byte {
	e := withExif([]byte{0xff, 0xd8}, orientation)
	return e[2:]
}

func strip(t *testing.T, data []byte, format string) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Strip(&out, bytes.NewReader(data), format); err != nil {
		t.Fatalf("Strip: %v", err)
	}
	return out.Bytes()
}

func pixels(t *testing.T, data []byte) *image.NRGBA {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewNRGBA(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

func TestStripJPEGKeepsEveryPixelAndTheOrientation(t *testing.T) {
	base := jpegBytes(t, quadrants(64, 48))
	icc := segment(0xe2, append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 40)...))
	dirty := withSegments(base,
		segment(0xe0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")),
		exifWithGPS(6),
		segment(0xe1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte("<x:xmpmeta>SECRET-GPS-POSITION</x:xmpmeta>")...)),
		segment(0xed, []byte("Photoshop 3.0\x00 SECRET-IPTC-CAPTION")),
		segment(0xfe, []byte("SECRET-COMMENT shot at the cabin")),
		segment(0xe2, append([]byte("MPF\x00"), bytes.Repeat([]byte{1}, 30)...)),
		icc,
	)
	dirty = append(dirty, []byte("TRAILER-SECRET-SECOND-IMAGE")...)
	for _, secret := range []string{"SECRET-GPS-POSITION", "SECRET-IPTC", "SECRET-COMMENT", "TRAILER-SECRET"} {
		if !bytes.Contains(dirty, []byte(secret)) {
			t.Fatalf("test setup: %s isn't in the input", secret)
		}
	}

	clean := strip(t, dirty, "jpeg")
	for _, gone := range []string{"SECRET", "xap", "Photoshop", "MPF", "TRAILER"} {
		if bytes.Contains(clean, []byte(gone)) {
			t.Errorf("the output still has %q", gone)
		}
	}
	// What decides how it looks is there: the profile and the orientation.
	if !bytes.Contains(clean, []byte("ICC_PROFILE\x00")) || !bytes.Contains(clean, []byte("JFIF")) {
		t.Error("the JFIF header or the colour profile was dropped")
	}
	info, err := Inspect(bytes.NewReader(clean))
	if err != nil || info.Orientation != 6 || info.Width != 64 || info.Height != 48 {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
	if bytes.Count(clean, []byte("Exif\x00\x00")) != 1 {
		t.Error("expected one minimal EXIF block")
	}
	// Not one pixel changed (a re-encode would change some), and the compressed data is the same bytes.
	if !reflect.DeepEqual(pixels(t, clean).Pix, pixels(t, base).Pix) {
		t.Error("the pixels differ")
	}
	scan := func(b []byte) []byte {
		return b[bytes.Index(b, []byte{0xff, 0xda}):bytes.LastIndex(b, []byte{0xff, 0xd9})]
	}
	if !bytes.Equal(scan(clean), scan(base)) {
		t.Error("the image data was not copied byte for byte")
	}
	if !bytes.HasSuffix(clean, []byte{0xff, 0xd9}) {
		t.Error("the output doesn't end at the end of the image")
	}
	// Streaming one byte at a time gives the same file.
	var slow bytes.Buffer
	if err := Strip(&slow, iotest.OneByteReader(bytes.NewReader(dirty)), "jpeg"); err != nil || !bytes.Equal(slow.Bytes(), clean) {
		t.Errorf("slow reader: %v, same=%v", err, bytes.Equal(slow.Bytes(), clean))
	}
}

func TestStripJPEGWithNoOrientationHasNoEXIF(t *testing.T) {
	base := jpegBytes(t, quadrants(16, 16))
	clean := strip(t, withSegments(base, exifWithGPS(1)), "jpeg")
	if bytes.Contains(clean, []byte("Exif")) || bytes.Contains(clean, []byte("SECRET")) {
		t.Error("an upright picture kept an EXIF block")
	}
	if !bytes.Equal(clean, base) && !reflect.DeepEqual(pixels(t, clean).Pix, pixels(t, base).Pix) {
		t.Error("pixels differ")
	}
}

// A hand-made file with the awkward parts of a scan: stuffed bytes, restart markers, fill bytes,
// two scans (a progressive file), and what follows the end.
func TestStripJPEGCopiesScansExactly(t *testing.T) {
	scan1 := []byte{0x12, 0xff, 0x00, 0x34, 0xff, 0xd0, 0x56, 0xff, 0xff, 0xd1, 0x78}
	scan2 := []byte{0x9a, 0xff, 0x00, 0xbc}
	in := []byte{0xff, 0xd8}
	in = append(in, segment(0xe1, []byte("Exif\x00\x00SECRET"))...)
	in = append(in, segment(0xdb, bytes.Repeat([]byte{3}, 65))...)
	in = append(in, segment(0xc2, []byte{8, 0, 8, 0, 8, 1, 1, 0x11, 0})...)
	in = append(in, segment(0xda, []byte{1, 1, 0, 0, 63, 0})...)
	in = append(in, scan1...)
	in = append(in, segment(0xc4, bytes.Repeat([]byte{5}, 20))...)
	in = append(in, segment(0xda, []byte{1, 1, 0, 0, 63, 0})...)
	in = append(in, scan2...)
	in = append(in, 0xff, 0xd9)
	in = append(in, []byte("after the end")...)

	got := strip(t, in, "jpeg")
	// The stuffed bytes, restart markers and the scans are all there, in order.
	for _, part := range [][]byte{scan1[:5], {0xff, 0xd0}, {0x56}, {0xff, 0xd1, 0x78}, scan2, segment(0xc4, bytes.Repeat([]byte{5}, 20))} {
		if !bytes.Contains(got, part) {
			t.Errorf("missing %x in %x", part, got)
		}
	}
	if bytes.Contains(got, []byte("SECRET")) || bytes.Contains(got, []byte("after the end")) || !bytes.HasSuffix(got, []byte{0xff, 0xd9}) {
		t.Errorf("output: %x", got)
	}
	if bytes.Count(got, []byte{0xff, 0xda}) != 2 {
		t.Error("both scans must be kept")
	}
}

func pngChunk(kind string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, kind...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, hcrc.ChecksumIEEE(append([]byte(kind), data...)))
}

// chunks lists the types of a PNG's chunks, and the bytes of its IDAT data.
func chunks(t *testing.T, data []byte) (kinds []string, idat []byte) {
	t.Helper()
	b := data[8:]
	for len(b) >= 12 {
		n := int(binary.BigEndian.Uint32(b[:4]))
		kinds = append(kinds, string(b[4:8]))
		if string(b[4:8]) == "IDAT" {
			idat = append(idat, b[8:8+n]...)
		}
		b = b[12+n:]
	}
	return kinds, idat
}

func TestStripPNGDropsTextAndTimeKeepsThePicture(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 3)
	}
	var enc bytes.Buffer
	if err := png.Encode(&enc, img); err != nil {
		t.Fatal(err)
	}
	base := enc.Bytes()
	// Insert chunks after IHDR (the first 8+25 bytes).
	cut := 8 + 25
	extra := bytes.Join([][]byte{
		pngChunk("tEXt", []byte("Comment\x00SECRET-GPS-POSITION")),
		pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00<x>SECRET-XMP</x>")),
		pngChunk("zTXt", []byte("Software\x00\x00SECRET")),
		pngChunk("eXIf", []byte("MM\x00\x2aSECRET-EXIF")),
		pngChunk("tIME", []byte{7, 0xea, 10, 8, 12, 0, 0}),
		pngChunk("pHYs", []byte{0, 0, 0x0b, 0x13, 0, 0, 0x0b, 0x13, 1}),
		pngChunk("gAMA", []byte{0, 1, 0x86, 0xa0}),
		pngChunk("vpAg", []byte("a private chunk")),
	}, nil)
	dirty := append(append(append([]byte{}, base[:cut]...), extra...), base[cut:]...)
	clean := strip(t, dirty, "png")
	kinds, idat := chunks(t, clean)
	if strings.Join(kinds, " ") != "IHDR pHYs gAMA IDAT IEND" {
		t.Errorf("chunks: %v", kinds)
	}
	_, baseIDAT := chunks(t, base)
	if !bytes.Equal(idat, baseIDAT) {
		t.Error("the image data differs")
	}
	if bytes.Contains(clean, []byte("SECRET")) {
		t.Error("a secret is still there")
	}
	if !reflect.DeepEqual(pixels(t, clean).Pix, img.Pix) {
		t.Error("the pixels differ")
	}
	var slow bytes.Buffer
	if err := Strip(&slow, iotest.OneByteReader(bytes.NewReader(dirty)), "png"); err != nil || !bytes.Equal(slow.Bytes(), clean) {
		t.Errorf("slow reader: %v", err)
	}
}

func TestStripFailsClosedOnADamagedFile(t *testing.T) {
	good := jpegBytes(t, quadrants(32, 32))
	png1 := pngBytes(t, quadrants(32, 32))
	for name, c := range map[string]struct {
		data   []byte
		format string
	}{
		"a JPEG cut short":     {good[:len(good)/2], "jpeg"},
		"not a JPEG":           {[]byte("hello, this is text"), "jpeg"},
		"empty":                {nil, "jpeg"},
		"a JPEG with no end":   {good[:len(good)-2], "jpeg"},
		"a PNG cut short":      {png1[:len(png1)/2], "png"},
		"a PNG with no IEND":   {png1[:len(png1)-12], "png"},
		"not a PNG":            {good, "png"},
		"a format it can't do": {good, "gif"},
	} {
		var out bytes.Buffer
		err := Strip(&out, bytes.NewReader(c.data), c.format)
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if c.format != "gif" && !errors.Is(err, ErrUnreadable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestStripOfACleanImageChangesNothingVisible(t *testing.T) {
	for _, data := range [][]byte{jpegBytes(t, quadrants(40, 30)), pngBytes(t, quadrants(40, 30))} {
		format := "jpeg"
		if data[0] == 0x89 {
			format = "png"
		}
		out := strip(t, data, format)
		if !reflect.DeepEqual(pixels(t, out).Pix, pixels(t, data).Pix) {
			t.Errorf("%s: pixels differ", format)
		}
		if format == "png" && !bytes.Equal(out, data) {
			t.Error("a PNG without metadata should come out identical")
		}
	}
}
