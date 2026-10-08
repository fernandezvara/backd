package httpapi

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/fernandezvara/backd/internal/imaging"
	"github.com/fernandezvara/backd/internal/jsonnum"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// imageHeadBytes is how much of a file's start is read to learn an image's size: a
// JPEG's dimensions come after its metadata segments, which can be long.
const imageHeadBytes = 256 << 10

// Version statuses recorded in a file's details (design/image-versions.md §4).
const (
	versionPending = "pending" // waits for a worker
	versionEmpty   = "empty"   // only functions make it, and none has yet
	versionSkipped = "skipped" // the file isn't an image the engine reads
	versionReady   = "ready"
	versionFailed  = "failed"
)

// readsImages reports whether the field can hold a picture the engine might read,
// so that its upload keeps the start of the file to find the size.
func readsImages(f *registry.FileField) bool {
	for _, t := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
		if f.Allows(t) {
			return true
		}
	}
	return false
}

// imageDetails is what a stored file's details gain when it is an image: its pixel
// size (as a viewer sees it) and the state of each declared version. head is the
// start of the file. instanceMax is BACKD_IMAGE_MAX_PIXELS.
func imageDetails(f *registry.FileField, head []byte, instanceMax int64) map[string]any {
	out := map[string]any{}
	info, err := imaging.Inspect(bytes.NewReader(head))
	if err == nil {
		w, h := info.Width, info.Height
		if info.Orientation >= 5 {
			w, h = h, w
		}
		out["width"], out["height"] = int64(w), int64(h)
	}
	if len(f.Versions) == 0 {
		return out
	}
	state := func(v registry.Version) map[string]any {
		switch {
		case err != nil && imaging.ReasonOf(err) != imaging.ReasonDecodeError:
			return map[string]any{"status": versionSkipped, "reason": imaging.ReasonOf(err)}
		case err != nil:
			return map[string]any{"status": versionFailed, "reason": imaging.ReasonDecodeError}
		case info.Pixels() > limitOf(f, instanceMax):
			return map[string]any{"status": versionFailed, "reason": imaging.ReasonTooLarge}
		case v.HasParams():
			return map[string]any{"status": versionPending}
		}
		return map[string]any{"status": versionEmpty}
	}
	versions := make(map[string]any, len(f.Versions))
	for _, v := range f.Versions {
		versions[v.Name] = state(v)
	}
	out["versions"] = versions
	return out
}

// limitOf is the pixel limit for a field: its own max_pixels, which may only be lower
// than the instance's.
func limitOf(f *registry.FileField, instanceMax int64) int64 {
	limit := cmp.Or(instanceMax, imaging.DefaultMaxPixels)
	if f.MaxPixels > 0 && f.MaxPixels < limit {
		return f.MaxPixels
	}
	return limit
}

// imageJSON is the details as the journal keeps them for a pending upload.
func imageJSON(details map[string]any) string {
	if len(details) == 0 {
		return ""
	}
	b, _ := json.Marshal(details)
	return string(b)
}

// withImage adds the image details a pending upload kept to the file's details.
func withImage(meta map[string]any, stored string) map[string]any {
	if stored == "" {
		return meta
	}
	dec := json.NewDecoder(strings.NewReader(stored))
	dec.UseNumber()
	var extra map[string]any
	if dec.Decode(&extra) != nil {
		return meta
	}
	for k, v := range jsonnum.Normalize(extra).(map[string]any) {
		meta[k] = v
	}
	return meta
}

// metadataFormat is the format whose metadata is removed from a file of the content type:
// "jpeg" or "png", "" for anything else, which is stored as it was sent.
func metadataFormat(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return "jpeg"
	case "image/png":
		return "png"
	}
	return ""
}

// errUploadChanged: the object read to be cleaned is not the one whose checksum was verified.
var errUploadChanged = errors.New("the object changed after it was verified")

// rewriteWithoutMetadata reads the object under key, writes it back to the same key without
// its metadata (a streaming copy, multipart when large) and returns the size and SHA-256 of
// what is stored. The bytes read must be the ones whose checksum was verified (wantSHA): the
// signed link that wrote them is valid until it expires, so the object could have been
// replaced since.
func rewriteWithoutMetadata(ctx context.Context, obj *storage.Objects, key, format, contentType, wantSHA string) (size int64, sum string, err error) {
	rc, _, err := obj.Get(ctx, key, "")
	if err != nil {
		return 0, "", err
	}
	defer rc.Close()
	read := sha256.New()
	in := io.TeeReader(rc, read)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		e := imaging.Strip(pw, in, format)
		pw.CloseWithError(e)
		if e == nil { // what follows the end of the image (dropped) was part of the verified file
			_, e = io.Copy(io.Discard, in)
		}
		done <- e
	}()
	written := sha256.New()
	put := obj.PutStream(ctx, key, io.TeeReader(&countingReader{r: pr, n: &size}, written), contentType)
	_ = pr.Close()
	serr := <-done
	switch {
	case errors.Is(serr, imaging.ErrUnreadable):
		return 0, "", serr
	case put != nil:
		return 0, "", put
	case serr != nil:
		return 0, "", serr
	case hex.EncodeToString(read.Sum(nil)) != wantSHA:
		return 0, "", errUploadChanged
	}
	return size, hex.EncodeToString(written.Sum(nil)), nil
}
