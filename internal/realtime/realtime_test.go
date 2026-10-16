package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// fakeSource is a change history: Open reads it from a position onwards and then follows
// what is appended. Position is the index of the last change read, as text.
type fakeSource struct {
	mu      sync.Mutex
	history []changefeed.Change
	wake    chan struct{}
}

func newFakeSource() *fakeSource { return &fakeSource{wake: make(chan struct{}, 64)} }

func (f *fakeSource) add(c changefeed.Change) {
	f.mu.Lock()
	n := len(f.history) + 1
	c.At = time.Unix(int64(n), 0).UTC()
	f.history = append(f.history, c)
	f.mu.Unlock()
	for i := 0; i < 8; i++ {
		select {
		case f.wake <- struct{}{}:
		default:
		}
	}
}

func (f *fakeSource) Open(_ context.Context, _ changefeed.WatchSet, after changefeed.Position) (changefeed.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.history)
	if after != nil {
		n = int(after[0])
	}
	return &fakeStream{src: f, next: n}, nil
}

type fakeStream struct {
	src  *fakeSource
	next int
}

func (s *fakeStream) Position() changefeed.Position { return changefeed.Position{byte(s.next)} }
func (s *fakeStream) Close()                        {}
func (s *fakeStream) Next(ctx context.Context) (changefeed.Event, error) {
	for {
		s.src.mu.Lock()
		if s.next < len(s.src.history) {
			c := s.src.history[s.next]
			s.next++
			s.src.mu.Unlock()
			return changefeed.Event{Change: &c, Position: changefeed.Position{byte(s.next)}}, nil
		}
		s.src.mu.Unlock()
		select {
		case <-ctx.Done():
			return changefeed.Event{}, ctx.Err()
		case <-s.src.wake:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

type rec struct {
	mu     sync.Mutex
	resets []string
}

func (r *rec) StreamOpened(string)    {}
func (r *rec) StreamClosed(string)    {}
func (r *rec) Event(string, string)   {}
func (r *rec) Refused(string, string) {}
func (r *rec) Reset(_, reason string) {
	r.mu.Lock()
	r.resets = append(r.resets, reason)
	r.mu.Unlock()
}

func newHub(t *testing.T, src *fakeSource, lim Limits) *Hub {
	t.Helper()
	set := changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: "r", DB: "db", Coll: "items"}}}
	mk := func(name string) *changefeed.Consumer {
		c := &changefeed.Consumer{Name: name, Source: src}
		c.SetWatched(set)
		return c
	}
	lazy := changefeed.NewLazy(mk("realtime"), time.Minute)
	t.Cleanup(lazy.Close)
	lim.OpenInterval = -1
	return &Hub{Feed: lazy, Replay: func() *changefeed.Consumer { return mk("replay") }, Limits: lim}
}

// sub follows items whose owner is who.
func sub(id, who string) *Subscription {
	return &Subscription{
		ID: id, DB: "db", Coll: "items",
		Visible: func(d map[string]any) (bool, error) { return d["owner"] == who, nil },
		Render:  func(d map[string]any) map[string]any { return d },
	}
}

func doc(id, owner string, v int) map[string]any {
	return map[string]any{"id": id, "owner": owner, "_meta": map[string]any{"version": int64(v)}}
}

type collector struct {
	mu     sync.Mutex
	frames []Frame
}

func (c *collector) run(t *testing.T, s *Stream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = s.Run(ctx, func(f Frame) error {
			if f.Event != "" {
				c.mu.Lock()
				c.frames = append(c.frames, f)
				c.mu.Unlock()
			}
			return nil
		})
	}()
}

func (c *collector) events(t *testing.T, n int) []Frame {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.frames) >= n {
			out := append([]Frame(nil), c.frames...)
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("wanted %d events, have %d: %v", n, len(c.frames), c.frames)
	return nil
}

func names(fs []Frame) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Event
	}
	return out
}

var key = []byte("0123456789abcdef0123456789abcdef")

func open(t *testing.T, h *Hub, principal string, subs ...*Subscription) *Stream {
	t.Helper()
	s, err := h.Open(Request{Realm: "r", Principal: principal, Key: key, Subscriptions: subs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// A change is created, updated or removed for a subscriber according to whether they could
// see the document before and after it; a stranger learns nothing.
func TestVisibilityDecidesTheEvent(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{})
	var mine, theirs collector
	mine.run(t, open(t, h, "user:a", sub("s", "a")))
	theirs.run(t, open(t, h, "user:b", sub("s", "b")))
	time.Sleep(30 * time.Millisecond)

	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "1", After: doc("1", "a", 1), Version: 1})
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Updated, Key: "1", Before: doc("1", "a", 1), After: doc("1", "a", 2), Version: 2})
	// Handed over: a loses sight of it, b gains it.
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Updated, Key: "1", Before: doc("1", "a", 2), After: doc("1", "b", 3), Version: 3})
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Removed, Key: "1", Before: doc("1", "b", 3), Version: 3})

	got := names(mine.events(t, 3))
	if want := []string{"created", "updated", "removed"}; !equal(got, want) {
		t.Fatalf("owner a: %v, want %v", got, want)
	}
	got = names(theirs.events(t, 2))
	if want := []string{"created", "removed"}; !equal(got, want) {
		t.Fatalf("owner b: %v, want %v", got, want)
	}
	// "removed" carries the id and the version, never the document.
	var m map[string]any
	_ = json.Unmarshal(mine.events(t, 3)[2].Data, &m)
	if m["id"] != "1" || m["doc"] != nil {
		t.Fatalf("removed payload: %v", m)
	}
}

func equal(a, b []string) bool {
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

// A change that arrives without the image its visibility needs is never turned into
// `removed`: the subscriptions are reset, and nobody learns an id.
func TestMissingPreimageFailsClosed(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{})
	obs := &rec{}
	h.Observer = obs
	var c, stranger collector
	c.run(t, open(t, h, "user:a", sub("s", "a")))
	stranger.run(t, open(t, h, "user:b", sub("s", "b")))
	time.Sleep(30 * time.Millisecond)

	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Removed, Key: "secret-id", Version: 4})
	got := c.events(t, 1)
	if got[0].Event != "reset" || !contains(string(got[0].Data), ReasonPreimageMiss) {
		t.Fatalf("got %v", got)
	}
	for _, f := range stranger.events(t, 1) {
		if f.Event == "removed" || contains(string(f.Data), "secret-id") {
			t.Fatalf("a stranger was told about the id: %s", f.Data)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && (len(s) >= len(sub)) && indexOf(s, sub) >= 0
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Over its buffer a stream is reset (slow_consumer) instead of growing or stalling the feed.
func TestSlowConsumerIsReset(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{BufferEvents: 3})
	s := open(t, h, "user:a", sub("s", "a"))
	time.Sleep(30 * time.Millisecond)
	for i := 0; i < 10; i++ { // nobody is reading
		src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "x", After: doc("x", "a", i+1), Version: int64(i + 1)})
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n, first := len(s.queue), ""
		if n > 0 {
			first = s.queue[0].Event
		}
		s.mu.Unlock()
		if n <= 3 && first != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var c collector
	c.run(t, s)
	got := c.events(t, 1)
	if len(got) > 4 {
		t.Fatalf("buffer grew: %d", len(got))
	}
	sawReset := false
	for _, f := range c.events(t, 1) {
		if f.Event == "reset" && contains(string(f.Data), ReasonSlowConsumer) {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatalf("no slow_consumer reset in %v", names(c.events(t, 1)))
	}
}

// A token is only good for the realm and caller it was made for.
func TestResumeTokenIsBound(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{})
	tok := Seal(key, Token{Pos: []byte{0}, Realm: "r", Principal: "user:a", Issued: time.Now().Unix()})
	for name, req := range map[string]Request{
		"another caller": {Realm: "r", Principal: "user:b", Key: key, Resume: tok},
		"another realm":  {Realm: "other", Principal: "user:a", Key: key, Resume: tok},
		"another key":    {Realm: "r", Principal: "user:a", Key: []byte("a different key of 32 bytes!!!!!!"), Resume: tok},
		"garbage":        {Realm: "r", Principal: "user:a", Key: key, Resume: "nope"},
	} {
		if _, err := h.Open(req); !errors.Is(err, ErrBadToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s, err := h.Open(Request{Realm: "r", Principal: "user:a", Key: key, Resume: tok, Subscriptions: []*Subscription{sub("s", "a")}})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

// Resuming replays what was missed, with current access, and then follows the live feed
// without repeating or skipping.
func TestResumeReplaysThenGoesLive(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{})
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "0", After: doc("0", "a", 1), Version: 1})
	first := open(t, h, "user:a", sub("s", "a"))
	var c1 collector
	c1.run(t, first)
	time.Sleep(30 * time.Millisecond)
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "1", After: doc("1", "a", 1), Version: 1})
	f := c1.events(t, 1)
	token := f[0].ID
	if token == "" {
		t.Fatal("the last frame of a change carries a resume token")
	}
	first.Close()
	// Missed while disconnected, one of them not for a.
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "2", After: doc("2", "a", 1), Version: 1})
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "3", After: doc("3", "b", 1), Version: 1})
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "4", After: doc("4", "a", 1), Version: 1})

	s, err := h.Open(Request{Realm: "r", Principal: "user:a", Key: key, Resume: token, Subscriptions: []*Subscription{sub("s", "a")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	var c2 collector
	c2.run(t, s)
	src.add(changefeed.Change{DB: "db", Coll: "items", Op: changefeed.Created, Key: "5", After: doc("5", "a", 1), Version: 1})
	got := c2.events(t, 3)
	ids := []string{}
	for _, fr := range got {
		var m struct {
			Doc struct{ ID string }
		}
		_ = json.Unmarshal(fr.Data, &m)
		ids = append(ids, m.Doc.ID)
	}
	if want := []string{"2", "4", "5"}; !equal(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	time.Sleep(60 * time.Millisecond)
	if n := len(c2.events(t, 3)); n != 3 {
		t.Fatalf("repeated events: %d", n)
	}
}

func TestLimits(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{StreamsPerUser: 2, Streams: 3, SubscriptionsPerStream: 1})
	a1 := open(t, h, "user:a")
	open(t, h, "user:a")
	if _, err := h.Open(Request{Realm: "r", Principal: "user:a", Key: key}); !errors.Is(err, ErrUserStreams) {
		t.Fatalf("per user: %v", err)
	}
	open(t, h, "user:b")
	if _, err := h.Open(Request{Realm: "r", Principal: "user:c", Key: key}); !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("per instance: %v", err)
	}
	a1.Close()
	if _, err := h.Open(Request{Realm: "r", Principal: "user:c", Key: key, Subscriptions: []*Subscription{sub("1", "a"), sub("2", "a")}}); !errors.Is(err, ErrTooManySubscriptions) {
		t.Fatalf("subscriptions: %v", err)
	}
	// One open a second per caller.
	h.Limits.OpenInterval = time.Hour
	s, err := h.Open(Request{Realm: "r", Principal: "user:z", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := h.Open(Request{Realm: "r", Principal: "user:z", Key: key}); !errors.Is(err, ErrOpenTooFast) {
		t.Fatalf("rate: %v", err)
	}
}

// A reload that changes a collection removes its subscriptions: the client subscribes again.
func TestConfigChangedRemovesSubscriptions(t *testing.T) {
	src := newFakeSource()
	h := newHub(t, src, Limits{})
	s := open(t, h, "user:a", sub("s", "a"))
	var c collector
	c.run(t, s)
	h.ConfigChanged(map[string]bool{CollKey("db", "items"): true})
	got := c.events(t, 1)
	if got[0].Event != "reset" || !contains(string(got[0].Data), ReasonConfigChanged) {
		t.Fatalf("got %v", got)
	}
	if len(s.IDs()) != 0 {
		t.Fatal("the subscription must be gone")
	}
}
