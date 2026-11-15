package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)} }

var perMinute = Rate{Limit: 60, Window: time.Minute} // one token a second, a burst of 60

func TestBurstThenTheRefillRate(t *testing.T) {
	c := newClock()
	l := New(0, c.now)
	for i := range 60 {
		if d := l.Allow("k", perMinute); !d.Allowed || d.Remaining != 59-i {
			t.Fatalf("request %d: %+v", i, d)
		}
	}
	d := l.Allow("k", perMinute)
	if d.Allowed || d.Remaining != 0 || d.RetryAfter != time.Second || d.Limit != 60 {
		t.Fatalf("the 61st: %+v", d)
	}
	c.advance(d.RetryAfter)
	if d := l.Allow("k", perMinute); !d.Allowed {
		t.Fatalf("after the wait: %+v", d)
	}
	if d := l.Allow("k", perMinute); d.Allowed {
		t.Fatal("only one token had refilled")
	}
	c.advance(10 * time.Second)
	if d := l.Allow("k", perMinute); !d.Allowed || d.Remaining != 9 {
		t.Fatalf("ten tokens refilled in ten seconds: %+v", d)
	}
}

func TestResetIsWhenTheBucketIsFullAgain(t *testing.T) {
	c := newClock()
	l := New(0, c.now)
	d := l.Allow("k", perMinute)
	if d.Reset != time.Second {
		t.Errorf("one token missing: Reset = %v", d.Reset)
	}
	for range 29 {
		d = l.Allow("k", perMinute)
	}
	if d.Remaining != 30 || d.Reset != 30*time.Second {
		t.Errorf("%+v", d)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l := New(0, newClock().now)
	r := Rate{Limit: 1, Window: time.Minute}
	if !l.Allow("a", r).Allowed || l.Allow("a", r).Allowed {
		t.Fatal("a has one token")
	}
	if !l.Allow("b", r).Allowed {
		t.Fatal("b is not affected by a")
	}
}

func TestNoLimitPassesAndStoresNothing(t *testing.T) {
	l := New(0, newClock().now)
	for _, r := range []Rate{{}, {Limit: 0, Window: time.Minute}, {Limit: 5}} {
		if d := l.Allow("k", r); !d.Allowed {
			t.Errorf("%+v refused", r)
		}
	}
	if l.Size() != 0 {
		t.Errorf("Size = %d", l.Size())
	}
}

func TestAChangedRateKeepsTheShareOfTheBucket(t *testing.T) {
	l := New(0, newClock().now)
	for range 30 {
		l.Allow("k", perMinute) // half the bucket
	}
	d := l.Allow("k", Rate{Limit: 120, Window: time.Minute}) // doubled: half of 120, minus this request
	if !d.Allowed || d.Remaining != 59 || d.Limit != 120 {
		t.Errorf("%+v", d)
	}
}

func TestIdleBucketsAreDropped(t *testing.T) {
	c := newClock()
	l := New(0, c.now)
	for i := range 1000 {
		l.Allow(fmt.Sprint("k", i), perMinute)
	}
	if l.Size() != 1000 {
		t.Fatalf("Size = %d", l.Size())
	}
	c.advance(2 * time.Minute) // every bucket has refilled
	l.Sweep()
	if l.Size() != 0 {
		t.Errorf("idle buckets were kept: Size = %d", l.Size())
	}
	// Allow sweeps a shard by itself, once a minute, as traffic comes in.
	for i := range 1000 {
		l.Allow(fmt.Sprint("k", i), perMinute)
	}
	c.advance(2 * time.Minute)
	for i := range 5000 {
		l.Allow(fmt.Sprint("fresh", i), perMinute)
	}
	if l.Size() > 5000+16 {
		t.Errorf("Allow did not sweep: Size = %d", l.Size())
	}
}

func TestAFullTableSharesAnOverflowBucketInsteadOfGrowing(t *testing.T) {
	l := New(16*4, newClock().now) // 4 buckets a shard
	r := Rate{Limit: 2, Window: time.Hour}
	for i := range 10_000 {
		l.Allow(fmt.Sprint("k", i), r)
	}
	if got := l.Size(); got > 16*5 { // the table plus one overflow bucket a shard
		t.Errorf("Size = %d: the table grew", got)
	}
	if l.Overflowed() == 0 {
		t.Error("nothing overflowed")
	}
	// New keys now share a bucket: once it is empty, an unseen key is refused.
	refused := 0
	for i := range 200 {
		if !l.Allow(fmt.Sprint("new", i), r).Allowed {
			refused++
		}
	}
	if refused == 0 {
		t.Error("an attack with many keys got a bucket of its own each")
	}
	// A key that already has a bucket is not affected by the overflow.
	l2 := New(16*4, newClock().now)
	l2.Allow("known", r)
	for i := range 10_000 {
		l2.Allow(fmt.Sprint("k", i), r)
	}
	if d := l2.Allow("known", r); !d.Allowed || d.Remaining != 0 {
		t.Errorf("known key: %+v", d)
	}
}

func TestConcurrentUse(t *testing.T) {
	l := New(0, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	r := Rate{Limit: 1000, Window: time.Hour}
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := 0
			for range 500 {
				if l.Allow("shared", r).Allowed {
					n++
				}
				l.Allow(fmt.Sprint("own", g), r)
			}
			mu.Lock()
			allowed += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	// 4,000 requests against a bucket of 1,000 that refills ~0.28 a second: the limit holds.
	if allowed < 1000 || allowed > 1010 {
		t.Errorf("allowed = %d, want about 1000", allowed)
	}
}

func BenchmarkAllow(b *testing.B) {
	l := New(0, nil)
	r := Rate{Limit: 1 << 30, Window: time.Minute}
	b.Run("one key", func(b *testing.B) {
		for b.Loop() {
			l.Allow("k", r)
		}
	})
	b.Run("many keys", func(b *testing.B) {
		keys := make([]string, 50_000)
		for i := range keys {
			keys[i] = fmt.Sprint("principal:", i)
		}
		i := 0
		for b.Loop() {
			l.Allow(keys[i%len(keys)], r)
			i++
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				l.Allow(fmt.Sprint("k", i%64), r)
				i++
			}
		})
	})
}
