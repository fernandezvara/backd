package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Multipart direct uploads: a file above what one signed PUT takes (5 GiB) is sent to the
// bucket in parts, each to its own signed link. The client declares the SHA-256 of every
// part at the start and each is signed into its link, so storage refuses a part that
// differs. Storage can't compute one SHA-256 of a multipart object, only a composite (the
// SHA-256 of the parts' digests, with the number of parts); backd checks that composite at
// the end, and records its digest as the file's sha256. The file is therefore verified
// exactly as a single PUT's is, but its checksum is not the SHA-256 of its bytes.

var (
	// multipartPartMin and multipartPartStep shape the part size: at least 64 MiB, and a
	// multiple of 16 MiB when the file needs more to stay within multipartMaxParts parts.
	multipartPartMin  int64 = 64 << 20
	multipartPartStep int64 = 16 << 20
	multipartMaxParts int64 = 1000
	// multipartAbove, when set, replaces the provider's single-PUT limit (tests).
	multipartAbove int64
	// multipartMinTTL is how long a multipart upload (and its links) lasts at least: a file
	// this large takes a while.
	multipartMinTTL = 24 * time.Hour
	maxLinkTTL      = 7 * 24 * time.Hour
)

// maxMultipartStartBody is the start call's body limit: it carries a digest per part.
const maxMultipartStartBody = 128 << 10

// multipartThreshold is the size above which a direct upload to the provider is multipart;
// 0 when the provider can't take one.
func multipartThreshold(p storage.Provider) int64 {
	if !p.SupportsMultipartUploads() {
		return 0
	}
	if multipartAbove > 0 {
		return multipartAbove
	}
	return p.MaxSinglePut
}

// PartSize is the size of the parts of a file of size bytes.
func partSizeFor(size int64) int64 {
	need := (size + multipartMaxParts - 1) / multipartMaxParts
	need = (need + multipartPartStep - 1) / multipartPartStep * multipartPartStep
	return max(multipartPartMin, need)
}

func partCount(size, partSize int64) int64 { return (size + partSize - 1) / partSize }

// multipartInfo is what a start call knows of a multipart upload.
type multipartInfo struct {
	partSize int64
	parts    []string // the declared SHA-256 of each part
}

// presignParts creates the storage's multipart upload and signs a link for every part.
func presignParts(r *http.Request, obj *storage.Objects, key, contentType string, size int64, m multipartInfo, ttl time.Duration) (uploadID string, links []map[string]any, linkExpires time.Time, err error) {
	ctx := r.Context()
	uploadID, err = obj.CreateMultipart(ctx, key, contentType)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	for i, sum := range m.parts {
		partLen := min(m.partSize, size-int64(i)*m.partSize)
		l, err := obj.PresignUploadPart(ctx, key, uploadID, int32(i+1), ttl, partLen, sum)
		if err != nil {
			_ = obj.AbortMultipart(ctx, key, uploadID)
			return "", nil, time.Time{}, err
		}
		linkExpires = l.ExpiresAt
		links = append(links, map[string]any{"number": i + 1, "url": l.URL, "headers": l.Headers, "size": partLen})
	}
	return uploadID, links, linkExpires, nil
}

// multipartTTL is how long a multipart upload may take: the realm's pending_ttl, at least a day,
// and no more than a link can last.
func multipartTTL(ttl time.Duration) time.Duration { return min(max(ttl, multipartMinTTL), maxLinkTTL) }

// checkMultipartStart validates the part digests of a start call for a file of size.
func checkMultipartStart(in *directStart) []Detail {
	in.partSize = partSizeFor(*in.Size)
	n := partCount(*in.Size, in.partSize)
	var details []Detail
	if int64(len(in.PartSHA256)) != n {
		return []Detail{{Path: "part_sha256", Reason: "is required: " + strconv.FormatInt(n, 10) + " digests, the SHA-256 of each consecutive part of " + strconv.FormatInt(in.partSize, 10) + " bytes (the last one shorter)"}}
	}
	for i, s := range in.PartSHA256 {
		if !sha256Hex(s) {
			details = append(details, Detail{Path: "part_sha256[" + strconv.Itoa(i) + "]", Reason: "must be 64 lower-case hex characters"})
		}
	}
	if len(details) == 0 {
		_, in.SHA256, _ = storage.CompositeSHA256(in.PartSHA256)
	}
	return details
}

// multipartMine fills a journal entry with the multipart details of a start.
func (in directStart) multipartEntry(e *auth.FileJournalEntry, uploadID string) {
	if uploadID != "" {
		e.MultipartID, e.PartSize, e.PartSHA256 = uploadID, in.partSize, in.PartSHA256
	}
}

// beginMultipart creates the storage's upload for a start call that is one. The reply's links
// are kept in in.links.
func (d *documents) beginMultipart(w http.ResponseWriter, r *http.Request, c *registry.Collection, obj *storage.Objects, key string, in *directStart, ttl time.Duration) (string, bool) {
	if !in.multipart {
		return "", true
	}
	id, links, expires, err := presignParts(r, obj, key, in.Type, *in.Size, multipartInfo{in.partSize, in.PartSHA256}, multipartTTL(ttl))
	if err != nil {
		logger(r.Context()).Error("start a multipart upload", "realm", c.Realm, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		return "", false
	}
	in.links, in.linksExpire = links, expires
	return id, true
}

func multipartTTLIf(multipart bool, ttl time.Duration) time.Duration {
	if multipart {
		return multipartTTL(ttl)
	}
	return ttl
}

// assembleParts is the first step of completing a multipart upload: it checks that every
// part is in the bucket as declared and has storage assemble them. It answers and returns
// false when the upload can't go on. An upload storage no longer knows is taken as
// assembled by an earlier attempt: the checks of the object that follow decide.
func (d *documents) assembleParts(ctx context.Context, w http.ResponseWriter, r *http.Request, obj *storage.Objects, e auth.FileJournalEntry, retry func(), reject func(int, string, string)) bool {
	unavailable := func(what string, err error) bool {
		retry()
		logger(r.Context()).Error(what, "error", err)
		writeError(w, r, http.StatusServiceUnavailable, codeStorageUnavailable, "file storage is not available")
		return false
	}
	got, err := obj.ListParts(ctx, e.Key, e.MultipartID)
	switch {
	case errors.Is(err, storage.ErrNoSuchUpload):
		return true
	case err != nil:
		return unavailable("list the parts of a multipart upload", err)
	}
	n := len(e.PartSHA256)
	var missing []int
	byNumber := map[int32]storage.Part{}
	for _, p := range got {
		byNumber[p.Number] = p
	}
	for i := 1; i <= n; i++ {
		p, ok := byNumber[int32(i)]
		if !ok {
			missing = append(missing, i)
			continue
		}
		partLen := min(e.PartSize, e.Size-int64(i-1)*e.PartSize)
		if p.Size != partLen || p.SHA256 != e.PartSHA256[i-1] {
			reject(http.StatusUnprocessableEntity, codeUploadMismatch, "part "+strconv.Itoa(i)+" is not the declared one")
			return false
		}
	}
	if len(got) > n {
		reject(http.StatusUnprocessableEntity, codeUploadMismatch, "more parts were uploaded than declared")
		return false
	}
	if len(missing) > 0 {
		retry()
		writeError(w, r, http.StatusConflict, codeFileNotUploaded, strconv.Itoa(len(missing))+" of "+strconv.Itoa(n)+" parts have not been uploaded yet (the first is part "+strconv.Itoa(missing[0])+"): PUT them, then complete")
		return false
	}
	if err := obj.CompleteMultipart(ctx, e.Key, e.MultipartID, got); err != nil && !errors.Is(err, storage.ErrNoSuchUpload) {
		return unavailable("assemble a multipart upload", err)
	}
	return true
}
