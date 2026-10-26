package changefeed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource scripts what each Open returns.
type fakeSource struct {
	mu     sync.Mutex
	opens  []openCall
	script []func(openCall) (Stream, error) // one per Open, the last repeats
}

type openCall struct {
	set   WatchSet
	after Position
}

func (f *fakeSource) Open(ctx context.Context, set WatchSet, after Position) (Stream, error) {
	f.mu.Lock()
	call := openCall{set: set, after: after}
	f.opens = append(f.opens, call)
	i := min(len(f.opens)-1, len(f.script)-1)
	fn := f.script[i]
	f.mu.Unlock()
	return fn(call)
}

func (f *fakeSource) calls() []openCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]openCall(nil), f.opens...)
}

// fakeStream yields its events, then blocks until the context is done, or fails with err.
type fakeStream struct {
	events []Event
	err    error
	pos    Position
	closed atomic.Bool
}

func (s *fakeStream) Next(ctx context.Context) (Event, error) {
	if len(s.events) > 0 {
		ev := s.events[0]
		s.events = s.events[1:]
		return ev, nil
	}
	if s.err != nil {
		return Event{}, s.err
	}
	<-ctx.Done()
	return Event{}, ctx.Err()
}
func (s *fakeStream) Position() Position { return s.pos }
func (s *fakeStream) Close()             { s.closed.Store(true) }

type recorder struct {
	mu      sync.Mutex
	changes []Change
	idles   []Position
	gaps    []Gap
	ch      chan struct{}
}

func newRecorder() *recorder { return &recorder{ch: make(chan struct{}, 64)} }

func (r *recorder) Change(_ context.Context, c Change, _ Position) {
	r.mu.Lock()
	r.changes = append(r.changes, c)
	r.mu.Unlock()
	r.ch <- struct{}{}
}
func (r *recorder) Idle(_ context.Context, p Position) {
	r.mu.Lock()
	r.idles = append(r.idles, p)
	r.mu.Unlock()
	r.ch <- struct{}{}
}
func (r *recorder) Gap(_ context.Context, g Gap) {
	r.mu.Lock()
	r.gaps = append(r.gaps, g)
	r.mu.Unlock()
	r.ch <- struct{}{}
}

func (r *recorder) wait(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-r.ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for notification %d of %d", i+1, n)
		}
	}
}

func fastBackoff(int) time.Duration { return time.Millisecond }

func set(colls ...string) WatchSet {
	var w WatchSet
	for _, c := range colls {
		w.Collections = append(w.Collections, Namespace{Realm: "shop", DB: "shop__main", Coll: c})
	}
	return w
}

func TestWatchSetEqualIgnoresOrder(t *testing.T) {
	a := WatchSet{Collections: set("a", "b").Collections, System: []Namespace{{Realm: "shop", DB: "shop___system", Coll: "users"}}}
	b := WatchSet{Collections: set("b", "a").Collections, System: a.System}
	if !a.Equal(b) {
		t.Error("same namespaces in another order must be equal")
	}
	if a.Equal(WatchSet{Collections: set("a").Collections, System: a.System}) {
		t.Error("different namespaces must differ")
	}
	if !(WatchSet{}).Empty() || a.Empty() || a.Count() != 3 {
		t.Errorf("Empty/Count: %v %v %d", (WatchSet{}).Empty(), a.Empty(), a.Count())
	}
}

func TestConsumerDeliversAndTracksPosition(t *testing.T) {
	st := &fakeStream{pos: Position("p0"), events: []Event{
		{Change: &Change{Key: "1", Op: Created}, Position: Position("p1")},
		{Position: Position("p2")}, // a quiet period
		{Change: &Change{Key: "1", Op: Removed}, Position: Position("p3")},
	}}
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) { return st, nil }}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Position)
	go func() { p, _ := c.Run(ctx, nil, rec); done <- p }()
	rec.wait(t, 4) // the position at open, two changes and a quiet period
	cancel()
	pos := <-done
	if string(pos) != "p3" {
		t.Errorf("final position = %q, want p3", pos)
	}
	if len(rec.changes) != 2 || rec.changes[0].Op != Created || rec.changes[1].Op != Removed || len(rec.idles) != 2 || string(rec.idles[0]) != "p0" || string(rec.idles[1]) != "p2" {
		t.Errorf("changes=%+v idles=%q", rec.changes, rec.idles)
	}
	if !st.closed.Load() {
		t.Error("stream not closed")
	}
}

func TestConsumerReopensAfterAnErrorFromTheLastPosition(t *testing.T) {
	first := &fakeStream{pos: Position("p0"), events: []Event{{Change: &Change{Key: "1", Op: Created}, Position: Position("p1")}}, err: errors.New("connection reset")}
	second := &fakeStream{pos: Position("p1")}
	n := 0
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) {
		n++
		if n == 1 {
			return first, nil
		}
		return second, nil
	}}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	rec := newRecorder()
	ctx := t.Context()
	go c.Run(ctx, nil, rec) //nolint:errcheck
	rec.wait(t, 1)
	deadline := time.Now().Add(3 * time.Second)
	for len(src.calls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	calls := src.calls()
	if len(calls) < 2 {
		t.Fatalf("opens = %d, want a reopen", len(calls))
	}
	if string(calls[0].after) != "" || string(calls[1].after) != "p1" {
		t.Errorf("resumed after %q then %q, want from now then p1", calls[0].after, calls[1].after)
	}
}

func TestConsumerReportsAGapAndContinuesFromNow(t *testing.T) {
	st := &fakeStream{pos: Position("p9")}
	n := 0
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) {
		n++
		if n == 1 {
			return nil, ErrHistoryLost
		}
		return st, nil
	}}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	rec := newRecorder()
	ctx := t.Context()
	go c.Run(ctx, Position("stale"), rec) //nolint:errcheck
	rec.wait(t, 1)
	if len(rec.gaps) != 1 || string(rec.gaps[0].From) != "stale" {
		t.Fatalf("gaps = %+v", rec.gaps)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(src.calls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls := src.calls(); len(calls) < 2 || calls[1].after != nil {
		t.Errorf("after the gap the stream must open from now, calls = %+v", calls)
	}
}

func TestSetWatchedReopensAfterTheLastPosition(t *testing.T) {
	var streams []*fakeStream
	var mu sync.Mutex
	src := &fakeSource{script: []func(openCall) (Stream, error){func(oc openCall) (Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		s := &fakeStream{pos: Position("start")}
		if len(streams) == 0 {
			s.events = []Event{{Change: &Change{Key: "1", Op: Updated}, Position: Position("p1")}}
		}
		streams = append(streams, s)
		return s, nil
	}}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	rec := newRecorder()
	ctx := t.Context()
	go c.Run(ctx, nil, rec) //nolint:errcheck
	rec.wait(t, 1)
	c.SetWatched(set("items", "orders"))
	deadline := time.Now().Add(3 * time.Second)
	for len(src.calls()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	calls := src.calls()
	if len(calls) < 2 {
		t.Fatalf("opens = %d", len(calls))
	}
	if string(calls[1].after) != "p1" || len(calls[1].set.Collections) != 2 {
		t.Errorf("reopen = %+v, want after p1 with both collections", calls[1])
	}
	mu.Lock()
	closed := streams[0].closed.Load()
	mu.Unlock()
	if !closed {
		t.Error("the first stream was not closed")
	}
}

func TestConsumerWaitsWhileNothingIsWatched(t *testing.T) {
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) { return &fakeStream{pos: Position("p0")}, nil }}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	rec := newRecorder()
	ctx := t.Context()
	go c.Run(ctx, nil, rec) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)
	if n := len(src.calls()); n != 0 {
		t.Fatalf("opened %d streams with nothing watched", n)
	}
	c.SetWatched(set("items"))
	deadline := time.Now().Add(3 * time.Second)
	for len(src.calls()) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(src.calls()); n != 1 {
		t.Fatalf("opens = %d after a set was given", n)
	}
}

func TestLazyOpensOnFirstSubscriberAndClosesAfterGrace(t *testing.T) {
	var opens atomic.Int32
	var live atomic.Int32
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) {
		opens.Add(1)
		live.Add(1)
		return &countedStream{pos: Position("p0"), live: &live}, nil
	}}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	l := NewLazy(c, 40*time.Millisecond)
	defer l.Close()

	time.Sleep(30 * time.Millisecond)
	if opens.Load() != 0 || l.Open() {
		t.Fatal("a lazy consumer with no subscribers must read nothing")
	}
	rel1 := l.Acquire(newRecorder())
	rel2 := l.Acquire(newRecorder())
	waitFor(t, func() bool { return opens.Load() == 1 && l.Open() })
	if l.Subscribers() != 2 {
		t.Errorf("Subscribers = %d", l.Subscribers())
	}
	rel1()
	rel1() // harmless
	time.Sleep(80 * time.Millisecond)
	if !l.Open() {
		t.Fatal("closed while a subscriber remained")
	}
	rel2()
	waitFor(t, func() bool { return !l.Open() })
	waitFor(t, func() bool { return live.Load() == 0 })

	// A later subscriber opens it again, from now.
	rel3 := l.Acquire(newRecorder())
	defer rel3()
	waitFor(t, func() bool { return opens.Load() == 2 && l.Open() })
	if calls := src.calls(); calls[1].after != nil {
		t.Errorf("the second open must start from now, got %q", calls[1].after)
	}
}

func TestLazyFansOutToEverySubscriber(t *testing.T) {
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) {
		return &fakeStream{pos: Position("p0"), events: []Event{
			{Change: &Change{Key: "1", Op: Created}, Position: Position("p1")},
		}}, nil
	}}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	l := NewLazy(c, time.Hour)
	defer l.Close()
	a, b := newRecorder(), newRecorder()
	// Both subscribe before the stream can deliver: the first Acquire starts the reader, but
	// the stream only yields once opened; the second Acquire races it, so allow a replay by
	// asserting each saw at least the change once the set changes.
	relA := l.Acquire(a)
	relB := l.Acquire(b)
	defer relA()
	defer relB()
	waitFor(t, func() bool { return len(src.calls()) >= 1 })
	// A set change reopens a stream carrying the event again, now with both subscribed.
	c.SetWatched(set("items", "orders"))
	a.wait(t, 1)
	b.wait(t, 1)
}

func TestCloseStopsAndRefusesLaterSubscribers(t *testing.T) {
	src := &fakeSource{script: []func(openCall) (Stream, error){func(openCall) (Stream, error) { return &fakeStream{pos: Position("p0")}, nil }}}
	c := &Consumer{Name: "t", Source: src, Backoff: fastBackoff}
	c.SetWatched(set("items"))
	l := NewLazy(c, time.Hour)
	l.Acquire(newRecorder())
	waitFor(t, func() bool { return l.Open() && len(src.calls()) == 1 })
	l.Close()
	if l.Open() {
		t.Error("still open after Close")
	}
	l.Acquire(newRecorder())()
	time.Sleep(20 * time.Millisecond)
	if l.Open() || len(src.calls()) != 1 {
		t.Errorf("Acquire after Close reopened the stream (opens=%d)", len(src.calls()))
	}
}

type countedStream struct {
	fakeStream
	live *atomic.Int32
	once sync.Once
}

func (s *countedStream) Close() { s.once.Do(func() { s.live.Add(-1) }) }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
