package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/fernandezvara/backd/internal/metrics"
	"github.com/fernandezvara/backd/internal/settings"
)

// startMetrics serves /metrics for the long-running commands that don't load
// the full settings (egress and the executor): METRICS_ADDR and METRICS_TOKEN
// as for serve. reserved are the addresses the process serves other things
// on. It returns the metrics to count with (nil when they are off) and a stop
// function that ends the listener.
func startMetrics(getenv func(string) string, log *slog.Logger, reserved ...string) (*metrics.Metrics, func(), error) {
	addr, token, err := settings.LoadMetrics(getenv, reserved...)
	if err != nil || addr == "" {
		return nil, func() {}, err
	}
	m := metrics.New(version, commit())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := m.Serve(ctx, addr, token, 5*time.Second, log.With("listener", "metrics")); err != nil {
			log.Error("metrics listener", "error", err)
		}
	}()
	return m, func() { cancel(); <-done }, nil
}
