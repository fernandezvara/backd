package realtime_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/realtime"
	"github.com/fernandezvara/backd/internal/registry"
)

// What `ready` promises: once a stream is open, a document written right after it is
// delivered, even to the first subscriber of an instance (whose change stream opens only
// then). BACKD_READY_RUNS sets how many times it is tried (default 20).
func TestFirstSubscriberMissesNothingAfterOpen(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	runs := 20
	if v, err := strconv.Atoi(os.Getenv("BACKD_READY_RUNS")); err == nil && v > 0 {
		runs = v
	}
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx) //nolint:errcheck
	realm := "t" + xid.New().String()
	t.Cleanup(func() {
		names, _ := client.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: "^" + realm + "__"}}}})
		for _, n := range names {
			_ = client.Database(n).Drop(ctx)
		}
	})
	root := t.TempDir()
	for p, c := range map[string]string{
		realm + "/realm.yaml":                  "auth:\n  strategies:\n    password: [password]\n  signup: open\n",
		realm + "/main/orders/schema.json":     `{"type":"object","properties":{"status":{"type":"string"}}}`,
		realm + "/main/orders/collection.yaml": "realtime: true\n",
	} {
		full := root + "/" + p
		if err := os.MkdirAll(full[:len(full)-lastSlash(full)-1], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := (&mongodb.Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	feedClient, err := mongodb.ConnectFeed(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer feedClient.Disconnect(ctx) //nolint:errcheck
	c := &changefeed.Consumer{Name: "realtime", Source: mongodb.NewChangeSource(feedClient), Log: log}
	c.SetWatched(mongodb.RealtimeWatchSet(reg))
	lazy := changefeed.NewLazy(c, 10*time.Millisecond) // the stream closes soon after the last subscriber
	defer lazy.Close()
	hub := &realtime.Hub{Feed: lazy, Limits: realtime.Limits{OpenInterval: -1}}
	sub := &realtime.Subscription{
		ID: "all", DB: realm + "__main", Coll: "orders",
		Visible: func(map[string]any) (bool, error) { return true, nil },
		Render:  func(d map[string]any) map[string]any { return d },
	}
	orders := client.Database(realm + "__main").Collection("orders")
	key := []byte("0123456789abcdef0123456789abcdef")

	for i := range runs {
		s, err := hub.Open(realtime.Request{Realm: realm, Principal: "user:a", Key: key, Subscriptions: []*realtime.Subscription{sub}, Context: ctx})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		got := make(chan struct{}, 1)
		go func() {
			_ = s.Run(ctx, func(f realtime.Frame) error {
				if f.Event == "created" {
					select {
					case got <- struct{}{}:
					default:
					}
				}
				return nil
			})
		}()
		now := time.Now().UTC()
		doc := bson.D{{Key: "_id", Value: "o" + strconv.Itoa(i)}, {Key: "_meta", Value: bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: int64(1)}}}, {Key: "status", Value: "paid"}}
		if _, err := orders.InsertOne(ctx, doc); err != nil { // right after Open returns, as after `ready`
			t.Fatal(err)
		}
		select {
		case <-got:
		case <-time.After(10 * time.Second):
			t.Fatalf("run %d: the document written right after the stream opened never arrived", i)
		}
		s.Close()
		deadline := time.Now().Add(5 * time.Second)
		for lazy.Open() && time.Now().Before(deadline) { // the next run must start with the feed closed
			time.Sleep(5 * time.Millisecond)
		}
		if lazy.Open() {
			t.Fatalf("run %d: the feed did not close after its grace", i)
		}
	}
}
