package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fernandezvara/backd/internal/buildinfo"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/registry"
)

const ExecutorHelp = `Runs server-side functions for backd: each call in its own Deno process,
with only the function's permissions and limits. Needs Deno ` + registry.DenoVersion + `
(the backd-executor image has it), never CONFIG_DIR or MongoDB.

Environment:
  BACKD_EXECUTOR_TOKEN     required: the secret shared with backd (32+ characters)
  EXECUTOR_ADDR            listen address (default :9100)
  EXECUTOR_DIR             writable directory for the bundle cache
                           (default: a backd-executor directory in the temp dir)
  EXECUTOR_MAX_PROCESSES   functions running at once (default 64)
  EXECUTOR_PROXY           backd-server egress's base URL (e.g. http://backd-egress:3128);
                           without it, function processes get no outbound
                           network beyond --allow-net on this host
  BACKD_EGRESS_KEY         required with EXECUTOR_PROXY (32+ characters): signs
                           each invocation's egress credentials
  DENO                     the Deno binary (default deno on the PATH)
  LOG_LEVEL                debug, info, warn or error (default info)

It must only be reachable by backd, and reach only backd's internal
listener and backd-server egress.
`

// executorSupported is false where the executor can't enforce its limits
// (anything but Linux); a variable so a test can cover the message.
var executorSupported = executor.Supported

const executorUnsupported = "backd-server executor runs only on Linux: it needs process limits and /proc that " +
	"other systems don't have. Run it with the backd-executor container image (or under WSL2 on Windows); " +
	"serve, worker, egress and the command line run on any system"

func ServeExecutor(getenv func(string) string, stderr io.Writer) error {
	if !executorSupported {
		return errors.New(executorUnsupported)
	}
	level := slog.LevelInfo
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", v)
		}
	}
	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
	addr := orDefault(getenv("EXECUTOR_ADDR"), ":9100")
	dir := orDefault(getenv("EXECUTOR_DIR"), filepath.Join(os.TempDir(), "backd-executor"))
	maxProcs := 64
	if v := getenv("EXECUTOR_MAX_PROCESSES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("EXECUTOR_MAX_PROCESSES must be a positive integer, got %q", v)
		}
		maxProcs = n
	}
	deno := orDefault(getenv("DENO"), "deno")
	ctx := context.Background()
	v, err := functions.DenoVersion(ctx, deno)
	if err != nil {
		return err
	}
	if v != registry.DenoVersion {
		return fmt.Errorf("this backd runs functions with Deno %s, but %s is %s", registry.DenoVersion, deno, v)
	}
	m, stopMetrics, err := startMetrics(getenv, log, addr)
	if err != nil {
		return err
	}
	defer stopMetrics()
	proxy := getenv("EXECUTOR_PROXY")
	egressKey := getenv("BACKD_EGRESS_KEY")
	ex, err := executor.New(executor.Config{
		Deno: deno, Dir: dir, Token: getenv("BACKD_EXECUTOR_TOKEN"), MaxProcesses: maxProcs,
		Proxy: proxy, EgressKey: []byte(egressKey), Log: log, Metrics: m,
	})
	if err != nil {
		return fmt.Errorf("%w (BACKD_EXECUTOR_TOKEN, or with EXECUTOR_PROXY set, BACKD_EGRESS_KEY)", err)
	}
	log.Info("executor ready", "version", buildinfo.Get(), "deno", v, "max_processes", maxProcs, "dir", dir, "proxy", proxy)

	srv := &http.Server{
		Handler:           ex.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Invocations carry their own deadlines (up to 24h for async jobs).
		IdleTimeout: 120 * time.Second,
	}
	// Running functions stop with their process group when the executor stops; runServer
	// gives short ones a moment to finish.
	return runServer(ctx, srv, addr, log)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
