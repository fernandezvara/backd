package httpapi

import "sync"

// concurrencyLimiter counts concurrent function calls per key (a
// function, or a realm), on this backd instance only (roadmap F8): with
// several instances, up to N × the limit can run across the deployment.
// Async jobs (F11) will wait in a shared queue instead, once they exist.
type concurrencyLimiter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newConcurrencyLimiter() *concurrencyLimiter {
	return &concurrencyLimiter{counts: map[string]int{}}
}

// tryAcquire increments key's count and reports success, unless it's
// already at max. max <= 0 means unlimited (always succeeds, untracked).
func (l *concurrencyLimiter) tryAcquire(key string, max int) bool {
	if max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[key] >= max {
		return false
	}
	l.counts[key]++
	return true
}

// release gives back a slot acquired with the same key and max.
func (l *concurrencyLimiter) release(key string, max int) {
	if max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[key] <= 1 {
		delete(l.counts, key)
	} else {
		l.counts[key]--
	}
}
