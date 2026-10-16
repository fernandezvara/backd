package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/triggers"
)

// stateFake is the dispatcher's recorded state, with one open gap.
type stateFake struct {
	mu       sync.Mutex
	resolved bool
}

func (s *stateFake) Status(context.Context) (triggers.DispatcherStatus, error) {
	return triggers.DispatcherStatus{Found: true, Held: true, Epoch: 7, Holder: "host-1", LeaseExpires: time.Now().Add(time.Minute), WatermarkAt: time.Now().Add(-3 * time.Second)}, nil
}

func (s *stateFake) Gaps(_ context.Context, realm string, all bool, _ int) ([]triggers.GapRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := triggers.GapRecord{
		Gap: triggers.Gap{Realm: realm, Collection: "orders", Trigger: "on_paid", Reason: triggers.GapWindow, From: time.Now().Add(-time.Hour), To: time.Now(), Count: 1},
		ID:  "gap-abc", RecordedAt: time.Now(),
	}
	if s.resolved {
		if !all {
			return nil, nil
		}
		g.ResolvedAt = time.Now()
	}
	return []triggers.GapRecord{g}, nil
}

func (s *stateFake) ResolveGap(_ context.Context, _, id string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "gap-abc" || s.resolved {
		return false, nil
	}
	s.resolved = true
	return true, nil
}

func TestTriggersStatusGapsAndResolveCommands(t *testing.T) {
	fnDir := "acme/main/" + registry.FunctionsDir
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":                  "roles:\n  ops:\n    admin: true\n",
		"acme/main/orders/schema.json":     `{"type":"object","properties":{"status":{"type":"string"}}}`,
		"acme/main/orders/collection.yaml": "triggers:\n  on_paid:\n    events: [created, updated]\n    when: \"document.status == 'paid'\"\n    function: main/notify\n    ordered: true\n",
		fnDir + "/notify/function.yaml":    "internal: true\nmode: async\n",
		fnDir + "/notify/index.js":         "",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry: reg,
		Ready:    func(context.Context) error { return nil },
		Triggers: &stateFake{},
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}

	out := c.expect(0, "dispatcher: leased (epoch 7)", "", "triggers", "status", "--realm", "acme", "--url", srv.URL)
	for _, want := range []string{"main/orders/on_paid", "created,updated", "main/notify", "runs with admin data access", "open gaps: 1", "document.status == 'paid'"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "host-1") {
		t.Error("the lease holder's name is shown")
	}
	c.expect(0, `"epoch":7`, "", "triggers", "status", "--realm", "acme", "--url", srv.URL, "--json")

	out = c.expect(0, "gap-abc", "", "triggers", "gaps", "--realm", "acme", "--url", srv.URL)
	if !strings.Contains(out, "window") || !strings.Contains(out, "open") {
		t.Errorf("gaps:\n%s", out)
	}
	c.expect(0, "gap gap-abc resolved at", "", "triggers", "resolve", "--realm", "acme", "--id", "gap-abc", "--url", srv.URL)
	code, out2, _ := c.run("", "triggers", "gaps", "--realm", "acme", "--url", srv.URL)
	if code != 0 || strings.Contains(out2, "gap-abc") {
		t.Errorf("open gaps after resolving: %d %q", code, out2)
	}
	c.expect(0, "resolved", "", "triggers", "gaps", "--realm", "acme", "--url", srv.URL, "--all")
	c.expect(1, "404", "", "triggers", "resolve", "--realm", "acme", "--id", "nope", "--url", srv.URL)
}

// rescan runs against MongoDB from CONFIG_DIR, queues jobs of its own and audits itself.
func TestTriggersRescanCommand(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	fnDir := realm + "/main/" + registry.FunctionsDir
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":                  "auth: enabled\nsignup: open\n",
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
