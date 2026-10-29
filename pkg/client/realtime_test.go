package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rtConn is one open stream, as the server sees it.
type rtConn struct {
	body    map[string]any
	send    chan string
	done    chan struct{}
	closeIt chan struct{}
}

func (c *rtConn) emit(event, id, data string) {
	s := ""
	if id != "" {
		s += "id: " + id + "\n"
	}
	c.send <- s + "event: " + event + "\ndata: " + data + "\n\n"
}

func (c *rtConn) hang() { close(c.closeIt) } // the server ends the stream

// rtServer is a fake backd with a live stream and a few collections to read.
type rtServer struct {
	t      *testing.T
	conns  chan *rtConn
	mu     sync.Mutex
	lists  map[string]string // "main/items" -> JSON of a page
	gets   map[string]string // "main/items/i1" -> JSON
	status map[string]int    // path -> forced status
	openFn func(w http.ResponseWriter) bool
	reads  atomic.Int32
}

func newRTServer(t *testing.T) (*rtServer, *Client) {
	s := &rtServer{t: t, conns: make(chan *rtConn, 8), lists: map[string]string{}, gets: map[string]string{}, status: map[string]int{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := New(Options{URL: srv.URL, Realm: "r", APIKey: "bdk_x"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return s, c
}

func (s *rtServer) setList(key, json string) { s.mu.Lock(); s.lists[key] = json; s.mu.Unlock() }

func (s *rtServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1/r/")
	if r.Method == "POST" && p == "_realtime" {
		s.stream(w, r)
		return
	}
	s.reads.Add(1)
	s.mu.Lock()
	code, forced := s.status[p]
	list, isList := s.lists[p]
	get, isGet := s.gets[p]
	s.mu.Unlock()
	switch {
	case forced:
		w.WriteHeader(code)
		fmt.Fprint(w, `{"error":{"code":"forbidden","message":"no"}}`)
	case isList:
		io.WriteString(w, list)
	case isGet:
		io.WriteString(w, get)
	case p == "_config":
		w.Header().Set("ETag", `"c1"`)
		io.WriteString(w, `{"values":{"flag":true},"cache_ttl":60}`)
	default:
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"no"}}`)
	}
}

func (s *rtServer) stream(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	if s.openFn != nil && !s.openFn(w) {
		return
	}
	conn := &rtConn{body: body, send: make(chan string, 32), done: make(chan struct{}), closeIt: make(chan struct{})}
	s.conns <- conn
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	defer close(conn.done)
	for {
		select {
		case msg := <-conn.send:
			io.WriteString(w, msg)
			w.(http.Flusher).Flush()
		case <-conn.closeIt:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *rtServer) next() *rtConn {
	select {
	case c := <-s.conns:
		return c
	case <-time.After(3 * time.Second):
		s.t.Fatal("no stream was opened")
		return nil
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func idsOf(docs []Doc) string {
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID())
	}
	return strings.Join(ids, ",")
}

func doc(id string, version int, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return fmt.Sprintf(`{"id":%q,"_meta":{"version":%d}%s}`, id, version, extra)
}

func page(docs ...string) string {
	return `{"items":[` + strings.Join(docs, ",") + `],"has_more":false}`
}

func TestLiveViewLoadsThenFollowsEvents(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page(doc("b", 1, `"n":2`), doc("a", 1, `"n":1`)))
	view, err := c.DB("main").Collection("items").Subscribe(SubscriptionSpec{Where: Where{"status": "open"}, Load: LoadSpec{Sort: []string{"n"}}}, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var statuses []LiveStatus
	var smu sync.Mutex
	view.OnStatus(func(st LiveStatus) { smu.Lock(); statuses = append(statuses, st); smu.Unlock() })
	conn := s.next()
	subs := conn.body["subscriptions"].([]any)
	first := subs[0].(map[string]any)
	if len(subs) != 1 || first["db"] != "main" || first["collection"] != "items" || first["where"].(map[string]any)["status"] != "open" || first["id"] != "s1" {
		t.Errorf("subscribed with %v", conn.body)
	}
	if conn.body["resume_from"] != nil {
		t.Error("resume_from on the first stream")
	}
	conn.emit("ready", "", `{"subscriptions":["s1"]}`)

	st, err := view.Ready(bg)
	if err != nil || st.Status != StatusLive || idsOf(st.Items) != "a,b" {
		t.Fatalf("%v %+v", err, st)
	}
	conn.emit("created", "tok1", `{"sub":"s1","doc":`+doc("c", 1, `"n":0`)+`}`)
	eventually(t, "created", func() bool { return idsOf(view.Items()) == "c,a,b" })
	conn.emit("updated", "tok2", `{"sub":"s1","doc":`+doc("a", 2, `"n":5`)+`}`)
	eventually(t, "updated", func() bool { return idsOf(view.Items()) == "c,b,a" })
	conn.emit("updated", "tok3", `{"sub":"s1","doc":`+doc("a", 1, `"n":-9`)+`}`) // an older version is ignored
	conn.emit("removed", "tok4", `{"sub":"s1","id":"b","version":1}`)
	eventually(t, "removed", func() bool { return idsOf(view.Items()) == "c,a" })
	conn.emit("removed", "tok5", `{"sub":"s1","id":"a","version":1}`) // older than the one held: kept
	conn.emit("created", "tok6", `{"sub":"s1","doc":`+doc("d", 1, `"n":9`)+`}`)
	eventually(t, "d", func() bool { return idsOf(view.Items()) == "c,a,d" })

	smu.Lock()
	defer smu.Unlock()
	if len(statuses) == 0 || statuses[len(statuses)-1] != StatusLive {
		t.Errorf("statuses %v", statuses)
	}
}

func TestEventsBeforeTheLoadAreBuffered(t *testing.T) {
	s, _ := newRTServer(t)
	gate := make(chan struct{})
	s.setList("main/items", page(doc("a", 1, "")))
	// hold the read until the events have been sent
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			<-gate
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv2.Close)
	c2, _ := New(Options{URL: srv2.URL, Realm: "r", APIKey: "k"})
	t.Cleanup(func() { c2.Close() })
	view, _ := c2.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	conn := s.next()
	conn.emit("ready", "", `{}`)
	conn.emit("created", "t1", `{"sub":"s1","doc":`+doc("b", 1, "")+`}`)
	conn.emit("updated", "t2", `{"sub":"s1","doc":`+doc("a", 2, `"x":1`)+`}`)
	time.Sleep(100 * time.Millisecond)
	if view.Status() == StatusLive {
		t.Error("live before the load finished")
	}
	close(gate)
	st, err := view.Ready(bg)
	if err != nil || idsOf(st.Items) != "a,b" || st.Items[0]["x"] != float64(1) {
		t.Fatalf("%v %+v", err, st.Items)
	}
}

func TestResumeAfterTheStreamDrops(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page(doc("a", 1, "")))
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{MaxBackoff: 10 * time.Millisecond})
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	conn.emit("created", "resume-1", `{"sub":"s1","doc":`+doc("b", 1, "")+`}`)
	eventually(t, "b", func() bool { return idsOf(view.Items()) == "a,b" })

	conn.hang()
	eventually(t, "resyncing", func() bool { return view.Status() == StatusResyncing })
	if idsOf(view.Items()) != "a,b" {
		t.Error("the items must stay while resyncing")
	}
	again := s.next()
	if again.body["resume_from"] != "resume-1" {
		t.Errorf("resume_from = %v", again.body["resume_from"])
	}
	reads := s.reads.Load()
	again.emit("ready", "", `{}`)
	view.Ready(bg)
	if s.reads.Load() != reads {
		t.Error("a resumed stream must not reload what it holds")
	}
}

func TestInvalidResumeTokenStartsOver(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page(doc("a", 1, "")))
	var attempts atomic.Int32
	s.openFn = func(w http.ResponseWriter) bool {
		if attempts.Add(1) == 2 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"invalid_resume_token","message":"too old"}}`)
			return false
		}
		return true
	}
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{MaxBackoff: 5 * time.Millisecond})
	conn := s.next()
	conn.emit("ready", "tok", `{}`)
	view.Ready(bg)
	conn.hang()
	third := s.next() // the second attempt was refused; the third starts without a token
	if third.body["resume_from"] != nil {
		t.Errorf("resume_from = %v", third.body["resume_from"])
	}
	s.setList("main/items", page(doc("a", 1, ""), doc("z", 1, "")))
	third.emit("ready", "", `{}`)
	eventually(t, "reload", func() bool { return idsOf(view.Items()) == "a,z" })
}

func TestResetReloadsWithJitter(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page(doc("a", 1, "")))
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{Random: func() float64 { return 0 }})
	resets := make(chan ResetInfo, 1)
	view.OnReset(func(i ResetInfo) { resets <- i })
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	s.setList("main/items", page(doc("a", 2, ""), doc("b", 1, "")))
	conn.emit("reset", "", `{"subs":["s1","nope"],"reason":"slow_consumer"}`)
	select {
	case i := <-resets:
		if i.Reason != "slow_consumer" || i.Subscription != "0" {
			t.Errorf("%+v", i)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reset callback")
	}
	eventually(t, "reload", func() bool { return idsOf(view.Items()) == "a,b" })
}

func TestConfigChangedResetReconnects(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	conn.emit("reset", "", `{"subs":["s1"],"reason":"config_changed"}`)
	again := s.next() // the subscription must be made again
	if len(again.body["subscriptions"].([]any)) != 1 {
		t.Errorf("%v", again.body)
	}
}

func TestSessionEndedClosesEveryView(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	errs := make(chan error, 1)
	view.OnError(func(err error) { errs <- err })
	updates := view.Updates()
	conn := s.next()
	conn.emit("session_ended", "", `{}`)
	select {
	case err := <-errs:
		if !errors.Is(err, ErrSessionEnded) || !errors.Is(err, ErrAuthentication) {
			t.Errorf("%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no error")
	}
	if _, err := view.Ready(bg); !errors.Is(err, ErrSessionEnded) || view.Status() != StatusClosed || !errors.Is(view.Err(), ErrSessionEnded) {
		t.Errorf("%v", err)
	}
	eventually(t, "the updates channel to close", func() bool {
		select {
		case _, ok := <-updates:
			return !ok
		default:
			return false
		}
	})
	if c.Realtime().Open() != 0 {
		t.Error("the failed view is still registered")
	}
}

func TestRefusedSubscriptionFailsOnlyThatView(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	s.setList("main/other", page())
	var attempts atomic.Int32
	s.openFn = func(w http.ResponseWriter) bool {
		if attempts.Add(1) == 1 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"code":"realtime_not_enabled","message":"nope","details":[{"path":"subscriptions[1].collection","reason":"x"}]}}`)
			return false
		}
		return true
	}
	good, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	bad, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "other"}}, SubscribeOptions{})
	conn := s.next() // the stream that is accepted holds the good view only
	if n := len(conn.body["subscriptions"].([]any)); n != 1 {
		t.Errorf("%d subscriptions", n)
	}
	conn.emit("ready", "", `{}`)
	if _, err := good.Ready(bg); err != nil {
		t.Errorf("good view: %v", err)
	}
	if _, err := bad.Ready(bg); !errors.Is(err, ErrRealtimeNotEnabled) {
		t.Errorf("bad view: %v", err)
	}
}

func TestSingleRefusedViewFails(t *testing.T) {
	s, c := newRTServer(t)
	s.openFn = func(w http.ResponseWriter) bool {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":"forbidden","message":"no"}}`)
		return false
	}
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	if _, err := view.Ready(bg); !errors.Is(err, ErrForbidden) {
		t.Errorf("%v", err)
	}
}

func TestCloseEndsTheStream(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	view.Close()
	view.Close()
	select {
	case <-conn.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream stayed open with no views")
	}
	if view.Status() != StatusClosed || c.Realtime().Open() != 0 {
		t.Error("not closed")
	}
}

func TestClientCloseClosesViews(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	c.Close()
	select {
	case <-conn.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream stayed open after Close")
	}
	eventually(t, "the view to close", func() bool { return view.Status() == StatusClosed })
	if _, err := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{}); err == nil {
		t.Error("a closed client takes no subscriptions")
	}
}

func TestFollowingOneDocument(t *testing.T) {
	s, c := newRTServer(t)
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items", Doc: "i1", ID: "one"}}, SubscribeOptions{})
	conn := s.next()
	if sub := conn.body["subscriptions"].([]any)[0].(map[string]any); sub["doc"] != "i1" || sub["where"] != nil {
		t.Errorf("%v", sub)
	}
	conn.emit("ready", "", `{}`)
	st, err := view.Ready(bg) // the document doesn't exist yet: 404 is an empty view
	if err != nil || len(st.Subscriptions["one"].Items) != 0 {
		t.Fatalf("%v %+v", err, st)
	}
	conn.emit("created", "t", `{"sub":"s1","doc":`+doc("i1", 1, "")+`}`)
	eventually(t, "the document", func() bool { return idsOf(view.Items()) == "i1" })
}

func TestAccessLostEmptiesTheView(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page(doc("a", 1, "")))
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{Random: func() float64 { return 0 }})
	lost := make(chan string, 1)
	view.OnAccessLost(func(name string) { lost <- name })
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	s.mu.Lock()
	s.status["main/items"] = 403
	s.mu.Unlock()
	conn.emit("reset", "", `{"subs":["s1"],"reason":"access_changed"}`)
	select {
	case name := <-lost:
		if name != "0" {
			t.Errorf("name %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no access_lost")
	}
	eventually(t, "an empty view", func() bool { return len(view.Items()) == 0 && view.Status() == StatusLive })
}

func TestReferencesAreKeptBesideTheList(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/posts", page(doc("p1", 1, `"author":"u1","tags":["t1","t2"]`), doc("p2", 1, `"author":"u1"`)))
	s.setList("main/users", page(doc("u1", 1, `"name":"Ada","secret":"x"`)))
	s.setList("main/tags", page(doc("t1", 1, `"label":"go"`), doc("t2", 1, `"label":"db"`)))
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "posts", References: map[string]ReferenceSpec{
		"author": {Field: "author", Collection: "users", Fields: []string{"name"}},
		"tags":   {Field: "tags", Collection: "tags"},
	}}}, SubscribeOptions{})
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	eventually(t, "references", func() bool {
		r := view.State().References
		return len(r["author"]) == 1 && len(r["tags"]) == 2
	})
	st := view.State()
	u := st.References["author"]["u1"]
	if u["name"] != "Ada" || u["secret"] != nil || u["id"] != "u1" || st.References["tags"]["t2"]["label"] != "db" {
		t.Errorf("%+v", st.References)
	}
}

func TestUpdatesChannelKeepsTheLatest(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page(doc("a", 1, "")))
	view, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	updates := view.Updates()
	conn := s.next()
	conn.emit("ready", "", `{}`)
	view.Ready(bg)
	for i := 2; i < 12; i++ {
		conn.emit("updated", fmt.Sprint("t", i), `{"sub":"s1","doc":`+doc("a", i, "")+`}`)
	}
	eventually(t, "the last version", func() bool { return versionOf(view.Items()[0]) == 11 })
	time.Sleep(50 * time.Millisecond)
	var last LiveState
	select {
	case last = <-updates:
	case <-time.After(time.Second):
		t.Fatal("no update")
	}
	for drained := false; !drained; {
		select {
		case last = <-updates:
		default:
			drained = true
		}
	}
	if versionOf(last.Items[0]) != 11 {
		t.Errorf("the last update has version %d", versionOf(last.Items[0]))
	}
}

func TestTeamScopeIsPartOfTheFilter(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	view, err := c.DB("main").Collection("items").InTeam("docs").Subscribe(SubscriptionSpec{Where: `{"status":"open"}`}, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	conn := s.next()
	w := conn.body["subscriptions"].([]any)[0].(map[string]any)["where"].(map[string]any)
	if w["_meta.team"] != "docs" || w["status"] != "open" {
		t.Errorf("where = %v", w)
	}
}

func TestSubscribeChecksTheSpecs(t *testing.T) {
	_, c := newRTServer(t)
	for _, specs := range [][]SubscriptionSpec{nil, {{DB: "main"}}, {{DB: "a", Collection: "b", ID: "x"}, {DB: "a", Collection: "c", ID: "x"}}} {
		if _, err := c.Realtime().Subscribe(specs, SubscribeOptions{}); err == nil {
			t.Errorf("%+v was accepted", specs)
		}
	}
}

func TestSubscriptionsShareOneStream(t *testing.T) {
	s, c := newRTServer(t)
	s.setList("main/items", page())
	s.setList("main/other", page())
	a, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "items"}}, SubscribeOptions{})
	b, _ := c.Realtime().Subscribe([]SubscriptionSpec{{DB: "main", Collection: "other", ID: "o"}, {DB: "main", Collection: "items", ID: "i"}}, SubscribeOptions{})
	conn := s.next()
	if n := len(conn.body["subscriptions"].([]any)); n != 3 {
		t.Errorf("%d subscriptions on the stream", n)
	}
	select {
	case <-s.conns:
		t.Error("a second stream was opened")
	case <-time.After(100 * time.Millisecond):
	}
	conn.emit("ready", "", `{}`)
	a.Ready(bg)
	st, _ := b.Ready(bg)
	if len(st.Subscriptions) != 2 || st.Subscriptions["o"].Status != StatusLive {
		t.Errorf("%+v", st)
	}
	// closing one view re-opens the stream with what is left
	b.Close()
	again := s.next()
	if n := len(again.body["subscriptions"].([]any)); n != 1 {
		t.Errorf("%d subscriptions after closing a view", n)
	}
}

func TestConfigLiveRereadsWhatChanged(t *testing.T) {
	s, c := newRTServer(t)
	cfg := c.Config()
	if _, err := cfg.Load(bg, []string{"flag"}); err != nil {
		t.Fatal(err)
	}
	reads := s.reads.Load()
	stop := cfg.Live("flag")
	conn := s.next()
	sub := conn.body["subscriptions"].([]any)[0].(map[string]any)
	if sub["id"] != "_config" || sub["config"].(map[string]any)["paths"].([]any)[0] != "flag" {
		t.Errorf("config subscription %v", sub)
	}
	conn.emit("ready", "", `{}`)
	eventually(t, "the re-read after ready", func() bool { return s.reads.Load() > reads })
	reads = s.reads.Load()
	conn.emit("config_changed", "", `{"paths":["other.thing"]}`)
	time.Sleep(100 * time.Millisecond)
	if s.reads.Load() != reads {
		t.Error("a change outside the followed paths caused a read")
	}
	conn.emit("config_changed", "", `{"paths":["flag"]}`)
	eventually(t, "the re-read after a change", func() bool { return s.reads.Load() > reads })
	stop()
	select {
	case <-conn.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream stayed open after stop")
	}
}

func TestConfigLiveReportsARefusal(t *testing.T) {
	s, c := newRTServer(t)
	s.openFn = func(w http.ResponseWriter) bool {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":"invalid_input","message":"bad paths","details":[{"path":"subscriptions[0].config","reason":"x"}]}}`)
		return false
	}
	errs := make(chan error, 1)
	c.Config().OnError(func(err error) { errs <- err })
	c.Config().Live("a")
	select {
	case err := <-errs:
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no error")
	}
}

func TestCompareValues(t *testing.T) {
	order := []any{nil, 1.0, 2.0, false, true, "a", "b"}
	for i := range order {
		for j := range order {
			got := compareValues(order[i], order[j])
			if (i < j && got >= 0) || (i == j && got != 0) || (i > j && got <= 0) {
				t.Errorf("compare(%v, %v) = %d", order[i], order[j], got)
			}
		}
	}
}

func TestDecodeDocs(t *testing.T) {
	type item struct {
		ID string  `json:"id"`
		N  float64 `json:"n"`
	}
	got, err := DecodeDocs[item]([]Doc{{"id": "a", "n": 2.0}, {"id": "b"}})
	if err != nil || len(got) != 2 || got[0].N != 2 || got[1].ID != "b" {
		t.Errorf("%v %+v", err, got)
	}
}
