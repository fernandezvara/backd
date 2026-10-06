package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Check levels.
const (
	CheckOK   = "ok"
	CheckWarn = "warn" // worth knowing, doesn't fail the check
	CheckFail = "fail"
	CheckSkip = "skipped"
)

// CheckStep is one thing `backd storage check` tried.
type CheckStep struct {
	Name   string
	Level  string
	Detail string
}

// CheckReport is what a check of a realm's storage found.
type CheckReport struct {
	Provider       string
	Endpoint       string
	PublicEndpoint string
	Bucket         string
	Prefix         string
	Steps          []CheckStep
	// ChecksumSHA256 is what the storage does with a signed x-amz-checksum-sha256:
	// "verified" (it rejects content that differs), "ignored" (it accepts it), or
	// "not tested".
	ChecksumSHA256 string
	// Encryption is the bucket's default encryption ("none", an algorithm, or
	// "unknown" when the provider doesn't expose it).
	Encryption string
	CORS       []CORSRule
	// LinkHost is the host a signed link is for (the public endpoint's when set).
	LinkHost string
}

// OK reports whether nothing failed.
func (r CheckReport) OK() bool {
	for _, s := range r.Steps {
		if s.Level == CheckFail {
			return false
		}
	}
	return true
}

func (r *CheckReport) add(name, level, detail string) {
	r.Steps = append(r.Steps, CheckStep{Name: name, Level: level, Detail: detail})
}

// check objects live under <prefix>/<realm>/_check/: not a file key, and gone when the check ends.
func (o *Objects) checkPrefix(realm string) string { return o.cfg.Prefix + "/" + realm + "/_check/" }

// Check verifies the realm's storage the way a file would use it: the bucket and
// the credentials, every operation backd needs on the realm's prefix, the signed
// checksum and signed links, and (where the provider exposes them) the bucket's
// CORS rules and default encryption. It uploads a few small objects under
// <prefix>/<realm>/_check/ and deletes them again, and never touches anything else.
func (o *Objects) Check(ctx context.Context, realm string) CheckReport {
	rep := CheckReport{Provider: o.cfg.Provider, Endpoint: o.cfg.Endpoint, PublicEndpoint: o.cfg.PublicEndpoint, Bucket: o.cfg.Bucket, Prefix: o.cfg.Prefix, ChecksumSHA256: "not tested", Encryption: "unknown"}
	var created []string
	defer func() {
		// Always clean up, even when a step failed or the context ended.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, k := range created {
			_ = o.Delete(cctx, k)
		}
	}()

	// The first request is a listing of the realm's prefix, not a HEAD of the bucket:
	// a key limited to its prefix (as it should be) may list that prefix but not ask
	// about the bucket itself. A missing bucket is the one answer that stops here; a
	// refusal is a warning (only a reconcile needs to list) and the write below says
	// whether the credentials work.
	canList := false
	if _, err := o.List(ctx, o.checkPrefix(realm), 1); err != nil {
		switch {
		case errors.Is(err, ErrBucketNotFound):
			rep.add("bucket", CheckFail, fmt.Sprintf("%s: %v", o.cfg.Bucket, describe(err)))
			return rep // nothing else can work
		case errors.Is(err, ErrAccessDenied):
			rep.add("bucket", CheckWarn, fmt.Sprintf("%s: listing the prefix was refused (%v); only `backd storage reconcile` needs it, and the steps below show whether the key works", o.cfg.Bucket, err))
		default:
			rep.add("bucket", CheckFail, fmt.Sprintf("%s: %v", o.cfg.Bucket, describe(err)))
			return rep
		}
	} else {
		canList = true
		rep.add("bucket", CheckOK, fmt.Sprintf("%s exists and the credentials reach it", o.cfg.Bucket))
	}

	id := make([]byte, 8)
	_, _ = rand.Read(id)
	base := o.checkPrefix(realm) + hex.EncodeToString(id)
	body := []byte("backd storage check " + time.Now().UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256(body)
	sumHex := hex.EncodeToString(sum[:])

	key := base + "-a"
	if err := o.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain", sumHex); err != nil {
		rep.add("write", CheckFail, "put on the prefix: "+describe(err))
		return rep
	}
	created = append(created, key)
	rep.add("write", CheckOK, "an object was stored under "+o.checkPrefix(realm))

	info, err := o.Head(ctx, key)
	switch {
	case err != nil:
		rep.add("head", CheckFail, describe(err))
	case info.Size != int64(len(body)):
		rep.add("head", CheckFail, fmt.Sprintf("the stored object has %d bytes, %d were sent", info.Size, len(body)))
	default:
		rep.add("head", CheckOK, "size and details read back")
	}

	got, err := o.download(ctx, key, "")
	switch {
	case err != nil:
		rep.add("read", CheckFail, describe(err))
	case !bytes.Equal(got, body):
		rep.add("read", CheckFail, "the object read back differs from what was stored")
	default:
		rep.add("read", CheckOK, "downloaded and identical")
	}
	if got, err := o.download(ctx, key, "bytes=0-5"); err != nil {
		rep.add("range", CheckFail, describe(err))
	} else if string(got) != string(body[:6]) {
		rep.add("range", CheckFail, fmt.Sprintf("a range request returned %q, want %q", got, body[:6]))
	} else {
		rep.add("range", CheckOK, "a range request returns just the range")
	}

	if !canList {
		rep.add("list", CheckWarn, "the prefix can't be listed: `backd storage reconcile` won't work with this key")
	} else if items, err := o.List(ctx, o.checkPrefix(realm), 10); err != nil {
		rep.add("list", CheckWarn, "listing the prefix failed ("+describe(err)+"): only `backd storage reconcile` needs it")
	} else if len(items) == 0 {
		rep.add("list", CheckWarn, "listing the prefix returned nothing: reconcile would miss objects")
	} else {
		rep.add("list", CheckOK, "the prefix can be listed (for reconcile)")
	}

	o.checkChecksum(ctx, &rep, base, body, sumHex, &created)
	o.checkLinks(ctx, &rep, base, body, sumHex, &created)

	if o.provider.ReadsCORS {
		rules, err := o.CORS(ctx)
		switch {
		case err != nil:
			rep.add("cors", CheckWarn, "the bucket's CORS rules couldn't be read: "+describe(err))
		case len(rules) == 0:
			rep.add("cors", CheckWarn, "the bucket has no CORS rules: a browser can't upload straight to it or fetch from it across origins (not needed for uploads and downloads through backd)")
		default:
			rep.CORS = rules
			rep.add("cors", CheckOK, fmt.Sprintf("%d CORS rule(s)", len(rules)))
		}
	} else {
		rep.add("cors", CheckSkip, o.provider.Label+" doesn't expose the bucket's CORS rules to this check: set them for the origins of your app and check by hand (direct uploads and cross-origin downloads need them)")
	}
	if o.provider.ReadsEncryption {
		alg, err := o.Encryption(ctx)
		switch {
		case err != nil:
			rep.add("encryption", CheckWarn, "the bucket's default encryption couldn't be read: "+describe(err))
		case alg == "":
			rep.Encryption = "none"
			rep.add("encryption", CheckWarn, "the bucket has no default encryption: files are stored unencrypted at rest (set it at the provider; backd sends no encryption headers)")
		default:
			rep.Encryption = alg
			rep.add("encryption", CheckOK, "default encryption: "+alg)
		}
	} else {
		rep.add("encryption", CheckSkip, o.provider.Label+" doesn't expose the bucket's default encryption: confirm it at the provider")
	}
	return rep
}

// download reads an object (or a range of it) fully.
func (o *Objects) download(ctx context.Context, key, rng string) ([]byte, error) {
	rc, _, err := o.Get(ctx, key, rng)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 1<<20))
}

// checkChecksum finds out whether the storage verifies a signed SHA-256: a PUT
// with a wrong digest must be rejected.
func (o *Objects) checkChecksum(ctx context.Context, rep *CheckReport, base string, body []byte, sumHex string, created *[]string) {
	if o.provider.ChecksumSHA256 == Unsupported {
		rep.add("checksum", CheckSkip, o.provider.Label+" doesn't verify x-amz-checksum-sha256: direct uploads aren't available")
		return
	}
	wrong := sha256.Sum256([]byte("something else"))
	key := base + "-bad"
	err := o.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain", hex.EncodeToString(wrong[:]))
	switch {
	case err == nil:
		*created = append(*created, key)
		rep.ChecksumSHA256 = "ignored"
		level := CheckFail
		if o.provider.ChecksumSHA256 == Unverified {
			level = CheckWarn
		}
		rep.add("checksum", level, "the storage accepted content that doesn't match the signed SHA-256: it doesn't verify it, so direct uploads would be unsafe")
	case errors.Is(err, ErrChecksumMismatch):
		rep.ChecksumSHA256 = "verified"
		rep.add("checksum", CheckOK, "a wrong SHA-256 is rejected by the storage")
	default:
		// Rejected, but not as a checksum problem: still a rejection of the bad content.
		rep.ChecksumSHA256 = "verified"
		rep.add("checksum", CheckOK, "a wrong SHA-256 is rejected by the storage ("+describe(err)+")")
	}
	info, err := o.Head(ctx, base+"-a")
	switch {
	case err != nil:
	case info.SHA256 == "":
		rep.add("stored checksum", CheckWarn, "the storage keeps no SHA-256 to read back: direct uploads are verified by the signed PUT only")
	case info.SHA256 != sumHex:
		rep.add("stored checksum", CheckFail, "the SHA-256 the storage reports differs from the content's")
	default:
		rep.add("stored checksum", CheckOK, "the storage keeps the SHA-256 and reports it back")
	}
}

// checkLinks signs a download and an upload link and uses them, when the links
// are for the address backd can reach.
func (o *Objects) checkLinks(ctx context.Context, rep *CheckReport, base string, body []byte, sumHex string, created *[]string) {
	get, err := o.PresignGet(ctx, base+"-a", time.Minute, "check.txt", "text/plain")
	if err != nil {
		rep.add("download link", CheckFail, describe(err))
		return
	}
	if u, err := url.Parse(get.URL); err == nil {
		rep.LinkHost = u.Host
	}
	if o.cfg.PublicEndpoint != "" {
		rep.add("download link", CheckSkip, "links are signed for "+rep.LinkHost+" (public_endpoint), which backd never connects to: open a link from a browser to try it")
		return
	}
	hc, err := NewHTTPClient(o.cfg.Endpoint, o.cfg.Bucket, o.provider.Addressing)
	if err != nil {
		rep.add("download link", CheckFail, err.Error())
		return
	}
	res, err := doLink(ctx, hc, "GET", get, nil)
	switch {
	case err != nil:
		rep.add("download link", CheckFail, describe(err))
	case res.status != http.StatusOK || !bytes.Equal(res.body, body):
		rep.add("download link", CheckFail, fmt.Sprintf("a signed link answered %d", res.status))
	case !strings.HasPrefix(res.header.Get("Content-Disposition"), "attachment"):
		rep.add("download link", CheckWarn, "the signed link works, but the storage didn't force a download (no attachment Content-Disposition)")
	default:
		rep.add("download link", CheckOK, "a signed link downloads the object, as an attachment")
	}

	if o.provider.ChecksumSHA256 == Unsupported {
		return
	}
	key := base + "-up"
	put, err := o.PresignPut(ctx, key, time.Minute, int64(len(body)), "text/plain", sumHex)
	if err != nil {
		rep.add("upload link", CheckFail, describe(err))
		return
	}
	res, err = doLink(ctx, hc, "PUT", put, body)
	if err != nil || res.status/100 != 2 {
		detail := ""
		if err != nil {
			detail = describe(err)
		} else {
			detail = fmt.Sprintf("answered %d: %s", res.status, strings.TrimSpace(string(res.body)))
		}
		rep.add("upload link", CheckFail, "a signed upload link was refused: "+detail)
		return
	}
	*created = append(*created, key)
	// The same link must refuse different content (the signed SHA-256 is the guard).
	tampered := append([]byte(nil), body...)
	tampered[0] ^= 0xff
	bad := base + "-up2"
	put2, _ := o.PresignPut(ctx, bad, time.Minute, int64(len(tampered)), "text/plain", sumHex)
	res, err = doLink(ctx, hc, "PUT", put2, tampered)
	if err == nil && res.status/100 == 2 {
		*created = append(*created, bad)
		rep.add("upload link", CheckFail, "a signed upload link accepted content that doesn't match its SHA-256")
		return
	}
	rep.add("upload link", CheckOK, "a signed upload link stores the object and refuses different content")
}

type linkResult struct {
	status int
	header http.Header
	body   []byte
}

func doLink(ctx context.Context, hc *http.Client, method string, l Link, body []byte) (linkResult, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.URL, rd)
	if err != nil {
		return linkResult{}, err
	}
	for k, v := range l.Headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	res, err := hc.Do(req)
	if err != nil {
		return linkResult{}, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return linkResult{status: res.StatusCode, header: res.Header, body: data}, nil
}

// describe turns an error into a sentence for a person.
func describe(err error) string {
	switch {
	case errors.Is(err, ErrAccessDenied):
		return err.Error() + ": check the access key and secret, and that the key may use this bucket and prefix"
	case errors.Is(err, ErrBucketNotFound):
		return "the bucket doesn't exist (or isn't visible to this key)"
	}
	return err.Error()
}
