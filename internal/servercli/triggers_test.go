package servercli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/cmdtest"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// rescan runs against MongoDB from CONFIG_DIR, queues jobs of its own and audits itself.
func TestTriggersRescanCommand(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	fnDir := realm + "/main/" + registry.FunctionsDir
	root := cmdtest.Write(t, map[string]string{
		realm + "/realm.yaml":                  "auth:\n  strategies:\n    password: [password]\n  signup: open\n",
		realm + "/main/orders/schema.json":     `{"type":"object","properties":{"status":{"type":"string"}}}`,
		realm + "/main/orders/collection.yaml": "triggers:\n  on_paid:\n    events: [created, updated]\n    when: \"document.status == 'paid'\"\n    function: main/notify\n  needs_before:\n    events: [updated]\n    when: \"before.status != 'paid'\"\n    function: main/notify\n",
		fnDir + "/notify/function.yaml":        "internal: true\nmode: async\ntimeout: 15s\n",
		fnDir + "/notify/index.js":             "",
	})
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri}
	c := &cliEnv{t: t, env: env}
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx) //nolint:errcheck
	t.Cleanup(func() {
		names, _ := client.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: "^" + realm + "__"}}}})
		for _, n := range names {
			_ = client.Database(n).Drop(ctx)
		}
	})
	// Provision the realm (the collection, its validator, the system database).
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&mongodb.Provisioner{Client: client, Registry: reg, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	orders := client.Database(realm + "__main").Collection("orders")
	base := time.Now().UTC().Add(-2 * time.Hour)
	for i, st := range []string{"paid", "draft", "paid"} {
		now := base.Add(time.Duration(i) * time.Minute)
		_, err := orders.InsertOne(ctx, bson.D{{Key: "_id", Value: "o" + string(rune('1'+i))}, {Key: "status", Value: st},
			{Key: "_meta", Value: bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: int64(1)}}}})
		if err != nil {
			t.Fatal(err)
		}
	}

	out := c.expect(0, "rescan", "", "triggers", "rescan", "--realm", realm, "--database", "main", "--collection", "orders", "--updated-since", "3h")
	if !strings.Contains(out, "3 documents read, 2 jobs queued") || !strings.Contains(out, "not rescanned: trigger needs_before") {
		t.Errorf("output:\n%s", out)
	}
	jobs := client.Database(mongodb.SystemDatabaseName(realm)).Collection(mongodb.JobsCollection)
	n, err := jobs.CountDocuments(ctx, bson.D{{Key: "origin", Value: "trigger:main/orders/on_paid"}})
	if err != nil || n != 2 {
		t.Fatalf("%d jobs %v", n, err)
	}
	// A second rescan is a new run: its jobs have their own ids.
	c.expect(0, "2 jobs queued", "", "triggers", "rescan", "--realm", realm, "--database", "main", "--collection", "orders", "--updated-since", "3h")
	if n, _ := jobs.CountDocuments(ctx, bson.D{{Key: "origin", Value: "trigger:main/orders/on_paid"}}); n != 4 {
		t.Errorf("after a second run: %d jobs, want 4", n)
	}
	// The range is honored: nothing was written in the last minute.
	c.expect(0, "0 documents read", "", "triggers", "rescan", "--realm", realm, "--database", "main", "--collection", "orders", "--updated-since", "1m")
	// Audited.
	audit := client.Database(mongodb.SystemDatabaseName(realm)).Collection(mongodb.AuditCollection)
	if n, _ := audit.CountDocuments(ctx, bson.D{{Key: "action", Value: auth.AuditTriggerRescan}}); n != 3 {
		t.Errorf("audit records = %d, want 3", n)
	}
	// Usage errors.
	c.expect(2, "--updated-since is required", "", "triggers", "rescan", "--realm", realm, "--database", "main", "--collection", "orders")
	c.expect(2, "--updated-since", "", "triggers", "rescan", "--realm", realm, "--database", "main", "--collection", "orders", "--updated-since", "yesterday")
}
