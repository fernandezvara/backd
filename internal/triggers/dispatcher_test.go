package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/registry"
)

// ---- fixtures ----

const orderSchema = `{"type":"object","properties":{"status":{"enum":["draft","paid","shipped"]},"total":{"type":"number"}}}`

func testRegistry(t *testing.T, collectionYAML string) *registry.Registry {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"shop/realm.yaml":                            "auth: enabled\nsignup: open\n",
		"shop/main/orders/schema.json":               orderSchema,
		"shop/main/orders/collection.yaml":           collectionYAML,
		"shop/main/_functions/deno.json":             "{}",
		"shop/main/_functions/on-paid/function.yaml": "internal: true\nmode: async\ntimeout: 20s\n",
		"shop/main/_functions/on-paid/index.ts":      "export default () => 1;\n",
	}
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
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

const paidTrigger = `triggers:
  on_paid:
    events: [created, updated]
    when: "document.status == 'paid' && before.status != 'paid'"
    function: main/on-paid
`

// history is a change history shared by dispatchers: positions are indexes into it.
type history struct {
	mu      sync.Mutex
	changes []changefeed.Change
	lost    int // the next n opens fail with lost history
	opens   []changefeed.Position
}

func (h *history) add(cs ...changefeed.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.changes = append(h.changes, cs...)
}

func (h *history) Open(_ context.Context, _ changefeed.WatchSet, after changefeed.Position) (changefeed.Stream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opens = append(h.opens, after)
	if h.lost > 0 && after != nil {
		h.lost--
		return nil, changefeed.ErrHistoryLost
	}
	idx := len(h.changes)
	if after != nil {
		idx, _ = strconv.Atoi(string(after))
	}
	return &histStream{h: h, idx: idx}, nil
}

type histStream struct {
	h   *history
	idx int
}

func (s *histStream) Position() changefeed.Position { return changefeed.Position(strconv.Itoa(s.idx)) }
func (s *histStream) Close()                        {}
func (s *histStream) Next(ctx context.Context) (changefeed.Event, error) {
	for {
		s.h.mu.Lock()
		if s.idx < len(s.h.changes) {
			c := s.h.changes[s.idx]
			s.idx++
			s.h.mu.Unlock()
			return changefeed.Event{Change: &c, Position: s.Position()}, nil
		}
		s.h.mu.Unlock()
		select {
		case <-ctx.Done():
			return changefeed.Event{}, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// memState is State in memory, with the lease semantics of the real one.
type memState struct {
	mu       sync.Mutex
	holder   string
	epoch    int64
	expires  time.Time
	pos      changefeed.Position
	at       time.Time
	gaps     []Gap
	failSet  int  // the next n SetWatermark calls fail with an error
	fenceAll bool // SetWatermark answers not-ok
	setCalls int
}

func (m *memState) AcquireLease(_ context.Context, holder string, ttl time.Duration) (Lease, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holder != "" && m.holder != holder && time.Now().Before(m.expires) {
		return Lease{}, false, nil
	}
	m.holder, m.epoch, m.expires = holder, m.epoch+1, time.Now().Add(ttl)
	return Lease{Holder: holder, Epoch: m.epoch}, true, nil
}

func (m *memState) RenewLease(_ context.Context, l Lease, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holder != l.Holder || m.epoch != l.Epoch {
		return false, nil
	}
	m.expires = time.Now().Add(ttl)
	return true, nil
}

func (m *memState) ReleaseLease(_ context.Context, l Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.holder == l.Holder && m.epoch == l.Epoch {
		m.expires = time.Time{}
	}
	return nil
}

func (m *memState) Watermark(context.Context) (changefeed.Position, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pos, m.at, m.pos != nil, nil
}

func (m *memState) SetWatermark(_ context.Context, l Lease, pos changefeed.Position, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCalls++
	if m.failSet > 0 {
		m.failSet--
		return false, errors.New("database unreachable")
	}
	if m.fenceAll || m.holder != l.Holder || m.epoch != l.Epoch {
		return false, nil
	}
	m.pos, m.at = pos, at
	return true, nil
}

func (m *memState) RecordGap(_ context.Context, g Gap) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gaps = append(m.gaps, g)
	return nil
}

func (m *memState) position() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(m.pos)
}

func (m *memState) gapList() []Gap {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Gap(nil), m.gaps...)
}

// memQueue is a Queue that collapses a job it already has, as the real one does.
type memQueue struct {
	mu      sync.Mutex
	jobs    map[string]Job
	order   []string
	created int
	dups    int
	fail    int // the next n Enqueue calls fail
}

func newQueue() *memQueue { return &memQueue{jobs: map[string]Job{}} }

func (q *memQueue) Enqueue(_ context.Context, _ string, jobs []Job) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail > 0 {
		q.fail--
		return 0, errors.New("queue unreachable")
	}
	created := 0
	for _, j := range jobs {
		if _, ok := q.jobs[j.ID]; ok {
			q.dups++
			continue
		}
		q.jobs[j.ID] = j
		q.order = append(q.order, j.ID)
		created++
	}
	q.created += created
	return created, nil
}

func (q *memQueue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

func (q *memQueue) list() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, 0, len(q.order))
	for _, id := range q.order {
		out = append(out, q.jobs[id])
	}
	return out
}

// recorder is an Observer that keeps what it was told.
type recorder struct {
	mu      sync.Mutex
	events  map[string]int
	leases  []int64
	dropped []int64
	gaps    []string
	created int
	dups    int
}

func (r *recorder) Event(realm, coll, trigger, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events == nil {
		r.events = map[string]int{}
	}
	r.events[trigger+"/"+outcome]++
}
func (r *recorder) Enqueued(_ string, created, dups int) {
	r.mu.Lock()
	r.created += created
	r.dups += dups
	r.mu.Unlock()
}
func (r *recorder) Lag(time.Duration)   {}
func (r *recorder) Watermark(time.Time) {}
func (r *recorder) Lease(held bool, e int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if held {
		r.leases = append(r.leases, e)
	} else {
		r.dropped = append(r.dropped, e)
	}
}
func (r *recorder) Gap(_, reason string) {
	r.mu.Lock()
	r.gaps = append(r.gaps, reason)
	r.mu.Unlock()
}

func (r *recorder) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[key]
}

func newDispatcher(t *testing.T, reg *registry.Registry, h *history, st State, q Queue, holder string, obs Observer) *Dispatcher {
	t.Helper()
	d := &Dispatcher{
		Source: h, State: st, Queue: q, Holder: holder, Observer: obs,
		LeaseTTL: 300 * time.Millisecond, FlushEvery: 5 * time.Millisecond, WatermarkEvery: 5 * time.Millisecond, BatchMax: 50,
		Backoff: func(int) time.Duration { return time.Millisecond },
	}
	d.SetRegistry(reg)
	return d
}

func run(t *testing.T, d *Dispatcher) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("dispatcher did not stop")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func change(op changefeed.Op, id string, version int64, inc uint32, before, after map[string]any) changefeed.Change {
	c := changefeed.Change{
		Realm: "shop", DB: "shop__main", Coll: "orders", Op: op, Key: id, At: t0, Increment: inc, Version: version,
		Before: before, After: after,
	}
	return c
}

func doc(id, status string, version int64) map[string]any {
	return map[string]any{"id": id, "status": status, "_meta": map[string]any{"version": version}}
}

// ---- tests ----

func TestEventAndJobIDs(t *testing.T) {
	c := change(changefeed.Updated, "o1", 2, 7, doc("o1", "draft", 1), doc("o1", "paid", 2))
	id := EventID(c)
	if len(id) != 32 || id != EventID(c) {
		t.Fatalf("id = %q", id)
	}
	other := c
	other.Increment = 8
	if EventID(other) == id {
		t.Error("another change of the same second must have another id")
	}
	// delete and recreate with the same id repeats the version: the id still differs
	rec := change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "draft", 1))
	del := change(changefeed.Removed, "o1", 1, 2, doc("o1", "draft", 1), nil)
	if EventID(rec) == EventID(del) {
		t.Error("creation and removal at version 1 collided")
	}
	a, b := JobID(id, "on_paid"), JobID(id, "audit")
	if a == b || a != JobID(id, "on_paid") || len(a) != 20 {
		t.Errorf("job ids: %q %q", a, b)
	}
}

func TestDispatchesMatchingChangesAsJobs(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q, obs := &memState{}, newQueue(), &recorder{}
	d := newDispatcher(t, reg, h, st, q, "a", obs)
	stop := run(t, d)
	defer stop()
	waitFor(t, "the stream to open", func() bool { return st.position() != "" })

	h.add(
		change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "paid", 1)),                      // fires: came from nothing
		change(changefeed.Updated, "o2", 2, 2, doc("o2", "draft", 1), doc("o2", "paid", 2)),    // fires: a transition
		change(changefeed.Updated, "o3", 3, 3, doc("o3", "paid", 2), doc("o3", "paid", 3)),     // same value again
		change(changefeed.Updated, "o4", 2, 4, doc("o4", "draft", 1), doc("o4", "shipped", 2)), // not paid
		change(changefeed.Removed, "o5", 4, 5, doc("o5", "paid", 3), nil),                      // not an event of the trigger
		change(changefeed.Created, "o6", 1, 6, nil, doc("o6", "draft", 1)),                     // draft
	)
	waitFor(t, "two jobs", func() bool { return q.size() == 2 })
	waitFor(t, "the position to reach the end", func() bool { return st.position() == "6" })

	jobs := q.list()
	for i, want := range []struct{ doc, event string }{{"o1", "created"}, {"o2", "updated"}} {
		j := jobs[i]
		if j.Realm != "shop" || j.Database != "main" || j.Function != "on-paid" || j.Origin != "trigger:main/orders/on_paid" ||
			j.Timeout != 20*time.Second || j.ID != JobID(j.EventID, "on_paid") || j.RequestID != j.EventID {
			t.Errorf("job %d = %+v", i, j)
		}
		var in map[string]any
		if err := json.Unmarshal(j.Input, &in); err != nil {
			t.Fatal(err)
		}
		d := in["doc"].(map[string]any)
		if in["id"] != j.EventID || in["event"] != want.event || in["collection"] != "orders" || d["id"] != want.doc ||
			in["depth"].(float64) != 0 || in["root"] != j.EventID || in["at"] != "2026-10-15T12:00:00Z" {
			t.Errorf("input %d = %v", i, in)
		}
		if _, ok := in["before"]; ok {
			t.Errorf("input %d carries a document body without `fields`", i)
		}
	}
	var in2 map[string]any
	_ = json.Unmarshal(jobs[1].Input, &in2)
	if ch := in2["changed"].([]any); len(ch) != 1 || ch[0] != "status" {
		t.Errorf("changed = %v", in2["changed"])
	}
	if obs.count("on_paid/matched") != 2 || obs.count("on_paid/no_match") != 3 {
		t.Errorf("events = %v", obs.events)
	}
}

func TestPositionAdvancesOnChangesNothingMatched(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q := &memState{}, newQueue()
	stop := run(t, newDispatcher(t, reg, h, st, q, "a", nil))
	defer stop()
	waitFor(t, "the stream to open", func() bool { return st.position() != "" })
	for i := 1; i <= 5; i++ {
		h.add(change(changefeed.Updated, "o"+strconv.Itoa(i), 2, uint32(i), doc("o", "draft", 1), doc("o", "shipped", 2)))
	}
	waitFor(t, "the position to follow", func() bool { return st.position() == "5" })
	if q.size() != 0 {
		t.Errorf("%d jobs for changes that match nothing", q.size())
	}
}

func TestInitialPositionIsStoredAtOpen(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	h.add(change(changefeed.Created, "old", 1, 1, nil, doc("old", "paid", 1))) // before the dispatcher ever ran
	st, q := &memState{}, newQueue()
	stop := run(t, newDispatcher(t, reg, h, st, q, "a", nil))
	defer stop()
	waitFor(t, "the first position to be stored", func() bool { return st.position() == "1" })
	if q.size() != 0 {
		t.Errorf("a fresh dispatcher started from now, yet queued %d jobs", q.size())
	}
}

// A crash between queueing the jobs and storing the position replays the changes, and the
// queue collapses them.
func TestReplayAfterACrashCollapsesOnJobIDs(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q, obs := &memState{}, newQueue(), &recorder{}

	// First run: the position can be stored once (at open), then the database "goes away".
	d1 := newDispatcher(t, reg, h, st, q, "a", obs)
	stop1 := run(t, d1)
	waitFor(t, "the first position", func() bool { return st.position() != "" })
	st.mu.Lock()
	st.failSet = 1 << 30 // every later store fails
	st.mu.Unlock()
	h.add(
		change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "paid", 1)),
		change(changefeed.Created, "o2", 1, 2, nil, doc("o2", "paid", 1)),
	)
	waitFor(t, "both jobs queued", func() bool { return q.size() == 2 })
	if st.position() != "0" {
		t.Fatalf("position = %q: it must not advance without being stored", st.position())
	}
	stop1() // the crash

	// Second run: it resumes at the stored position and reads the same changes again.
	st.mu.Lock()
	st.failSet = 0
	st.mu.Unlock()
	d2 := newDispatcher(t, reg, h, st, q, "b", obs)
	stop2 := run(t, d2)
	defer stop2()
	waitFor(t, "the position to reach the end", func() bool { return st.position() == "2" })
	if q.size() != 2 {
		t.Fatalf("queue holds %d jobs after the replay, want 2", q.size())
	}
	waitFor(t, "the replay to be counted as duplicates", func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return obs.dups >= 2
	})
}

// A handoff between instances loses nothing and duplicates nothing.
func TestHandoffContinuesFromTheStoredPosition(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q := &memState{}, newQueue()
	obsA, obsB := &recorder{}, &recorder{}

	stopA := run(t, newDispatcher(t, reg, h, st, q, "a", obsA))
	waitFor(t, "a to lead", func() bool { return st.position() != "" })
	for i := 1; i <= 10; i++ {
		h.add(change(changefeed.Created, "a"+strconv.Itoa(i), 1, uint32(i), nil, doc("a", "paid", 1)))
	}
	waitFor(t, "a to queue the first ten", func() bool { return q.size() == 10 && st.position() == "10" })

	stopB := run(t, newDispatcher(t, reg, h, st, q, "b", obsB)) // waits: a holds the lease
	time.Sleep(50 * time.Millisecond)
	if len(obsB.leases) != 0 {
		t.Fatal("b took a lease a holds")
	}
	h.add(change(changefeed.Created, "x", 1, 11, nil, doc("x", "paid", 1))) // while a still leads
	waitFor(t, "a to queue it", func() bool { return q.size() == 11 })
	stopA() // a goes away, releasing the lease
	h.add(change(changefeed.Created, "y", 1, 12, nil, doc("y", "paid", 1)))
	waitFor(t, "b to take over and queue the rest", func() bool { return q.size() == 12 })
	defer stopB()
	if len(obsB.leases) != 1 || obsB.leases[0] <= obsA.leases[0] {
		t.Errorf("epochs: a=%v b=%v, want b's to be later", obsA.leases, obsB.leases)
	}
	if st.position() != "12" {
		t.Errorf("position = %q", st.position())
	}
}

func TestOnlyOneInstanceLeads(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q := &memState{}, newQueue()
	var recs []*recorder
	for _, name := range []string{"a", "b", "c"} {
		r := &recorder{}
		recs = append(recs, r)
		stop := run(t, newDispatcher(t, reg, h, st, q, name, r))
		defer stop()
	}
	waitFor(t, "a leader", func() bool { return st.position() != "" })
	h.add(change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "paid", 1)))
	waitFor(t, "the job", func() bool { return q.size() == 1 })
	time.Sleep(100 * time.Millisecond)
	leaders := 0
	for _, r := range recs {
		r.mu.Lock()
		if len(r.leases) > 0 {
			leaders++
		}
		r.mu.Unlock()
	}
	if leaders != 1 || q.size() != 1 {
		t.Fatalf("leaders = %d, jobs = %d", leaders, q.size())
	}
}

// A holder whose writes are refused (it was replaced) stops and does not move the position.
func TestFencedHolderStandsDown(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q, obs := &memState{}, newQueue(), &recorder{}
	d := newDispatcher(t, reg, h, st, q, "a", obs)
	stop := run(t, d)
	defer stop()
	waitFor(t, "a to lead", func() bool { return st.position() != "" })
	st.mu.Lock()
	st.fenceAll = true
	st.mu.Unlock()
	h.add(change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "paid", 1)))
	waitFor(t, "a to stand down", func() bool {
		obs.mu.Lock()
		defer obs.mu.Unlock()
		return len(obs.dropped) >= 1
	})
	if st.position() != "0" {
		t.Errorf("a fenced holder moved the position to %q", st.position())
	}
}

func TestMissingPreImageSkipsAndRecordsAGap(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q, obs := &memState{}, newQueue(), &recorder{}
	stop := run(t, newDispatcher(t, reg, h, st, q, "a", obs))
	defer stop()
	waitFor(t, "the stream", func() bool { return st.position() != "" })
	h.add(
		change(changefeed.Updated, "o1", 2, 1, nil, doc("o1", "paid", 2)), // no pre-image, the condition needs `before`
		change(changefeed.Updated, "o2", 2, 2, nil, doc("o2", "paid", 2)),
		change(changefeed.Created, "o3", 1, 3, nil, doc("o3", "paid", 1)), // a creation has no before: fine
	)
	waitFor(t, "the creation's job", func() bool { return q.size() == 1 })
	waitFor(t, "the gap", func() bool { return len(st.gapList()) == 1 })
	g := st.gapList()[0]
	if g.Reason != GapPreImage || g.Realm != "shop" || g.Collection != "orders" || g.Trigger != "on_paid" || g.Count != 2 || len(g.Keys) != 2 {
		t.Errorf("gap = %+v", g)
	}
	if obs.count("on_paid/missing_preimage") != 2 {
		t.Errorf("events = %v", obs.events)
	}
}

func TestWindowMissedIsRecordedAndReadingContinues(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q, obs := &memState{}, newQueue(), &recorder{}
	// A stored position the history no longer reaches.
	st.pos, st.at = changefeed.Position("0"), time.Now().Add(-2*time.Hour)
	h.lost = 1
	stop := run(t, newDispatcher(t, reg, h, st, q, "a", obs))
	defer stop()
	waitFor(t, "the window gap", func() bool { return len(st.gapList()) == 1 })
	g := st.gapList()[0]
	if g.Reason != GapWindow || time.Since(g.From) < time.Hour {
		t.Errorf("gap = %+v", g)
	}
	waitFor(t, "a fresh position", func() bool { return st.position() == "0" })
	h.add(change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "paid", 1)))
	waitFor(t, "reading to continue from now", func() bool { return q.size() == 1 })
}

func TestAFailingQueueHoldsTheStreamAndLosesNothing(t *testing.T) {
	reg := testRegistry(t, paidTrigger)
	h := &history{}
	st, q := &memState{}, newQueue()
	q.fail = 3
	stop := run(t, newDispatcher(t, reg, h, st, q, "a", nil))
	defer stop()
	waitFor(t, "the stream", func() bool { return st.position() != "" })
	for i := 1; i <= 3; i++ {
		h.add(change(changefeed.Created, "o"+strconv.Itoa(i), 1, uint32(i), nil, doc("o", "paid", 1)))
	}
	waitFor(t, "all three jobs", func() bool { return q.size() == 3 })
	waitFor(t, "the position", func() bool { return st.position() == "3" })
}

func TestReloadAppliesToTheNextChange(t *testing.T) {
	reg := testRegistry(t, "")
	h := &history{}
	st, q := &memState{}, newQueue()
	d := newDispatcher(t, reg, h, st, q, "a", nil)
	stop := run(t, d)
	defer stop()
	// With no triggers nothing is watched: the consumer waits.
	time.Sleep(30 * time.Millisecond)
	if n := len(h.opens); n != 0 {
		t.Fatalf("opened %d streams with nothing to watch", n)
	}
	d.SetRegistry(testRegistry(t, paidTrigger))
	waitFor(t, "the stream to open", func() bool { return st.position() != "" })
	h.add(change(changefeed.Created, "o1", 1, 1, nil, doc("o1", "paid", 1)))
	waitFor(t, "the job", func() bool { return q.size() == 1 })
}

const contractTriggers = `triggers:
  ids:
    events: [created, updated, removed]
    function: main/on-paid
  slim:
    events: [created, updated, removed]
    function: main/on-paid
    fields: [status]
  whole:
    events: [created, updated, removed]
    function: main/on-paid
    fields: ["*"]
  who:
    events: [created, updated, removed]
    function: main/on-paid
    session: true
`

func jobFor(jobs []Job, trigger string) (Job, map[string]any) {
	for _, j := range jobs {
		if j.Origin == "trigger:main/orders/"+trigger {
			var in map[string]any
			_ = json.Unmarshal(j.Input, &in)
			return j, in
		}
	}
	return Job{}, nil
}

func written(by string, version int64, status string, total float64) map[string]any {
	return map[string]any{
		"id": "o1", "status": status, "total": total,
		"_meta": map[string]any{"version": version, "updated_by": by, "created_by": by},
	}
}

func runContract(t *testing.T, h *history, st *memState, q *memQueue) (stop func()) {
	t.Helper()
	reg := testRegistry(t, contractTriggers)
	stop = run(t, newDispatcher(t, reg, h, st, q, "a", nil))
	waitFor(t, "the stream", func() bool { return st.position() != "" })
	return stop
}

func TestInputCarriesBodiesOnlyWhenFieldsAsks(t *testing.T) {
	h, st, q := &history{}, &memState{}, newQueue()
	defer runContract(t, h, st, q)()
	before, after := written("user:u1", 1, "draft", 10), written("user:u1", 2, "paid", 10)
	h.add(change(changefeed.Updated, "o1", 2, 1, before, after))
	waitFor(t, "four jobs", func() bool { return q.size() == 4 })
	jobs := q.list()

	_, ids := jobFor(jobs, "ids")
	if _, has := ids["before"]; has {
		t.Error("a trigger without `fields` carried a body")
	}
	if _, has := ids["after"]; has {
		t.Error("a trigger without `fields` carried a body")
	}
	if _, has := ids["by"]; has {
		t.Error("`by` without session: true")
	}

	_, slim := jobFor(jobs, "slim")
	b, a := slim["before"].(map[string]any), slim["after"].(map[string]any)
	if b["status"] != "draft" || a["status"] != "paid" || b["id"] != "o1" || a["_meta"] == nil {
		t.Errorf("slim = %v / %v", b, a)
	}
	if _, has := a["total"]; has {
		t.Errorf("a field that was not listed rode along: %v", a)
	}

	_, whole := jobFor(jobs, "whole")
	if whole["after"].(map[string]any)["total"] != float64(10) || whole["before"].(map[string]any)["status"] != "draft" {
		t.Errorf("whole = %v", whole)
	}
}

func TestInputBodiesAreNullWhereThereIsNoDocument(t *testing.T) {
	h, st, q := &history{}, &memState{}, newQueue()
	defer runContract(t, h, st, q)()
	h.add(
		change(changefeed.Created, "o1", 1, 1, nil, written("user:u1", 1, "draft", 1)),
		change(changefeed.Removed, "o1", 1, 2, written("user:u1", 1, "draft", 1), nil),
	)
	waitFor(t, "eight jobs", func() bool { return q.size() == 8 })
	var created, removed map[string]any
	for _, j := range q.list() {
		if j.Origin != "trigger:main/orders/slim" {
			continue
		}
		var in map[string]any
		_ = json.Unmarshal(j.Input, &in)
		if in["event"] == "created" {
			created = in
		} else {
			removed = in
		}
	}
	if v, has := created["before"]; !has || v != nil || created["after"] == nil {
		t.Errorf("created: before=%v (present %v) after=%v", v, has, created["after"])
	}
	if v, has := removed["after"]; !has || v != nil || removed["before"] == nil {
		t.Errorf("removed: after=%v (present %v) before=%v", v, has, removed["before"])
	}
}

func TestByIsTheUserWhoWrote(t *testing.T) {
	h, st, q := &history{}, &memState{}, newQueue()
	defer runContract(t, h, st, q)()
	cases := []struct {
		name string
		c    changefeed.Change
		want string // "" means absent
	}{
		{"user creates", change(changefeed.Created, "a", 1, 1, nil, written("user:u1", 1, "draft", 1)), "u1"},
		{"user updates", change(changefeed.Updated, "b", 2, 2, written("user:u9", 1, "draft", 1), written("user:u2", 2, "paid", 1)), "u2"},
		{"user removes: the last writer", change(changefeed.Removed, "c", 3, 3, written("user:u3", 2, "paid", 1), nil), "u3"},
		{"an api key", change(changefeed.Created, "d", 1, 4, nil, written("key:ci", 1, "draft", 1)), ""},
		{"a function", change(changefeed.Created, "e", 1, 5, nil, written("func:shop/main/ship", 1, "draft", 1)), ""},
		{"anonymous", change(changefeed.Created, "f", 1, 6, nil, written("anonymous", 1, "draft", 1)), ""},
		{"an erase", change(changefeed.Updated, "g", 3, 7, written("user:u4", 2, "paid", 1), written("backd:erase", 3, "paid", 1)), ""},
		{"no writer recorded", change(changefeed.Created, "h", 1, 8, nil, map[string]any{"id": "h", "status": "draft", "_meta": map[string]any{"version": int64(1)}}), ""},
	}
	for _, c := range cases {
		h.add(c.c)
	}
	waitFor(t, "every job", func() bool { return q.size() == 4*len(cases) })
	byKey := map[string]map[string]any{}
	for _, j := range q.list() {
		if j.Origin != "trigger:main/orders/who" {
			continue
		}
		var in map[string]any
		_ = json.Unmarshal(j.Input, &in)
		byKey[in["doc"].(map[string]any)["id"].(string)] = in
	}
	for i, c := range cases {
		in := byKey[c.c.Key]
		got, has := in["by"].(string)
		switch {
		case c.want == "" && has:
			t.Errorf("%d %s: by = %q, want none", i, c.name, got)
		case c.want != "" && got != c.want:
			t.Errorf("%d %s: by = %q (present %v), want %q", i, c.name, got, has, c.want)
		}
	}
}

func TestOversizedInputDropsBodiesAndSaysSo(t *testing.T) {
	h, st, q := &history{}, &memState{}, newQueue()
	defer runContract(t, h, st, q)()
	big := written("user:u1", 1, "draft", 1)
	big["note"] = strings.Repeat("x", MaxInputBytes+10)
	h.add(change(changefeed.Created, "o1", 1, 1, nil, big))
	waitFor(t, "four jobs", func() bool { return q.size() == 4 })
	_, whole := jobFor(q.list(), "whole")
	if whole["truncated"] != true {
		t.Errorf("truncated = %v", whole["truncated"])
	}
	if _, has := whole["after"]; has {
		t.Error("the oversized body was kept")
	}
	if whole["doc"].(map[string]any)["id"] != "o1" || whole["id"] == nil {
		t.Errorf("the ids must stay: %v", whole)
	}
	// a trigger without `fields` is unaffected: it never had a body
	_, ids := jobFor(q.list(), "ids")
	if _, has := ids["truncated"]; has {
		t.Error("`truncated` on an input that carried no body")
	}
}

func TestJobCarriesTheChainRootAndDepthZero(t *testing.T) {
	h, st, q := &history{}, &memState{}, newQueue()
	defer runContract(t, h, st, q)()
	h.add(change(changefeed.Created, "o1", 1, 1, nil, written("user:u1", 1, "draft", 1)))
	waitFor(t, "jobs", func() bool { return q.size() == 4 })
	for _, j := range q.list() {
		if j.ChainRoot != j.EventID || j.ChainDepth != 0 || j.EventID == "" {
			t.Errorf("job %s: chain %q/%d event %q", j.Origin, j.ChainRoot, j.ChainDepth, j.EventID)
		}
	}
}
