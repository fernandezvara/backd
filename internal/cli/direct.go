package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// inProcess is an http.RoundTripper that hands requests to a handler without a network. The
// response body streams: the handler runs on its own goroutine and writes into a pipe.
type inProcess struct{ h http.Handler }

func (p inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	r := req.Clone(req.Context())
	r.RemoteAddr = "127.0.0.1:0"
	r.RequestURI = req.URL.RequestURI()
	if r.Body == nil {
		r.Body = http.NoBody
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					_ = pw.CloseWithError(errors.New("the stream was cut"))
				} else {
					_ = pw.CloseWithError(fmt.Errorf("handler panic: %v", rec))
				}
				w.WriteHeader(http.StatusInternalServerError) // only if nothing was sent yet
				return
			}
			w.WriteHeader(http.StatusOK) // a handler that wrote nothing
			_ = pw.Close()
		}()
		p.h.ServeHTTP(w, r)
	}()
	select {
	case <-w.ready:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	resp := &http.Response{
		Status: fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)), StatusCode: w.status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: w.header.Clone(), Body: pr, Request: req, ContentLength: -1,
	}
	return resp, nil
}

// pipeWriter is the ResponseWriter of an in-process request.
type pipeWriter struct {
	header http.Header
	pw     *io.PipeWriter
	status int
	once   sync.Once
	ready  chan struct{}
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status = status
		close(w.ready)
	})
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(p)
}

func (w *pipeWriter) Flush() {}
