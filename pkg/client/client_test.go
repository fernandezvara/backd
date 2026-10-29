package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{Realm: "r"}); err == nil {
		t.Error("URL is required")
	}
	if _, err := New(Options{URL: "http://x"}); err == nil {
		t.Error("Realm is required")
	}
	if _, err := New(Options{URL: "nope", Realm: "r"}); err == nil {
		t.Error("URL must be an address")
	}
}

func TestDoBuildsThePathAndSendsJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/r/main/a%20b/x%2Fy" || r.URL.RawQuery != "limit=2" {
			t.Errorf("url = %s", r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || string(b) != `{"n":1}` || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("%s %s %s", r.Method, b, r.Header)
		}
		if r.Header.Get("X-Extra") != "1" || r.Header.Get("X-One") != "2" {
			t.Errorf("headers = %v", r.Header)
		}
		w.Write([]byte(`{"ok":true}`))
	}, func(o *Options) { o.Headers = map[string]string{"X-Extra": "1"} })
	var out struct{ OK bool }
	res, err := c.Do(bg, "POST", []string{"main", "a b", "x/y"}, map[string]int{"n": 1}, &out,
		WithQuery(map[string][]string{"limit": {"2"}}), WithHeader("X-One", "2"))
	if err != nil || !out.OK || res.Status != 200 {
		t.Fatalf("%v %+v %+v", err, out, res)
	}
}

func TestCredentials(t *testing.T) {
	var got string
	h := func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("Authorization") }
	c := newTestClient(t, h, func(o *Options) { o.APIKey = "bdk_1" })
	c.Do(bg, "GET", []string{"a"}, nil, nil)
	if got != "Bearer bdk_1" {
		t.Errorf("api key: %q", got)
	}
	c.Do(bg, "GET", []string{"a"}, nil, nil, WithoutAuth())
	if got != "" {
		t.Errorf("WithoutAuth: %q", got)
	}
	s := NewMemoryStorage()
	c = newTestClient(t, h, func(o *Options) { o.Storage = s })
	c.Do(bg, "GET", []string{"a"}, nil, nil)
	if got != "" {
		t.Errorf("signed out: %q", got)
	}
	s.Set(bg, "tok")
	c.Do(bg, "GET", []string{"a"}, nil, nil)
	if got != "Bearer tok" {
		t.Errorf("session: %q", got)
	}
}

func TestSessionRefusedCallsTheExpiryHook(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"code":"unauthenticated","message":"expired"}}`))
	}, func(o *Options) { o.Storage = NewMemoryStorage() })
	c.store.Set(bg, "tok")
	var n int
	c.expired = func(context.Context) { n++ }
	if _, err := c.Do(bg, "GET", []string{"a"}, nil, nil); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err = %v", err)
	}
	c.Do(bg, "GET", []string{"a"}, nil, nil, WithoutExpire())
	c.Do(bg, "GET", []string{"a"}, nil, nil, WithoutAuth())
	if n != 1 {
		t.Errorf("expired called %d times; want 1 (not with WithoutExpire or WithoutAuth)", n)
	}
}

func TestRetries(t *testing.T) {
	var calls atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
			return
		}
		w.Write([]byte(`{}`))
	}
	c := newTestClient(t, h, func(o *Options) { o.Retry = Retry{Attempts: 2, MaxDelay: time.Millisecond} })

	if _, err := c.Do(bg, "GET", []string{"a"}, nil, nil); err != nil || calls.Load() != 3 {
		t.Errorf("GET: %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.Do(bg, "POST", []string{"a"}, map[string]int{}, nil); !errors.Is(err, ErrRetryable) || calls.Load() != 1 {
		t.Errorf("a plain write is not repeated: %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.Do(bg, "PUT", []string{"a"}, map[string]int{}, nil, WithHeader("If-Match", `"1"`)); err != nil || calls.Load() != 3 {
		t.Errorf("a write with If-Match: %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.Do(bg, "GET", []string{"a"}, nil, nil, WithRetry(Retry{})); err == nil || calls.Load() != 1 {
		t.Errorf("WithRetry(0): %v after %d calls", err, calls.Load())
	}
}

func TestRetryGivesUpWithTheLastError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(503)
	}, func(o *Options) { o.Retry = Retry{Attempts: 1, MaxDelay: time.Millisecond} })
	_, err := c.Do(bg, "GET", []string{"a"}, nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Status != 503 || e.Code != "http_503" || !errors.Is(err, ErrRetryable) {
		t.Errorf("err = %#v", err)
	}
}

func TestRetryWaitIsCancelable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	}, func(o *Options) { o.Retry = Retry{Attempts: 3, MaxDelay: time.Minute} })
	ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	_, err := c.Do(ctx, "GET", []string{"a"}, nil, nil)
	if !errors.Is(err, ErrNetwork) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestNetworkErrors(t *testing.T) {
	c, _ := New(Options{URL: "http://127.0.0.1:1", Realm: "r"})
	_, err := c.Do(bg, "GET", []string{"a"}, nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != "network_error" || !errors.Is(err, ErrNetwork) || errors.Is(err, ErrNotFound) {
		t.Errorf("err = %#v", err)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err = c.Do(ctx, "GET", []string{"a"}, nil, nil)
	if !errors.As(err, &e) || e.Code != "aborted" || !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %#v", err)
	}
}

func TestRawBodyAndProgress(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "0123456789" || r.Header.Get("Content-Type") != "image/png" || r.ContentLength != 10 {
			t.Errorf("body %q %s %d", b, r.Header.Get("Content-Type"), r.ContentLength)
		}
		w.WriteHeader(204)
	})
	var last UploadProgress
	_, err := c.Do(bg, "PUT", []string{"f"}, nil, nil, WithRawBody(strings.NewReader("0123456789"), "image/png"),
		WithProgress(func(p UploadProgress) { last = p }))
	if err != nil || last != (UploadProgress{Loaded: 10, Total: 10}) {
		t.Errorf("%v %+v", err, last)
	}
}

func TestRawAnswerIsReturnedUnread(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "*/*" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Write([]byte("bytes"))
	})
	res, err := c.Do(bg, "GET", []string{"f"}, nil, nil, WithRaw())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if b, _ := io.ReadAll(res.Body); string(b) != "bytes" {
		t.Errorf("%q", b)
	}
}

func TestStaleTypesAreReportedOnce(t *testing.T) {
	var sent string
	var n int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("X-Backd-Types")
		w.Header().Set("X-Backd-Types-Stale", "true")
	}, func(o *Options) { o.Types = "abc"; o.OnStaleTypes = func() { n++ } })
	c.Do(bg, "GET", []string{"a"}, nil, nil)
	c.Do(bg, "GET", []string{"a"}, nil, nil)
	if sent != "abc" || n != 1 {
		t.Errorf("sent %q, hook called %d times", sent, n)
	}
}

func TestNoTypesHeaderWithoutTypes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["X-Backd-Types"]; ok {
			t.Error("X-Backd-Types sent")
		}
		w.Header().Set("X-Backd-Types-Stale", "true")
	})
	c.Do(bg, "GET", []string{"a"}, nil, nil)
}

func TestOrgClientsKeepATokenEach(t *testing.T) {
	c, _ := New(Options{URL: "http://x", Realm: "r"})
	a, err := c.Org(" ACME ")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.Org("acme")
	o, _ := c.Org("other")
	if a != b || a == o || a.OrgRef() != "acme" || a.Realm() != "r" {
		t.Errorf("org clients: %p %p %p %q", a, b, o, a.OrgRef())
	}
	a.store.Set(bg, "ta")
	if tok, _ := o.store.Get(bg); tok != "" {
		t.Errorf("the other organization saw the token %q", tok)
	}
	if tok, _ := c.store.Get(bg); tok != "" {
		t.Errorf("the root saw the token %q", tok)
	}
	if again, _ := a.Org("other"); again != o {
		t.Error("Org from an org client reaches the same clients")
	}
	if _, err := c.Org(" "); err == nil {
		t.Error("an empty organization is refused")
	}
	d, _ := New(Options{URL: "http://x", Realm: "r", Storage: tokenOnly{}})
	if _, err := d.Org("acme"); err == nil {
		t.Error("a storage without slots is refused")
	}
}

type tokenOnly struct{ TokenStorage }

func TestCloseRunsClosersOnce(t *testing.T) {
	c, _ := New(Options{URL: "http://x", Realm: "r"})
	var n int
	c.OnClose(func() { n++ })
	c.Close()
	c.Close()
	if n != 1 {
		t.Errorf("closer ran %d times", n)
	}
}

type mappedError struct{ error }

func (m mappedError) Unwrap() error { return m.error }

func TestMapErrorAndErrorPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"not_found","message":"no"}}`)
	}, func(o *Options) {
		o.MapError = func(err error) error { return mappedError{fmt.Errorf("hint: %w", err)} }
	})
	_, err := c.Do(bg, "GET", []string{"_admin", "jobs"}, nil, nil)
	var m mappedError
	var e *Error
	if !errors.As(err, &m) || !errors.Is(err, ErrNotFound) || !errors.As(err, &e) || e.Path != "_admin/jobs" || !strings.HasPrefix(err.Error(), "hint: ") {
		t.Errorf("%#v", err)
	}
	// without a mapper the error is the client's own, with its path
	c2 := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	_, err = c2.Do(bg, "GET", []string{"a", "b"}, nil, nil)
	if !errors.As(err, &e) || e.Path != "a/b" {
		t.Errorf("%#v", err)
	}
}
