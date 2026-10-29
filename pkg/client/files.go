package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileDetails describes a stored file, as the field of a document holds it.
type FileDetails struct {
	ID         string `json:"id"` // fl_…
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Type       string `json:"type"` // detected from the content
	SHA256     string `json:"sha256"`
	UploadedAt string `json:"uploaded_at"`
	// Width and Height are the pixels of an image, as a viewer sees it.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Versions is the state of each version the field declares (versions: in collection.yaml).
	Versions map[string]VersionState `json:"versions,omitempty"`
	// Public is true for a file of a public field (public: true): it is read at URL, with no signed link.
	Public bool `json:"public,omitempty"`
	// KeyName is a public file's name as its address ends with it.
	KeyName string `json:"key_name,omitempty"`
	// URL is a public file's permanent address, or the signed link of a read with FileLinks.
	URL string `json:"url,omitempty"`
	// ExpiresAt is when a signed URL stops working (never set on a public file's address).
	ExpiresAt string `json:"expires_at,omitempty"`
}

// VersionState is the state of one version (a resized copy) of an image.
type VersionState struct {
	// Status is "pending", "ready", "empty", "skipped" or "failed".
	Status      string         `json:"status"`
	Reason      string         `json:"reason,omitempty"`
	ID          string         `json:"id,omitempty"` // fv_…, once ready
	Size        int64          `json:"size,omitempty"`
	Type        string         `json:"type,omitempty"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	// Custom is true for a copy made with parameters a function gave (Generate), not the declared ones.
	Custom      bool   `json:"custom,omitempty"`
	GeneratedAt string `json:"generated_at,omitempty"`
	// URL is the copy's permanent address, on a public file once it is ready.
	URL string `json:"url,omitempty"`
}

// VersionParams are the parameters of Generate.
type VersionParams struct {
	MaxWidth  int    `json:"max_width,omitempty"`
	MaxHeight int    `json:"max_height,omitempty"`
	Fit       string `json:"fit,omitempty"` // "contain", "cover" or "stretch"
	Quality   int    `json:"quality,omitempty"`
	Format    string `json:"format,omitempty"` // "jpeg" or "png"
	Upscale   bool   `json:"upscale,omitempty"`
}

// FileLink is a link to read a file.
type FileLink struct {
	URL string `json:"url"`
	// ExpiresAt is RFC 3339; nil for the address of a public file, which never expires.
	ExpiresAt *string `json:"expires_at"`
}

// FileField is how a collection declares one of its file fields.
type FileField struct {
	Name string `json:"name"`
	// Upload is "proxy" (the bytes go through backd) or "direct" (to the storage with a signed request).
	Upload string `json:"upload"`
	// Download is "presigned" or "proxy".
	Download string `json:"download"`
	Multiple bool   `json:"multiple"`
	MaxFiles int    `json:"max_files"`
	MaxSize  int64  `json:"max_size"` // bytes per file
	// Types are the allowed content types; empty allows any.
	Types []string `json:"types"`
	// MultipartAbove: files above this many bytes are sent in parts (direct fields that can hold more).
	MultipartAbove int64 `json:"multipart_above,omitempty"`
}

// FileSource is the data of a file to upload: something that can be read at any offset (it is
// hashed before it is sent, and sent in parts when it is large) and its size.
type FileSource struct {
	R    io.ReaderAt
	Size int64
	// Name and Type default to the file's own: the base name of a path, a type from its extension.
	Name, Type string
	closer     io.Closer
}

// FileFromBytes is the source of data held in memory.
func FileFromBytes(b []byte, name, contentType string) *FileSource {
	return &FileSource{R: bytes.NewReader(b), Size: int64(len(b)), Name: name, Type: contentType}
}

// FileFromReaderAt is the source of anything that reads at an offset, such as an *os.File.
func FileFromReaderAt(r io.ReaderAt, size int64, name, contentType string) *FileSource {
	return &FileSource{R: r, Size: size, Name: name, Type: contentType}
}

// OpenFile opens a file on disk as a source; close it when the upload is done.
func OpenFile(path string) (*FileSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &FileSource{R: f, Size: fi.Size(), Name: filepath.Base(path), Type: mime.TypeByExtension(filepath.Ext(path)), closer: f}, nil
}

// Close releases the file of a source made by OpenFile.
func (s *FileSource) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func (s *FileSource) section(off, n int64) *io.SectionReader { return io.NewSectionReader(s.R, off, n) }

func (s *FileSource) describe(name, typ string) (string, string) {
	if name == "" {
		name = s.Name
	}
	if name == "" {
		name = "file"
	}
	if typ == "" {
		typ = s.Type
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	return name, typ
}

// UploadOptions are the options of an upload.
type UploadOptions struct {
	// Name and Type override the source's own.
	Name, Type string
	// IfMatch makes the upload conditional on the document's version (a number or an ETag).
	IfMatch any
	// OnProgress reports how much has been sent.
	OnProgress func(UploadProgress)
}

func (o UploadOptions) headers() ([]RequestOption, error) {
	if o.IfMatch == nil {
		return nil, nil
	}
	v, err := IfMatchValue(o.IfMatch)
	if err != nil {
		return nil, err
	}
	return []RequestOption{WithHeader("If-Match", v)}, nil
}

// Files are the files of one field of one document: coll.Files(id, "photos").
type Files struct {
	coll  *Collection
	id    string
	field string
}

// Files returns the files held in a field of a document.
func (c *Collection) Files(id, field string) *Files { return &Files{coll: c, id: id, field: field} }

func (f *Files) path(rest ...string) []string {
	return append(append(f.coll.Path(), f.id, "_files", f.field), rest...)
}

// List returns the files the field holds.
func (f *Files) List(ctx context.Context, opts ...RequestOption) ([]FileDetails, error) {
	var doc map[string]json.RawMessage
	if _, err := f.coll.c.Do(ctx, "GET", append(f.coll.Path(), f.id), nil, &doc, opts...); err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(doc[f.field])
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return nil, nil
	case raw[0] == '[':
		var list []FileDetails
		return list, json.Unmarshal(raw, &list)
	}
	var one FileDetails
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, err
	}
	return []FileDetails{one}, nil
}

// which resolves the file to act on: the one given, or the only one the field holds.
func (f *Files) which(ctx context.Context, fileID string, opts []RequestOption) (string, *FileDetails, error) {
	if fileID != "" {
		return fileID, nil, nil
	}
	files, err := f.List(ctx, opts...)
	if err != nil {
		return "", nil, err
	}
	switch len(files) {
	case 0:
		return "", nil, newError(404, "not_found", "the document holds no file in "+f.field, kNotFound)
	case 1:
		return files[0].ID, &files[0], nil
	}
	return "", nil, fmt.Errorf("client: Files(%s): the field holds several files: say which one (a file id)", f.field)
}

// FileContent is a file read: its bytes are Body, which the caller closes.
type FileContent struct {
	// File is set when Get looked the file up (no file id given).
	File   *FileDetails
	Header http.Header
	Body   io.ReadCloser
}

// Bytes reads and closes the body.
func (c *FileContent) Bytes() ([]byte, error) {
	defer c.Body.Close()
	return io.ReadAll(c.Body)
}

// Text reads and closes the body as text.
func (c *FileContent) Text() (string, error) { b, err := c.Bytes(); return string(b), err }

// GetFileOptions are the options of Files.Get and Files.Link.
type GetFileOptions struct {
	// Version reads a version (a resized copy) of the file.
	Version string
}

// Get reads a file. With no fileID it is the only file the field holds.
func (f *Files) Get(ctx context.Context, fileID string, o GetFileOptions, opts ...RequestOption) (*FileContent, error) {
	id, details, err := f.which(ctx, fileID, opts)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if o.Version != "" {
		q.Set("version", o.Version)
	}
	res, err := f.coll.c.Do(ctx, "GET", f.path(id), nil, nil, append([]RequestOption{WithQuery(q), WithRaw()}, opts...)...)
	if err != nil {
		return nil, err
	}
	return &FileContent{File: details, Header: res.Header, Body: res.Body}, nil
}

// PutOptions are the options of Files.Put.
type PutOptions struct {
	// Name is the display name; Type the content type (application/octet-stream by default).
	Name, Type string
	// IfMatch makes the write conditional on the document's version.
	IfMatch    any
	OnProgress func(UploadProgress)
}

// Put sends a file through backd (a field with proxy uploads) and attaches it to the document. It
// returns the document. Use UploadFile to let the client choose the way the field takes files.
func (f *Files) Put(ctx context.Context, data io.Reader, o PutOptions, opts ...RequestOption) (Doc, error) {
	req := []RequestOption{WithRawBody(data, o.Type)}
	if o.Name != "" {
		req = append(req, WithQuery(url.Values{"name": {o.Name}}))
	}
	if o.OnProgress != nil {
		req = append(req, WithProgress(o.OnProgress))
	}
	if o.IfMatch != nil {
		req = append(req, WithIfMatch(o.IfMatch))
	}
	var doc Doc
	if _, err := f.coll.c.Do(ctx, "POST", f.path(), nil, &doc, append(req, opts...)...); err != nil {
		return nil, err
	}
	return doc, nil
}

// Delete removes a file from the field and returns the document. With no fileID it clears the field.
func (f *Files) Delete(ctx context.Context, fileID string, ifMatch any, opts ...RequestOption) (Doc, error) {
	p := f.path()
	if fileID != "" {
		p = f.path(fileID)
	}
	if ifMatch != nil {
		opts = append(opts, WithIfMatch(ifMatch))
	}
	var doc Doc
	if _, err := f.coll.c.Do(ctx, "DELETE", p, nil, &doc, opts...); err != nil {
		return nil, err
	}
	return doc, nil
}

// Link returns a link to read a file (signed, or the permanent address of a public file).
func (f *Files) Link(ctx context.Context, fileID string, o GetFileOptions, opts ...RequestOption) (*FileLink, error) {
	id, _, err := f.which(ctx, fileID, opts)
	if err != nil {
		return nil, err
	}
	q := url.Values{"link": {"json"}}
	if o.Version != "" {
		q.Set("version", o.Version)
	}
	var l FileLink
	if _, err := f.coll.c.Do(ctx, "GET", f.path(id), nil, &l, append([]RequestOption{WithQuery(q)}, opts...)...); err != nil {
		return nil, err
	}
	return &l, nil
}

// FileVersion acts on one version of a file.
type FileVersion struct {
	f      *Files
	name   string
	fileID string
}

// Version addresses the version name of a file (the only one of the field with no fileID).
func (f *Files) Version(name, fileID string) *FileVersion {
	return &FileVersion{f: f, name: name, fileID: fileID}
}

func (v *FileVersion) call(ctx context.Context, method string, body any, opts []RequestOption) (*VersionState, error) {
	id, _, err := v.f.which(ctx, v.fileID, opts)
	if err != nil {
		return nil, err
	}
	var st VersionState
	if _, err := v.f.coll.c.Do(ctx, method, v.f.path(id, "versions", v.name), body, &st, opts...); err != nil {
		return nil, err
	}
	return &st, nil
}

// Regenerate makes the version again with the parameters collection.yaml declares.
func (v *FileVersion) Regenerate(ctx context.Context, opts ...RequestOption) (*VersionState, error) {
	return v.call(ctx, "POST", nil, opts)
}

// Generate makes the version with other parameters; only a writable version allows it.
func (v *FileVersion) Generate(ctx context.Context, p VersionParams, opts ...RequestOption) (*VersionState, error) {
	return v.call(ctx, "POST", p, opts)
}

// Delete drops the copy: the version is pending again (a worker makes it) or empty (only functions make it).
func (v *FileVersion) Delete(ctx context.Context, opts ...RequestOption) (*VersionState, error) {
	return v.call(ctx, "DELETE", nil, opts)
}

// FileField returns how the collection declares a file field. The answer is kept by the client.
func (c *Collection) FileField(ctx context.Context, field string, opts ...RequestOption) (*FileField, error) {
	key := strings.Join(append(c.Path(), field), "/")
	cl := c.c
	cl.fieldMu.Lock()
	found := cl.fields[key]
	cl.fieldMu.Unlock()
	if found != nil {
		return found, nil
	}
	var ff FileField
	if _, err := cl.Do(ctx, "GET", append(c.Path(), "_files", field), nil, &ff, opts...); err != nil {
		return nil, err
	}
	cl.fieldMu.Lock()
	if cl.fields == nil {
		cl.fields = map[string]*FileField{}
	}
	cl.fields[key] = &ff
	cl.fieldMu.Unlock()
	return &ff, nil
}

// UploadFile uploads a file into a field of a document the way the field takes it (through backd,
// or straight to the storage, in parts when it is large) and returns the document.
func (c *Collection) UploadFile(ctx context.Context, id, field string, src *FileSource, o UploadOptions, opts ...RequestOption) (Doc, error) {
	info, err := c.FileField(ctx, field)
	if err != nil {
		return nil, err
	}
	if info.Upload == "direct" {
		_, answer, err := c.direct(ctx, field, append(c.Path(), id, "_files", field, "uploads"), info, src, o, opts)
		if err != nil {
			return nil, err
		}
		var doc Doc
		return doc, json.Unmarshal(answer, &doc)
	}
	name, typ := src.describe(o.Name, o.Type)
	return c.Files(id, field).Put(ctx, src.section(0, src.Size), PutOptions{Name: name, Type: typ, IfMatch: o.IfMatch, OnProgress: o.OnProgress}, opts...)
}

// PreparedUpload is a file stored but not yet attached to a document: put Ref in the document's field.
type PreparedUpload struct {
	ID    string
	Token string
	// Ref is what goes in the document's field.
	Ref map[string]string
	// ExpiresAt is RFC 3339: an upload that is not used expires then.
	ExpiresAt string
	// File is what backd stored.
	File struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		Type   string `json:"type"`
		SHA256 string `json:"sha256"`
	}
}

// PrepareUpload stores a file before its document exists, so the document can be created with the
// file in one write: create the document with the returned Ref in the field.
func (c *Collection) PrepareUpload(ctx context.Context, field string, src *FileSource, o UploadOptions, opts ...RequestOption) (*PreparedUpload, error) {
	info, err := c.FileField(ctx, field)
	if err != nil {
		return nil, err
	}
	o.IfMatch = nil
	var s startAnswer
	var file json.RawMessage
	if info.Upload == "direct" {
		start, answer, err := c.direct(ctx, field, append(c.Path(), "_files", field, "uploads"), info, src, o, opts)
		if err != nil {
			return nil, err
		}
		s = *start
		var a struct {
			File json.RawMessage `json:"file"`
		}
		if err := json.Unmarshal(answer, &a); err != nil {
			return nil, err
		}
		file = a.File
	} else {
		name, typ := src.describe(o.Name, o.Type)
		req := []RequestOption{WithRawBody(src.section(0, src.Size), typ), WithQuery(url.Values{"name": {name}})}
		if o.OnProgress != nil {
			req = append(req, WithProgress(o.OnProgress))
		}
		var out struct {
			startAnswer
			File json.RawMessage `json:"file"`
		}
		if _, err := c.c.Do(ctx, "POST", append(c.Path(), "_files", field, "uploads"), nil, &out, append(req, opts...)...); err != nil {
			return nil, err
		}
		s, file = out.startAnswer, out.File
	}
	p := &PreparedUpload{ID: s.UploadID, Token: s.UploadToken, Ref: map[string]string{"upload": s.UploadID, "token": s.UploadToken}, ExpiresAt: s.ExpiresAt}
	return p, json.Unmarshal(file, &p.File)
}

// startAnswer is the answer to starting an upload.
type startAnswer struct {
	UploadID    string `json:"upload_id"`
	UploadToken string `json:"upload_token"`
	ExpiresAt   string `json:"expires_at"`
	URL         string `json:"url"`
	// Headers must be sent with the PUT to URL.
	Headers   map[string]string `json:"headers"`
	Multipart bool              `json:"multipart"`
	PartSize  int64             `json:"part_size"`
	Parts     []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Size    int64             `json:"size"`
	} `json:"parts"`
}

const (
	mib             = 1 << 20
	maxParts        = 1000
	partAttempts    = 4
	partConcurrency = 3
)

// partSizeFor is the size of the parts a large file is sent in: at most 1000 parts, in steps of 16 MiB, 64 MiB at least.
func partSizeFor(size int64) int64 {
	perPart := (size + maxParts - 1) / maxParts
	need := (perPart + 16*mib - 1) / (16 * mib) * (16 * mib)
	return max(64*mib, need)
}

func hashRange(ctx context.Context, src *FileSource, off, n int64) (string, error) {
	h := sha256.New()
	r := src.section(off, n)
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", networkError(ctx, err)
		}
		k, err := r.Read(buf)
		h.Write(buf[:k])
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// direct sends a file straight to the storage: declare it (with its hash), PUT it (in parts when
// large), then ask backd to complete the upload.
func (c *Collection) direct(ctx context.Context, field string, startPath []string, info *FileField, src *FileSource, o UploadOptions, opts []RequestOption) (*startAnswer, json.RawMessage, error) {
	name, typ := src.describe(o.Name, o.Type)
	multipart := info.MultipartAbove > 0 && src.Size > info.MultipartAbove
	decl := map[string]any{"name": name, "size": src.Size, "type": typ}
	if multipart {
		ps := partSizeFor(src.Size)
		var digests []string
		for at := int64(0); at < src.Size; at += ps {
			d, err := hashRange(ctx, src, at, min(ps, src.Size-at))
			if err != nil {
				return nil, nil, err
			}
			digests = append(digests, d)
		}
		decl["part_sha256"] = digests
	} else {
		d, err := hashRange(ctx, src, 0, src.Size)
		if err != nil {
			return nil, nil, err
		}
		decl["sha256"] = d
	}
	hdr, err := o.headers()
	if err != nil {
		return nil, nil, err
	}
	var start startAnswer
	if _, err := c.c.Do(ctx, "POST", startPath, decl, &start, append(hdr, opts...)...); err != nil {
		return nil, nil, err
	}
	if start.Multipart {
		err = c.putParts(ctx, &start, src, o.OnProgress)
	} else {
		err = c.c.putToStorage(ctx, start.URL, start.Headers, src.section(0, src.Size), src.Size, o.OnProgress)
	}
	if err != nil {
		return nil, nil, err
	}
	done := append(c.Path(), "_files", field, "uploads", start.UploadID, "complete")
	var answer json.RawMessage
	if _, err := c.c.Do(ctx, "POST", done, map[string]string{"upload_token": start.UploadToken}, &answer, append(hdr, opts...)...); err != nil {
		return nil, nil, err
	}
	return &start, answer, nil
}

// putToStorage sends one PUT to a signed address of the storage: no credentials of backd.
func (c *Client) putToStorage(ctx context.Context, address string, headers map[string]string, body io.Reader, size int64, progress func(UploadProgress)) error {
	if progress != nil {
		body = &progressReader{r: body, total: size, fn: progress}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, address, body)
	if err != nil {
		return newError(0, "invalid_request", "the storage address is not usable: "+err.Error(), kValidation)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = size
	if size == 0 {
		req.Body = http.NoBody
	}
	res, err := c.doer.Do(req)
	if err != nil {
		return networkError(ctx, err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBody))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return newError(res.StatusCode, "upload_failed", fmt.Sprintf("the storage refused the file (%d): was the file changed after it was declared?", res.StatusCode), kBackd)
	}
	return nil
}

// putParts sends the parts of a large file, a few at a time, repeating a part that failed.
func (c *Collection) putParts(ctx context.Context, start *startAnswer, src *FileSource, progress func(UploadProgress)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	sent := make([]int64, len(start.Parts))
	report := func(i int, n int64) {
		if progress == nil {
			return
		}
		mu.Lock()
		sent[i] = n
		var total int64
		for _, s := range sent {
			total += s
		}
		mu.Unlock()
		progress(UploadProgress{Loaded: total, Total: src.Size})
	}
	jobs := make(chan int)
	errc := make(chan error, len(start.Parts))
	var wg sync.WaitGroup
	for w := 0; w < min(partConcurrency, len(start.Parts)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				part := start.Parts[i]
				for attempt := 1; ; attempt++ {
					err := c.c.putToStorage(ctx, part.URL, part.Headers, src.section(int64(i)*start.PartSize, part.Size), part.Size,
						func(p UploadProgress) { report(i, p.Loaded) })
					if err == nil {
						break
					}
					var e *Error
					retryable := errors.As(err, &e) && (e.kind.is(kNetwork) && ctx.Err() == nil || e.Status >= 500)
					if !retryable || attempt >= partAttempts {
						errc <- err
						cancel()
						return
					}
					report(i, 0)
					if sleep(ctx, 500*time.Millisecond<<(attempt-1)) != nil {
						errc <- networkError(ctx, ctx.Err())
						return
					}
				}
				report(i, part.Size)
			}
		}()
	}
feed:
	for i := range start.Parts {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(errc)
	if err, ok := <-errc; ok {
		return err
	}
	return nil
}

// FileURLOptions are the options of FileURL.
type FileURLOptions struct {
	Version string
	// File, when you hold the file's details (from a read of the document), lets a public file's
	// address be made without a request.
	File *FileDetails
}

// FileURL returns a link to read a file: the permanent address of a public file (made without a
// request when File is given), or a signed link. It returns nil for a version that isn't ready.
func (c *Collection) FileURL(ctx context.Context, id, field, fileID string, o FileURLOptions, opts ...RequestOption) (*FileLink, error) {
	if o.File != nil && o.File.Public {
		u := PublicURL(o.File, o.Version)
		if u == "" {
			return nil, nil
		}
		return &FileLink{URL: u}, nil
	}
	l, err := c.Files(id, field).Link(ctx, fileID, GetFileOptions{Version: o.Version}, opts...)
	var e *Error
	if err != nil && o.Version != "" && errors.As(err, &e) && e.Code == "version_unavailable" {
		return nil, nil
	}
	return l, err
}

// DownloadFile copies a file's bytes to dst and returns how many were written.
func (c *Collection) DownloadFile(ctx context.Context, id, field, fileID string, dst io.Writer, o GetFileOptions, opts ...RequestOption) (int64, error) {
	content, err := c.Files(id, field).Get(ctx, fileID, o, opts...)
	if err != nil {
		return 0, err
	}
	defer content.Body.Close()
	return io.Copy(dst, content.Body)
}

// DeleteFile removes one file from a field and returns the document.
func (c *Collection) DeleteFile(ctx context.Context, id, field, fileID string, opts ...RequestOption) (Doc, error) {
	if fileID == "" {
		return nil, errors.New("client: DeleteFile needs the id of the file to remove (Files(id, field).Delete clears the field)")
	}
	return c.Files(id, field).Delete(ctx, fileID, nil, opts...)
}

// PublicURL is a public file's permanent address, or of one of its versions once that is ready.
// It is "" for a file that isn't public, and for a version that isn't ready.
func PublicURL(file *FileDetails, version string) string {
	if file == nil || !file.Public {
		return ""
	}
	if version == "" {
		return file.URL
	}
	if v, ok := file.Versions[version]; ok && v.Status == "ready" {
		return v.URL
	}
	return ""
}

// VersionInfo says where a version stands.
type VersionInfo struct {
	// Status is "undeclared" when the field doesn't declare the version.
	Status string
	Ready  bool
	Reason string
	Width  int
	Height int
}

// VersionStatus says where the version name of a file stands.
func VersionStatus(file *FileDetails, name string) VersionInfo {
	if file != nil {
		if v, ok := file.Versions[name]; ok {
			return VersionInfo{Status: v.Status, Ready: v.Status == "ready", Reason: v.Reason, Width: v.Width, Height: v.Height}
		}
	}
	return VersionInfo{Status: "undeclared"}
}
