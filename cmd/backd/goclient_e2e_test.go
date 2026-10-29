package main

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/pkg/client"
)

// The Go client against a real backd and MongoDB: sign-up and sessions, documents and the query
// language, the revalidation cache, aggregations, the app configuration and a live view.
func TestGoClientEndToEnd(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":                "signup: open\n",
		realm + "/app/notes/schema.json":     `{"type": "object", "additionalProperties": false, "properties": {"title": {"type": "string"}, "status": {"enum": ["open", "done"]}, "amount": {"type": "integer"}}, "required": ["title", "status"]}`,
		realm + "/app/notes/collection.yaml": "realtime: true\naggregate:\n  group_by: [status]\n  metrics: [amount]\n" + rulesSection("read: user != nil\nwrite: user != nil\n"),
		realm + "/_realm/config/rules.yaml":  "read:\n  banner: user != nil\n  limits: user != nil\n",
		realm + "/_realm/config/schema.json": `{"type": "object", "additionalProperties": false, "properties": {"banner": {"type": "string", "default": "hello"}, "limits": {"type": "object", "additionalProperties": false, "properties": {"notes": {"type": "integer", "default": 50}}}}}`,
	})
	env := map[string]string{"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "PASSWORD_HASH_CONCURRENCY": "1", "BACKD_EVENTS": "true"}
	getenv := func(k string) string { return env[k] }
	ctx := context.Background()
	t.Cleanup(func() {
		if mc, err := mongodb.Connect(ctx, uri); err == nil {
			for _, db := range []string{realm + "__app", realm + "___system"} {
				_ = mc.Database(db).Drop(ctx)
			}
			_ = mc.Disconnect(ctx)
		}
	})
	var provisionErr strings.Builder
	if code := run([]string{"provision"}, getenv, nil, io.Discard, &provisionErr); code != 0 {
		t.Fatalf("provision failed: %s", &provisionErr)
	}
	a, err := setup(ctx, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	defer a.openEvents()() // what serve does: the change feed behind live views
	api := httptest.NewServer(a.handler())
	defer api.Close()

	c, err := client.New(client.Options{URL: api.URL, Realm: realm})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var events []client.AuthEvent
	c.Auth().OnChange(func(ch client.AuthChange) { events = append(events, ch.Event) })

	// Sign-up and the session.
	s, err := c.Auth().Signup(ctx, client.SignUpInput{Email: "ada@example.com", Password: "dev-p4ssw0rd!"})
	if err != nil || s.Token == "" {
		t.Fatalf("signup: %v %+v", err, s)
	}
	me, err := c.Auth().Me(ctx)
	if err != nil || me.Email != "ada@example.com" {
		t.Fatalf("me: %v %+v", err, me)
	}
	if _, err := c.DB("app").Collection("notes").Get(ctx, "nope", client.ReadOptions{}); !errors.Is(err, client.ErrNotFound) {
		t.Errorf("a missing document: %v", err)
	}

	// Documents and the query language.
	notes := c.DB("app").Collection("notes")
	var ids []string
	for i, n := range []struct {
		title, status string
		amount        int
	}{{"a", "open", 10}, {"b", "open", 20}, {"c", "done", 5}, {"d", "done", 7}, {"e", "open", 1}} {
		d, err := notes.Create(ctx, map[string]any{"title": n.title, "status": n.status, "amount": n.amount})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, d.ID())
	}
	page, err := notes.List(ctx, client.Query{Where: client.Where{"status": "open", "amount": client.Gte(10)}, OrderBy: []string{"-amount"}, Count: true})
	if err != nil || len(page.Items) != 2 || page.Items[0]["title"] != "b" || page.Total == nil || *page.Total != 2 {
		t.Fatalf("list: %v %+v", err, page)
	}
	n := 0
	for doc, err := range notes.Iterate(ctx, client.Query{Limit: 2, OrderBy: []string{"title"}}) {
		if err != nil {
			t.Fatal(err)
		}
		_ = doc
		n++
	}
	if n != 5 {
		t.Errorf("iterating over cursors gave %d documents; want 5", n)
	}
	found, err := notes.List(ctx, client.Query{Where: client.Or(client.Where{"title": "a"}, client.Where{"title": client.In("c", "d")})})
	if err != nil || len(found.Items) != 3 {
		t.Errorf("$or: %v %+v", err, found)
	}

	// Conditional writes and the revalidation cache.
	doc, err := notes.Get(ctx, ids[0], client.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notes.Get(ctx, ids[0], client.ReadOptions{}); err != nil || c.CacheStats().Hits != 1 {
		t.Errorf("a second read of an unchanged document must be a 304: %v %+v", err, c.CacheStats())
	}
	version := doc.Meta().Version
	updated, err := notes.Patch(ctx, ids[0], map[string]any{"status": "done"}, client.WithIfMatch(version))
	if err != nil || updated["status"] != "done" || updated.Meta().Version != version+1 {
		t.Fatalf("patch: %v %+v", err, updated)
	}
	if _, err := notes.Replace(ctx, ids[0], map[string]any{"title": "a", "status": "open"}, client.WithIfMatch(version)); !errors.Is(err, client.ErrVersionMismatch) {
		t.Errorf("a stale If-Match: %v", err)
	}
	if again, err := notes.Get(ctx, ids[0], client.ReadOptions{}); err != nil || again["status"] != "done" {
		t.Errorf("a write must drop the cached copy: %v %+v", err, again)
	}
	if err := notes.Delete(ctx, ids[4]); err != nil {
		t.Fatal(err)
	}
	results, err := c.DB("app").Batch(ctx, []client.BatchOperation{
		{Op: "create", Collection: "notes", Document: map[string]any{"title": "f", "status": "open", "amount": 3}},
		{Op: "delete", Collection: "notes", ID: ids[3]},
	})
	if err != nil || len(results) != 2 {
		t.Fatalf("batch: %v %v", err, results)
	}

	// An aggregation.
	agg, err := notes.Aggregate(ctx, client.AggregateParams{GroupBy: []string{"status"}, Metrics: []string{"count", "sum:amount"}, OrderBy: []string{"status"}})
	if err != nil || len(agg.Groups) != 2 {
		t.Fatalf("aggregate: %v %+v", err, agg)
	}

	// The app configuration.
	cfg, err := c.Config().Load(ctx, nil)
	if err != nil || cfg["banner"] != "hello" {
		t.Fatalf("config: %v %v", err, cfg)
	}
	if limit, err := c.Config().Get(ctx, "limits.notes"); err != nil || limit != float64(50) {
		t.Errorf("config limits.notes: %v %v", err, limit)
	}

	// A live view follows a second client's writes.
	view, err := notes.Subscribe(client.SubscriptionSpec{Where: client.Where{"status": "open"}, Load: client.LoadSpec{Sort: []string{"title"}}}, client.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	readyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := view.Ready(readyCtx); err != nil {
		t.Fatalf("live view: %v", err)
	}
	other, _ := client.New(client.Options{URL: api.URL, Realm: realm, Storage: client.NewMemoryStorage()})
	defer other.Close()
	if _, err := other.Auth().Login(ctx, client.SignInInput{Email: "ada@example.com", Password: "dev-p4ssw0rd!"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.DB("app").Collection("notes").Create(ctx, map[string]any{"title": "live", "status": "open"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		seen := false
		for _, d := range view.Items() {
			seen = seen || d["title"] == "live"
		}
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the live view never saw the new note: %v", view.Items())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Signing out ends the session.
	if err := c.Auth().Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Auth().Me(ctx); !errors.Is(err, client.ErrAuthentication) {
		t.Errorf("me after logout: %v", err)
	}
	if len(events) < 2 || events[0] != client.SignedIn || events[1] != client.SignedOut {
		t.Errorf("events = %v", events)
	}
}
