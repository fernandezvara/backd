package imaging

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Metadata removal. A photograph carries more than its pixels: EXIF (the camera, the time,
// often the GPS position), XMP, IPTC, comments; a PNG can carry text chunks and a time. Strip
// copies an image without any of it and without touching a single pixel: the compressed
// image data is copied byte for byte, only the segments around it are left out. What
// determines how the picture looks stays: for a JPEG its tables, frame, scans, JFIF header,
// ICC colour profile and Adobe colour marker, plus a minimal EXIF holding only the orientation
// flag, so that the original still displays upright; for a PNG its critical chunks, colour
// information (gamma, chromaticities, sRGB, ICC profile), transparency, the animation chunks
// and the pixel density.

// ErrUnreadable means the data is not an image of the format that can be copied: a damaged
// file. Strip fails closed on it rather than pass the metadata through.
var ErrUnreadable = errors.New("imaging: the image can't be read to remove its metadata")

// Strip copies the JPEG or PNG in src to dst without its metadata, as described above. format
// is "jpeg" or "png" (Info.Format); any other is an error. Data after the end of a JPEG (an
// embedded second image, a trailer) is dropped. dst gets the stream as it goes; on an error
// what was written is incomplete.
func Strip(dst io.Writer, src io.Reader, format string) error {
	in := &failedRead{r: src}
	err := copyStripped(dst, in, format)
	if in.err != nil && !errors.Is(in.err, io.EOF) {
		return in.err // the source failed (a size limit, a broken connection): that is the error
	}
	return err
}

// failedRead remembers the error a reader gave, so a failure of the source isn't taken for a
// damaged image.
type failedRead struct {
	r   io.Reader
	err error
}

func (f *failedRead) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && f.err == nil {
		f.err = err
	}
	return n, err
}

func copyStripped(dst io.Writer, src io.Reader, format string) error {
	br := bufio.NewReaderSize(src, 64<<10)
	bw := bufio.NewWriterSize(dst, 64<<10)
	var err error
	switch format {
	case "jpeg":
		err = stripJPEG(bw, br)
	case "png":
		err = stripPNG(bw, br)
	default:
		return fmt.Errorf("imaging: metadata of %q can't be removed", format)
	}
	if err != nil {
		return err
	}
	return bw.Flush()
}

func unreadable(why string) error { return fmt.Errorf("%w: %s", ErrUnreadable, why) }

// stripJPEG walks the marker segments.
func stripJPEG(w *bufio.Writer, r *bufio.Reader) error {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi != [2]byte{0xff, 0xd8} {
		return unreadable("not a JPEG")
	}
	_, _ = w.Write(soi[:])
	orientation := 1
	// The minimal EXIF goes before the first table or frame, by when every application
	// segment (where the real EXIF is) has been seen.
	wroteExif := false
	writeExif := func() {
		if wroteExif {
			return
		}
		wroteExif = true
		if orientation != 1 {
			_, _ = w.Write(exifOrientationSegment(orientation))
		}
	}
	for {
		code, err := nextMarker(r)
		if err != nil {
			return unreadable("the markers end before the image does")
		}
		switch {
		case code == 0xd9: // EOI: the end; anything after is dropped
			writeExif()
			_, _ = w.Write([]byte{0xff, 0xd9})
			return nil
		case code == 0x01 || (code >= 0xd0 && code <= 0xd8): // markers without a length
			_, _ = w.Write([]byte{0xff, code})
			continue
		}
		var l [2]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return unreadable("a segment is cut short")
		}
		n := int(binary.BigEndian.Uint16(l[:])) - 2
		if n < 0 {
			return unreadable("a segment has no length")
		}
		switch {
		case code == 0xe1 || (code >= 0xe3 && code <= 0xed) || code == 0xef || code == 0xfe: // EXIF/XMP, IPTC, other application data, comments
			seg := make([]byte, n)
			if _, err := io.ReadFull(r, seg); err != nil {
				return unreadable("a segment is cut short")
			}
			if code == 0xe1 && len(seg) >= 14 && string(seg[:6]) == "Exif\x00\x00" {
				if o := tiffOrientation(seg[6:]); o != 1 {
					orientation = o
				}
			}
		case code == 0xe0 || code == 0xee || (code == 0xe2): // JFIF; Adobe colour transform; APP2 (kept only for an ICC profile)
			seg := make([]byte, n)
			if _, err := io.ReadFull(r, seg); err != nil {
				return unreadable("a segment is cut short")
			}
			if code == 0xe2 && !(len(seg) >= 12 && string(seg[:12]) == "ICC_PROFILE\x00") {
				continue // another APP2: FlashPix, MPF
			}
			_, _ = w.Write([]byte{0xff, code})
			_, _ = w.Write(l[:])
			_, _ = w.Write(seg)
		default: // tables, frame headers, scan headers: kept as they are
			writeExif()
			_, _ = w.Write([]byte{0xff, code})
			_, _ = w.Write(l[:])
			if _, err := io.CopyN(w, r, int64(n)); err != nil {
				return unreadable("a segment is cut short")
			}
			if code == 0xda { // a scan: its entropy-coded data follows, up to the next marker
				if err := copyScan(w, r); err != nil {
					return err
				}
			}
		}
	}
}

// nextMarker reads up to the next marker (0xff and its code), skipping fill bytes.
func nextMarker(r *bufio.Reader) (byte, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if b != 0xff {
		return 0, unreadable("data where a marker should be")
	}
	for {
		c, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if c != 0xff {
			return c, nil
		}
	}
}

// copyScan copies entropy-coded data, which has no segments, up to (not including) the next
// marker that is not a restart marker or a stuffed 0xff00. The marker is left unread.
func copyScan(w *bufio.Writer, r *bufio.Reader) error {
	for {
		p, err := r.Peek(2)
		if err != nil {
			return unreadable("the image data is cut short")
		}
		if p[0] != 0xff {
			b, _ := r.ReadByte()
			_ = w.WriteByte(b)
			continue
		}
		switch c := p[1]; {
		case c == 0xff: // a fill byte before a marker: look again at the second
			_ = w.WriteByte(0xff)
			_, _ = r.Discard(1)
		case c == 0x00 || (c >= 0xd0 && c <= 0xd7): // a stuffed byte, or a restart marker
			_ = w.WriteByte(0xff)
			_ = w.WriteByte(c)
			_, _ = r.Discard(2)
		default:
			return nil
		}
	}
}

// exifOrientationSegment is an APP1 segment holding an EXIF block with the orientation tag
// alone.
func exifOrientationSegment(o int) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, uint16(o))
	tiff = append(tiff, 0, 0)       // the value field is 4 bytes
	tiff = append(tiff, 0, 0, 0, 0) // no next IFD
	body := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	return append(seg, body...)
}

// pngKept are the chunks (besides the critical ones, which start with a capital letter) that
// change how the picture looks or plays, or describe its density; all others are dropped.
var pngKept = map[string]bool{
	"tRNS": true, "gAMA": true, "cHRM": true, "sRGB": true, "iCCP": true, "sBIT": true, "bKGD": true, "hIST": true,
	"pHYs": true, "sPLT": true, "cICP": true, "mDCV": true, "cLLI": true, "acTL": true, "fcTL": true, "fdAT": true,
}

func stripPNG(w *bufio.Writer, r *bufio.Reader) error {
	sig := make([]byte, 8)
	if _, err := io.ReadFull(r, sig); err != nil || string(sig) != "\x89PNG\r\n\x1a\n" {
		return unreadable("not a PNG")
	}
	_, _ = w.Write(sig)
	for {
		var head [8]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return unreadable("the chunks end before the image does")
		}
		n := int64(binary.BigEndian.Uint32(head[:4]))
		kind := string(head[4:8])
		critical := kind[0] >= 'A' && kind[0] <= 'Z'
		if critical || pngKept[kind] {
			_, _ = w.Write(head[:])
			if _, err := io.CopyN(w, r, n+4); err != nil { // the data and its CRC
				return unreadable("a chunk is cut short")
			}
		} else if _, err := io.CopyN(io.Discard, r, n+4); err != nil {
			return unreadable("a chunk is cut short")
		}
		if kind == "IEND" {
			return nil
		}
	}
}
