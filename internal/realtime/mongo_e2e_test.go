package realtime_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/realtime"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// A stream over a real change stream: visibility transitions, a removal told from the
// pre-change image, and a resume that replays what was missed and then follows live.
func TestStreamAgainstMongoDB(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
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
		realm + "/realm.yaml":                  "auth: enabled\nsignup: open\n",
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
	src := mongodb.NewChangeSource(feedClient)
	set := mongodb.RealtimeWatchSet(reg)
	mk := func(name string) *changefeed.Consumer {
		c := &changefeed.Consumer{Name: name, Source: src, Log: log}
		c.SetWatched(set)
		return c
	}
	lazy := changefeed.NewLazy(mk("realtime"), time.Second)
	defer lazy.Close()
	hub := &realtime.Hub{Feed: lazy, Replay: func() *changefeed.Consumer { return mk("replay") }, Limits: realtime.Limits{OpenInterval: -1}}

	paid := storage.Condition{Field: "status", Op: storage.OpEq, Value: "paid"}
	sub := &realtime.Subscription{
		ID: "paid", DB: realm + "__main", Coll: "orders",
		Visible: func(d map[string]any) (bool, error) { return storage.Match(d, paid) },
		Render:  func(d map[string]any) map[string]any { return d },
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	frames := make(chan realtime.Frame, 64)
	runStream := func(resume string) *realtime.Stream {
		s, err := hub.Open(realtime.Request{Realm: realm, Principal: "user:a", Key: key, Subscriptions: []*realtime.Subscription{sub}, Resume: resume})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_ = s.Run(ctx, func(f realtime.Frame) error {
				if f.Event != "" {
					frames <- f
				}
				return nil
			})
		}()
		return s
	}
	want := func(event string) realtime.Frame {
		t.Helper()
		select {
		case f := <-frames:
			if f.Event != event {
				t.Fatalf("got %s %s, want %s", f.Event, f.Data, event)
			}
			return f
		case <-time.After(20 * time.Second):
			t.Fatalf("timed out waiting for %s", event)
		}
		return realtime.Frame{}
	}

	orders := client.Database(realm + "__main").Collection("orders")
	meta := func(v int64) bson.D {
		now := time.Now().UTC()
		return bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: v}}
	}
	put := func(id, status string, v int64) {
		t.Helper()
		doc := bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: meta(v)}, {Key: "status", Value: status}}
		if _, err := orders.ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, doc, options.Replace().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
	}

	s := runStream("")
	time.Sleep(500 * time.Millisecond) // the stream opens from now, when the first subscriber arrives
	put("o1", "draft", 1)              // not visible: nothing
	put("o1", "paid", 2)               // becomes visible: created
	want("created")
	put("o1", "paid", 3) // still visible: updated
	f := want("updated")
	put("o1", "refunded", 4) // no longer visible: removed
	want("removed")
	if _, err := orders.DeleteOne(ctx, bson.D{{Key: "_id", Value: "o1"}}); err != nil { // already invisible: nothing
		t.Fatal(err)
	}
	put("o2", "paid", 1)
	want("created")
	put("o3", "paid", 1)
	want("created")
	token := f.ID
	s.Close()

	// Resuming after the update of o1: what followed is replayed with the caller's access
	// now (o1's refund is its removal from the view; deleting an invisible document says
	// nothing), and then the stream follows live without repeating.
	s2 := runStream(token)
	defer s2.Close()
	got := []string{}
	for i := 0; i < 3; i++ {
		select {
		case f := <-frames:
			got = append(got, f.Event+":"+idOf(f))
		case <-time.After(20 * time.Second):
			t.Fatalf("timed out waiting for the replay, have %v", got)
		}
	}
	if want := []string{"removed:o1", "created:o2", "created:o3"}; !equalStrings(got, want) {
		t.Fatalf("replay = %v, want %v", got, want)
	}
	put("o4", "paid", 1)
	select {
	case f := <-frames:
		if f.Event != "created" || idOf(f) != "o4" {
			t.Fatalf("live after the replay: %s %s", f.Event, f.Data)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for the live event")
	}
}

func idOf(f realtime.Frame) string {
	var m struct {
		ID  string `json:"id"`
		Doc struct {
			ID string `json:"id"`
		} `json:"doc"`
	}
	_ = json.Unmarshal(f.Data, &m)
	if m.ID != "" {
		return m.ID
	}
	return m.Doc.ID
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lastSlash(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return len(p) - 1 - i
		}
	}
	return len(p)
}
