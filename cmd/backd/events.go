package main

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/mongodb"
)

// feedSource connects the change-stream client on its first use: an instance that never has a
// realtime subscriber never opens a connection for it (design/triggers-and-realtime.md §3).
type feedSource struct {
	uri string

	mu     sync.Mutex
	client *mongo.Client
	src    *mongodb.ChangeSource
}

func (f *feedSource) Open(ctx context.Context, set changefeed.WatchSet, after changefeed.Position) (changefeed.Stream, error) {
	f.mu.Lock()
	if f.src == nil {
		client, err := mongodb.ConnectFeed(ctx, f.uri)
		if err != nil {
			f.mu.Unlock()
			return nil, err
		}
		f.client, f.src = client, mongodb.NewChangeSource(client)
	}
	src := f.src
	f.mu.Unlock()
	return src.Open(ctx, set, after)
}

func (f *feedSource) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client != nil {
		_ = f.client.Disconnect(context.Background())
		f.client, f.src = nil, nil
	}
}

// openEvents prepares this instance's realtime consumer when BACKD_EVENTS allows it. Nothing
// is read until a subscriber arrives, so it costs nothing on an instance without any. It
// returns the function that closes it.
func (a *app) openEvents() func() {
	if !a.cfg.Events {
		a.log.Info("BACKD_EVENTS=false: this instance reads no change stream and serves no realtime subscriptions")
		return func() {}
	}
	src := a.feedSource()
	consumer := &changefeed.Consumer{Name: "realtime", Source: src, Log: a.log, Observer: a.metrics}
	set := mongodb.RealtimeWatchSet(a.reg)
	consumer.SetWatched(set)
	a.events = changefeed.NewLazy(consumer, 0)
	if !set.Empty() {
		a.log.Info("change feed ready", "realtime_collections", len(set.Collections), "opens_on", "first subscriber")
	}
	return func() { a.events.Close() }
}

// feedSource is the instance's change-stream source, shared by the realtime consumer and the
// trigger dispatcher (each opens its own stream on it). It connects on first use.
func (a *app) feedSource() *feedSource {
	if a.feed == nil {
		a.feed = &feedSource{uri: a.cfg.MongoURI}
	}
	return a.feed
}
