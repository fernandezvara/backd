// Package ratelimit is the request limiter of design/rate-limiting.md: a token bucket per key,
// held in memory on one backd instance. It makes no I/O and takes no HTTP types, so the server
// decides what a key is (a class, a principal, a client address) and what a refusal looks like.
//
// A bucket holds up to Limit tokens and refills Limit tokens per Window; a request takes one. A
// client may burst to the whole allowance and then runs at the refill rate. A bucket that is full
// is the same as no bucket, so idle ones are dropped, and the number of buckets is capped: when
// the table is full a key it has not seen shares one overflow bucket, so a flood of new keys
// degrades to a shared limit instead of growing memory.
package ratelimit

import (
	"hash/maphash"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Rate is an allowance: Limit requests per Window (the size of the bucket and how fast it
// refills). The zero value, or a Limit below 1, is no limit.
type Rate struct {
	Limit  int
	Window time.Duration
}

// Unlimited reports whether r limits nothing.
func (r Rate) Unlimited() bool { return r.Limit < 1 || r.Window <= 0 }

// Decision is what Allow decided, with what a client needs to slow down before it is refused.
type Decision struct {
	Allowed bool
	// Limit is the size of the bucket.
	Limit int
	// Remaining is how many more requests would pass right now.
	Remaining int
	// RetryAfter is how long until one request passes; zero when Allowed.
	RetryAfter time.Duration
	// Reset is how long until the bucket is full again.
	Reset time.Duration
}

const (
	shards = 16
	// DefaultMaxBuckets is how many buckets one Limiter keeps.
	DefaultMaxBuckets = 100_000
	sweepEvery        = time.Minute
	overflowKey       = "\x00overflow"
)

type bucket struct {
	tokens float64
	at     time.Time
	rate   Rate
}

type shard struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	nextSweep time.Time
}

// Limiter holds the buckets of one instance. It is safe for concurrent use.
type Limiter struct {
	shards     [shards]shard
	seed       maphash.Seed
	perShard   int
	now        func() time.Time
	overflowed atomic.Int64
}

// New returns a Limiter that keeps at most maxBuckets buckets (DefaultMaxBuckets when it is below
// 1). now is the clock; time.Now when nil.
func New(maxBuckets int, now func() time.Time) *Limiter {
	if maxBuckets < 1 {
		maxBuckets = DefaultMaxBuckets
	}
	if now == nil {
		now = time.Now
	}
	l := &Limiter{seed: maphash.MakeSeed(), perShard: max(1, maxBuckets/shards), now: now}
	for i := range l.shards {
		l.shards[i].buckets = map[string]*bucket{}
	}
	return l
}

// Allow takes one token from key's bucket under rate and says whether the request passes. A rate
// that limits nothing always passes and stores nothing.
func (l *Limiter) Allow(key string, rate Rate) Decision {
	if rate.Unlimited() {
		return Decision{Allowed: true, Limit: 0, Remaining: math.MaxInt32}
	}
	now := l.now()
	s := &l.shards[maphash.String(l.seed, key)%shards]
	s.mu.Lock()
	defer s.mu.Unlock()
	if !now.Before(s.nextSweep) {
		sweep(s, now)
		s.nextSweep = now.Add(sweepEvery)
	}
	b := s.buckets[key]
	if b == nil {
		if len(s.buckets) >= l.perShard {
			// The table is full: unseen keys share one bucket per shard.
			l.overflowed.Add(1)
			if b = s.buckets[overflowKey]; b == nil {
				b = &bucket{tokens: float64(rate.Limit), at: now, rate: rate}
				s.buckets[overflowKey] = b
			}
		} else {
			b = &bucket{tokens: float64(rate.Limit), at: now, rate: rate}
			s.buckets[key] = b
		}
	}
	return b.take(now, rate)
}

// take refills the bucket for the time that has passed and takes one token if there is one.
func (b *bucket) take(now time.Time, rate Rate) Decision {
	// A changed rate (the configuration moved on) applies from now: keep the share of the bucket.
	if b.rate != rate {
		if b.rate.Limit > 0 {
			b.tokens = b.tokens / float64(b.rate.Limit) * float64(rate.Limit)
		}
		b.rate = rate
	}
	capacity := float64(rate.Limit)
	perSecond := capacity / rate.Window.Seconds()
	if elapsed := now.Sub(b.at).Seconds(); elapsed > 0 {
		b.tokens = math.Min(capacity, b.tokens+elapsed*perSecond)
	}
	b.at = now
	d := Decision{Limit: rate.Limit}
	if b.tokens >= 1 {
		b.tokens--
		d.Allowed = true
	} else {
		d.RetryAfter = secondsToDuration((1 - b.tokens) / perSecond)
	}
	d.Remaining = int(math.Floor(b.tokens))
	d.Reset = secondsToDuration((capacity - b.tokens) / perSecond)
	return d
}

func secondsToDuration(s float64) time.Duration {
	return time.Duration(math.Ceil(s*1e3)) * time.Millisecond
}

// sweep drops the buckets that have refilled completely: they are the same as new ones.
func sweep(s *shard, now time.Time) {
	for k, b := range s.buckets {
		if k == overflowKey {
			continue
		}
		perSecond := float64(b.rate.Limit) / b.rate.Window.Seconds()
		if b.tokens+now.Sub(b.at).Seconds()*perSecond >= float64(b.rate.Limit) {
			delete(s.buckets, k)
		}
	}
}

// Sweep drops every idle bucket now. Allow does it a shard at a time, once a minute, as keys come in;
// this is for a caller that wants the table small without waiting for traffic.
func (l *Limiter) Sweep() {
	now := l.now()
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		sweep(s, now)
		s.nextSweep = now.Add(sweepEvery)
		s.mu.Unlock()
	}
}

// Size is how many buckets are held now.
func (l *Limiter) Size() int {
	n := 0
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		n += len(s.buckets)
		s.mu.Unlock()
	}
	return n
}

// Overflowed is how many requests, since the start, used an overflow bucket because the table was full.
func (l *Limiter) Overflowed() int64 { return l.overflowed.Load() }
