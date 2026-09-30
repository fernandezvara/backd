package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ServerTimeouts returns HTTP server timeouts consistent with a per-request
// storage deadline: writes get opTimeout plus headroom (and at least 30s).
func ServerTimeouts(srv *http.Server, opTimeout time.Duration) {
	srv.ReadHeaderTimeout = 10 * time.Second
	srv.ReadTimeout = 30 * time.Second
	srv.WriteTimeout = max(30*time.Second, opTimeout+10*time.Second)
	srv.IdleTimeout = 120 * time.Second
}

// Run serves on ln until ctx is done, then shuts down gracefully: the
// listener closes at once (new connections are refused) and in-flight
// requests get up to shutdownTimeout to finish. Requests still running
// after that are aborted and Run returns an error.
func Run(ctx context.Context, srv *http.Server, ln net.Listener, shutdownTimeout time.Duration, log *slog.Logger) error {
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

	log.Info("shutting down; draining in-flight requests", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown did not finish within %s; remaining requests aborted: %w", shutdownTimeout, err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("shutdown complete")
	return nil
}
