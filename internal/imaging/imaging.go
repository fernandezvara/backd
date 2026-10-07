// Package imaging turns one image into resized copies, safely: pure Go (no cgo,
// no external library), the pixel count checked from the header before anything
// is decoded, the orientation flag applied, a time limit per image, and output
// that never carries metadata.
//
// It reads JPEG, PNG, GIF (the first frame) and WebP, and writes JPEG and PNG.
// WebP output is not offered: the pure-Go encoders are lossless only, and a
// lossless WebP of a photograph is about eight times the size of the JPEG.
//
// An Engine decodes an image once and renders every version asked of it.
package imaging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"time"

	_ "image/gif" // registered for DecodeConfig and Decode
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// Reasons an image could not be made, as the version statuses will name them.
const (
	ReasonNotAnImage        = "not_an_image"
	ReasonUnsupportedFormat = "unsupported_format"
	ReasonTooLarge          = "too_large"
	ReasonDecodeError       = "decode_error"
	ReasonTimeout           = "timeout"
	ReasonInvalidParameters = "invalid_parameters"
)

// DefaultMaxPixels is the pixel limit when none is set.
const DefaultMaxPixels int64 = 40_000_000

const defaultTimeout = 30 * time.Second

// Error is why an image could not be read or made; Reason is one of the Reason constants.
type Error struct {
	Reason string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Reason
	}
	return e.Reason + ": " + e.Detail
}

// ReasonOf returns the Reason of an *Error, or ReasonDecodeError for any other error.
func ReasonOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonDecodeError
}

// Info is what an image's header says, read without decoding it.
type Info struct {
	Format      string // jpeg, png, gif or webp
	Width       int
	Height      int
	Orientation int // EXIF orientation, 1 to 8 (1: as stored)
}

// Pixels is Width × Height.
func (i Info) Pixels() int64 { return int64(i.Width) * int64(i.Height) }

// Limits bound the work done for one image.
type Limits struct {
	// MaxPixels is the most pixels an image may have (width × height), checked
	// from the header before decoding. Zero means DefaultMaxPixels.
	MaxPixels int64
	// Timeout bounds the whole of one image: decoding and every version. Zero
	// means 30 seconds.
	Timeout time.Duration
	// Concurrency is how many images one Engine processes at the same time.
	// Zero or less means 1.
	Concurrency int
}

func (l Limits) maxPixels() int64 {
	if l.MaxPixels <= 0 {
		return DefaultMaxPixels
	}
	return l.MaxPixels
}

func (l Limits) timeout() time.Duration {
	if l.Timeout <= 0 {
		return defaultTimeout
	}
	return l.Timeout
}

// Inspect reads the image's format, dimensions and orientation from its header,
// without decoding it. It fails with ReasonNotAnImage for content that isn't an
// image, ReasonUnsupportedFormat for an image format it doesn't read (BMP, TIFF,
// HEIC, AVIF, SVG, …) and ReasonDecodeError for a damaged header. The reader is
// left at the start.
func Inspect(r io.ReadSeeker) (Info, error) {
	head := make([]byte, 64)
	n, _ := io.ReadFull(r, head)
	head = head[:n]
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Info{}, err
	}
	if other := otherFormat(head); other != "" {
		return Info{}, &Error{ReasonUnsupportedFormat, other}
	}
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			return Info{}, &Error{Reason: ReasonNotAnImage}
		}
		return Info{}, &Error{ReasonDecodeError, "the header can't be read"}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Info{}, &Error{ReasonDecodeError, "the image has no size"}
	}
	info := Info{Format: format, Width: cfg.Width, Height: cfg.Height, Orientation: 1}
	if format == "jpeg" {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return Info{}, err
		}
		info.Orientation = jpegOrientation(r)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Info{}, err
	}
	return info, nil
}

// otherFormat names an image (or image-like) format this package doesn't read,
// recognised by its first bytes; empty when it isn't one.
func otherFormat(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("BM")):
		return "bmp"
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		return "tiff"
	case bytes.HasPrefix(b, []byte("\x00\x00\x01\x00")):
		return "ico"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		switch string(b[8:12]) {
		case "avif", "avis":
			return "avif"
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return "heic"
		}
	case bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("<svg")), bytes.HasPrefix(b, []byte("<?xml")):
		return "svg"
	case bytes.HasPrefix(b, []byte("\xff\x4f\xff\x51")), bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cjP  ")):
		return "jpeg2000"
	}
	return ""
}

// Engine makes the versions of images, at most Limits.Concurrency at a time.
type Engine struct {
	limits Limits
	slots  chan struct{}
}

// New returns an Engine working within the limits.
func New(l Limits) *Engine {
	n := l.Concurrency
	if n < 1 {
		n = 1
	}
	return &Engine{limits: l, slots: make(chan struct{}, n)}
}

// Limits returns the limits the Engine works within, with the defaults filled in.
func (e *Engine) Limits() Limits {
	return Limits{MaxPixels: e.limits.maxPixels(), Timeout: e.limits.timeout(), Concurrency: cap(e.slots)}
}

// Outcome is one version of a Process call: a Result, or why it couldn't be made.
type Outcome struct {
	Result *Result
	Err    error
}

// Process reads one image and makes a version for each of the parameter sets, in
// order, decoding the image once. maxPixels, when above zero, lowers the Engine's
// pixel limit for this image (a field may only lower the operator's limit).
//
// The error is for the image itself (not an image, too large, damaged, timed out);
// each version has its own Outcome. Parameters that don't validate are an error
// before anything is read.
func (e *Engine) Process(ctx context.Context, src io.ReadSeeker, maxPixels int64, versions []Params) ([]Outcome, error) {
	for _, p := range versions {
		if err := p.Validate(); err != nil {
			return nil, err
		}
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return nil, &Error{ReasonTimeout, "waiting for a free slot"}
	}
	ctx, cancel := context.WithTimeout(ctx, e.limits.timeout())
	defer cancel()

	info, err := Inspect(src)
	if err != nil {
		return nil, err
	}
	limit := e.limits.maxPixels()
	if maxPixels > 0 && maxPixels < limit {
		limit = maxPixels
	}
	if info.Pixels() > limit {
		return nil, &Error{ReasonTooLarge, fmt.Sprintf("%d×%d is %d pixels, over the limit of %d", info.Width, info.Height, info.Pixels(), limit)}
	}

	type done struct {
		out []Outcome
		err error
	}
	ch := make(chan done, 1)
	go func() {
		// The work can't be interrupted, so it runs on its own and is abandoned at the
		// timeout; a panic in a decoder must not take the process with it.
		defer func() {
			if recover() != nil {
				ch <- done{err: &Error{ReasonDecodeError, "the image could not be processed"}}
			}
		}()
		img, _, err := image.Decode(src)
		if err != nil {
			ch <- done{err: &Error{ReasonDecodeError, "the image can't be decoded"}}
			return
		}
		out := make([]Outcome, len(versions))
		for i, p := range versions {
			if ctx.Err() != nil {
				out[i].Err = &Error{Reason: ReasonTimeout}
				continue
			}
			res, err := render(img, info, p)
			out[i] = Outcome{Result: res, Err: err}
		}
		ch <- done{out: out}
	}()
	select {
	case d := <-ch:
		return d.out, d.err
	case <-ctx.Done():
		return nil, &Error{ReasonTimeout, fmt.Sprintf("over %s", e.limits.timeout())}
	}
}
