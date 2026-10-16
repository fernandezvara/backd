package realtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// Load scenarios for the realtime hub (design/triggers-and-realtime.md §6, issue 27.20), over a
// scripted change history so that they need no database and are exact. They run at a reduced
// scale in CI; BACKD_LOAD_SCALE=10 gives the numbers of the operations guide, and
// BACKD_LOAD_REPORT=<file> appends the result lines.

func loadScale() int {
	if n, err := strconv.Atoi(os.Getenv("BACKD_LOAD_SCALE")); err == nil && n > 0 {
		return n
	}
	return 1
}

func report(t *testing.T, scenario string, kv ...any) {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario=%s", scenario)
	for i := 0; i+1 < len(kv); i += 2 {
		switch v := kv[i+1].(type) {
		case float64:
			fmt.Fprintf(&b, " %v=%.1f", kv[i], v)
		case time.Duration:
			fmt.Fprintf(&b, " %v=%dms", kv[i], v.Milliseconds())
		default:
			fmt.Fprintf(&b, " %v=%v", kv[i], v)
		}
	}
	t.Log("RESULT " + b.String())
	if path := os.Getenv("BACKD_LOAD_REPORT"); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, b.String())
			f.Close()
		}
	}
}

// cpuSeconds is the process's user and system CPU time.
func cpuSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6 + float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
}

func heapMB() float64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// history is a scripted change history shared by several feeds.
type history struct {
	mu      sync.Mutex
	changes []changefeed.Change
	opens   atomic.Int64
	wake    chan struct{}
}

func newHistory() *history { return &history{wake: make(chan struct{})} }

func (h *history) add(cs ...changefeed.Change) {
	h.mu.Lock()
	for _, c := range cs {
		c.At = time.Unix(int64(len(h.changes)/1000+1), 0).UTC()
		c.Increment = uint32(len(h.changes) % 1000)
		h.changes = append(h.changes, c)
	}
	old := h.wake
	h.wake = make(chan struct{})
	h.mu.Unlock()
	close(old)
}

func (h *history) size() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.changes)
}

func pos(n int) changefeed.Position {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	return b
}

func (h *history) Open(_ context.Context, _ changefeed.WatchSet, after changefeed.Position) (changefeed.Stream, error) {
	h.opens.Add(1)
	h.mu.Lock()
	n := len(h.changes)
	h.mu.Unlock()
	if after != nil {
		n = int(binary.BigEndian.Uint64(after))
	}
	return &histStream{h: h, n: n}, nil
}

type histStream struct {
	h *history
	n int
}

func (s *histStream) Position() changefeed.Position { return pos(s.n) }
func (s *histStream) Close()                        {}
func (s *histStream) Next(ctx context.Context) (changefeed.Event, error) {
	for {
		s.h.mu.Lock()
		if s.n < len(s.h.changes) {
			c := s.h.changes[s.n]
			s.n++
			s.h.mu.Unlock()
			return changefeed.Event{Change: &c, Position: pos(s.n)}, nil
		}
		wake := s.h.wake
		s.h.mu.Unlock()
		select {
		case <-ctx.Done():
			return changefeed.Event{}, ctx.Err()
		case <-wake:
		}
	}
}

// counter is an Observer that counts.
type counter struct {
	indexed, scanned, events atomic.Int64
	resets                   sync.Map // reason → *atomic.Int64
}

func (c *counter) StreamOpened(string)    {}
func (c *counter) StreamClosed(string)    {}
func (c *counter) Event(string, string)   { c.events.Add(1) }
func (c *counter) Refused(string, string) {}
func (c *counter) Checks(_, kind string, n int) {
	if kind == "indexed" {
		c.indexed.Add(int64(n))
	} else {
		c.scanned.Add(int64(n))
	}
}
func (c *counter) Reset(_, reason string) {
	v, _ := c.resets.LoadOrStore(reason, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}
func (c *counter) resetCount(reason string) int64 {
	if v, ok := c.resets.Load(reason); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

func loadHub(t *testing.T, h *history, lim Limits, obs Observer) *Hub {
	t.Helper()
	set := changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: "r", DB: "db", Coll: "items"}}}
	c := &changefeed.Consumer{Name: "realtime", Source: h}
	c.SetWatched(set)
	lazy := changefeed.NewLazy(c, time.Minute)
	t.Cleanup(lazy.Close)
	lim.OpenInterval = -1
	if lim.StreamsPerUser == 0 {
		lim.StreamsPerUser = 1 << 20
	}
	if lim.Streams == 0 {
		lim.Streams = 1 << 20
	}
	return &Hub{Feed: lazy, Limits: lim, Observer: obs, ReloadJitter: 50 * time.Millisecond, ReloadConcurrency: 64, RefreshEvery: -1}
}

// drainer reads a stream and counts what it gets.
func drainer(s *Stream, got *atomic.Int64) {
	go func() {
		_ = s.Run(context.Background(), func(f Frame) error {
			if f.Event == "created" || f.Event == "updated" || f.Event == "removed" {
				got.Add(1)
			}
			return nil
		})
	}()
}

func orgSub(id, org string) *Subscription {
	return &Subscription{
		ID: id, DB: "db", Coll: "items",
		Visible: func(d map[string]any) (bool, error) { return d["org"] == org, nil },
		Render:  func(d map[string]any) map[string]any { return d },
		Keys:    []Key{{Field: "org", Values: []any{org}}},
	}
}

func unkeyedSub(id, org string) *Subscription {
	s := orgSub(id, org)
	s.Keys = nil
	return s
}

func changeFor(org string, i int, extra map[string]any) changefeed.Change {
	d := map[string]any{"id": fmt.Sprint("d", i), "org": org, "_meta": map[string]any{"version": int64(1)}}
	for k, v := range extra {
		d[k] = v
	}
	return changefeed.Change{Realm: "r", DB: "db", Coll: "items", Op: changefeed.Created, Key: fmt.Sprint("d", i), After: d, Version: 1}
}

// feedOpen waits for the hub's feed to be reading: it opens from the moment of the first
// stream, so a change added before that is not part of what it reads.
func feedOpen(t *testing.T, h *history, want int64) {
	t.Helper()
	waitUntil(t, "the feed to open", func() bool { return h.opens.Load() >= want })
	time.Sleep(50 * time.Millisecond)
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Many subscribers on one collection, with and without an equality on a grouping field: the
// same events arrive, but the checks per change differ by the number of subscribers.
func TestLoadFanOutKeyedAndUnkeyed(t *testing.T) {
	streams := 1000 * loadScale()
	orgs := 50
	changes := 200
	run := func(keyed bool) (checksPerChange float64, perChange time.Duration, delivered int64) {
		h := newHistory()
		obs := &counter{}
		hub := loadHub(t, h, Limits{}, obs)
		var got atomic.Int64
		for i := 0; i < streams; i++ {
			org := fmt.Sprint("o", i%orgs)
			mk := unkeyedSub
			if keyed {
				mk = orgSub
			}
			s, err := hub.Open(Request{Realm: "r", Principal: fmt.Sprint("u", i), Key: key, Subscriptions: []*Subscription{mk("s", org)}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			drainer(s, &got)
		}
		feedOpen(t, h, 1)
		start := time.Now()
		var cs []changefeed.Change
		for i := 0; i < changes; i++ {
			cs = append(cs, changeFor(fmt.Sprint("o", i%orgs), i, nil))
		}
		h.add(cs...)
		want := int64(changes * streams / orgs)
		waitUntil(t, "every event", func() bool { return got.Load() >= want })
		elapsed := time.Since(start)
		checks := obs.indexed.Load() + obs.scanned.Load()
		return float64(checks) / float64(changes), elapsed / time.Duration(changes), got.Load()
	}
	cpu0 := cpuSeconds()
	kc, kt, kd := run(true)
	uc, ut, ud := run(false)
	if kd != ud {
		t.Fatalf("delivered %d with keys and %d without: they must be the same", kd, ud)
	}
	if kc >= uc {
		t.Fatalf("checks per change with keys (%.1f) must be below those without (%.1f)", kc, uc)
	}
	report(t, "fanout", "streams", streams, "orgs", orgs, "changes", changes,
		"checks_per_change_keyed", kc, "checks_per_change_unkeyed", uc,
		"per_change_keyed_us", float64(kt.Microseconds()), "per_change_unkeyed_us", float64(ut.Microseconds()), "cpu_s", cpuSeconds()-cpu0)
}

// Large documents are rendered once per subscription that sees them.
func TestLoadLargeDocuments(t *testing.T) {
	streams := 50 * loadScale()
	h := newHistory()
	obs := &counter{}
	// The budget is lifted: this scenario measures rendering and delivery, and a slow machine must
	// not turn a slow reader into a reset (the budget has its own scenario).
	hub := loadHub(t, h, Limits{BufferBytes: 64 << 20, BufferEvents: 100000, Memory: 16 << 30}, obs)
	var got atomic.Int64
	for i := 0; i < streams; i++ {
		s, err := hub.Open(Request{Realm: "r", Principal: fmt.Sprint("u", i), Key: key, Subscriptions: []*Subscription{orgSub("s", "o")}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		drainer(s, &got)
	}
	feedOpen(t, h, 1)
	big := strings.Repeat("x", 32<<10)
	n := 20
	start := time.Now()
	for i := 0; i < n; i++ {
		h.add(changeFor("o", i, map[string]any{"body": big}))
	}
	waitUntil(t, "every event", func() bool { return got.Load() >= int64(n*streams) })
	elapsed := time.Since(start)
	report(t, "large_documents", "streams", streams, "documents", n, "doc_kib", 32, "events", got.Load(), "elapsed", elapsed,
		"mib_per_s", float64(int(got.Load())*(32<<10))/(1<<20)/elapsed.Seconds())
}

// A mass stall: clients that stopped reading make the instance's buffers hit its memory
// budget; the largest are reset, memory stays bounded and the feed keeps up for the others.
func TestLoadSlowClientsAndMemoryBudget(t *testing.T) {
	stalled := 200 * loadScale()
	budget := int64(8 << 20)
	h := newHistory()
	obs := &counter{}
	hub := loadHub(t, h, Limits{BufferBytes: 1 << 20, BufferEvents: 100000, Memory: budget}, obs)
	for i := 0; i < stalled; i++ { // nobody reads these
		s, err := hub.Open(Request{Realm: "r", Principal: fmt.Sprint("slow", i), Key: key, Subscriptions: []*Subscription{orgSub("s", "o")}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
	}
	// The healthy client reads everything; under a tight budget it may be reset too, if its
	// buffer is momentarily the largest, so what is asserted is that it keeps receiving.
	var got atomic.Int64
	var sawMarker atomic.Bool
	healthy, err := hub.Open(Request{Realm: "r", Principal: "healthy", Key: key, Subscriptions: []*Subscription{orgSub("s", "o")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(healthy.Close)
	go func() {
		_ = healthy.Run(context.Background(), func(f Frame) error {
			if f.Event == "created" {
				got.Add(1)
				if strings.Contains(string(f.Data), "marker") {
					sawMarker.Store(true)
				}
			}
			return nil
		})
	}()
	feedOpen(t, h, 1)
	big := strings.Repeat("x", 1<<10)
	n := 1000
	var peak atomic.Int64
	stopSampling := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopSampling:
				return
			default:
			}
			if b := hub.Buffered(); b > peak.Load() {
				peak.Store(b)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	defer close(stopSampling)
	start := time.Now()
	for i := 0; i < n; i++ {
		h.add(changeFor("o", i, map[string]any{"body": big}))
	}
	waitUntil(t, "the healthy client to receive most of it", func() bool { return got.Load() >= int64(n/2) })
	elapsed := time.Since(start)
	// Once the burst is over the feed has not stalled: a later change still reaches the client.
	time.Sleep(300 * time.Millisecond)
	h.add(changeFor("o", n, map[string]any{"body": "marker"}))
	waitUntil(t, "the marker", func() bool { return sawMarker.Load() })
	if b := hub.Buffered(); b > budget+(2<<20) {
		t.Fatalf("buffers hold %d bytes over a budget of %d", b, budget)
	}
	if obs.resetCount(ReasonSlowConsumer) == 0 {
		t.Fatal("no stalled stream was reset")
	}
	report(t, "memory_budget", "stalled_streams", stalled, "budget_mib", float64(budget>>20), "peak_buffered_mib", float64(peak.Load())/(1<<20),
		"slow_consumer_resets", obs.resetCount(ReasonSlowConsumer), "healthy_client_events", got.Load(), "elapsed", elapsed)
}

// An organization-wide role change: every member's access is read again, only those whose
// effective access differs are reset, and the reloads are not repeated.
func TestLoadAccessChangeStorm(t *testing.T) {
	members := 1000 * loadScale()
	h := newHistory()
	obs := &counter{}
	hub := loadHub(t, h, Limits{}, obs)
	var reloads atomic.Int64
	for i := 0; i < members; i++ {
		differs := i%2 == 0
		acc := &Access{SessionID: fmt.Sprint("s", i), UserID: fmt.Sprint("u", i), Orgs: []string{"o1"},
			Reload: func(context.Context) (Reloaded, error) {
				reloads.Add(1)
				return Reloaded{Orgs: []string{"o1"}, Subs: map[string]Visibility{"s": {Changed: differs, Visible: func(map[string]any) (bool, error) { return true, nil }}}}, nil
			}}
		s, err := hub.Open(Request{Realm: "r", Principal: fmt.Sprint("u", i), Key: key, Subscriptions: []*Subscription{orgSub("s", "o1")}, Access: acc})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		var sink atomic.Int64
		drainer(s, &sink)
	}
	feedOpen(t, h, 1)
	start := time.Now()
	h.add(changefeed.Change{Realm: "r", DB: "r__system", Coll: "orgs", System: true, Op: changefeed.Updated, Key: "o1"})
	waitUntil(t, "every reload", func() bool { return reloads.Load() >= int64(members) })
	waitUntil(t, "the resets", func() bool { return obs.resetCount(ReasonAccessChanged) >= int64(members/2) })
	elapsed := time.Since(start)
	time.Sleep(500 * time.Millisecond)
	if got := reloads.Load(); got != int64(members) {
		t.Fatalf("%d reloads for %d members: one change must read each access once", got, members)
	}
	if got := obs.resetCount(ReasonAccessChanged); got != int64(members/2) {
		t.Fatalf("%d resets, want %d: only those whose access differs", got, members/2)
	}
	report(t, "access_storm", "members", members, "reloads", reloads.Load(), "resets", obs.resetCount(ReasonAccessChanged), "elapsed", elapsed)
}

// Several realms and collections on one consumer, several instances on one history, and
// replicas without subscribers that read nothing.
func TestLoadRealmsInstancesAndIdleReplicas(t *testing.T) {
	h := newHistory()
	// An idle replica: a hub with no streams never opens the feed.
	idle := loadHub(t, h, Limits{}, nil)
	time.Sleep(100 * time.Millisecond)
	if n := h.opens.Load(); n != 0 || idle.Feed.Open() {
		t.Fatalf("an idle replica opened %d streams", n)
	}
	// Two instances (two hubs, each with its own feed) over one history: each sees everything.
	hubs := []*Hub{loadHub(t, h, Limits{}, nil), loadHub(t, h, Limits{}, nil)}
	var got [2]atomic.Int64
	for i, hub := range hubs {
		for r := 0; r < 5; r++ {
			sub := orgSub("s", fmt.Sprint("o", r))
			s, err := hub.Open(Request{Realm: "r", Principal: fmt.Sprint("u", i, r), Key: key, Subscriptions: []*Subscription{sub}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			drainer(s, &got[i])
		}
	}
	feedOpen(t, h, 2)
	for i := 0; i < 100; i++ {
		h.add(changeFor(fmt.Sprint("o", i%5), i, nil))
	}
	waitUntil(t, "both instances", func() bool { return got[0].Load() >= 100 && got[1].Load() >= 100 })
	if opens := h.opens.Load(); opens != 2 {
		t.Fatalf("%d feeds opened for two instances with subscribers, want 2", opens)
	}
	report(t, "instances", "instances", 2, "idle_replica_opens", 0, "events_each", got[0].Load())
}

// A soak: a steady flow of changes and a churn of streams must not make the heap grow.
func TestLoadSoak(t *testing.T) {
	secs := 3
	if v, err := strconv.Atoi(os.Getenv("BACKD_SOAK")); err == nil && v > 0 {
		secs = v
	}
	h := newHistory()
	hub := loadHub(t, h, Limits{}, &counter{})
	var got atomic.Int64
	end := time.Now().Add(time.Duration(secs) * time.Second)
	var heapFirst float64
	sent := 0
	for round := 0; time.Now().Before(end); round++ {
		var open []*Stream
		for i := 0; i < 50; i++ {
			s, err := hub.Open(Request{Realm: "r", Principal: fmt.Sprint("u", round, i), Key: key, Subscriptions: []*Subscription{orgSub("s", fmt.Sprint("o", i%5))}})
			if err != nil {
				t.Fatal(err)
			}
			drainer(s, &got)
			open = append(open, s)
		}
		for i := 0; i < 100; i++ {
			h.add(changeFor(fmt.Sprint("o", i%5), sent, nil))
			sent++
		}
		time.Sleep(20 * time.Millisecond)
		for _, s := range open {
			s.Close()
		}
		if round == 10 {
			heapFirst = heapMB() - float64(h.size())*0.001 // the history's own growth is not the hub's
		}
	}
	waitUntil(t, "the hub to have no streams", func() bool { return hub.Streams() == 0 })
	// The scripted history keeps every change it was given: let it go, to see what the hub holds.
	h.mu.Lock()
	h.changes = nil
	h.mu.Unlock()
	hub.Feed.Close()
	heapLast := heapMB()
	if heapFirst > 0 && heapLast > heapFirst+32 {
		t.Errorf("heap grew from %.1f to %.1f MiB", heapFirst, heapLast)
	}
	if hub.Buffered() != 0 {
		t.Errorf("%d bytes still buffered with no streams", hub.Buffered())
	}
	report(t, "soak", "seconds", secs, "changes", sent, "events", got.Load(), "heap_first_mb", heapFirst, "heap_last_mb", heapLast)
}
