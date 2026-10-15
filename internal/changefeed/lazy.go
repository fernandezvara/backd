package changefeed

import (
	"context"
	"sync"
	"time"
)

// DefaultGrace is how long a Lazy consumer stays open after its last subscriber left.
const DefaultGrace = 30 * time.Second

// Lazy shares one Consumer between subscribers and keeps it closed while there are none: the
// stream opens, from now, when the first subscriber arrives, and closes after Grace once the
// last one has left. An instance with no subscribers therefore reads nothing
// (design/triggers-and-realtime.md §3).
type Lazy struct {
	c     *Consumer
	grace time.Duration

	mu       sync.Mutex
	handlers map[int]Handler
	next     int
	cancel   context.CancelFunc
	done     chan struct{}
	timer    *time.Timer
	closed   bool
}

// NewLazy wraps c. A grace of 0 is DefaultGrace.
func NewLazy(c *Consumer, grace time.Duration) *Lazy {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return &Lazy{c: c, grace: grace, handlers: map[int]Handler{}}
}

// Acquire registers a subscriber, opening the stream if it is the first. The returned
// function removes it; calling it more than once is harmless.
func (l *Lazy) Acquire(h Handler) (release func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return func() {}
	}
	id := l.next
	l.next++
	l.handlers[id] = h
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if l.cancel == nil {
		l.start()
	}
	var once sync.Once
	return func() { once.Do(func() { l.release(id) }) }
}

// Open reports whether the stream is running.
func (l *Lazy) Open() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cancel != nil
}

// Subscribers is how many handlers are registered.
func (l *Lazy) Subscribers() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.handlers)
}

// start must be called with l.mu held.
func (l *Lazy) start() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	l.cancel, l.done = cancel, done
	go func() {
		defer close(done)
		_, _ = l.c.Run(ctx, nil, fanout{l})
	}()
}

func (l *Lazy) release(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.handlers, id)
	if len(l.handlers) > 0 || l.cancel == nil || l.timer != nil {
		return
	}
	l.timer = time.AfterFunc(l.grace, l.idle)
}

// idle closes the stream when nobody came back during the grace period.
func (l *Lazy) idle() {
	l.mu.Lock()
	if len(l.handlers) > 0 || l.cancel == nil {
		l.mu.Unlock()
		return
	}
	cancel, done := l.cancel, l.done
	l.cancel, l.done, l.timer = nil, nil, nil
	l.mu.Unlock()
	cancel()
	<-done
}

// Close stops the stream whatever the subscribers; later Acquire calls do nothing.
func (l *Lazy) Close() {
	l.mu.Lock()
	l.closed = true
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.handlers = map[int]Handler{}
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (l *Lazy) snapshot() []Handler {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Handler, 0, len(l.handlers))
	for _, h := range l.handlers {
		out = append(out, h)
	}
	return out
}

// fanout delivers to every subscriber registered when the call happens.
type fanout struct{ l *Lazy }

func (f fanout) Change(ctx context.Context, c Change, at Position) {
	for _, h := range f.l.snapshot() {
		h.Change(ctx, c, at)
	}
}

func (f fanout) Idle(ctx context.Context, at Position) {
	for _, h := range f.l.snapshot() {
		h.Idle(ctx, at)
	}
}

func (f fanout) Gap(ctx context.Context, g Gap) {
	for _, h := range f.l.snapshot() {
		h.Gap(ctx, g)
	}
}

// SetWatched replaces what the consumer reads, from the configuration in force. It takes
// effect at once on an open stream (which resumes after its position) and otherwise at the
// next open.
func (l *Lazy) SetWatched(set WatchSet) { l.c.SetWatched(set) }
