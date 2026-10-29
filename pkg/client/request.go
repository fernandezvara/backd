package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// UploadProgress says how much of an upload has been sent.
type UploadProgress struct {
	Loaded int64 // bytes sent
	Total  int64 // bytes in all, when known (0 otherwise)
}

// Response is the answer to a successful call.
type Response struct {
	Status int
	Header http.Header
	// Body is the unread answer of a call made with [WithRaw] (a file's bytes); the caller closes it.
	Body io.ReadCloser
	// Data is the answer's body of any other call.
	Data []byte
	// FromCache is true when the server answered 304 and Data is the body kept from an earlier read.
	FromCache bool
}

// RequestOption changes one call made with [Client.Do].
type RequestOption func(*request)

type request struct {
	method      string
	path        []string
	query       url.Values
	body        any
	raw         io.Reader
	contentType string
	headers     http.Header
	progress    func(UploadProgress)
	wantRaw     bool
	noAuth      bool
	noExpire    bool
	reauth      bool
	noCache     bool
	retry       *Retry
	err         error
}

// WithQuery adds query parameters.
func WithQuery(q url.Values) RequestOption {
	return func(r *request) {
		if r.query == nil {
			r.query = url.Values{}
		}
		for k, vs := range q {
			for _, v := range vs {
				r.query.Add(k, v)
			}
		}
	}
}

// WithHeader sets a header for the call.
func WithHeader(key, value string) RequestOption {
	return func(r *request) { r.headers.Set(key, value) }
}

// WithRawBody sends r as the body as it is (a file's bytes) instead of JSON. contentType defaults
// to application/octet-stream. Without a [io.Seeker] the call is not repeated on a retry.
func WithRawBody(body io.Reader, contentType string) RequestOption {
	return func(r *request) { r.raw, r.contentType = body, contentType }
}

// WithContentType sets the content type of a JSON body, such as application/merge-patch+json.
func WithContentType(contentType string) RequestOption {
	return func(r *request) { r.contentType = contentType }
}

// WithRaw returns the answer unread in [Response.Body], for a file's bytes. The caller closes it.
func WithRaw() RequestOption { return func(r *request) { r.wantRaw = true } }

// WithProgress reports how much of the body has been sent.
func WithProgress(f func(UploadProgress)) RequestOption {
	return func(r *request) { r.progress = f }
}

// WithoutAuth sends no credentials.
func WithoutAuth() RequestOption { return func(r *request) { r.noAuth = true } }

// WithoutExpire keeps a refused session token from ending the session (sign-in calls use it).
func WithoutExpire() RequestOption { return func(r *request) { r.noExpire = true } }

// WithReauth sends the fresh proof of identity that Auth.Reauth gave, while it is good.
func WithReauth() RequestOption { return func(r *request) { r.reauth = true } }

// WithoutCache skips the revalidation cache for this read.
func WithoutCache() RequestOption { return func(r *request) { r.noCache = true } }

// WithRetry overrides the client's retry settings for this call.
func WithRetry(retry Retry) RequestOption { return func(r *request) { r.retry = &retry } }

// Do sends one call to /v1/{realm}/{path…} and returns the answer. It is the extension point other
// code builds on: it adds the credentials, applies retries and the revalidation cache, maps
// failures to [*Error] and reports stale types. path is the segments after the realm; each is
// encoded here. body, when not nil, is sent as JSON; a successful JSON answer is decoded into out
// when out is not nil.
func (c *Client) Do(ctx context.Context, method string, path []string, body, out any, opts ...RequestOption) (*Response, error) {
	r := &request{method: method, path: path, body: body, headers: http.Header{}}
	for _, o := range opts {
		o(r)
	}
	if r.err != nil {
		return nil, r.err
	}
	res, err := c.do(ctx, r)
	if err != nil {
		return nil, c.mapError(r.path, err)
	}
	if out != nil && !r.wantRaw && len(res.Data) > 0 {
		if err := json.Unmarshal(res.Data, out); err != nil {
			return nil, newError(res.Status, "invalid_response", "the answer is not the JSON expected: "+err.Error(), kBackd)
		}
	}
	return res, nil
}

func (c *Client) endpoint(path []string, query url.Values) string {
	var b strings.Builder
	b.WriteString(c.url)
	b.WriteString("/v1/")
	b.WriteString(url.PathEscape(c.realm))
	for _, s := range path {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	if len(query) > 0 {
		b.WriteByte('?')
		b.WriteString(query.Encode())
	}
	return b.String()
}

// credentials returns the Authorization header value ("" for none), whether a session token was
// part of it, and the caller as the cache knows it.
func (c *Client) credentials(ctx context.Context) (header string, session bool, owner string, err error) {
	if c.apiKey != "" {
		return "Bearer " + c.apiKey, false, "key:" + c.apiKey, nil
	}
	token, err := c.store.Get(ctx)
	if err != nil {
		return "", false, "", err
	}
	if token == "" {
		return "", false, "", nil
	}
	return "Bearer " + token, true, "session:" + token, nil
}

func (c *Client) do(ctx context.Context, r *request) (*Response, error) {
	var jsonBody []byte
	if r.body != nil && r.raw == nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, newError(0, "invalid_request", "the body can't be encoded: "+err.Error(), kValidation)
		}
		jsonBody = b
	}
	endpoint := c.endpoint(r.path, r.query)

	headers := http.Header{}
	if r.wantRaw {
		headers.Set("Accept", "*/*")
	} else {
		headers.Set("Accept", "application/json")
	}
	for k, v := range c.opts.Headers {
		headers.Set(k, v)
	}
	for k, vs := range r.headers {
		headers[k] = vs
	}
	switch {
	case r.raw != nil:
		ct := r.contentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		headers.Set("Content-Type", ct)
	case jsonBody != nil:
		ct := r.contentType
		if ct == "" {
			ct = "application/json"
		}
		headers.Set("Content-Type", ct)
	}
	if c.opts.Types != "" {
		headers.Set("X-Backd-Types", c.opts.Types)
	}
	if p := c.proof.Load(); r.reauth && p != nil && p.expires.After(time.Now()) {
		headers.Set("X-Backd-Reauth", p.token)
	}
	var sentSession bool
	var owner string
	if !r.noAuth {
		h, session, o, err := c.credentials(ctx)
		if err != nil {
			return nil, err
		}
		if h != "" {
			headers.Set("Authorization", h)
		}
		sentSession, owner = session, o
	}
	cc := c.cacheFor(r, headers, owner)
	held := cc.lookup(endpoint, headers)

	retry := c.retry
	if r.retry != nil {
		retry = *r.retry
	}
	attempts := 0
	if r.method == http.MethodGet || headers.Get("If-Match") != "" || headers.Get("Idempotency-Key") != "" {
		attempts = max(0, retry.Attempts)
	}
	var rewind func() error
	if r.raw != nil && attempts > 0 {
		if s, ok := r.raw.(io.Seeker); ok {
			start, err := s.Seek(0, io.SeekCurrent)
			if err != nil {
				attempts = 0
			} else {
				rewind = func() error { _, err := s.Seek(start, io.SeekStart); return err }
			}
		} else {
			attempts = 0
		}
	}

	for attempt := 0; ; attempt++ {
		var reqBody io.Reader
		var length int64 = -1
		switch {
		case r.raw != nil:
			reqBody, length = r.raw, readerSize(r.raw)
			if r.progress != nil {
				reqBody = &progressReader{r: r.raw, total: max(length, 0), fn: r.progress}
			}
		case jsonBody != nil:
			reqBody, length = bytes.NewReader(jsonBody), int64(len(jsonBody))
		}
		req, err := http.NewRequestWithContext(ctx, r.method, endpoint, reqBody)
		if err != nil {
			return nil, newError(0, "invalid_request", "the request can't be made: "+err.Error(), kValidation)
		}
		req.Header = headers.Clone()
		if reqBody != nil {
			req.ContentLength = length
			if length == 0 {
				req.Body = http.NoBody
			}
		}
		res, err := c.doer.Do(req)
		if err != nil {
			return nil, networkError(ctx, err)
		}
		if c.opts.Types != "" && res.Header.Get("X-Backd-Types-Stale") == "true" && c.staleReported.CompareAndSwap(false, true) {
			if c.opts.OnStaleTypes != nil {
				c.opts.OnStaleTypes()
			} else {
				slog.Warn("backd: the generated types are out of date with the server: run `backd types` and commit the result")
			}
		}
		if done, out, err := cc.answer(res, endpoint, held, r); done {
			return out, err
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			if r.wantRaw {
				return &Response{Status: res.StatusCode, Header: res.Header, Body: res.Body}, nil
			}
			data, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				return nil, networkError(ctx, err)
			}
			cc.store(endpoint, res, data)
			return &Response{Status: res.StatusCode, Header: res.Header, Data: data}, nil
		}
		apiErr := errorFromResponse(res)
		var e *Error
		if asError(apiErr, &e) && sessionGone(e) && sentSession && !r.noExpire && c.expired != nil {
			c.expired(ctx)
		}
		if e != nil && e.kind.is(kRetryable) && attempt < attempts {
			if rewind != nil {
				if err := rewind(); err != nil {
					return nil, apiErr
				}
			}
			if err := sleep(ctx, backoff(attempt, e.RetryAfter, retry.MaxDelay)); err != nil {
				return nil, networkError(ctx, err)
			}
			continue
		}
		return nil, apiErr
	}
}

// sessionGone reports whether the server no longer accepts the session: it expired or was ended
// (unauthenticated), or it belongs to no organization in a realm that has them (session_unscoped).
func sessionGone(e *Error) bool { return e.Code == "unauthenticated" || e.Code == "session_unscoped" }

// backoff is the wait before the next try: what the server asked, else exponential from 500ms; never more than max.
func backoff(attempt int, retryAfter, maxDelay time.Duration) time.Duration {
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	d := retryAfter
	if d == 0 {
		d = 500 * time.Millisecond << min(attempt, 20)
	}
	return min(d, maxDelay)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readerSize is the number of bytes r will deliver, or -1 when it isn't known.
func readerSize(r io.Reader) int64 {
	switch v := r.(type) {
	case interface{ Len() int }:
		return int64(v.Len())
	case *io.SectionReader:
		return v.Size()
	case *os.File:
		if fi, err := v.Stat(); err == nil && fi.Mode().IsRegular() {
			pos, _ := v.Seek(0, io.SeekCurrent)
			return fi.Size() - pos
		}
	}
	return -1
}

type progressReader struct {
	r      io.Reader
	loaded int64
	total  int64
	fn     func(UploadProgress)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.loaded += int64(n)
		p.fn(UploadProgress{Loaded: p.loaded, Total: p.total})
	}
	return n, err
}

// mapError records where the call went and gives Options.MapError its say.
func (c *Client) mapError(path []string, err error) error {
	var e *Error
	if errors.As(err, &e) && e.Path == "" {
		e.Path = strings.Join(path, "/")
	}
	if c.opts.MapError != nil {
		return c.opts.MapError(err)
	}
	return err
}
