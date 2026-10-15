package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/settings"
)

func eventsApp(events bool) *app {
	return &app{
		cfg: settings.Settings{Events: events, MongoURI: "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=200"},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		reg: &registry.Registry{Realms: map[string]*registry.Realm{}},
	}
}

func TestOpenEventsOffReadsNothing(t *testing.T) {
	a := eventsApp(false)
	a.openEvents()()
	if a.events != nil {
		t.Fatal("BACKD_EVENTS=false must leave no consumer")
	}
}

// An instance that never has a subscriber never connects: the feed client is made on the
// first open.
func TestOpenEventsConnectsOnlyOnFirstOpen(t *testing.T) {
	a := eventsApp(true)
	closeFn := a.openEvents()
	defer closeFn()
	if a.events == nil || a.events.Open() || a.events.Subscribers() != 0 {
		t.Fatalf("events = %+v, want an idle consumer", a.events)
	}
}

func TestFeedSourceFailsOpenWhenUnreachableAndConnectsLazily(t *testing.T) {
	f := &feedSource{uri: "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=200"}
	defer f.close()
	if _, err := f.Open(context.Background(), changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: "r", DB: "r__d", Coll: "c"}}}, nil); err == nil {
		t.Fatal("an unreachable MongoDB must fail the open")
	}
	if f.client != nil {
		t.Error("a failed connect must not be kept")
	}
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping the connected half")
	}
	g := &feedSource{uri: uri}
	st, err := g.Open(context.Background(), changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: "r", DB: "r__d", Coll: "c"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if g.client == nil {
		t.Error("the client should be kept after the first open")
	}
	g.close()
	if g.client != nil {
		t.Error("close must disconnect")
	}
}
