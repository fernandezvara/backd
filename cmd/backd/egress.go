package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fernandezvara/backd/internal/egress"
)

const egressHelp = `Runs backd's egress role: the only way a function process reaches the
network. An HTTP forward/CONNECT proxy that resolves every target itself
and refuses loopback, private, link-local (metadata), CGNAT, multicast and
unspecified addresses; each function process authenticates with a token
scoped to its own invocation (minted by ` + "`backd executor`" + `), so the proxy
enforces that invocation's allowlist too.

Environment:
  BACKD_EGRESS_KEY      required: the secret shared with the executor
                        (32+ characters), verifies each process's token
  EGRESS_ADDR           listen address (default :3128)
  EGRESS_ALLOW_PRIVATE  comma-separated host:port exceptions to the
                        private-address block (backd's callback listener)
  LOG_LEVEL             debug, info, warn or error (default info)

It must be the executor network's only route out; MongoDB and backd's
other endpoints must stay unreachable from it (network placement, not
something this process enforces).
`

func serveEgress(getenv func(string) string, stderr io.Writer) error {
	level := slog.LevelInfo
	if v := getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", v)
		}
	}
	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level}))
	key := getenv("BACKD_EGRESS_KEY")
	if len(key) < 32 {
		return errors.New("BACKD_EGRESS_KEY must be at least 32 characters")
	}
	addr := orDefault(getenv("EGRESS_ADDR"), ":3128")
	var allowPrivate []string
	for _, v := range strings.Split(getenv("EGRESS_ALLOW_PRIVATE"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			allowPrivate = append(allowPrivate, v)
		}
	}
	m, stopMetrics, err := startMetrics(getenv, log, addr)
	if err != nil {
		return err
	}
	defer stopMetrics()
	p := &egress.Proxy{Key: []byte(key), AllowPrivate: allowPrivate, Log: log, Metrics: m}
	log.Info("egress ready", "version", version, "allow_private", allowPrivate)

	srv := &http.Server{Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", ln.Addr().String())
		errc <- srv.Serve(ln)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
