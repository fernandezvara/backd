package changefeed

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Consumer reads one stream over a watched set and hands what it reads to a Handler. It
// resumes by itself: after an error from the position it last reached, after a change of the
// watched set (a configuration reload) from the same position, and — when the history no
// longer reaches the position — from now, reporting a Gap.
type Consumer struct {
	// Name labels the consumer's metrics and logs ("realtime", "dispatcher").
	Name     string
	Source   Source
	Log      *slog.Logger
	Observer Observer
	// Backoff is the wait before the nth consecutive reopen after an error; nil is 100ms
	// doubling to 5s.
	Backoff func(attempt int) time.Duration

	mu      sync.Mutex
	set     WatchSet
	changed chan struct{} // closed and replaced when the set changes
}

// SetWatched replaces the watched set. A running consumer reopens its stream after the last
// position it reached; one with nothing watched waits.
func (c *Consumer) SetWatched(set WatchSet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.changed = make(chan struct{})
	}
	if c.set.Equal(set) {
		return
	}
	c.set = set
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Consumer) watched() (WatchSet, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.changed = make(chan struct{})
	}
	return c.set, c.changed
}

func (c *Consumer) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Consumer) backoff(attempt int) time.Duration {
	if c.Backoff != nil {
		return c.Backoff(attempt)
	}
	d := 100 * time.Millisecond << min(attempt, 6)
	return min(d, 5*time.Second)
}

func (c *Consumer) state(open bool, n int) {
	if c.Observer != nil {
		c.Observer.FeedState(c.Name, open, n)
	}
}

func (c *Consumer) reopened(reason string) {
	if c.Observer != nil {
		c.Observer.FeedReopened(c.Name, reason)
	}
}

// Run reads until ctx is done, starting after the given position (nil: from now). It returns
// the last position it reached, which a caller that keeps one durably stores.
func (c *Consumer) Run(ctx context.Context, after Position, h Handler) (Position, error) {
	pos := after
	attempt := 0
	for ctx.Err() == nil {
		set, changed := c.watched()
		if set.Empty() {
			c.state(false, 0)
			select {
			case <-ctx.Done():
			case <-changed:
			}
			continue
		}
		stream, err := c.Source.Open(ctx, set, pos)
		switch {
		case ctx.Err() != nil:
			return pos, ctx.Err()
		case errors.Is(err, ErrHistoryLost):
			c.log().Warn("change history lost: continuing from now", "consumer", c.Name)
			h.Gap(ctx, Gap{From: pos, At: time.Now().UTC()})
			c.reopened(ReasonGap)
			pos = nil
			continue
		case err != nil:
			c.log().Warn("change stream could not open", "consumer", c.Name, "error", err)
			c.reopened(ReasonError)
			if !sleep(ctx, c.backoff(attempt)) {
				return pos, ctx.Err()
			}
			attempt++
			continue
		}
		attempt = 0
		if pos == nil {
			// A stream opened from now: its position is known at once, and a handler that
			// stores positions must have one even if nothing is ever written.
			pos = stream.Position()
			h.Idle(ctx, pos)
		}
		c.state(true, set.Count())
		c.log().Debug("change stream open", "consumer", c.Name, "databases", set.describe(), "namespaces", set.Count())

		reason, err := c.pump(ctx, stream, &pos, h, changed)
		stream.Close()
		c.state(false, 0)
		switch {
		case ctx.Err() != nil:
			return pos, ctx.Err()
		case reason == ReasonSetChanged:
			c.reopened(ReasonSetChanged)
		case errors.Is(err, ErrHistoryLost):
			c.log().Warn("change history lost: continuing from now", "consumer", c.Name)
			h.Gap(ctx, Gap{From: pos, At: time.Now().UTC()})
			c.reopened(ReasonGap)
			pos = nil
		default:
			c.log().Warn("change stream failed", "consumer", c.Name, "error", err)
			c.reopened(ReasonError)
			if !sleep(ctx, c.backoff(attempt)) {
				return pos, ctx.Err()
			}
			attempt++
		}
	}
	return pos, ctx.Err()
}

// pump reads one open stream until it fails or the watched set changes.
func (c *Consumer) pump(ctx context.Context, s Stream, pos *Position, h Handler, changed <-chan struct{}) (reason string, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	setChanged := make(chan struct{})
	go func() {
		select {
		case <-changed:
			close(setChanged)
			cancel()
		case <-sctx.Done():
		}
	}()
	for {
		ev, err := s.Next(sctx)
		if err != nil {
			select {
			case <-setChanged:
				return ReasonSetChanged, nil
			default:
			}
			return "", err
		}
		if ev.Position != nil {
			*pos = ev.Position
		}
		if ev.Change == nil {
			h.Idle(ctx, ev.Position)
			continue
		}
		if c.Observer != nil {
			c.Observer.FeedEvent(c.Name, ev.Change.Op, ev.Change.System)
			if !ev.Change.At.IsZero() {
				c.Observer.FeedLag(c.Name, max(0, time.Since(ev.Change.At)))
			}
		}
		h.Change(ctx, *ev.Change, ev.Position)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
