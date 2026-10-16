package triggers_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/triggers"
)

// This file runs the dispatcher against a real MongoDB: provisioning, the change stream,
// the lease and the jobs collection.

const orderSchema = `{"type":"object","properties":{"status":{"enum":["draft","paid","shipped"]},"total":{"type":"number"}}}`

const paidTrigger = `triggers:
  on_paid:
    events: [created, updated]
    when: "document.status == 'paid' && before.status != 'paid'"
    function: main/on-paid
`

type realQueue struct {
	client *mongo.Client
}

func (q realQueue) Enqueue(ctx context.Context, realm string, jobs []triggers.Job) (int, error) {
	out := make([]auth.Job, len(jobs))
	for i, j := range jobs {
		out[i] = auth.Job{
			ID: j.ID, Database: j.Database, Function: j.Function, Input: j.Input, CallerActor: "system",
			ActsAsFunction: true, Origin: j.Origin, TimeoutMS: j.Timeout.Milliseconds(), RequestID: j.RequestID,
			EventID: j.EventID, ChainRoot: j.ChainRoot, ChainDepth: j.ChainDepth,
			SerialKey: j.SerialKey, SerialOrder: j.SerialOrder,
		}
	}
	return (&auth.Users{Store: mongodb.NewAuthStore(q.client, realm)}).EnqueueJobs(ctx, out)
}

func (q realQueue) Backlog(ctx context.Context, realm string) (int, error) {
	return mongodb.NewAuthStore(q.client, realm).CountTriggerJobs(ctx)
}

func writeTree(t *testing.T, realm, collectionYAML string) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		realm + "/realm.yaml":                            "auth: enabled\nsignup: open\n",
		realm + "/main/orders/schema.json":               orderSchema,
		realm + "/main/orders/collection.yaml":           collectionYAML,
		realm + "/main/_functions/deno.json":             "{}",
		realm + "/main/_functions/on-paid/function.yaml": "internal: true\nmode: async\ntimeout: 20s\n",
		realm + "/main/_functions/on-paid/index.ts":      "export default () => 1;\n",
	}
	for p, c := range files {
		full := root + "/" + p
		if err := os.MkdirAll(full[:len(full)-len(baseName(full))-1], 0o755); err != nil {
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
	return reg
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func jobsOf(t *testing.T, client *mongo.Client, realm string) []bson.M {
	t.Helper()
	cur, err := client.Database(mongodb.SystemDatabaseName(realm)).Collection(mongodb.JobsCollection).
		Find(context.Background(), bson.D{{Key: "origin", Value: "trigger:main/orders/on_paid"}})
	if err != nil {
		t.Fatal(err)
	}
	var out []bson.M
	if err := cur.All(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDispatchAgainstMongoDB(t *testing.T) {
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
	state := &mongodb.TriggerState{Client: client, Deployment: realm}
	t.Cleanup(func() {
		names, _ := client.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: "^" + realm + "__"}}}})
		for _, n := range names {
			_ = client.Database(n).Drop(ctx)
		}
		db := client.Database(mongodb.DeploymentDatabase)
		_, _ = db.Collection(mongodb.TriggerStateCollection).DeleteMany(ctx, bson.D{{Key: "_id", Value: "dispatcher:" + realm}})
		_, _ = db.Collection(mongodb.TriggerGapsCollection).DeleteMany(ctx, bson.D{{Key: "deployment", Value: realm}})
	})

	reg := writeTree(t, realm, paidTrigger)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := (&mongodb.Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	orders := client.Database(realm + "__main").Collection("orders")
	meta := func(v int64) bson.D {
		now := time.Now().UTC()
		return bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: v}}
	}
	insert := func(id, status string) {
		t.Helper()
		if _, err := orders.InsertOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: meta(1)}, {Key: "status", Value: status}}); err != nil {
			t.Fatal(err)
		}
	}
	replace := func(id, status string, v int64) {
		t.Helper()
		if _, err := orders.ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: meta(v)}, {Key: "status", Value: status}}); err != nil {
			t.Fatal(err)
		}
	}

	mk := func(holder string) (*triggers.Dispatcher, func()) {
		d := &triggers.Dispatcher{
			Source: mongodb.NewChangeSource(client), State: state, Queue: realQueue{client}, Holder: holder, Log: log,
			LeaseTTL: 2 * time.Second, FlushEvery: 20 * time.Millisecond, WatermarkEvery: 50 * time.Millisecond,
		}
		d.SetRegistry(reg)
		cctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = d.Run(cctx); close(done) }()
		return d, func() {
			cancel()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Error("dispatcher did not stop")
			}
		}
	}

	_, stopA := mk("a")
	waitFor(t, "the first position to be stored", func() bool {
		pos, _, found, _ := state.Watermark(ctx)
		return found && len(pos) > 0
	})

	insert("o1", "paid")     // fires: created paid
	insert("o2", "draft")    // does not
	replace("o2", "paid", 2) // fires: a transition, needs the pre-image
	replace("o2", "paid", 3) // same value again: no
	insert("o3", "shipped")  // no
	waitFor(t, "two jobs", func() bool { return len(jobsOf(t, client, realm)) == 2 })
	time.Sleep(300 * time.Millisecond)
	if n := len(jobsOf(t, client, realm)); n != 2 {
		t.Fatalf("%d jobs, want exactly 2", n)
	}
	for _, j := range jobsOf(t, client, realm) {
		if j["function"] != "on-paid" || j["database"] != "main" || j["acts_as_function"] != true || j["status"] != "queued" || j["caller_actor"] != "system" {
			t.Errorf("job = %v", j)
		}
	}

	// A handoff: a is stopped, b takes the lease and continues after a's position.
	stopA()
	insert("o4", "paid")
	_, stopB := mk("b")
	defer stopB()
	waitFor(t, "b to queue the change made while nobody led", func() bool { return len(jobsOf(t, client, realm)) == 3 })
	insert("o5", "paid")
	waitFor(t, "b to follow live", func() bool { return len(jobsOf(t, client, realm)) == 4 })
	time.Sleep(300 * time.Millisecond)
	if n := len(jobsOf(t, client, realm)); n != 4 {
		t.Fatalf("%d jobs after the handoff, want exactly 4 (one per matching change)", n)
	}

	// A write a trigger run made carries its chain: the event it causes is one deeper.
	chainedMeta := meta(1)
	chainedMeta = append(chainedMeta, bson.E{Key: "chain", Value: bson.D{{Key: "root", Value: "r-1"}, {Key: "depth", Value: int64(1)}}})
	if _, err := orders.InsertOne(ctx, bson.D{{Key: "_id", Value: "o6"}, {Key: "_meta", Value: chainedMeta}, {Key: "status", Value: "paid"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the chained event's job", func() bool { return len(jobsOf(t, client, realm)) == 5 })
	deepest := 0
	for _, j := range jobsOf(t, client, realm) {
		if d, _ := j["chain_depth"].(int32); int(d) > deepest {
			deepest = int(d)
			if j["chain_root"] != "r-1" {
				t.Errorf("chain root = %v", j["chain_root"])
			}
		}
	}
	if deepest != 2 {
		t.Errorf("deepest chain depth = %d, want 2", deepest)
	}
}
