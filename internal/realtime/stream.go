package realtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// Subscription is one thing a stream follows: a collection filtered by Where, or one
// document of it. Visible is what decides, for the stream's caller, whether a document is
// part of it: the subscription's filter, the collection's read rule and soft deletion,
// evaluated in memory on the document as stored.
type Subscription struct {
	ID string
	// DB and Coll name what a change reports: the MongoDB database and the collection.
	DB, Coll string
	Doc      string // a document id; "" follows the collection
	Visible  func(doc map[string]any) (bool, error)
	// Render turns a stored document into what a read returns.
	Render func(doc map[string]any) map[string]any
	// Keys are the equalities the subscription can only be visible under (KeysOf of its
	// resolved filter): they let a change find it without asking every subscription.
	Keys []Key

	owner     *Stream
	filed     string // doc, group or scan: where the index holds it
	keyField  string
	keyValues []string

	pendingKeys []Key // the keys after a reload, until the index takes them
}

func (s *Subscription) collKey() string { return CollKey(s.DB, s.Coll) }

// Frame is one server-sent event. Event "" with no data is a heartbeat.
type Frame struct {
	ID    string // a resume token; set on the last frame of a change
	Event string
	Data  []byte
}

// Heartbeat is the frame that keeps an idle connection alive.
var Heartbeat = Frame{}

type cursor struct {
	at  time.Time
	inc uint32
}

func (a cursor) after(b cursor) bool {
	return a.at.After(b.at) || (a.at.Equal(b.at) && a.inc > b.inc)
}

type pendingChange struct {
	c     changefeed.Change
	at    changefeed.Position
	cands []*Subscription
}

// Stream is one open connection. It is a changefeed.Handler of the instance's shared feed.
type Stream struct {
	hub       *Hub
	realm     string
	principal string
	key       []byte
	lim       Limits

	mu       sync.Mutex
	subs     []*Subscription
	queue    []Frame
	size     int
	closed   bool
	last     cursor
	hasLast  bool
	replay   bool // live changes wait in pending until the replay reaches them
	pending  []pendingChange
	resetAt  map[string]time.Time
	resetDue map[string]bool

	access    *Access
	ending    bool // an end event is queued: the stream closes once it is written
	reloading bool
	dirty     bool

	notify chan struct{}
	done   chan struct{}
	ended  chan struct{} // closed when Run returned
	cancel context.CancelFunc
	once   sync.Once
}

func newStream(h *Hub, req Request, lim Limits) *Stream {
	return &Stream{
		hub: h, realm: req.Realm, principal: req.Principal, key: req.Key, lim: lim,
		subs: append([]*Subscription(nil), req.Subscriptions...), access: req.Access,
		notify: make(chan struct{}, 1), done: make(chan struct{}), ended: make(chan struct{}),
		resetAt: map[string]time.Time{}, resetDue: map[string]bool{},
	}
}

// IDs are the stream's subscription ids, in the order they were asked for.
func (s *Stream) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.subs))
	for i, sub := range s.subs {
		out[i] = sub.ID
	}
	return out
}

// Done is closed when the stream is closed.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Close ends the stream and releases what it holds. It is safe to call more than once.
func (s *Stream) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		freed := s.size
		s.queue, s.size, s.pending = nil, 0, nil
		cancel := s.cancel
		subs := s.subs
		s.subs = nil
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.hub.charge(-int64(freed))
		s.hub.remove(s, subs)
		if s.hub.Observer != nil {
			s.hub.Observer.StreamClosed(s.realm)
		}
		close(s.done)
	})
}

// Run writes the stream's frames with w until ctx is done, w fails or the stream is closed.
func (s *Stream) Run(ctx context.Context, w func(Frame) error) error {
	defer s.finished()
	t := time.NewTicker(s.lim.Heartbeat)
	defer t.Stop()
	for {
		if err := s.drain(w); err != nil {
			return err
		}
		s.mu.Lock()
		ending := s.ending && len(s.queue) == 0
		s.mu.Unlock()
		if ending {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return nil
		case <-s.notify:
		case <-t.C:
			if err := w(Heartbeat); err != nil {
				return err
			}
		}
	}
}

func (s *Stream) finished() {
	select {
	case <-s.ended:
	default:
		close(s.ended)
	}
}

func (s *Stream) drain(w func(Frame) error) error {
	s.mu.Lock()
	batch := s.queue
	freed := s.size
	s.queue, s.size = nil, 0
	s.mu.Unlock()
	s.hub.charge(-int64(freed))
	for _, f := range batch {
		if err := w(f); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) buffered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

func (s *Stream) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// enqueue adds frames; the caller holds s.mu. It reports whether the buffer overflowed (and
// was replaced by a reset) and how many bytes it now holds more.
func (s *Stream) enqueueLocked(frames ...Frame) (delta int64) {
	if s.closed {
		return 0
	}
	n := 0
	for _, f := range frames {
		n += len(f.Data) + len(f.Event) + len(f.ID) + 16
	}
	if s.size+n > s.lim.BufferBytes || len(s.queue)+len(frames) > s.lim.BufferEvents {
		delta = s.dropLocked(ReasonSlowConsumer)
		return delta
	}
	s.queue = append(s.queue, frames...)
	s.size += n
	return int64(n)
}

// dropLocked empties the buffer and queues a reset of every subscription. It returns the
// change in buffered bytes.
func (s *Stream) dropLocked(reason string) int64 {
	before := s.size
	s.queue, s.size = nil, 0
	f := s.resetFrame(s.allIDs(), reason)
	s.queue = append(s.queue, f)
	s.size = len(f.Data) + len(f.Event) + 16
	if s.hub.Observer != nil {
		s.hub.Observer.Reset(s.realm, reason)
	}
	return int64(s.size - before)
}

func (s *Stream) allIDs() []string {
	ids := make([]string, len(s.subs))
	for i, sub := range s.subs {
		ids[i] = sub.ID
	}
	return ids
}

func (s *Stream) resetFrame(ids []string, reason string) Frame {
	data, _ := json.Marshal(map[string]any{"subs": ids, "reason": reason})
	return Frame{Event: "reset", Data: data}
}

// resetAll asks the client to reload every subscription.
func (s *Stream) resetAll(reason string) {
	s.mu.Lock()
	f := s.resetFrame(s.allIDs(), reason)
	delta := s.enqueueLocked(f)
	s.mu.Unlock()
	s.hub.charge(delta)
	if s.hub.Observer != nil {
		s.hub.Observer.Reset(s.realm, reason)
	}
	s.wake()
}

func (s *Stream) slowConsumer() {
	s.mu.Lock()
	delta := s.dropLocked(ReasonSlowConsumer)
	s.mu.Unlock()
	s.hub.charge(delta)
	s.wake()
}

// configChanged removes the subscriptions of the named collections and says so.
func (s *Stream) configChanged(colls map[string]bool) {
	s.mu.Lock()
	var gone []string
	kept := s.subs[:0:0]
	for _, sub := range s.subs {
		if colls[sub.collKey()] {
			gone = append(gone, sub.ID)
			continue
		}
		kept = append(kept, sub)
	}
	if len(gone) == 0 {
		s.mu.Unlock()
		return
	}
	var removed []*Subscription
	for _, sub := range s.subs {
		if colls[sub.collKey()] {
			removed = append(removed, sub)
		}
	}
	s.subs = kept
	delta := s.enqueueLocked(s.resetFrame(gone, ReasonConfigChanged))
	s.mu.Unlock()
	s.hub.unindex(removed)
	s.hub.charge(delta)
	if s.hub.Observer != nil {
		s.hub.Observer.Reset(s.realm, ReasonConfigChanged)
	}
	s.wake()
}

// --- the live feed: the hub finds the candidates and hands each stream its own ---

// deliver gives the stream a change and the subscriptions of it that may be affected.
func (s *Stream) deliver(c changefeed.Change, at changefeed.Position, cands []*Subscription) {
	s.mu.Lock()
	if s.closed || s.ending {
		s.mu.Unlock()
		return
	}
	if s.replay {
		if len(s.pending) >= s.lim.BufferEvents {
			// The replay can't catch up with the feed: start over from a reload.
			s.pending, s.replay = nil, false
			delta := s.dropLocked(ReasonHistoryExpired)
			cancel := s.cancel
			s.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			s.hub.charge(delta)
			s.wake()
			return
		}
		s.pending = append(s.pending, pendingChange{c, at, cands})
		s.mu.Unlock()
		return
	}
	delta := s.processLocked(c, at, cands)
	s.mu.Unlock()
	s.hub.charge(delta)
	s.hub.enforce()
	s.wake()
}

// processLocked evaluates one change for every subscription and queues what they should see.
func (s *Stream) processLocked(c changefeed.Change, at changefeed.Position, subs []*Subscription) int64 {
	cur := cursor{c.At, c.Increment}
	if s.hasLast && !cur.after(s.last) {
		return 0 // already handled: a replay overlapping the live feed, or a reopened feed
	}
	s.last, s.hasLast = cur, true

	var frames []Frame
	var delta int64
	missing := false
	for _, sub := range subs {
		if sub.DB != c.DB || sub.Coll != c.Coll || (sub.Doc != "" && sub.Doc != c.Key) {
			continue
		}
		if c.Op != changefeed.Created && c.Before == nil {
			// Without the document before the change, whether it was visible can't be said:
			// telling anyone "removed" could reveal an id, guessing could keep a document
			// visible to someone who lost access. Reload instead.
			missing = true
			continue
		}
		before := c.Op != changefeed.Created && s.visible(sub, c.Before)
		after := c.Op != changefeed.Removed && s.visible(sub, c.After)
		var event string
		var data map[string]any
		switch {
		case after && !before:
			event, data = "created", map[string]any{"sub": sub.ID, "doc": sub.Render(c.After)}
		case after && before:
			event, data = "updated", map[string]any{"sub": sub.ID, "doc": sub.Render(c.After)}
		case before:
			v := c.Version
			if c.After != nil {
				v = versionOf(c.After)
			}
			event, data = "removed", map[string]any{"sub": sub.ID, "id": c.Key, "version": v}
		default:
			continue
		}
		raw, err := json.Marshal(data)
		if err != nil {
			continue
		}
		frames = append(frames, Frame{Event: event, Data: raw})
		if s.hub.Observer != nil {
			s.hub.Observer.Event(s.realm, event)
		}
	}
	if len(frames) > 0 {
		// The resume token rides on the last frame of a change: a client that resumes after
		// it has everything of that change.
		fp := ""
		if s.access != nil {
			fp = s.access.Fingerprint
		}
		frames[len(frames)-1].ID = Seal(s.key, Token{Pos: at, Realm: s.realm, Principal: s.principal, Issued: s.hub.now().Unix(), Access: fp})
		delta += s.enqueueLocked(frames...)
	}
	if missing {
		delta += s.preimageMissingLocked(c.DB, c.Coll)
	}
	return delta
}

func (s *Stream) visible(sub *Subscription, doc map[string]any) bool {
	if doc == nil {
		return false
	}
	ok, err := sub.Visible(doc)
	return err == nil && ok // a rule that can't be evaluated shows nothing
}

// preimageMissingLocked resets the collection's subscriptions, at most once a second: a
// misconfigured retention is a trickle of reloads, not a storm.
func (s *Stream) preimageMissingLocked(db, coll string) int64 {
	k := CollKey(db, coll)
	now := s.hub.now()
	if last, ok := s.resetAt[k]; ok && now.Sub(last) < time.Second {
		if !s.resetDue[k] {
			s.resetDue[k] = true
			time.AfterFunc(time.Second-now.Sub(last), func() { s.flushPreimageReset(k) })
		}
		return 0
	}
	s.resetAt[k] = now
	return s.enqueueLocked(s.resetFrame(s.idsOfLocked(k), ReasonPreimageMiss))
}

// preimageMissing resets the collection's subscriptions of this stream, at most once a second.
func (s *Stream) preimageMissing(db, coll string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	delta := s.preimageMissingLocked(db, coll)
	s.mu.Unlock()
	s.hub.charge(delta)
	s.hub.enforce()
	s.wake()
}

func (s *Stream) flushPreimageReset(k string) {
	s.mu.Lock()
	delete(s.resetDue, k)
	s.resetAt[k] = s.hub.now()
	delta := s.enqueueLocked(s.resetFrame(s.idsOfLocked(k), ReasonPreimageMiss))
	s.mu.Unlock()
	s.hub.charge(delta)
	s.wake()
}

func (s *Stream) idsOfLocked(collKey string) []string {
	var ids []string
	for _, sub := range s.subs {
		if sub.collKey() == collKey {
			ids = append(ids, sub.ID)
		}
	}
	return ids
}

func versionOf(doc map[string]any) int64 {
	meta, _ := doc["_meta"].(map[string]any)
	switch v := meta["version"].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// --- resuming ---

// startReplay reads the history after the token's position with a consumer of its own and,
// while it does, holds the changes the shared feed delivers. Once the replay reaches the
// first of those it stops, and the shared feed carries on: replayed events are evaluated
// with the caller's current access, so a resume can't show more than a list would.
func (s *Stream) startReplay(t Token) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.replay, s.cancel = true, cancel
	s.mu.Unlock()
	c := s.hub.Replay()
	go func() {
		_, _ = c.Run(ctx, changefeed.Position(t.Pos), replayHandler{s})
	}()
}

type replayHandler struct{ s *Stream }

func (r replayHandler) Change(_ context.Context, c changefeed.Change, at changefeed.Position) {
	s := r.s
	if c.System {
		return
	}
	s.mu.Lock()
	if s.closed || !s.replay {
		s.mu.Unlock()
		return
	}
	delta := s.processLocked(c, at, s.subs)
	if len(s.pending) > 0 {
		first := cursor{s.pending[0].c.At, s.pending[0].c.Increment}
		if !first.after(cursor{c.At, c.Increment}) {
			delta += s.handoffLocked()
		}
	}
	s.mu.Unlock()
	s.hub.charge(delta)
	s.hub.enforce()
	s.wake()
}

func (r replayHandler) Idle(context.Context, changefeed.Position) {}

// Gap: the history the token pointed at is gone.
func (r replayHandler) Gap(context.Context, changefeed.Gap) {
	s := r.s
	s.mu.Lock()
	var delta int64
	if s.replay && !s.closed {
		f := s.resetFrame(s.allIDs(), ReasonHistoryExpired)
		delta = s.enqueueLocked(f)
		delta += s.handoffLocked()
	}
	s.mu.Unlock()
	if s.hub.Observer != nil {
		s.hub.Observer.Reset(s.realm, ReasonHistoryExpired)
	}
	s.hub.charge(delta)
	s.wake()
}

// handoffLocked ends the replay and delivers what the live feed brought meanwhile.
func (s *Stream) handoffLocked() int64 {
	s.replay = false
	cancel := s.cancel
	s.cancel = nil
	if cancel != nil {
		go cancel()
	}
	pending := s.pending
	s.pending = nil
	if s.access != nil {
		go s.reload() // changes of the caller's access while replaying were not watched
	}
	var delta int64
	for _, p := range pending {
		delta += s.processLocked(p.c, p.at, p.cands)
	}
	return delta
}
