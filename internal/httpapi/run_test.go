package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// startRun serves h via Run on a random port and returns its URL, a cancel
// func that triggers shutdown, and a channel with Run's result.
func startRun(t *testing.T, h http.Handler, shutdownTimeout time.Duration) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, &http.Server{Handler: h}, ln, shutdownTimeout, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()
	return "http://" + ln.Addr().String(), cancel, done
}

func TestRunDrainsInFlightRequests(t *testing.T) {
	const inFlight = 10
	var started sync.WaitGroup
	started.Add(inFlight)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Done()
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	})
	url, shutdown, done := startRun(t, h, 5*time.Second)

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	results := make(chan string, inFlight)
	for range inFlight {
		go func() {
			resp, err := client.Get(url)
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			results <- string(body)
		}()
	}
	started.Wait() // all requests are being handled
	shutdown()     // what SIGTERM does

	for range inFlight {
		if got := <-results; got != "done" {
			t.Errorf("in-flight request = %q, want it to complete", got)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if _, err := client.Get(url); err == nil {
		t.Error("new connection accepted after shutdown")
	}
}

func TestRunShutdownTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var started sync.WaitGroup
	started.Add(1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Done()
		<-release
	})
	url, shutdown, done := startRun(t, h, 100*time.Millisecond)
	go func() { _, _ = http.Get(url) }()
	started.Wait()
	shutdown()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not finish within 100ms") {
			t.Errorf("Run = %v, want shutdown timeout error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not give up after the shutdown timeout")
	}
}

func TestServerTimeouts(t *testing.T) {
	srv := &http.Server{}
	ServerTimeouts(srv, 10*time.Second)
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.IdleTimeout == 0 || srv.WriteTimeout != 30*time.Second {
		t.Errorf("timeouts = %+v", srv)
	}
	ServerTimeouts(srv, time.Minute)
	if srv.WriteTimeout != 70*time.Second {
		t.Errorf("write timeout = %s, want op timeout + 10s", srv.WriteTimeout)
	}
}
