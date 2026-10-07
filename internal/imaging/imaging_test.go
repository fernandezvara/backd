package imaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	crc "hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	red   = color.NRGBA{255, 0, 0, 255}
	green = color.NRGBA{0, 255, 0, 255}
	blue  = color.NRGBA{0, 0, 255, 255}
	white = color.NRGBA{255, 255, 255, 255}
)

// quadrants is a w×h image: red top-left, green top-right, blue bottom-left, white bottom-right.
func quadrants(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := red
			switch {
			case x >= w/2 && y < h/2:
				c = green
			case x < w/2 && y >= h/2:
				c = blue
			case x >= w/2 && y >= h/2:
				c = white
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif inserts an APP1 block with an orientation (and some GPS-like text) after SOI.
func withExif(jpg []byte, orientation uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, orientation)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	tiff = append(tiff, []byte("SECRET-GPS-POSITION")...)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xff, 0xd8, 0xff, 0xe1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(seg)+2))
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func process(t *testing.T, e *Engine, data []byte, p Params) *Result {
	t.Helper()
	out, err := e.Process(context.Background(), bytes.NewReader(data), 0, []Params{p})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out[0].Err != nil {
		t.Fatalf("version: %v", out[0].Err)
	}
	return out[0].Result
}

func decode(t *testing.T, r *Result) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(r.Data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// near reports whether the pixel at (x, y) is close to want (JPEG is lossy).
func near(img image.Image, x, y int, want color.NRGBA) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	d := func(a uint32, w uint8) int {
		v := int(a>>8) - int(w)
		if v < 0 {
			v = -v
		}
		return v
	}
	return d(r, want.R) < 40 && d(g, want.G) < 40 && d(b, want.B) < 40
}

func TestFits(t *testing.T) {
	e := New(Limits{})
	src := pngBytes(t, quadrants(200, 100))
	for _, tc := range []struct {
		name string
		p    Params
		w, h int
	}{
		{"contain by width", Params{MaxWidth: 100}, 100, 50},
		{"contain by height", Params{MaxHeight: 25}, 50, 25},
		{"contain in a square", Params{MaxWidth: 60, MaxHeight: 60}, 60, 30},
		{"contain is the default", Params{MaxWidth: 60, MaxHeight: 60, Fit: Contain}, 60, 30},
		{"cover fills the box", Params{MaxWidth: 60, MaxHeight: 60, Fit: Cover}, 60, 60},
		{"cover wide", Params{MaxWidth: 100, MaxHeight: 20, Fit: Cover}, 100, 20},
		{"cover with one dimension is contain", Params{MaxWidth: 50, Fit: Cover}, 50, 25},
		{"stretch ignores proportions", Params{MaxWidth: 30, MaxHeight: 90, Fit: Stretch}, 30, 90},
		{"stretch keeps a missing dimension", Params{MaxWidth: 50, Fit: Stretch}, 50, 100},
		{"no enlarging", Params{MaxWidth: 1000, MaxHeight: 1000}, 200, 100},
		{"no enlarging when covering", Params{MaxWidth: 1000, MaxHeight: 1000, Fit: Cover}, 200, 100},
		{"no enlarging when stretching", Params{MaxWidth: 1000, MaxHeight: 20, Fit: Stretch}, 200, 20},
		{"enlarging when asked", Params{MaxWidth: 400, MaxHeight: 400, Upscale: true}, 400, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := process(t, e, src, tc.p)
			if r.Width != tc.w || r.Height != tc.h {
				t.Fatalf("result %d×%d, want %d×%d", r.Width, r.Height, tc.w, tc.h)
			}
			b := decode(t, r).Bounds()
			if b.Dx() != tc.w || b.Dy() != tc.h {
				t.Fatalf("image %d×%d, want %d×%d", b.Dx(), b.Dy(), tc.w, tc.h)
			}
		})
	}
}

func TestCoverCropsTheCenter(t *testing.T) {
	// Covering a tall box keeps the middle strip, which still holds a bit of each quadrant.
	e := New(Limits{})
	r := process(t, e, pngBytes(t, quadrants(200, 100)), Params{MaxWidth: 50, MaxHeight: 100, Fit: Cover, Format: PNG})
	img := decode(t, r)
	if !near(img, 5, 10, red) || !near(img, 45, 10, green) || !near(img, 5, 90, blue) || !near(img, 45, 90, white) {
		t.Fatal("the crop is not the centered strip: corners are not the quadrants' colors")
	}
}

func TestOrientation(t *testing.T) {
	e := New(Limits{})
	base := jpegBytes(t, quadrants(32, 16)) // as stored: red green / blue white
	// the corners (top-left, top-right, bottom-left, bottom-right) a viewer sees for each flag
	for o, want := range map[uint16][4]color.NRGBA{
		1: {red, green, blue, white},
		2: {green, red, white, blue},
		3: {white, blue, green, red},
		4: {blue, white, red, green},
		5: {red, blue, green, white},
		6: {blue, red, white, green},
		7: {white, green, blue, red},
		8: {green, white, red, blue},
	} {
		r := process(t, e, withExif(base, o), Params{MaxWidth: 100, MaxHeight: 100, Format: PNG})
		wantW, wantH := 32, 16
		if o >= 5 {
			wantW, wantH = 16, 32
		}
		if r.Width != wantW || r.Height != wantH {
			t.Fatalf("orientation %d: %d×%d, want %d×%d", o, r.Width, r.Height, wantW, wantH)
		}
		img := decode(t, r)
		w, h := r.Width, r.Height
		for i, pt := range [4][2]int{{2, 2}, {w - 3, 2}, {2, h - 3}, {w - 3, h - 3}} {
			if !near(img, pt[0], pt[1], want[i]) {
				t.Errorf("orientation %d: corner %d is %v, want %v", o, i, img.At(pt[0], pt[1]), want[i])
			}
		}
	}
}

func TestOrientedBoxIsTheViewersBox(t *testing.T) {
	// A 200×100 stored image turned a quarter is 100×200 to the viewer: a 100-wide box
	// leaves it as it is, a 50-wide one halves it.
	e := New(Limits{})
	src := withExif(jpegBytes(t, quadrants(200, 100)), 6)
	if r := process(t, e, src, Params{MaxWidth: 50}); r.Width != 50 || r.Height != 100 {
		t.Fatalf("got %d×%d, want 50×100", r.Width, r.Height)
	}
}

func TestFormats(t *testing.T) {
	e := New(Limits{})
	rgba := quadrants(40, 20)
	translucent := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for i := range translucent.Pix {
		translucent.Pix[i] = 128
	}
	var g bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 40, 20), color.Palette{color.White, color.Black})
	gif.Encode(&g, pal, nil)
	webp, err := os.ReadFile("testdata/photo.lossy.webp") // from golang.org/x/image's testdata (BSD)
	if err != nil {
		t.Fatal(err)
	}
	gopher, err := os.ReadFile("testdata/gopher.lossless.webp")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, source, wantType string
		data                   []byte
		p                      Params
	}{
		{"jpeg stays jpeg", "jpeg", "image/jpeg", jpegBytes(t, rgba), Params{MaxWidth: 20}},
		{"png stays png", "png", "image/png", pngBytes(t, rgba), Params{MaxWidth: 20}},
		{"gif becomes png", "gif", "image/png", g.Bytes(), Params{MaxWidth: 20}},
		{"webp opaque becomes jpeg", "webp", "image/jpeg", webp, Params{MaxWidth: 50}},
		{"webp with transparency becomes png", "webp", "image/png", gopher, Params{MaxWidth: 50}},
		{"png to jpeg", "png", "image/jpeg", pngBytes(t, rgba), Params{MaxWidth: 20, Format: JPEG}},
		{"jpeg to png", "jpeg", "image/png", jpegBytes(t, rgba), Params{MaxWidth: 20, Format: PNG}},
		{"transparent png to jpeg", "png", "image/jpeg", pngBytes(t, translucent), Params{MaxWidth: 20, Format: JPEG}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Inspect(bytes.NewReader(tc.data))
			if err != nil || info.Format != tc.source {
				t.Fatalf("Inspect = %+v, %v; want format %s", info, err, tc.source)
			}
			r := process(t, e, tc.data, tc.p)
			if r.Type != tc.wantType {
				t.Fatalf("type %s, want %s", r.Type, tc.wantType)
			}
			decode(t, r)
		})
	}
}

func TestQuality(t *testing.T) {
	e := New(Limits{})
	noisy := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	for i := range noisy.Pix {
		noisy.Pix[i] = uint8(i * 7 % 251)
	}
	low := process(t, e, pngBytes(t, noisy), Params{MaxWidth: 200, Format: JPEG, Quality: 20})
	high := process(t, e, pngBytes(t, noisy), Params{MaxWidth: 200, Format: JPEG, Quality: 95})
	if len(low.Data) >= len(high.Data) {
		t.Fatalf("quality 20 is %d bytes, 95 is %d", len(low.Data), len(high.Data))
	}
}

func TestGifTakesTheFirstFrame(t *testing.T) {
	pal := color.Palette{color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}}
	a := image.NewPaletted(image.Rect(0, 0, 10, 10), pal)
	b := image.NewPaletted(image.Rect(0, 0, 10, 10), pal)
	for i := range b.Pix {
		b.Pix[i] = 1
	}
	var buf bytes.Buffer
	gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{1, 1}})
	r := process(t, New(Limits{}), buf.Bytes(), Params{MaxWidth: 10})
	if !near(decode(t, r), 5, 5, red) {
		t.Fatal("not the first frame")
	}
}

func TestRefusals(t *testing.T) {
	e := New(Limits{})
	for _, tc := range []struct {
		name   string
		data   []byte
		reason string
	}{
		{"text", []byte("hello, this is not an image at all"), ReasonNotAnImage},
		{"empty", nil, ReasonNotAnImage},
		{"pdf", []byte("%PDF-1.7\n1 0 obj"), ReasonNotAnImage},
		{"bmp", append([]byte("BM"), make([]byte, 60)...), ReasonUnsupportedFormat},
		{"tiff", append([]byte("II*\x00"), make([]byte, 60)...), ReasonUnsupportedFormat},
		{"avif", append([]byte("\x00\x00\x00\x18ftypavif"), make([]byte, 60)...), ReasonUnsupportedFormat},
		{"heic", append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 60)...), ReasonUnsupportedFormat},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`), ReasonUnsupportedFormat},
		{"truncated png", pngBytes(t, quadrants(50, 50))[:40], ReasonDecodeError},
		{"damaged jpeg", append(jpegBytes(t, quadrants(50, 50))[:200], bytes.Repeat([]byte{0x13}, 50)...), ReasonDecodeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.Process(context.Background(), bytes.NewReader(tc.data), 0, []Params{{MaxWidth: 10}})
			if got := ReasonOf(err); err == nil || got != tc.reason {
				t.Fatalf("error %v (%s), want %s", err, got, tc.reason)
			}
		})
	}
}

// A PNG header that claims a huge size, with no pixel data behind it: it can only be
// refused by the header, since decoding would fail (or allocate gigabytes).
func TestOversizedImageIsRefusedWithoutDecoding(t *testing.T) {
	w, h := 20000, 20000 // 400 MP
	hdr := []byte("\x89PNG\r\n\x1a\n")
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(w))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(h))
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	chunk := func(kind string, data []byte) []byte {
		c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
		c = append(c, kind...)
		c = append(c, data...)
		return binary.BigEndian.AppendUint32(c, crc32(append([]byte(kind), data...)))
	}
	data := append(hdr, chunk("IHDR", ihdr)...)

	e := New(Limits{MaxPixels: 40_000_000})
	_, err := e.Process(context.Background(), bytes.NewReader(data), 0, []Params{{MaxWidth: 10}})
	if ReasonOf(err) != ReasonTooLarge {
		t.Fatalf("error %v, want too_large", err)
	}
	info, err := Inspect(bytes.NewReader(data))
	if err != nil || info.Width != w || info.Height != h {
		t.Fatalf("Inspect = %+v, %v", info, err)
	}
}

func TestAFieldCanOnlyLowerThePixelLimit(t *testing.T) {
	e := New(Limits{MaxPixels: 1000})
	src := pngBytes(t, quadrants(40, 20)) // 800 pixels
	if _, err := e.Process(context.Background(), bytes.NewReader(src), 500, []Params{{MaxWidth: 10}}); ReasonOf(err) != ReasonTooLarge {
		t.Fatalf("a lower field limit: %v", err)
	}
	if _, err := e.Process(context.Background(), bytes.NewReader(src), 10_000_000, []Params{{MaxWidth: 10}}); err != nil {
		t.Fatalf("a higher field limit must not raise the operator's: %v", err)
	}
	big := pngBytes(t, quadrants(40, 40)) // 1600 pixels
	if _, err := e.Process(context.Background(), bytes.NewReader(big), 10_000_000, []Params{{MaxWidth: 10}}); ReasonOf(err) != ReasonTooLarge {
		t.Fatalf("the operator's limit stands: %v", err)
	}
}

func TestOneDecodeSeveralVersions(t *testing.T) {
	e := New(Limits{})
	out, err := e.Process(context.Background(), bytes.NewReader(pngBytes(t, quadrants(200, 100))), 0, []Params{
		{MaxWidth: 100, Format: JPEG}, {MaxWidth: 50, MaxHeight: 50, Fit: Cover, Format: PNG}, {MaxWidth: 20},
	})
	if err != nil || len(out) != 3 {
		t.Fatalf("%v, %d outcomes", err, len(out))
	}
	want := [][2]int{{100, 50}, {50, 50}, {20, 10}}
	for i, o := range out {
		if o.Err != nil || o.Result.Width != want[i][0] || o.Result.Height != want[i][1] {
			t.Errorf("version %d: %+v %v, want %v", i, o.Result, o.Err, want[i])
		}
	}
}

func TestInvalidParameters(t *testing.T) {
	e := New(Limits{})
	for _, p := range []Params{
		{}, {MaxWidth: -1}, {MaxWidth: 10, Fit: "zoom"}, {MaxWidth: 10, Quality: 101},
		{MaxWidth: 10, Quality: -1}, {MaxWidth: 10, Format: "webp"},
	} {
		_, err := e.Process(context.Background(), strings.NewReader("x"), 0, []Params{p})
		if ReasonOf(err) != ReasonInvalidParameters {
			t.Errorf("%+v: %v", p, err)
		}
	}
}

func TestOutputCarriesNoMetadata(t *testing.T) {
	e := New(Limits{})
	src := withExif(jpegBytes(t, quadrants(64, 64)), 1)
	if !bytes.Contains(src, []byte("SECRET-GPS-POSITION")) {
		t.Fatal("test setup: no metadata in the source")
	}
	for _, f := range []Format{JPEG, PNG} {
		r := process(t, e, src, Params{MaxWidth: 64, Format: f})
		for _, marker := range []string{"SECRET-GPS", "Exif", "http://ns.adobe.com", "eXIf", "tEXt", "iTXt"} {
			if bytes.Contains(r.Data, []byte(marker)) {
				t.Errorf("%s output contains %q", f, marker)
			}
		}
	}
}

func TestTimeout(t *testing.T) {
	e := New(Limits{Timeout: time.Nanosecond})
	start := time.Now()
	_, err := e.Process(context.Background(), bytes.NewReader(pngBytes(t, quadrants(2000, 2000))), 0, []Params{{MaxWidth: 10}})
	if ReasonOf(err) != ReasonTimeout {
		t.Fatalf("error %v, want timeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the timeout didn't end the call")
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	e := New(Limits{Concurrency: 2})
	// Fill both slots, then ask for a third with a short context: it must not get one.
	for i := 0; i < 2; i++ {
		e.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := e.Process(ctx, bytes.NewReader(pngBytes(t, quadrants(4, 4))), 0, []Params{{MaxWidth: 2}})
	if ReasonOf(err) != ReasonTimeout {
		t.Fatalf("a third image with both slots busy: %v", err)
	}
	<-e.slots
	if _, err := e.Process(context.Background(), bytes.NewReader(pngBytes(t, quadrants(4, 4))), 0, []Params{{MaxWidth: 2}}); err != nil {
		t.Fatal(err)
	}
	if got := e.Limits().Concurrency; got != 2 {
		t.Fatalf("Concurrency = %d", got)
	}
}

func TestDeriveConcurrency(t *testing.T) {
	const gib = 1 << 30
	for _, tc := range []struct {
		cpus      float64
		memory    int64
		maxPixels int64
		want      int
	}{
		{8, 0, 40_000_000, 4},         // memory unknown: half the CPUs
		{8, 16 * gib, 40_000_000, 4},  // plenty of memory
		{8, 1 * gib, 40_000_000, 3},   // 40 MP takes 160 MB: half of 1 GiB holds three
		{16, 2 * gib, 40_000_000, 6},  // memory is the limit
		{1, 16 * gib, 40_000_000, 1},  // at least one
		{0.5, 0, 40_000_000, 1},       // a fraction of a CPU
		{4, 100 << 20, 40_000_000, 1}, // memory for none: still one
		{8, 4 * gib, 10_000_000, 4},   // a lower pixel limit lets more run
	} {
		got := DeriveConcurrency(tc.cpus, tc.memory, tc.maxPixels)
		if got != tc.want {
			t.Errorf("DeriveConcurrency(%v, %d, %d) = %d, want %d", tc.cpus, tc.memory, tc.maxPixels, got, tc.want)
		}
	}
}

func TestContainerLimits(t *testing.T) {
	dir := t.TempDir()
	write := func(name, v string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v+"\n"), 0o644)
	}
	write("cpu.max", "150000 100000")
	write("memory.max", "2147483648")
	if cpus, mem := containerLimits(dir); cpus != 1.5 || mem != 2147483648 {
		t.Errorf("v2 = %v, %d", cpus, mem)
	}
	write("cpu.max", "max 100000")
	write("memory.max", "max")
	if cpus, mem := containerLimits(dir); cpus != 0 || mem != 0 {
		t.Errorf("unlimited = %v, %d", cpus, mem)
	}
	v1 := t.TempDir()
	os.MkdirAll(filepath.Join(v1, "cpu"), 0o755)
	os.MkdirAll(filepath.Join(v1, "memory"), 0o755)
	os.WriteFile(filepath.Join(v1, "cpu/cpu.cfs_quota_us"), []byte("200000\n"), 0o644)
	os.WriteFile(filepath.Join(v1, "cpu/cpu.cfs_period_us"), []byte("100000\n"), 0o644)
	os.WriteFile(filepath.Join(v1, "memory/memory.limit_in_bytes"), []byte("1073741824\n"), 0o644)
	if cpus, mem := containerLimits(v1); cpus != 2 || mem != 1073741824 {
		t.Errorf("v1 = %v, %d", cpus, mem)
	}
	if cpus, mem := containerLimits(filepath.Join(dir, "none")); cpus != 0 || mem != 0 {
		t.Errorf("missing = %v, %d", cpus, mem)
	}
}

func TestErrorType(t *testing.T) {
	err := error(&Error{ReasonTooLarge, "big"})
	var e *Error
	if !errors.As(err, &e) || e.Error() != "too_large: big" || ReasonOf(errors.New("x")) != ReasonDecodeError {
		t.Fatal("Error / ReasonOf")
	}
}

func crc32(b []byte) uint32 { return crc.ChecksumIEEE(b) }
