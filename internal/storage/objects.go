package storage

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ObjectsConfig is a realm's connection to its object storage, as registry.StorageSettings
// resolves it, with the keys read from the realm's secrets.
type ObjectsConfig struct {
	Provider       string
	Endpoint       string
	PublicEndpoint string // "" unless the provider allows it; only signed into links
	Region         string
	Bucket         string
	Prefix         string
	AccessKey      string
	SecretKey      string
}

// Objects stores and reads a realm's files in its bucket: only operations every
// supported provider has, all signed with AWS Signature Version 4.
type Objects struct {
	cfg      ObjectsConfig
	provider Provider
	client   *s3.Client
	presign  *s3.PresignClient
}

// Errors the object operations map the storage's answers to.
var (
	ErrObjectNotFound   = errors.New("object not found")
	ErrAccessDenied     = errors.New("the storage refused the credentials or the operation")
	ErrBucketNotFound   = errors.New("the bucket doesn't exist")
	ErrChecksumMismatch = errors.New("the storage rejected the content: its checksum doesn't match")
)

// NewObjects connects to the storage a realm declares. It makes no request.
func NewObjects(cfg ObjectsConfig) (*Objects, error) {
	p, ok := LookupProvider(cfg.Provider)
	if !ok {
		return nil, fmt.Errorf("storage provider %q isn't supported", cfg.Provider)
	}
	hc, err := NewHTTPClient(cfg.Endpoint, cfg.Bucket, p.Addressing)
	if err != nil {
		return nil, err
	}
	options := func(endpoint string, hc aws.HTTPClient) s3.Options {
		o := s3.Options{
			Region:       cfg.Region,
			BaseEndpoint: aws.String(endpoint),
			UsePathStyle: p.Addressing == PathStyle,
			Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
			HTTPClient:   hc,
			// Recent SDKs add CRC checksums to every upload, which some S3-compatible
			// services reject: ask for a checksum only where an operation needs one.
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
			RetryMaxAttempts:           3,
		}
		if !p.ChecksumsWhenRequired {
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenSupported
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenSupported
		}
		return o
	}
	client := s3.New(options(cfg.Endpoint, hc))
	// Signing is local: links for browsers are signed for the public address, which
	// backd never connects to (its client has no HTTP transport at all).
	signFor := client
	if cfg.PublicEndpoint != "" {
		signFor = s3.New(options(cfg.PublicEndpoint, noNetwork{}))
	}
	return &Objects{cfg: cfg, provider: p, client: client, presign: s3.NewPresignClient(signFor)}, nil
}

// noNetwork is the HTTP client of the client that only signs: it refuses to send.
type noNetwork struct{}

func (noNetwork) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("storage: the public endpoint is only for signing links, backd never connects to it")
}

// Config returns the connection's settings.
func (o *Objects) Config() ObjectsConfig { return o.cfg }

// Provider returns the connection's provider entry.
func (o *Objects) Provider() Provider { return o.provider }

// Key is the object key of a file: <prefix>/<realm>/<database>/<collection>/<file id>.
func (o *Objects) Key(realm, database, collection, fileID string) string {
	return ObjectKey(o.cfg.Prefix, realm, database, collection, fileID)
}

// ObjectKey is the key of a file's object under a prefix: it needs no connection, so
// what only has to name an object (a deletion to queue) works while the storage is down.
func ObjectKey(prefix, realm, database, collection, fileID string) string {
	return strings.Join([]string{prefix, realm, database, collection, fileID}, "/")
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	SHA256       string // hex; "" when the storage keeps none
	LastModified time.Time
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var nf *types.NotFound
	var nk *types.NoSuchKey
	if errors.As(err, &nf) || errors.As(err, &nk) {
		return fmt.Errorf("%w", ErrObjectNotFound)
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return ErrObjectNotFound
		case "NoSuchBucket":
			return ErrBucketNotFound
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "AllAccessDisabled", "Forbidden", "403":
			return fmt.Errorf("%w (%s)", ErrAccessDenied, api.ErrorCode())
		case "BadDigest", "InvalidDigest", "XAmzContentSHA256Mismatch", "InvalidRequest", "ChecksumMismatch":
			if strings.Contains(strings.ToLower(api.ErrorMessage()), "checksum") || strings.Contains(api.ErrorCode(), "Digest") || strings.Contains(api.ErrorCode(), "SHA256") {
				return fmt.Errorf("%w (%s)", ErrChecksumMismatch, api.ErrorCode())
			}
		}
	}
	return err
}

func b64(hexSum string) (*string, error) {
	raw, err := hex.DecodeString(hexSum)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("sha256 must be 64 hex characters, got %q", hexSum)
	}
	return aws.String(base64.StdEncoding.EncodeToString(raw)), nil
}

func fromB64(sum *string) string {
	if sum == nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(*sum)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(raw)
}

// Put stores body (size bytes) under key. With sha256 (hex) the storage verifies
// the content against it and rejects the object when it differs.
func (o *Objects) Put(ctx context.Context, key string, body io.Reader, size int64, contentType, sha256 string) error {
	in := &s3.PutObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size)}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if sha256 != "" {
		sum, err := b64(sha256)
		if err != nil {
			return err
		}
		in.ChecksumSHA256 = sum
	}
	_, err := o.client.PutObject(ctx, in)
	return mapError(err)
}

// Head returns an object's details, including its stored SHA-256 when it has one.
func (o *Objects) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := o.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		return ObjectInfo{}, mapError(err)
	}
	return ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType), ETag: aws.ToString(out.ETag),
		SHA256: fromB64(out.ChecksumSHA256), LastModified: aws.ToTime(out.LastModified)}, nil
}

// Get opens an object; rng is an HTTP Range value ("bytes=0-99") or "". The caller closes the body.
func (o *Objects) Get(ctx context.Context, key, rng string) (io.ReadCloser, ObjectInfo, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	out, err := o.client.GetObject(ctx, in)
	if err != nil {
		return nil, ObjectInfo{}, mapError(err)
	}
	return out.Body, ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType), ETag: aws.ToString(out.ETag), LastModified: aws.ToTime(out.LastModified)}, nil
}

// Delete removes an object; a missing one is not an error (S3 answers success).
func (o *Objects) Delete(ctx context.Context, key string) error {
	_, err := o.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key)})
	return mapError(err)
}

// List returns up to max objects whose key starts with prefix. It is for
// `backd storage check` and the manual reconcile only: backd never lists the
// bucket in normal operation.
func (o *Objects) List(ctx context.Context, prefix string, max int) ([]ObjectInfo, error) {
	out, err := o.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(o.cfg.Bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(int32(max))})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]ObjectInfo, len(out.Contents))
	for i, c := range out.Contents {
		items[i] = ObjectInfo{Key: aws.ToString(c.Key), Size: aws.ToInt64(c.Size), ETag: aws.ToString(c.ETag), LastModified: aws.ToTime(c.LastModified)}
	}
	return items, nil
}

// ListAll calls fn for every object under prefix, page by page, until fn returns
// false. It is for the manual reconcile: nothing else lists the bucket.
func (o *Objects) ListAll(ctx context.Context, prefix string, fn func(ObjectInfo) bool) error {
	p := s3.NewListObjectsV2Paginator(o.client, &s3.ListObjectsV2Input{Bucket: aws.String(o.cfg.Bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return mapError(err)
		}
		for _, c := range page.Contents {
			if !fn(ObjectInfo{Key: aws.ToString(c.Key), Size: aws.ToInt64(c.Size), ETag: aws.ToString(c.ETag), LastModified: aws.ToTime(c.LastModified)}) {
				return nil
			}
		}
	}
	return nil
}

// Link is a signed URL.
type Link struct {
	URL       string
	ExpiresAt time.Time
	// Headers must be sent with the request (a signed PUT's content type, length
	// and checksum).
	Headers map[string]string
}

func link(u string, signed http.Header, ttl time.Duration, now time.Time) Link {
	l := Link{URL: u, ExpiresAt: now.Add(ttl), Headers: map[string]string{}}
	for k := range signed {
		switch strings.ToLower(k) {
		case "host", "content-length":
			// the HTTP client sets these
		default:
			l.Headers[k] = signed.Get(k)
		}
	}
	return l
}

// PresignGet signs a download link valid for ttl that forces the browser to save
// the file as filename (an attachment) with the stored type. It makes no request.
func (o *Objects) PresignGet(ctx context.Context, key string, ttl time.Duration, filename, contentType string) (Link, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key)}
	if filename != "" {
		in.ResponseContentDisposition = aws.String(attachment(filename))
	}
	if contentType != "" {
		in.ResponseContentType = aws.String(contentType)
	}
	now := time.Now()
	out, err := o.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return Link{}, err
	}
	return link(out.URL, out.SignedHeader, ttl, now), nil
}

// attachment is a Content-Disposition forcing a download, with the name safe in
// a header (the display name is sanitized elsewhere; this only quotes it).
func attachment(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// PresignPut signs an upload link valid for ttl for exactly size bytes of
// contentType with the given SHA-256 (hex): the storage rejects a PUT that
// differs from what was signed. It makes no request.
func (o *Objects) PresignPut(ctx context.Context, key string, ttl time.Duration, size int64, contentType, sha256 string) (Link, error) {
	sum, err := b64(sha256)
	if err != nil {
		return Link{}, err
	}
	in := &s3.PutObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), ContentLength: aws.Int64(size), ChecksumSHA256: sum}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	now := time.Now()
	out, err := o.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return Link{}, err
	}
	return link(out.URL, out.SignedHeader, ttl, now), nil
}

// CORSRule is one rule of the bucket's CORS configuration.
type CORSRule struct {
	Origins, Methods, Headers []string
}

// CORS reads the bucket's CORS rules (none: an empty list, nil error).
func (o *Objects) CORS(ctx context.Context) ([]CORSRule, error) {
	out, err := o.client.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(o.cfg.Bucket)})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "NoSuchCORSConfiguration" {
			return nil, nil
		}
		return nil, mapError(err)
	}
	rules := make([]CORSRule, len(out.CORSRules))
	for i, r := range out.CORSRules {
		rules[i] = CORSRule{Origins: r.AllowedOrigins, Methods: r.AllowedMethods, Headers: r.AllowedHeaders}
	}
	return rules, nil
}

// Encryption reads the bucket's default encryption algorithm ("" when none).
func (o *Objects) Encryption(ctx context.Context) (string, error) {
	out, err := o.client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(o.cfg.Bucket)})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "ServerSideEncryptionConfigurationNotFoundError" {
			return "", nil
		}
		return "", mapError(err)
	}
	if out.ServerSideEncryptionConfiguration != nil {
		for _, r := range out.ServerSideEncryptionConfiguration.Rules {
			if d := r.ApplyServerSideEncryptionByDefault; d != nil {
				return string(d.SSEAlgorithm), nil
			}
		}
	}
	return "", nil
}

// HeadBucket reports whether the bucket exists and the credentials can reach it.
func (o *Objects) HeadBucket(ctx context.Context) error {
	_, err := o.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(o.cfg.Bucket)})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) { // a HEAD has no body to name the error: a 404 is a missing bucket
			return ErrBucketNotFound
		}
	}
	return mapError(err)
}

// UploadPartSize is the size of the parts a streamed upload is cut into; an object
// smaller than one is a single PUT.
const UploadPartSize = 8 << 20

// PutStream stores a stream of unknown length under key, in parts when it is large
// (a multipart upload, aborted if the stream fails), holding at most one part in
// memory. It does not verify a checksum: the caller hashes the stream as it passes.
func (o *Objects) PutStream(ctx context.Context, key string, body io.Reader, contentType string) error {
	tm := transfermanager.New(o.client, func(opt *transfermanager.Options) {
		opt.PartSizeBytes = UploadPartSize
		opt.MultipartUploadThreshold = UploadPartSize
		opt.Concurrency = 1
		if o.provider.ChecksumsWhenRequired {
			opt.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		}
	})
	in := &transfermanager.UploadObjectInput{Bucket: aws.String(o.cfg.Bucket), Key: aws.String(key), Body: body}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	_, err := tm.UploadObject(ctx, in)
	return mapError(err)
}
