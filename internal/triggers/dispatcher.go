package triggers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/registry"
)

// Defaults of a Dispatcher.
const (
	DefaultLeaseTTL       = 15 * time.Second
	DefaultFlushEvery     = 250 * time.Millisecond
	DefaultWatermarkEvery = time.Second
	DefaultBatchMax       = 200
)

// Dispatcher turns changes into jobs (design §9). Run it on every instance that may dispatch
// (BACKD_TRIGGERS=true): they compete for one lease and only the holder reads.
type Dispatcher struct {
	// Source opens the change streams; State and Queue are the durable parts.
	Source changefeed.Source
	State  State
	Queue  Queue
	// Holder names this instance in the lease; it must be unique per process.
	Holder string
	// Config returns a realm's app configuration, for conditions that use appConfig; may be
	// nil. reg is the configuration the dispatcher is running.
	Config func(ctx context.Context, reg *registry.Registry, realm string) map[string]any
	// Limits returns a realm's trigger limits (the instance's, lowered by the realm's); nil
	// is registry.DefaultTriggerLimits for every realm.
	Limits   func(reg *registry.Registry, realm string) registry.TriggerLimits
	Observer Observer
	Log      *slog.Logger
	Now      func() time.Time

	LeaseTTL       time.Duration
	FlushEvery     time.Duration
	WatermarkEvery time.Duration
	BatchMax       int
	// Backoff is the wait between attempts to queue a batch that failed; nil is 100ms
	// doubling to 5s.
	Backoff func(attempt int) time.Duration

	ix       atomic.Pointer[index]
	mu       sync.Mutex
	consumer *changefeed.Consumer
	changed  chan struct{} // closed and replaced when the registry changes
}

// SetRegistry gives the dispatcher the configuration in force. Called again on a reload, it
// applies to the next change onward: nothing is replayed.
func (d *Dispatcher) SetRegistry(reg *registry.Registry) {
	d.ix.Store(buildIndex(reg))
	d.mu.Lock()
	c := d.consumer
	if d.changed != nil {
		close(d.changed)
	}
	d.changed = make(chan struct{})
	d.mu.Unlock()
	if c != nil {
		c.SetWatched(WatchSet(reg))
	}
}

func (d *Dispatcher) limits(reg *registry.Registry, realm string) registry.TriggerLimits {
	if d.Limits != nil {
		return d.Limits(reg, realm).OrDefault()
	}
	return registry.DefaultTriggerLimits
}

// BacklogCacheTTL is how long a realm's count of queued trigger jobs is reused.
const BacklogCacheTTL = 5 * time.Second

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d *Dispatcher) leaseTTL() time.Duration {
	if d.LeaseTTL > 0 {
		return d.LeaseTTL
	}
	return DefaultLeaseTTL
}

func (d *Dispatcher) flushEvery() time.Duration {
	if d.FlushEvery > 0 {
		return d.FlushEvery
	}
	return DefaultFlushEvery
}

func (d *Dispatcher) watermarkEvery() time.Duration {
	if d.WatermarkEvery > 0 {
		return d.WatermarkEvery
	}
	return DefaultWatermarkEvery
}

func (d *Dispatcher) batchMax() int {
	if d.BatchMax > 0 {
		return d.BatchMax
	}
	return DefaultBatchMax
}

func (d *Dispatcher) backoff(attempt int) time.Duration {
	if d.Backoff != nil {
		return d.Backoff(attempt)
	}
	return min(100*time.Millisecond<<min(attempt, 6), 5*time.Second)
}

// Run competes for the lease and dispatches while it holds it, until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) error {
	if d.ix.Load() == nil {
		return errors.New("triggers: SetRegistry must be called before Run")
	}
	for ctx.Err() == nil {
		// With no trigger declared there is nothing to dispatch and no lease to compete for.
		if d.ix.Load().empty() {
			d.mu.Lock()
			if d.changed == nil {
				d.changed = make(chan struct{})
			}
			ch := d.changed
			d.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ch:
			}
			continue
		}
		lease, ok, err := d.State.AcquireLease(ctx, d.Holder, d.leaseTTL())
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			d.log().Warn("trigger lease could not be acquired", "error", err)
		case ok:
			d.lead(ctx, lease)
			continue
		}
		if !sleepCtx(ctx, d.leaseTTL()/3) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// lead dispatches while lease is held: it returns when the lease is lost, the context ends
// or the stream can't go on.
func (d *Dispatcher) lead(ctx context.Context, lease Lease) {
	d.log().Info("trigger dispatcher holds the lease", "epoch", lease.Epoch)
	if d.Observer != nil {
		d.Observer.Lease(true, lease.Epoch)
		defer d.Observer.Lease(false, lease.Epoch)
	}
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Keep the lease; losing it, or not being able to tell for a whole TTL, ends the lead.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.keepLease(lctx, lease, cancel)
	}()

	pos, at, found, err := d.State.Watermark(lctx)
	if err != nil {
		d.log().Error("trigger watermark unreadable", "error", err)
		cancel()
		wg.Wait()
		d.release(lease)
		return
	}
	if !found {
		pos, at = nil, d.now()
	}
	h := &handler{d: d, lease: lease, lost: cancel, startedAt: at, flushedAt: d.now()}
	c := &changefeed.Consumer{Name: "dispatcher", Source: d.Source, Log: d.log(), Backoff: func(a int) time.Duration { return d.backoff(a) }}
	c.SetWatched(WatchSet(d.ix.Load().reg))
	d.mu.Lock()
	d.consumer = c
	d.mu.Unlock()

	// The timer flushes what the stream's own quiet would leave waiting.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(d.flushEvery())
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				h.tick(lctx)
			}
		}
	}()

	_, _ = c.Run(lctx, pos, h)
	cancel()
	wg.Wait()
	d.mu.Lock()
	d.consumer = nil
	d.mu.Unlock()

	// A last flush with what the stream gave, while the lease is still ours.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_ = h.flush(fctx, true)
	fcancel()
	d.release(lease)
}

func (d *Dispatcher) release(l Lease) {
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.State.ReleaseLease(rctx, l); err != nil {
		d.log().Warn("trigger lease could not be released", "error", err)
	}
}

// keepLease renews every third of the TTL. It calls lost when the lease is gone or could not
// be renewed for a whole TTL (the database may have given it to someone else by then).
func (d *Dispatcher) keepLease(ctx context.Context, l Lease, lost func()) {
	t := time.NewTicker(d.leaseTTL() / 3)
	defer t.Stop()
	lastOK := d.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ok, err := d.State.RenewLease(ctx, l, d.leaseTTL())
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			if d.now().Sub(lastOK) >= d.leaseTTL() {
				d.log().Warn("trigger lease could not be renewed in time: standing down", "error", err)
				lost()
				return
			}
		case !ok:
			d.log().Warn("trigger lease was taken over: standing down", "epoch", l.Epoch)
			lost()
			return
		default:
			lastOK = d.now()
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// handler receives the changes while one lease is held, queues the jobs they make in
// batches, and stores the position only after the jobs of everything before it are queued:
// a crash in between replays the changes, and the queue collapses them on their job ids.
type handler struct {
	d     *Dispatcher
	lease Lease
	lost  func()

	mu        sync.Mutex
	pending   map[string][]Job // by realm
	npending  int
	gaps      map[gapKey]*Gap
	pos       changefeed.Position
	dirty     bool      // pos moved since it was stored
	flushedAt time.Time // when the position was last stored
	startedAt time.Time // when the stored position was written, for a window gap
	lastAt    time.Time // the cluster time of the newest change seen, for lag
	fenced    bool

	chained  map[[3]string]*rate // by realm, collection, trigger
	backlogs map[string]backlogOf
}

type gapKey struct{ realm, collection, trigger, reason string }

// rate counts the chained events a trigger handled in one minute.
type rate struct {
	minute int64
	n      int
}

// backlogOf is a realm's queued trigger jobs, counted at most every BacklogCacheTTL.
type backlogOf struct {
	n  int
	at time.Time
}

var errFenced = errors.New("triggers: the lease is no longer ours")

func (h *handler) Change(ctx context.Context, c changefeed.Change, at changefeed.Position) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ix := h.d.ix.Load(); ix != nil {
		if ct := ix.byNS[[2]string{c.DB, c.Coll}]; ct != nil && !c.System {
			h.consider(ctx, ct, c)
		}
	}
	h.pos, h.dirty, h.lastAt = at, true, c.At
	if h.npending >= h.d.batchMax() {
		h.flushUntilDone(ctx)
	}
}

func (h *handler) consider(ctx context.Context, ct *collectionTriggers, c changefeed.Change) {
	var config func(context.Context, string) map[string]any
	if h.d.Config != nil {
		config = func(ctx context.Context, realm string) map[string]any {
			return h.d.Config(ctx, h.d.ix.Load().reg, realm)
		}
	}
	for _, ev := range ct.evaluate(ctx, c, config) {
		if h.d.Observer != nil && ev.outcome != outcomeMatched {
			h.d.Observer.Event(ct.realm, ct.collection, ev.trigger.Name, ev.outcome)
		}
		switch ev.outcome {
		case outcomeMatched:
			if !h.admit(ctx, ct, ev, c) {
				continue
			}
			if h.d.Observer != nil {
				h.d.Observer.Event(ct.realm, ct.collection, ev.trigger.Name, ev.outcome)
			}
			if h.pending == nil {
				h.pending = map[string][]Job{}
			}
			h.pending[ct.realm] = append(h.pending[ct.realm], *ev.job)
			h.npending++
		case outcomeMissingPreImge:
			h.addGap(ct.realm, ct.collection, ev.trigger.Name, GapPreImage, c.At, c.Key)
		case outcomeError:
			h.d.log().Warn("trigger condition failed", "realm", ct.realm, "collection", ct.collection, "trigger", ev.trigger.Name, "error", ev.err)
		}
	}
}

// admit decides whether a matched event becomes a job. Loops are bounded by the chain:
// an event deeper than the realm's max_chain, or a trigger's chained events above its
// per-minute rate, are dropped, logged and metered. A first-party event is never dropped for
// being many: an import of thousands of documents delivers every one; only a realm's
// backlog ceiling, which protects the database, records them as a gap instead.
func (h *handler) admit(ctx context.Context, ct *collectionTriggers, ev evaluation, c changefeed.Change) bool {
	lim := h.d.limits(h.d.ix.Load().reg, ct.realm)
	drop := func(reason string) bool {
		if h.d.Observer != nil {
			h.d.Observer.Dropped(ct.realm, ct.collection, ev.trigger.Name, reason)
		}
		h.d.log().Warn("trigger event dropped", "realm", ct.realm, "collection", ct.collection, "trigger", ev.trigger.Name,
			"reason", reason, "depth", ev.depth, "root", ev.job.ChainRoot, "document", c.Key)
		return false
	}
	if ev.depth > 0 {
		if ev.depth > lim.MaxChain {
			return drop(DropChain)
		}
		minute := h.d.now().Unix() / 60
		key := [3]string{ct.realm, ct.collection, ev.trigger.Name}
		if h.chained == nil {
			h.chained = map[[3]string]*rate{}
		}
		r := h.chained[key]
		if r == nil || r.minute != minute {
			r = &rate{minute: minute}
			h.chained[key] = r
		}
		if r.n >= lim.MaxChainedPerMinute {
			return drop(DropBurst)
		}
		r.n++
	}
	if h.backlog(ctx, ct.realm)+len(h.pending[ct.realm]) >= lim.MaxBacklog {
		h.addGap(ct.realm, ct.collection, ev.trigger.Name, GapBacklog, c.At, c.Key)
		return false
	}
	return true
}

// backlog is the realm's queued trigger jobs, reused for a few seconds. When the count can't
// be read the last one stands: a database that can't count can't queue either, which the
// flush's own retry handles.
func (h *handler) backlog(ctx context.Context, realm string) int {
	b, ok := h.backlogs[realm]
	if ok && h.d.now().Sub(b.at) < BacklogCacheTTL {
		return b.n
	}
	n, err := h.d.Queue.Backlog(ctx, realm)
	if err != nil {
		h.d.log().Warn("trigger backlog could not be counted", "realm", realm, "error", err)
		return b.n
	}
	if h.backlogs == nil {
		h.backlogs = map[string]backlogOf{}
	}
	h.backlogs[realm] = backlogOf{n: n, at: h.d.now()}
	if h.d.Observer != nil {
		h.d.Observer.Backlog(realm, n)
	}
	return n
}

func (h *handler) addGap(realm, collection, trigger, reason string, at time.Time, key string) {
	if h.gaps == nil {
		h.gaps = map[gapKey]*Gap{}
	}
	k := gapKey{realm, collection, trigger, reason}
	g := h.gaps[k]
	if g == nil {
		g = &Gap{Realm: realm, Collection: collection, Trigger: trigger, Reason: reason, From: at, To: at}
		h.gaps[k] = g
	}
	g.Count++
	if at.After(g.To) {
		g.To = at
	}
	if key != "" && len(g.Keys) < MaxGapKeys {
		g.Keys = append(g.Keys, key)
	}
}

func (h *handler) Idle(ctx context.Context, at changefeed.Position) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slicesEqual(h.pos, at) {
		h.pos, h.dirty = at, true
	}
	if h.npending > 0 || len(h.gaps) > 0 || h.d.now().Sub(h.flushedAt) >= h.d.watermarkEvery() {
		_ = h.flushLocked(ctx)
	}
}

func slicesEqual(a, b changefeed.Position) bool { return string(a) == string(b) }

// Gap records that the history no longer reached the stored position.
func (h *handler) Gap(ctx context.Context, g changefeed.Gap) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.d.now()
	gap := Gap{Reason: GapWindow, From: h.startedAt, To: now}
	if err := h.d.State.RecordGap(ctx, gap); err != nil {
		h.d.log().Error("trigger gap could not be recorded", "error", err)
	}
	if h.d.Observer != nil {
		h.d.Observer.Gap("", GapWindow)
	}
	h.d.log().Error("trigger events fell out of the change history while no dispatcher read them: they are gone from the stream (see `backd triggers status`)", "from", gap.From, "to", gap.To)
	h.startedAt = now
	h.pos, h.dirty = nil, true
}

// tick flushes on the timer.
func (h *handler) tick(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.npending > 0 || len(h.gaps) > 0 || (h.dirty && h.d.now().Sub(h.flushedAt) >= h.d.watermarkEvery()) {
		_ = h.flushLocked(ctx)
	}
}

func (h *handler) flush(ctx context.Context, force bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !force && h.npending == 0 && len(h.gaps) == 0 && !h.dirty {
		return nil
	}
	return h.flushLocked(ctx)
}

// flushUntilDone retries a failing flush with backoff: while the queue is unreachable the
// stream is not read further (the position doesn't advance either), which is the
// backpressure that keeps memory bounded.
func (h *handler) flushUntilDone(ctx context.Context) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		err := h.flushLocked(ctx)
		if err == nil || errors.Is(err, errFenced) {
			return
		}
		h.d.log().Warn("trigger jobs could not be queued; retrying (the stream waits)", "error", err)
		if !sleepCtx(ctx, h.d.backoff(attempt)) {
			return
		}
	}
}

// flushLocked queues the pending jobs, records the gaps and stores the position, in that
// order. It must hold h.mu.
func (h *handler) flushLocked(ctx context.Context) error {
	if h.fenced {
		return errFenced
	}
	for realm, jobs := range h.pending {
		created, err := h.d.Queue.Enqueue(ctx, realm, jobs)
		if err != nil {
			return err
		}
		if h.d.Observer != nil {
			h.d.Observer.Enqueued(realm, created, len(jobs)-created)
		}
		delete(h.pending, realm)
		h.npending -= len(jobs)
	}
	for k, g := range h.gaps {
		if err := h.d.State.RecordGap(ctx, *g); err != nil {
			return err
		}
		if h.d.Observer != nil {
			h.d.Observer.Gap(g.Realm, g.Reason)
		}
		delete(h.gaps, k)
	}
	if h.dirty && h.pos != nil {
		now := h.d.now()
		ok, err := h.d.State.SetWatermark(ctx, h.lease, h.pos, now)
		if err != nil {
			return err
		}
		if !ok {
			h.fenced = true
			h.lost()
			return errFenced
		}
		h.dirty, h.flushedAt = false, now
		if h.d.Observer != nil {
			h.d.Observer.Watermark(now)
			if !h.lastAt.IsZero() {
				h.d.Observer.Lag(max(0, now.Sub(h.lastAt)))
			}
		}
	}
	return nil
}
