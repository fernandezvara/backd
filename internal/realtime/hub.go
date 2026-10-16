package realtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// Reset reasons: what a `reset` event says about why a client has to reload.
const (
	ReasonHistoryExpired = "history_expired" // the change history no longer reaches the stream
	ReasonAccessChanged  = "access_changed"  // the subscriber's access changed (design §19)
	ReasonSlowConsumer   = "slow_consumer"   // the stream's buffer was dropped
	ReasonPreimageMiss   = "preimage_missing"
	ReasonConfigChanged  = "config_changed" // the configuration the subscription depended on changed: subscribe again
)

// Limits are what bounds a deployment's streams.
type Limits struct {
	SubscriptionsPerStream int
	StreamsPerUser         int
	Streams                int // per instance
	BufferBytes            int // per stream
	BufferEvents           int // per stream
	Memory                 int64
	Heartbeat              time.Duration
	// OpenInterval is the least time between two streams opened by one principal.
	OpenInterval time.Duration
	// ResumeMaxAge is how old a resume token may be before the history is taken as gone.
	ResumeMaxAge time.Duration
}

// DefaultLimits are used for what is zero.
var DefaultLimits = Limits{
	SubscriptionsPerStream: 20, StreamsPerUser: 5, Streams: 10000, BufferBytes: 256 << 10, BufferEvents: 1000,
	Memory: 256 << 20, Heartbeat: 25 * time.Second, OpenInterval: time.Second, ResumeMaxAge: 24 * time.Hour,
}

// OrDefault fills the zero fields.
func (l Limits) OrDefault() Limits {
	d := DefaultLimits
	if l.SubscriptionsPerStream <= 0 {
		l.SubscriptionsPerStream = d.SubscriptionsPerStream
	}
	if l.StreamsPerUser <= 0 {
		l.StreamsPerUser = d.StreamsPerUser
	}
	if l.Streams <= 0 {
		l.Streams = d.Streams
	}
	if l.BufferBytes <= 0 {
		l.BufferBytes = d.BufferBytes
	}
	if l.BufferEvents <= 0 {
		l.BufferEvents = d.BufferEvents
	}
	if l.Memory <= 0 {
		l.Memory = d.Memory
	}
	if l.Heartbeat <= 0 {
		l.Heartbeat = d.Heartbeat
	}
	if l.OpenInterval == 0 {
		l.OpenInterval = d.OpenInterval
	}
	if l.ResumeMaxAge <= 0 {
		l.ResumeMaxAge = d.ResumeMaxAge
	}
	return l
}

// Refusals when a stream can't be opened. Each says when to try again.
var (
	ErrTooManyStreams = errors.New("realtime: this instance has too many open streams")
	ErrUserStreams    = errors.New("realtime: too many open streams for this caller")
	ErrOpenTooFast    = errors.New("realtime: streams are opened too fast")
)

// Observer receives the realtime metrics; every method may be called on a nil receiver by
// its implementation.
type Observer interface {
	StreamOpened(realm string)
	StreamClosed(realm string)
	Event(realm, event string)
	Reset(realm, reason string)
	Refused(realm, reason string)
	// Checks counts the subscriptions a change was evaluated against: kind "indexed" (found by
	// document id or key) or "scanned" (no key: asked on every change).
	Checks(realm, kind string, n int)
}

// Hub holds the open streams of one instance.
type Hub struct {
	// Feed is the instance's shared consumer; every stream is a handler of it.
	Feed *changefeed.Lazy
	// Replay makes a consumer that reads from a resume position, over the same set the Feed
	// reads. Nil disables resuming: a token is taken as expired history.
	Replay   func() *changefeed.Consumer
	Limits   Limits
	Observer Observer
	Now      func() time.Time

	imu      sync.RWMutex
	index    map[string]*collIndex // by CollKey
	attached func()                // releases the feed while there are streams

	mu       sync.Mutex
	streams  map[*Stream]struct{}
	perUser  map[string]int
	lastOpen map[string]time.Time
	mem      int64
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Hub) limits() Limits { return h.Limits.OrDefault() }

// Request opens a stream.
type Request struct {
	Realm string
	// Principal names who the stream is for ("user:u_1", "key:ci", "anon:<ip>"); streams per
	// caller and resume tokens are counted and bound by it.
	Principal     string
	Key           []byte // the realm's resume key
	Subscriptions []*Subscription
	Resume        string // a token, or ""
	// Limits lower the instance's: per stream subscriptions and streams per user, from the realm.
	MaxStreamsPerUser int
	// Access ties the stream to its caller's session, user or key, so that changes of them
	// reach it; nil for anonymous callers and realms without authentication.
	Access *Access
}

// ErrTooManySubscriptions is returned when a request has more subscriptions than allowed.
var ErrTooManySubscriptions = errors.New("realtime: too many subscriptions")

// Open registers a stream. The caller writes `ready`, then calls Run, and always Close.
func (h *Hub) Open(req Request) (*Stream, error) {
	lim := h.limits()
	if len(req.Subscriptions) > lim.SubscriptionsPerStream {
		return nil, ErrTooManySubscriptions
	}
	var tok *Token
	expired := false
	if req.Resume != "" {
		t, err := Open(req.Key, req.Resume)
		if err != nil || t.Realm != req.Realm || t.Principal != req.Principal {
			return nil, ErrBadToken
		}
		if h.now().Sub(time.Unix(t.Issued, 0)) > lim.ResumeMaxAge || h.Replay == nil {
			expired = true
		} else {
			tok = &t
		}
	}
	maxUser := lim.StreamsPerUser
	if req.MaxStreamsPerUser > 0 {
		maxUser = min(maxUser, req.MaxStreamsPerUser)
	}

	h.mu.Lock()
	switch {
	case len(h.streams) >= lim.Streams:
		h.mu.Unlock()
		h.refused(req.Realm, "instance")
		return nil, ErrTooManyStreams
	case h.perUser[req.Principal] >= maxUser:
		h.mu.Unlock()
		h.refused(req.Realm, "user")
		return nil, ErrUserStreams
	case lim.OpenInterval > 0 && h.now().Sub(h.lastOpen[req.Principal]) < lim.OpenInterval:
		h.mu.Unlock()
		h.refused(req.Realm, "rate")
		return nil, ErrOpenTooFast
	}
	if h.streams == nil {
		h.streams, h.perUser, h.lastOpen = map[*Stream]struct{}{}, map[string]int{}, map[string]time.Time{}
	}
	s := newStream(h, req, lim)
	h.streams[s] = struct{}{}
	h.perUser[req.Principal]++
	h.lastOpen[req.Principal] = h.now()
	if len(h.lastOpen) > 4096 {
		h.pruneOpens()
	}
	h.mu.Unlock()

	if h.Observer != nil {
		h.Observer.StreamOpened(req.Realm)
	}
	h.attach(s)
	s.startExpiry()
	switch {
	case expired:
		s.resetAll(ReasonHistoryExpired)
	case tok != nil:
		s.startReplay(*tok)
	}
	return s, nil
}

func (h *Hub) pruneOpens() {
	cut := h.now().Add(-time.Minute)
	for k, t := range h.lastOpen {
		if t.Before(cut) {
			delete(h.lastOpen, k)
		}
	}
}

func (h *Hub) refused(realm, reason string) {
	if h.Observer != nil {
		h.Observer.Refused(realm, reason)
	}
}

func (h *Hub) remove(s *Stream, subs []*Subscription) {
	h.unindex(subs)
	h.mu.Lock()
	if _, ok := h.streams[s]; ok {
		delete(h.streams, s)
		if h.perUser[s.principal]--; h.perUser[s.principal] <= 0 {
			delete(h.perUser, s.principal)
		}
	}
	none := len(h.streams) == 0
	h.mu.Unlock()
	if none {
		h.detach()
	}
}

// attach indexes the stream's subscriptions and makes sure the feed delivers to the hub.
func (h *Hub) attach(s *Stream) {
	h.imu.Lock()
	if h.index == nil {
		h.index = map[string]*collIndex{}
	}
	for _, sub := range s.subs {
		sub.owner = s
		ci := h.index[sub.collKey()]
		if ci == nil {
			ci = newCollIndex()
			h.index[sub.collKey()] = ci
		}
		ci.add(sub)
	}
	if h.attached == nil {
		h.attached = h.Feed.Acquire(h)
	}
	h.imu.Unlock()
}

// detach lets the feed close when no stream is left (after its grace).
func (h *Hub) detach() {
	h.imu.Lock()
	release := h.attached
	h.mu.Lock()
	idle := len(h.streams) == 0
	h.mu.Unlock()
	if idle {
		h.attached = nil
	} else {
		release = nil
	}
	h.imu.Unlock()
	if release != nil {
		release()
	}
}

func (h *Hub) unindex(subs []*Subscription) {
	if len(subs) == 0 {
		return
	}
	h.imu.Lock()
	for _, sub := range subs {
		if ci := h.index[sub.collKey()]; ci != nil && sub.filed != "" {
			ci.remove(sub)
			if len(ci.streams) == 0 {
				delete(h.index, sub.collKey())
			}
		}
	}
	h.imu.Unlock()
}

// Change is the hub as the feed's handler: it finds, once per change, the subscriptions the
// change may matter to, and hands each stream its own.
func (h *Hub) Change(_ context.Context, c changefeed.Change, at changefeed.Position) {
	if c.System {
		h.systemChange(c)
		return
	}
	key := CollKey(c.DB, c.Coll)
	h.imu.RLock()
	ci := h.index[key]
	if ci == nil {
		h.imu.RUnlock()
		return
	}
	if c.Op != changefeed.Created && c.Before == nil {
		// The image that says who could see the document is missing: no subscription can be
		// decided, so each stream with one reloads (coalesced), and nobody is told an id.
		streams := make([]*Stream, 0, len(ci.streams))
		for s := range ci.streams {
			streams = append(streams, s)
		}
		h.imu.RUnlock()
		for _, s := range streams {
			s.preimageMissing(c.DB, c.Coll)
		}
		return
	}
	cands, indexed, scanned := ci.candidates(c.Key, c.Before, c.After)
	h.imu.RUnlock()
	if h.Observer != nil && len(cands) > 0 {
		h.Observer.Checks(c.Realm, "indexed", indexed)
		h.Observer.Checks(c.Realm, "scanned", scanned)
	}
	by := map[*Stream][]*Subscription{}
	for _, sub := range cands {
		by[sub.owner] = append(by[sub.owner], sub)
	}
	for s, subs := range by {
		s.deliver(c, at, subs)
	}
}

// Idle is part of changefeed.Handler: nothing to deliver.
func (h *Hub) Idle(context.Context, changefeed.Position) {}

// Gap is called when the feed lost history: whatever the streams missed is gone.
func (h *Hub) Gap(context.Context, changefeed.Gap) {
	h.mu.Lock()
	streams := make([]*Stream, 0, len(h.streams))
	for s := range h.streams {
		streams = append(streams, s)
	}
	h.mu.Unlock()
	for _, s := range streams {
		s.resetAll(ReasonHistoryExpired)
		s.reload() // system changes may be among what was lost
	}
}

// Streams is how many streams are open.
func (h *Hub) Streams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.streams)
}

// Buffered is the bytes held in all buffers.
func (h *Hub) Buffered() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mem
}

func (h *Hub) charge(n int64) {
	h.mu.Lock()
	h.mem += n
	h.mu.Unlock()
}

// enforce drops the largest buffers while the instance is over its memory budget: those
// streams are reset, so memory stays bounded and no stream ever stalls the change feed.
func (h *Hub) enforce() {
	budget := h.limits().Memory
	for {
		h.mu.Lock()
		if h.mem <= budget {
			h.mu.Unlock()
			return
		}
		var big *Stream
		bigN := 0
		for s := range h.streams {
			if n := s.buffered(); n > bigN {
				big, bigN = s, n
			}
		}
		h.mu.Unlock()
		if big == nil || bigN == 0 {
			return
		}
		big.slowConsumer()
	}
}

// ConfigChanged tells the streams that subscriptions to these collections (keyed by
// MongoDB database and collection) no longer follow the configuration they were opened
// with: they are removed from their streams and the client is told to subscribe again.
func (h *Hub) ConfigChanged(colls map[string]bool) {
	if len(colls) == 0 {
		return
	}
	h.mu.Lock()
	streams := make([]*Stream, 0, len(h.streams))
	for s := range h.streams {
		streams = append(streams, s)
	}
	h.mu.Unlock()
	for _, s := range streams {
		s.configChanged(colls)
	}
}

// CollKey is how ConfigChanged names a collection.
func CollKey(db, coll string) string { return db + "\x00" + coll }
