// Package executor runs server-side functions: `backd executor` is a
// supervisor that runs each invocation in its own Deno process, with only
// the function's permissions and limits, and returns its output.
//
// It never reads CONFIG_DIR or MongoDB. backd sends it an invocation (the
// bundle's hash and URL, limits, network allowlist and an envelope with
// the input, caller, secrets and callback token); it fetches the bundle
// from backd by hash, runs it through runner.js and reports the result.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	jsclient "github.com/fernandezvara/backd/clients/js"
	"github.com/fernandezvara/backd/internal/egress"
	"github.com/fernandezvara/backd/internal/metrics"
)

//go:embed runner.js
var runnerJS []byte

// Result statuses.
const (
	StatusOK            = "ok"
	StatusFunctionError = "function_error" // the function threw ctx.error(4xx, …)
	StatusError         = "error"          // the function threw anything else
	StatusTimeout       = "timeout"
	StatusMemory        = "memory"
	StatusCPU           = "cpu"
	StatusCrash         = "crash"            // the process ended without a result
	StatusOutputTooBig  = "output_too_large" // more than max_output
	StatusBusy          = "busy"             // the executor is at its process limit
	StatusBundle        = "bundle"           // the bundle couldn't be fetched or verified
	StatusCancelled     = "cancelled"        // an administrator cancelled the job while it ran (the worker stopped the run)
)

const (
	// rssOverhead is what a Deno process uses beyond the function's heap
	// (runtime, runner, client); the RSS limit is memory + this.
	rssOverhead = 128 << 20
	maxLogBytes = 64 << 10
	// maxRequestBytes bounds an invocation request (input included).
	maxRequestBytes = 64 << 20
)

// InvokeRequest is one invocation, sent by backd.
type InvokeRequest struct {
	Function  string   `json:"function"` // <realm>/<database>/<name>, for logs
	Bundle    Bundle   `json:"bundle"`
	TimeoutMS int64    `json:"timeout_ms"`
	MemoryMB  int64    `json:"memory_mb"` // V8 heap
	MaxOutput int64    `json:"max_output"`
	Network   []string `json:"network,omitempty"` // host or host:port
	Envelope  Envelope `json:"envelope"`
}

// Bundle names a function's bundle: backd serves it at URL.
type Bundle struct {
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

// Envelope is what the function's process receives on stdin.
type Envelope struct {
	// Mode tells the runner how to shape ctx and interpret the
	// handler's return value: "sync"/"async" use Input/Output; "webhook"
	// uses Webhook/the Result's Webhook field instead.
	Mode    string            `json:"mode,omitempty"`
	Input   json.RawMessage   `json:"input,omitempty"`
	Webhook *WebhookRequest   `json:"webhook,omitempty"`
	User    json.RawMessage   `json:"user,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
	// Mask lists values that must never appear in the function's logs or
	// error message (such as a token in a link), without being given to it
	// as secrets: the executor replaces them like it does secrets' values.
	Mask           []string  `json:"mask,omitempty"`
	Callback       *Callback `json:"callback,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	RequestID      string    `json:"request_id,omitempty"`
}

// WebhookRequest is a webhook call's raw body and headers (roadmap
// F13), for a function to verify its sender's signature itself. The
// body is treated as UTF-8 text: real-world webhook senders (Stripe,
// GitHub, and similar) all send JSON.
type WebhookRequest struct {
	Body    string      `json:"body"`
	Headers http.Header `json:"headers,omitempty"`
}

// Callback is how the function reaches backd: ctx.db with Token (as the
// caller), ctx.admin.db with AdminToken (functions with admin: true).
type Callback struct {
	URL        string `json:"url"`
	Realm      string `json:"realm"`
	Database   string `json:"database,omitempty"` // the function's own database, for ctx.call
	Token      string `json:"token,omitempty"`
	AdminToken string `json:"admin_token,omitempty"`
	Email      bool   `json:"email,omitempty"` // ctx.email.send
}

// Result is how an invocation ended.
type Result struct {
	Status        string           `json:"status"`
	Output        json.RawMessage  `json:"output,omitempty"`
	Webhook       *WebhookResponse `json:"webhook,omitempty"`
	FunctionError *FunctionError   `json:"function_error,omitempty"`
	// Message explains other failures, for backd's logs only (it may hold
	// a stack trace); callers never see it.
	Message    string    `json:"message,omitempty"`
	Logs       []LogLine `json:"logs,omitempty"`
	DurationMS int64     `json:"duration_ms"`
}

// WebhookResponse is a webhook function's own answer to its caller
// (roadmap F13): unlike sync/async, the function sets the HTTP status
// and body itself, not just a JSON value.
type WebhookResponse struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers,omitempty"`
}

// FunctionError is an error the function returns to its caller.
type FunctionError struct {
	Status  int             `json:"status"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

// LogLine is one line the function logged, secrets masked.
type LogLine struct {
	Level string `json:"level"`
	Line  string `json:"line"`
}

// Config configures an Executor.
type Config struct {
	Deno         string // the deno binary; default "deno"
	Dir          string // writable working directory (runner, bundle cache)
	Token        string // shared with backd: authenticates both directions
	MaxProcesses int    // function processes at once; default 64
	// Proxy is backd egress's base URL (e.g. http://backd-egress:3128), set
	// as HTTP(S)_PROXY for function processes with a per-invocation token
	// (EgressKey) embedded as its credentials, scoped to that call's
	// allowed hosts. Both empty: functions get no outbound network at all
	// beyond what --allow-net itself permits on this host.
	Proxy     string
	EgressKey []byte
	// Metrics counts runs; nil turns it off.
	Metrics *metrics.Metrics
	Log     *slog.Logger
	HTTP    *http.Client // for bundle fetches
}

// Executor runs invocations.
type Executor struct {
	cfg     Config
	runner  string // path of runner.js
	bundles string // cache directory
	denoDir string
	slots   chan struct{}
	fetchMu sync.Mutex
}

// ErrUnsupportedPlatform means this system can't enforce a function's limits.
var ErrUnsupportedPlatform = errors.New("the functions executor runs only on Linux: it needs process limits and /proc, which other systems don't have")

// New prepares the working directory: the runner and the JS client it
// imports, and the bundle cache. It refuses to run where !Supported.
func New(cfg Config) (*Executor, error) {
	if !Supported {
		return nil, ErrUnsupportedPlatform
	}
	if cfg.Deno == "" {
		cfg.Deno = "deno"
	}
	if cfg.MaxProcesses <= 0 {
		cfg.MaxProcesses = 64
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if len(cfg.Token) < 32 {
		return nil, errors.New("the executor token must be at least 32 characters")
	}
	if cfg.Proxy != "" && len(cfg.EgressKey) < 32 {
		return nil, errors.New("the egress key must be at least 32 bytes when a proxy is set")
	}
	e := &Executor{cfg: cfg, slots: make(chan struct{}, cfg.MaxProcesses)}
	e.bundles = filepath.Join(cfg.Dir, "bundles")
	e.denoDir = filepath.Join(cfg.Dir, "deno")
	for _, d := range []string{e.bundles, e.denoDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	// A fresh runtime directory per start: this binary's runner and client.
	runtime, err := os.MkdirTemp(cfg.Dir, "runtime-")
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(runtime, "client"), 0o755); err != nil {
		return nil, err
	}
	e.runner = filepath.Join(runtime, "runner.js")
	if err := os.WriteFile(e.runner, runnerJS, 0o444); err != nil {
		return nil, err
	}
	err = fs.WalkDir(jsclient.Sources, "src", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := jsclient.Sources.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(runtime, "client", filepath.Base(path)), data, 0o444)
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

// Handler serves POST /invoke (with the token) and GET /healthz.
func (e *Executor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("POST /invoke", func(w http.ResponseWriter, r *http.Request) {
		if !e.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req InvokeRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes)).Decode(&req); err != nil {
			http.Error(w, "invalid invocation: "+err.Error(), http.StatusBadRequest)
			return
		}
		res := e.Invoke(r.Context(), req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	})
	return mux
}

func (e *Executor) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(token), []byte(e.cfg.Token)) == 1
}

// Invoke runs one invocation to its end. ctx ending (the caller went away)
// kills the function.
func (e *Executor) Invoke(ctx context.Context, req InvokeRequest) Result {
	start := time.Now()
	res := e.invoke(ctx, req)
	res.DurationMS = time.Since(start).Milliseconds()
	if res.Status != StatusBusy { // busy was counted when it was refused
		e.cfg.Metrics.ExecutorFinished(res.Status, time.Since(start), true)
	}
	attrs := []any{"function", req.Function, "status", res.Status, "duration_ms", res.DurationMS, "request_id", req.Envelope.RequestID}
	if res.Message != "" && res.Status != StatusOK {
		attrs = append(attrs, "message", res.Message)
	}
	e.cfg.Log.Info("invocation", attrs...)
	return res
}

func (e *Executor) invoke(ctx context.Context, req InvokeRequest) Result {
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		e.cfg.Metrics.ExecutorFinished(StatusBusy, 0, false)
		return Result{Status: StatusBusy, Message: fmt.Sprintf("the executor runs its maximum of %d functions", e.cfg.MaxProcesses)}
	}
	e.cfg.Metrics.ExecutorStarted()
	bundle, err := e.bundle(ctx, req.Bundle)
	if err != nil {
		return Result{Status: StatusBundle, Message: err.Error()}
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || req.MemoryMB <= 0 || req.MaxOutput <= 0 {
		return Result{Status: StatusError, Message: "invalid limits"}
	}
	allow := slicesClone(req.Network)
	if cb := req.Envelope.Callback; cb != nil {
		u, err := url.Parse(cb.URL)
		if err != nil || u.Host == "" {
			return Result{Status: StatusError, Message: "invalid callback URL"}
		}
		allow = append(allow, hostPort(u))
	}
	return e.run(ctx, bundle, req, timeout, allow)
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// proxyURL returns e.cfg.Proxy with a fresh egress token embedded as its
// credentials (Deno sends URL userinfo as the proxy's Proxy-Authorization),
// scoped to allow and expiring shortly after timeout — the same hosts
// --allow-net already permits this process, so the proxy enforces that
// allowlist too, in addition to resolving and blocking private addresses.
//
// The password must be present, even empty: a bare user@host userinfo (no
// ':', as url.User alone produces) isn't recognized as carrying
// credentials by Deno's proxy URL parsing, which then silently connects
// directly instead of through the proxy.
func (e *Executor) proxyURL(allow []string, timeout time.Duration) (string, error) {
	u, err := url.Parse(e.cfg.Proxy)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid egress proxy URL %q", e.cfg.Proxy)
	}
	token := egress.Sign(e.cfg.EgressKey, egress.Claims{
		Hosts:   allow,
		Expires: time.Now().Add(timeout + 5*time.Second),
	})
	u.User = url.UserPassword(token, "")
	return u.String(), nil
}

func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" {
		return u.Hostname() + ":443"
	}
	return u.Hostname() + ":80"
}

// bundle returns the cached bundle file of b, fetching it from backd first
// if needed; its SHA-256 must match.
func (e *Executor) bundle(ctx context.Context, b Bundle) (string, error) {
	if len(b.SHA256) != 64 || strings.Trim(b.SHA256, "0123456789abcdef") != "" {
		return "", errors.New("invalid bundle hash")
	}
	path := filepath.Join(e.bundles, b.SHA256+".js")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	e.fetchMu.Lock()
	defer e.fetchMu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.cfg.Token)
	res, err := e.cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch bundle: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch bundle: %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxRequestBytes))
	if err != nil {
		return "", fmt.Errorf("fetch bundle: %w", err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != b.SHA256 {
		return "", errors.New("fetched bundle doesn't match its hash")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o444); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// run starts the function's process and supervises it to its end.
func (e *Executor) run(ctx context.Context, bundle string, req InvokeRequest, timeout time.Duration, allow []string) Result {
	args := []string{"run", "--no-prompt", "--cached-only", "--no-config", "--no-lock",
		"--allow-read=" + bundle,
		fmt.Sprintf("--v8-flags=--max-old-space-size=%d,--single-threaded", req.MemoryMB)}
	if len(allow) > 0 {
		args = append(args, "--allow-net="+strings.Join(allow, ","))
	}
	args = append(args, e.runner, bundle)
	cmd := exec.Command(e.cfg.Deno, args...)
	cmd.Dir = filepath.Dir(e.runner)
	cmd.Env = []string{"DENO_DIR=" + e.denoDir, "NO_COLOR=1", "DENO_NO_UPDATE_CHECK=1"}
	if v := os.Getenv("LD_LIBRARY_PATH"); v != "" {
		cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH="+v)
	}
	if e.cfg.Proxy != "" {
		proxyURL, err := e.proxyURL(allow, timeout)
		if err != nil {
			return Result{Status: StatusError, Message: "egress credentials: " + err.Error()}
		}
		cmd.Env = append(cmd.Env, "HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL)
	}
	cmd.SysProcAttr = processAttr()
	stdin, _ := cmd.StdinPipe()
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return Result{Status: StatusError, Message: "start deno: " + err.Error()}
	}
	pid := cmd.Process.Pid
	cpu := uint64(math.Ceil(timeout.Seconds())) + 1
	limitProcess(pid, cpu)
	kill := func() { killGroup(pid) }

	var stderr bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		io.Copy(limitWriter{&stderr, maxLogBytes}, stderrPipe)
		close(stderrDone)
	}()

	// The protocol: ready, envelope, result.
	type line struct {
		data []byte
		err  error
	}
	lines := make(chan line, 2)
	maxLine := int(req.MaxOutput) + 4096
	go func() {
		r := bufio.NewReaderSize(stdoutPipe, 64<<10)
		for i := 0; i < 2; i++ {
			data, err := readLine(r, maxLine)
			lines <- line{data, err}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	watch := time.NewTicker(50 * time.Millisecond)
	defer watch.Stop()
	rssLimit := req.MemoryMB<<20 + rssOverhead

	var res Result
	state := "ready"
	ended := false
	for !ended {
		select {
		case l := <-lines:
			switch {
			case errors.Is(l.err, errLineTooLong):
				res = Result{Status: StatusOutputTooBig, Message: fmt.Sprintf("output over max_output (%d bytes)", req.MaxOutput)}
				ended = true
			case l.err != nil:
				res = Result{Status: StatusCrash, Message: "the process ended without a result"}
				ended = true
			case state == "ready":
				var ready struct {
					Ready bool   `json:"ready"`
					Error string `json:"error"`
				}
				if json.Unmarshal(l.data, &ready) != nil || !ready.Ready {
					res = Result{Status: StatusError, Message: "load failed: " + ready.Error}
					ended = true
					break
				}
				env, _ := json.Marshal(req.Envelope)
				stdin.Write(append(env, '\n'))
				stdin.Close()
				state = "running"
			default:
				res = parseResult(l.data)
				switch {
				case res.Status == StatusOK && res.Webhook != nil && int64(len(res.Webhook.Body)) > req.MaxOutput:
					res = Result{Status: StatusOutputTooBig, Message: fmt.Sprintf("webhook body of %d bytes over max_output (%d bytes)", len(res.Webhook.Body), req.MaxOutput)}
				case res.Status == StatusOK && res.Webhook == nil && int64(len(res.Output)) > req.MaxOutput:
					res = Result{Status: StatusOutputTooBig, Message: fmt.Sprintf("output of %d bytes over max_output (%d bytes)", len(res.Output), req.MaxOutput)}
				}
				ended = true
			}
		case <-deadline.C:
			res = Result{Status: StatusTimeout, Message: fmt.Sprintf("stopped after %s", timeout)}
			ended = true
		case <-ctx.Done():
			res = Result{Status: StatusTimeout, Message: "the caller went away"}
			ended = true
		case <-watch.C:
			if rss := rssOf(pid); rss > rssLimit {
				res = Result{Status: StatusMemory, Message: fmt.Sprintf("memory limit: %d MiB resident", rss>>20)}
				ended = true
			}
		}
	}
	kill()
	waitErr := cmd.Wait()
	<-stderrDone
	if res.Status == StatusCrash {
		res = crashReason(cmd, waitErr, stderr.String(), cpu)
	}
	masked := maskValues(req.Envelope)
	res.Logs = parseLogs(stderr.String(), masked)
	if res.Status != StatusOK {
		res.Message = mask(res.Message, masked)
	}
	return res
}

var errLineTooLong = errors.New("line too long")

// readLine reads one line of at most max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, errLineTooLong
		}
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

func parseResult(data []byte) Result {
	var r struct {
		OK            bool             `json:"ok"`
		Output        json.RawMessage  `json:"output"`
		Webhook       *WebhookResponse `json:"webhook"`
		FunctionError *FunctionError   `json:"function_error"`
		Error         string           `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{Status: StatusError, Message: "unreadable result"}
	}
	switch {
	case r.OK && r.Webhook != nil:
		return Result{Status: StatusOK, Webhook: r.Webhook}
	case r.OK:
		if len(r.Output) == 0 {
			r.Output = json.RawMessage("null")
		}
		return Result{Status: StatusOK, Output: r.Output}
	case r.FunctionError != nil:
		return Result{Status: StatusFunctionError, FunctionError: r.FunctionError}
	default:
		return Result{Status: StatusError, Message: r.Error}
	}
}

// crashReason tells why a process ended without a result.
func crashReason(cmd *exec.Cmd, waitErr error, stderr string, cpuLimit uint64) Result {
	if ps := cmd.ProcessState; ps != nil {
		if used, ok := cpuUsed(ps); ok && used >= time.Duration(cpuLimit)*time.Second-500*time.Millisecond {
			return Result{Status: StatusCPU, Message: fmt.Sprintf("CPU limit: %s used", used.Round(time.Millisecond))}
		}
	}
	if strings.Contains(stderr, "out of memory") || strings.Contains(stderr, "heap limit") {
		return Result{Status: StatusMemory, Message: "memory limit: the JavaScript heap is full"}
	}
	msg := "the process ended without a result"
	if waitErr != nil {
		msg += ": " + waitErr.Error()
	}
	if tail := lastLines(stderr, 5); tail != "" {
		msg += "\n" + tail
	}
	return Result{Status: StatusCrash, Message: msg}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// parseLogs turns the captured stderr into log lines, secrets masked.
// Lines that aren't the runner's JSON (Deno's own messages) are errors.
func parseLogs(s string, masked []string) []LogLine {
	var out []LogLine
	for _, raw := range strings.Split(s, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var l LogLine
		if json.Unmarshal([]byte(raw), &l) != nil || l.Level == "" {
			l = LogLine{Level: "error", Line: raw}
		}
		l.Line = mask(l.Line, masked)
		out = append(out, l)
	}
	if len(s) >= maxLogBytes {
		out = append(out, LogLine{Level: "warn", Line: fmt.Sprintf("logs truncated at %d KiB", maxLogBytes>>10)})
	}
	return out
}

// maskValues are the exact values to hide: the function's secrets and the
// extra values backd asked to mask.
func maskValues(env Envelope) []string {
	out := slices.Clone(env.Mask)
	for _, v := range env.Secrets {
		out = append(out, v)
	}
	return out
}

// mask replaces every one of the values with ***.
func mask(s string, values []string) string {
	for _, v := range values {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

type limitWriter struct {
	b   *bytes.Buffer
	max int
}

func (w limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.b.Len(); room > 0 {
		w.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
