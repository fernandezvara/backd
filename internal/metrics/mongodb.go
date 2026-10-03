package metrics

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// MongoBuckets are the buckets of MongoDB command durations, in seconds.
var MongoBuckets = []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// mongoOperations are the commands that get their own label; any other
// command is "other", so the label stays a closed set.
var mongoOperations = map[string]bool{
	"find": true, "getMore": true, "insert": true, "update": true, "delete": true, "findAndModify": true,
	"aggregate": true, "count": true, "distinct": true, "createIndexes": true, "dropIndexes": true,
	"create": true, "collMod": true, "listCollections": true, "listIndexes": true,
	"commitTransaction": true, "abortTransaction": true,
}

// MongoMonitor times every command the driver sends. Hand it to
// mongodb.Connect: one hook measures the storage, the auth stores, the jobs
// and everything else, without a wrapper per call. Nil when metrics are off.
func (m *Metrics) MongoMonitor() *event.CommandMonitor {
	if m == nil {
		return nil
	}
	observe := func(name string, d time.Duration, outcome string) {
		if !mongoOperations[name] {
			name = "other"
		}
		m.mongoTime.WithLabelValues(name, outcome).Observe(d.Seconds())
	}
	return &event.CommandMonitor{
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) {
			observe(e.CommandName, time.Duration(e.Duration), "ok")
		},
		Failed: func(_ context.Context, e *event.CommandFailedEvent) {
			outcome := "error"
			if e.Failure != nil && (errors.Is(e.Failure, context.DeadlineExceeded) || mongo.IsTimeout(e.Failure) || strings.Contains(e.Failure.Error(), "context deadline exceeded")) {
				outcome = "timeout"
			}
			observe(e.CommandName, time.Duration(e.Duration), outcome)
		},
	}
}

// WatchMongo pings MongoDB every interval until ctx ends and keeps
// backd_mongodb_up current. It does nothing when metrics are off.
func (m *Metrics) WatchMongo(ctx context.Context, ping func(context.Context) error, interval time.Duration) {
	if m == nil {
		return
	}
	check := func() {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err := ping(pctx)
		if err == nil {
			m.mongoUp.Set(1)
		} else if !errors.Is(ctx.Err(), context.Canceled) {
			m.mongoUp.Set(0)
		}
	}
	check()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
