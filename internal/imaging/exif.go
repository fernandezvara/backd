package imaging

import (
	"bufio"
	"encoding/binary"
	"io"
)

// jpegOrientation reads the EXIF orientation (1 to 8) of a JPEG by walking its
// segments up to the first APP1 "Exif" block; 1 when there is none or it can't be read.
func jpegOrientation(r io.Reader) int {
	br := bufio.NewReader(r)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xff, 0xd8} {
		return 1
	}
	for {
		b, err := br.ReadByte()
		if err != nil || b != 0xff {
			return 1
		}
		code, err := br.ReadByte()
		for err == nil && code == 0xff { // fill bytes
			code, err = br.ReadByte()
		}
		if err != nil || code == 0xda || code == 0xd9 { // start of scan, end of image
			return 1
		}
		if code == 0x01 || (code >= 0xd0 && code <= 0xd8) { // markers without a length
			continue
		}
		var l [2]byte
		if _, err := io.ReadFull(br, l[:]); err != nil {
			return 1
		}
		n := int(binary.BigEndian.Uint16(l[:])) - 2
		if n < 0 {
			return 1
		}
		if code != 0xe1 || n < 14 || n > 1<<16 {
			if _, err := br.Discard(n); err != nil {
				return 1
			}
			continue
		}
		seg := make([]byte, n)
		if _, err := io.ReadFull(br, seg); err != nil {
			return 1
		}
		if string(seg[:6]) != "Exif\x00\x00" {
			continue
		}
		return tiffOrientation(seg[6:])
	}
}

// tiffOrientation finds tag 0x0112 in the first IFD of an EXIF TIFF block.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[off : off+2]))
	for i := 0; i < count; i++ {
		e := off + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) != 0x0112 {
			continue
		}
		if v := int(bo.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}
