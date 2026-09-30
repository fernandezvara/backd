package httpapi

import (
	"sync"
	"testing"
)

func TestConcurrencyLimiter(t *testing.T) {
	l := newConcurrencyLimiter()

	// Unlimited (max <= 0): always succeeds, and isn't tracked.
	for range 5 {
		if !l.tryAcquire("x", 0) {
			t.Fatal("unlimited should always succeed")
		}
	}
	if len(l.counts) != 0 {
		t.Errorf("unlimited key was tracked: %v", l.counts)
	}

	// Limited: succeeds up to max, then refuses. Two separate calls, not
	// one condition checked twice: each mutates the count, so the second
	// genuinely differs from the first (a single "!tryAcquire() ||
	// !tryAcquire()" reads as the same check duplicated, which is why it
	// once was mistakenly collapsed to one call).
	if !l.tryAcquire("a", 2) {
		t.Fatal("the first acquire of 2 should succeed")
	}
	if !l.tryAcquire("a", 2) {
		t.Fatal("the second acquire of 2 should succeed")
	}
	if l.tryAcquire("a", 2) {
		t.Fatal("the third acquire of 2 should fail")
	}
	// A different key has its own count.
	if !l.tryAcquire("b", 1) {
		t.Fatal("a different key should have its own slot")
	}

	// Releasing frees a slot for the next caller.
	l.release("a", 2)
	if !l.tryAcquire("a", 2) {
		t.Fatal("acquire should succeed after a release")
	}

	// Releasing down to zero removes the key (no unbounded map growth).
	l.release("a", 2)
	l.release("a", 2)
	if _, ok := l.counts["a"]; ok {
		t.Errorf("key not cleaned up: %v", l.counts)
	}
}

func TestConcurrencyLimiterConcurrent(t *testing.T) {
	l := newConcurrencyLimiter()
	const max = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	inFlight, peak := 0, 0
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !l.tryAcquire("k", max) {
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			mu.Lock()
			inFlight--
			mu.Unlock()
			l.release("k", max)
		}()
	}
	wg.Wait()
	if peak > max {
		t.Errorf("peak concurrent holders = %d, want <= %d", peak, max)
	}
	if len(l.counts) != 0 {
		t.Errorf("counts not cleaned up: %v", l.counts)
	}
}
