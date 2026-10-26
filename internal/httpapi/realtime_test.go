package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/realtime"
	"github.com/fernandezvara/backd/internal/registry"
)

// feedFake is a change history the realtime hub reads, appended to by the tests.
type feedFake struct {
	opens atomic.Int64
	mu    sync.Mutex
	hist  []changefeed.Change
}

// settle waits until the feed is reading: it opens, from now, with the first stream, and a change
// added before that is not part of what it reads.
func (f *feedFake) settle() {
	deadline := time.Now().Add(5 * time.Second)
	for f.opens.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
}

func (f *feedFake) add(c changefeed.Change) {
	f.mu.Lock()
	c.At = time.Unix(int64(len(f.hist)+1), 0).UTC()
	f.hist = append(f.hist, c)
	f.mu.Unlock()
}

func (f *feedFake) Open(_ context.Context, _ changefeed.WatchSet, after changefeed.Position) (changefeed.Stream, error) {
	f.opens.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.hist)
	if after != nil {
		n = int(after[0])
	}
	return &feedStream{f: f, n: n}, nil
}

type feedStream struct {
	f *feedFake
	n int
}

func (s *feedStream) Position() changefeed.Position { return changefeed.Position{byte(s.n)} }
func (s *feedStream) Close()                        {}
func (s *feedStream) Next(ctx context.Context) (changefeed.Event, error) {
	for {
		s.f.mu.Lock()
		if s.n < len(s.f.hist) {
			c := s.f.hist[s.n]
			s.n++
			s.f.mu.Unlock()
			return changefeed.Event{Change: &c, Position: changefeed.Position{byte(s.n)}}, nil
		}
		s.f.mu.Unlock()
		select {
		case <-ctx.Done():
			return changefeed.Event{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

type realtimeFixture struct {
	srv  *httptest.Server
	feed *feedFake
	svc  *auth.Users
	mdb  string // the MongoDB database of acme/app
	reg  *registry.Registry
}

func newRealtimeFixture(t *testing.T, lim realtime.Limits) *realtimeFixture {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"acme/realm.yaml":                "signup: open\nroles:\n  staff: {}\n",
		"acme/app/items/schema.json":     itemSchema,
		"acme/app/items/collection.yaml": "realtime: true\nrules:\n  read: user != nil && document._meta.owner == user.id\n",
		"acme/app/plain/schema.json":     itemSchema,
		"acme/_realm/config/schema.json": appConfigSchema,
		"acme/_realm/config/rules.yaml":  appConfigRules,
	}
	for p, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	hasher := auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1})
	svc := &auth.Users{Store: authtest.NewMemStore(), Hasher: hasher, Settings: reg.Realms["acme"].Settings}
	feed := &feedFake{}
	set := changefeed.WatchSet{}
	mk := func(name string) *changefeed.Consumer {
		c := &changefeed.Consumer{Name: name, Source: feed}
		c.SetWatched(set)
		return c
	}
	_ = set
	lazy := changefeed.NewLazy(mk("realtime"), time.Minute)
	t.Cleanup(lazy.Close)
	// The fake feed ignores the set, but an empty one would keep the consumer closed.
	c, _ := reg.Collection("acme", "app", "items")
	ns := changefeed.Namespace{Realm: "acme", DB: c.MongoDatabase, Coll: "items"}
	set.Collections = []changefeed.Namespace{ns}
	lazy.SetWatched(set)
	lim.OpenInterval = -1
	hub := &realtime.Hub{Feed: lazy, Limits: lim, Replay: func() *changefeed.Consumer { return mk("replay") }}
	var buf bytes.Buffer
	f := newFixtureWith(t, reg, &memStore{}, func(cfg *Config) {
		cfg.Users = func(realm string) *auth.Users { return svc }
		cfg.Log = slog.New(slog.NewJSONHandler(&buf, nil))
		cfg.Realtime = hub
		cfg.RealtimeLimits = lim
	})
	srv := httptest.NewServer(f.h)
	t.Cleanup(srv.Close)
	return &realtimeFixture{srv: srv, feed: feed, svc: svc, mdb: c.MongoDatabase, reg: reg}
}

type sseEvent struct {
	ID, Event, Data string
}

// subscribe opens a stream and returns its events as they arrive.
func (f *realtimeFixture) subscribe(t *testing.T, token, body string) (*http.Response, <-chan sseEvent) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1/acme/_realtime", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	out := make(chan sseEvent, 64)
	if resp.StatusCode != http.StatusOK {
		close(out)
		return resp, out
	}
	go func() {
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.Event != "" {
					out <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.Data = line[6:]
			}
		}
		_ = sc.Err() // the test ends the stream by closing the body
	}()
	return resp, out
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	return sseEvent{}
}

func (f *realtimeFixture) signup(t *testing.T, email string) (token, id string) {
	t.Helper()
	u, session, err := f.svc.Signup(context.Background(), email, "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	return session, u.User.ID
}

func (f *realtimeFixture) item(id, owner string, v int) map[string]any {
	return map[string]any{"id": id, "name": id, "_meta": map[string]any{"owner": owner, "version": int64(v)}}
}

const itemsSub = `{"subscriptions":[{"id":"mine","db":"app","collection":"items"}]}`

func TestRealtimeStreamsOnlyWhatTheCallerMayRead(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	ada, adaID := f.signup(t, "ada@example.com")
	bob, bobID := f.signup(t, "bob@example.com")

	resp, adaEvents := f.subscribe(t, ada, itemsSub)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if ev := next(t, adaEvents); ev.Event != "ready" || !strings.Contains(ev.Data, `"mine"`) {
		t.Fatalf("first event: %+v", ev)
	}
	_, bobEvents := f.subscribe(t, bob, itemsSub)
	next(t, bobEvents)
	f.feed.settle()

	d := f.item("i1", adaID, 1)
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "i1", After: d, Version: 1})
	ev := next(t, adaEvents)
	if ev.Event != "created" || ev.ID == "" || !strings.Contains(ev.Data, `"sub":"mine"`) || !strings.Contains(ev.Data, `"i1"`) {
		t.Fatalf("ada: %+v", ev)
	}
	// Bob's own document is the next thing he hears: he heard nothing of Ada's.
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "i2", After: f.item("i2", bobID, 1), Version: 1})
	ev = next(t, bobEvents)
	if ev.Event != "created" || strings.Contains(ev.Data, `"id":"i1"`) {
		t.Fatalf("bob: %+v", ev)
	}
	// Ada hands i1 over to Bob: Ada's view loses it, Bob's gains it.
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Updated, Key: "i1", Before: d, After: f.item("i1", bobID, 2), Version: 2})
	if ev = next(t, adaEvents); ev.Event != "removed" || !strings.Contains(ev.Data, `"id":"i1"`) {
		t.Fatalf("ada after handover: %+v", ev)
	}
	if ev = next(t, bobEvents); ev.Event != "created" || !strings.Contains(ev.Data, `"i1"`) {
		t.Fatalf("bob after handover: %+v", ev)
	}
}

func TestRealtimeRefusals(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{SubscriptionsPerStream: 2, StreamsPerUser: 1})
	ada, _ := f.signup(t, "ada@example.com")

	status := func(token, body string) (int, string) {
		resp, _ := f.subscribe(t, token, body)
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		code := ""
		if e, ok := out["error"].(map[string]any); ok {
			code, _ = e["code"].(string)
		}
		return resp.StatusCode, code
	}
	for name, tc := range map[string]struct {
		token, body string
		status      int
		code        string
	}{
		"anonymous":     {"", itemsSub, 401, "unauthenticated"},
		"not enabled":   {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"plain"}]}`, 400, "realtime_not_enabled"},
		"unknown":       {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"nope"}]}`, 400, "invalid_query"},
		"too many":      {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items"},{"id":"b","db":"app","collection":"items"},{"id":"c","db":"app","collection":"items"}]}`, 400, "too_many_subscriptions"},
		"like":          {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items","where":{"name":{"$like":"a%"}}}]}`, 400, "invalid_query"},
		"search":        {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items","where":{"$search":"x"}}]}`, 400, "invalid_query"},
		"duplicate ids": {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items"},{"id":"a","db":"app","collection":"items"}]}`, 400, "invalid_query"},
		"doc and where": {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items","doc":"x","where":{"name":"a"}}]}`, 400, "invalid_query"},
		"none":          {ada, `{"subscriptions":[]}`, 400, "invalid_query"},
		"unknown field": {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items"}],"order_by":"name"}`, 400, "invalid_json"},
		"config twice":  {ada, `{"subscriptions":[{"id":"a","config":{}},{"id":"b","config":{}}]}`, 400, "invalid_query"},
		"config + coll": {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items","config":{}}]}`, 400, "invalid_query"},
		"config key":    {ada, `{"subscriptions":[{"id":"a","config":{"paths":["nope"]}}]}`, 400, "invalid_query"},
		"forged resume": {ada, `{"subscriptions":[{"id":"a","db":"app","collection":"items"}],"resume_from":"nope"}`, 400, "invalid_resume_token"},
	} {
		if got, code := status(tc.token, tc.body); got != tc.status || code != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, got, code, tc.status, tc.code)
		}
	}
	// A second open stream for the same caller is refused while the first is open.
	_, ev := f.subscribe(t, ada, itemsSub)
	next(t, ev)
	if got, code := status(ada, itemsSub); got != 429 || code != "too_many_requests" {
		t.Errorf("second stream: %d %s", got, code)
	}
}

func TestRealtimeResumeIsBoundToTheCaller(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	ada, adaID := f.signup(t, "ada@example.com")
	bob, _ := f.signup(t, "bob@example.com")
	_, evs := f.subscribe(t, ada, itemsSub)
	next(t, evs)
	f.feed.settle()
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "i1", After: f.item("i1", adaID, 1), Version: 1})
	token := next(t, evs).ID

	body := `{"subscriptions":[{"id":"mine","db":"app","collection":"items"}],"resume_from":"` + token + `"}`
	if resp, _ := f.subscribe(t, bob, body); resp.StatusCode != 400 {
		t.Fatalf("another caller's token: %d", resp.StatusCode)
	}
	resp, out := f.subscribe(t, ada, body)
	if resp.StatusCode != 200 {
		t.Fatalf("own token: %d", resp.StatusCode)
	}
	if next(t, out).Event != "ready" {
		t.Fatal("ready first")
	}
}

// testHub is a realtime hub over an empty history, for tests that need the route to exist.
func testHub(t *testing.T) *realtime.Hub {
	t.Helper()
	feed := &feedFake{}
	mk := func(name string) *changefeed.Consumer {
		c := &changefeed.Consumer{Name: name, Source: feed}
		c.SetWatched(changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: "acme", DB: "acme__app", Coll: "live"}}})
		return c
	}
	lazy := changefeed.NewLazy(mk("realtime"), time.Minute)
	t.Cleanup(lazy.Close)
	return &realtime.Hub{Feed: lazy, Replay: func() *changefeed.Consumer { return mk("replay") }}
}

// A removal that arrives without its pre-change image is never told as "removed": the
// subscriber reloads, and a stranger learns nothing, not even the id.
func TestRealtimeMissingPreimageRevealsNothing(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	ada, _ := f.signup(t, "ada@example.com")
	bob, bobID := f.signup(t, "bob@example.com")
	_, adaEvents := f.subscribe(t, ada, itemsSub)
	next(t, adaEvents)
	_, bobEvents := f.subscribe(t, bob, itemsSub)
	next(t, bobEvents)
	f.feed.settle()

	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Removed, Key: "secret-doc-id", Version: 9})
	for name, ch := range map[string]<-chan sseEvent{"ada": adaEvents, "bob": bobEvents} {
		ev := next(t, ch)
		if ev.Event != "reset" || !strings.Contains(ev.Data, "preimage_missing") || strings.Contains(ev.Data, "secret-doc-id") {
			t.Fatalf("%s: %+v", name, ev)
		}
	}
	// And the stream goes on: the next change reaches its owner.
	time.Sleep(1100 * time.Millisecond)
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "b1", After: f.item("b1", bobID, 1), Version: 1})
	if ev := next(t, bobEvents); ev.Event != "created" {
		t.Fatalf("after the reset: %+v", ev)
	}
}

// `where` narrows a subscription, and a document subscription follows one id.
func TestRealtimeWhereAndDocument(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	ada, adaID := f.signup(t, "ada@example.com")
	body := `{"subscriptions":[
		{"id":"bigs","db":"app","collection":"items","where":{"age":{"$gte":18}}},
		{"id":"one","db":"app","collection":"items","doc":"i2"}]}`
	_, evs := f.subscribe(t, ada, body)
	next(t, evs)
	f.feed.settle()

	mk := func(id string, age int64) map[string]any {
		d := f.item(id, adaID, 1)
		d["age"] = age
		return d
	}
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "i1", After: mk("i1", 5), Version: 1})
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "i2", After: mk("i2", 30), Version: 1})
	got := map[string]bool{}
	for range 2 {
		ev := next(t, evs)
		var m struct{ Sub string }
		_ = json.Unmarshal([]byte(ev.Data), &m)
		got[m.Sub] = true
	}
	if !got["bigs"] || !got["one"] {
		t.Fatalf("i2 reaches both subscriptions (i1, under 18, neither): %v", got)
	}
}

func (f *realtimeFixture) logout(t *testing.T, token string) {
	t.Helper()
	p, err := f.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Logout(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func (f *realtimeFixture) sessionID(t *testing.T, token string) string {
	t.Helper()
	p, err := f.svc.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return p.Session.ID
}

// A session that is revoked ends the stream it opened: the client is told, and the stream closes.
func TestRealtimeStreamEndsWhenItsSessionIsRevoked(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	ada, adaID := f.signup(t, "ada@example.com")
	bob, _ := f.signup(t, "bob@example.com")
	sid := f.sessionID(t, ada)
	_, adaEvents := f.subscribe(t, ada, itemsSub)
	next(t, adaEvents)
	_, bobEvents := f.subscribe(t, bob, itemsSub)
	next(t, bobEvents)
	f.feed.settle()

	// Another session's end is none of Ada's business.
	f.feed.add(changefeed.Change{Realm: "acme", DB: "acme__system", Coll: "sessions", System: true, Op: changefeed.Removed, Key: "someone-else"})
	f.feed.add(changefeed.Change{Realm: "acme", DB: f.mdb, Coll: "items", Op: changefeed.Created, Key: "i1", After: f.item("i1", adaID, 1), Version: 1})
	if ev := next(t, adaEvents); ev.Event != "created" {
		t.Fatalf("an unrelated session ended Ada's stream: %+v", ev)
	}
	// The session is revoked in the store, and the change reaches the feed.
	f.logout(t, ada)
	f.feed.add(changefeed.Change{Realm: "acme", DB: "acme__system", Coll: "sessions", System: true, Op: changefeed.Removed, Key: sid})
	if ev := next(t, adaEvents); ev.Event != "session_ended" {
		t.Fatalf("got %+v", ev)
	}
	select {
	case _, ok := <-adaEvents:
		if ok {
			t.Fatal("the stream stayed open")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the stream did not close")
	}
	// Reconnecting with the revoked credential is refused.
	if resp, _ := f.subscribe(t, ada, itemsSub); resp.StatusCode != 401 {
		t.Fatalf("reconnect: %d", resp.StatusCode)
	}
}

// A change of memberships makes the stream read the access again: with the credential gone
// (here: revoked without its own event) it ends.
func TestRealtimeReloadFindsARevokedSession(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	ada, adaID := f.signup(t, "ada@example.com")
	_, evs := f.subscribe(t, ada, itemsSub)
	next(t, evs)
	f.feed.settle()
	f.logout(t, ada)
	f.feed.add(changefeed.Change{Realm: "acme", DB: "acme__system", Coll: "org_members", System: true, Op: changefeed.Updated, Key: "o1/" + adaID})
	if ev := next(t, evs); ev.Event != "session_ended" {
		t.Fatalf("got %+v", ev)
	}
}

// A stream that follows the configuration needs no collection, anonymous callers may, and the
// event names the version and the keys that changed.
func TestRealtimeFollowsTheConfiguration(t *testing.T) {
	f := newRealtimeFixture(t, realtime.Limits{})
	_, ev := f.subscribe(t, "", `{"subscriptions":[{"id":"cfg","config":{"paths":["maintenance"]}}]}`)
	if ready := next(t, ev); ready.Event != "ready" || !strings.Contains(ready.Data, `"cfg"`) {
		t.Fatalf("ready = %+v", ready)
	}
	f.feed.settle()

	svc := &appconfig.Service{Store: f.svc.Store, Config: f.reg.Realms["acme"].AppConfig, Now: time.Now}
	if _, err := svc.Apply(context.Background(), 0, appconfig.Input{Set: map[string]any{"maintenance.enabled": true}}, "test"); err != nil {
		t.Fatal(err)
	}
	f.feed.add(changefeed.Change{Realm: "acme", DB: "acme___system", Coll: "app_config", System: true, Op: changefeed.Updated, Key: "current"})
	got := next(t, ev)
	var data struct {
		ID, Version string
		Paths       []string
	}
	if err := json.Unmarshal([]byte(got.Data), &data); err != nil || got.Event != "config_changed" || data.ID != "cfg" || data.Version != "c1" || len(data.Paths) != 1 || data.Paths[0] != "maintenance.enabled" {
		t.Fatalf("got %+v (%v)", got, err)
	}
}
