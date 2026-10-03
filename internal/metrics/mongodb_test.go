package metrics

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoMonitorLabels(t *testing.T) {
	m := New("dev", "x")
	mon := m.MongoMonitor()
	ev := func(name string, d time.Duration) event.CommandFinishedEvent {
		return event.CommandFinishedEvent{CommandName: name, Duration: d}
	}
	ctx := context.Background()
	mon.Succeeded(ctx, &event.CommandSucceededEvent{CommandFinishedEvent: ev("find", time.Millisecond)})
	mon.Succeeded(ctx, &event.CommandSucceededEvent{CommandFinishedEvent: ev("somethingNew-"+strings.Repeat("x", 20), time.Millisecond)})
	mon.Failed(ctx, &event.CommandFailedEvent{CommandFinishedEvent: ev("insert", time.Millisecond), Failure: errors.New("boom")})
	mon.Failed(ctx, &event.CommandFailedEvent{CommandFinishedEvent: ev("update", time.Millisecond), Failure: context.DeadlineExceeded})
	_, body := scrape(t, m, "", nil)
	for _, want := range []string{
		`backd_mongodb_operation_duration_seconds_count{operation="find",outcome="ok"} 1`,
		`backd_mongodb_operation_duration_seconds_count{operation="other",outcome="ok"} 1`,
		`backd_mongodb_operation_duration_seconds_count{operation="insert",outcome="error"} 1`,
		`backd_mongodb_operation_duration_seconds_count{operation="update",outcome="timeout"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "somethingNew") {
		t.Error("an unknown command name became a label")
	}
	var off *Metrics
	if off.MongoMonitor() != nil {
		t.Error("a monitor without metrics")
	}
}

func TestWatchMongo(t *testing.T) {
	m := New("dev", "x")
	var fail bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.WatchMongo(ctx, func(context.Context) error {
			if fail {
				return errors.New("down")
			}
			return nil
		}, 5*time.Millisecond)
		close(done)
	}()
	wait := func(want string) {
		t.Helper()
		for range 200 {
			if _, body := scrape(t, m, "", nil); strings.Contains(body, want) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("never saw %q", want)
	}
	wait("backd_mongodb_up 1")
	fail = true
	wait("backd_mongodb_up 0")
	cancel()
	<-done
}

// TestMongoMonitorAgainstMongoDB checks the monitor sees real commands.
func TestMongoMonitorAgainstMongoDB(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	m := New("dev", "x")
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(m.MongoMonitor()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	ctx := context.Background()
	coll := client.Database("metrics_test").Collection("c")
	if _, err := coll.InsertOne(ctx, bson.M{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := coll.FindOne(ctx, bson.M{}).Err(); err != nil {
		t.Fatal(err)
	}
	_ = client.Database("metrics_test").RunCommand(ctx, bson.D{{Key: "noSuchCommand", Value: 1}}).Err()
	_ = coll.Drop(ctx)
	_, body := scrape(t, m, "", nil)
	for _, want := range []string{`operation="insert",outcome="ok"`, `operation="find",outcome="ok"`, `outcome="error"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
}
