package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Multipart uploads, for direct uploads above what one signed PUT can carry. The client
// sends each part to a link backd signed with that part's SHA-256 in it, so the storage
// rejects a part that differs; backd then completes the upload and checks the object's
// composite checksum, the one S3 makes of the parts' checksums, against the one it
// computes from what the client declared. The bytes never pass through backd.

// ErrNoSuchUpload means the multipart upload does not exist (any more): it was completed,
// aborted, or never made.
var ErrNoSuchUpload = errors.New("storage: no such multipart upload")

// Part is one uploaded part of a multipart upload.
type Part struct {
	Number int32
	ETag   string
	Size   int64
	SHA256 string // hex
}

// CreateMultipart starts a multipart upload of key whose parts carry a SHA-256, and
// returns its id.
func (o *Objects) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), ChecksumAlgorithm: types.ChecksumAlgorithmSha256}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	out, err := o.client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", mapError(err)
	}
	return aws.ToString(out.UploadId), nil
}

// PresignUploadPart signs the upload of part number of the multipart upload, valid for
// ttl, for exactly size bytes with the given SHA-256 (hex): the storage rejects a part
// that differs. It makes no request.
func (o *Objects) PresignUploadPart(ctx context.Context, key, uploadID string, number int32, ttl time.Duration, size int64, sha256hex string) (Link, error) {
	sum, err := b64(sha256hex)
	if err != nil {
		return Link{}, err
	}
	in := &s3.UploadPartInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(number), ContentLength: aws.Int64(size), ChecksumSHA256: sum}
	now := time.Now()
	out, err := o.presign.PresignUploadPart(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return Link{}, err
	}
	return link(out.URL, out.SignedHeader, ttl, now), nil
}

// ListParts returns the parts uploaded so far, in order. ErrNoSuchUpload when the upload
// is gone.
func (o *Objects) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	var out []Part
	var marker *string
	for {
		page, err := o.client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumberMarker: marker, MaxParts: aws.Int32(1000)})
		if err != nil {
			return nil, mapError(err)
		}
		for _, p := range page.Parts {
			out = append(out, Part{Number: aws.ToInt32(p.PartNumber), ETag: aws.ToString(p.ETag), Size: aws.ToInt64(p.Size), SHA256: fromB64(p.ChecksumSHA256)})
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		marker = page.NextPartNumberMarker
	}
}

// CompleteMultipart assembles the parts into the object. The storage checks that each
// part's SHA-256 is the one it recorded.
func (o *Objects) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	done := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		sum, err := b64(p.SHA256)
		if err != nil {
			return err
		}
		done[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag), ChecksumSHA256: sum}
	}
	_, err := o.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: done},
	})
	return mapError(err)
}

// AbortMultipart drops the multipart upload and the parts it holds. One that is gone is
// not an error.
func (o *Objects) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := o.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	if err = mapError(err); errors.Is(err, ErrNoSuchUpload) {
		return nil
	}
	return err
}

// CompositeSHA256 is what a storage reports as the checksum of an object made of parts
// with the given SHA-256 digests (hex): the SHA-256 of the digests joined, in base64,
// then "-" and the number of parts. The second result is the same digest in hex, which
// backd records as the file's sha256.
func CompositeSHA256(partSHA256 []string) (reported, hexDigest string, err error) {
	h := sha256.New()
	for _, p := range partSHA256 {
		raw, err := hex.DecodeString(p)
		if err != nil || len(raw) != sha256.Size {
			return "", "", fmt.Errorf("part sha256 must be 64 hex characters, got %q", p)
		}
		h.Write(raw)
	}
	sum := h.Sum(nil)
	return base64.StdEncoding.EncodeToString(sum) + "-" + strconv.Itoa(len(partSHA256)), hex.EncodeToString(sum), nil
}

// IsCompositeOf reports whether an object's checksum as the storage returns it
// ("<base64>-<parts>") is the composite of the given part digests.
func IsCompositeOf(reported string, partSHA256 []string) bool {
	want, _, err := CompositeSHA256(partSHA256)
	return err == nil && strings.TrimSpace(reported) == want
}
