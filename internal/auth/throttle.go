package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/registry"
)

// Login throttling: failures are counted per account and per client IP
// in a sliding window. Past a threshold, each new attempt must wait an
// exponentially growing delay. There is no lockout: waiting always works.
// The thresholds, the window and the longest delay are the realm's
// login_throttle (registry.LoginThrottle); the defaults are the values this
// feature had before they were configurable.

// Attempts is the failure counter of one key ("account:<email>" or "ip:<addr>").
type Attempts struct {
	Failures      int
	LastFailureAt time.Time
}

// ThrottledError means the caller must wait before trying again.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed attempts; retry in %s", e.RetryAfter.Round(time.Second))
}

// delay is how long to wait after the last failure, given the count.
func delay(failures, threshold int, maxDelay time.Duration) time.Duration {
	if failures < threshold {
		return 0
	}
	n := failures - threshold
	if n >= 20 {
		return maxDelay
	}
	return min(time.Second<<n, maxDelay)
}

// IPKey groups client addresses for throttling: IPv4 addresses one by
// one, IPv6 by /64, since one client usually controls a whole /64.
func IPKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	if addr.Is6() {
		p, _ := addr.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + addr.String()
}

// accountKey is the counter key of an email, whether or not it is
// registered, so throttling doesn't reveal which emails exist.
func accountKey(email string) string {
	e, err := registry.NormalizeEmail(email)
	if err != nil {
		return ""
	}
	return "account:" + e
}

type throttleKey struct {
	key       string
	threshold int
}

func keys(t registry.LoginThrottle, email, ip string) []throttleKey {
	var out []throttleKey
	if k := accountKey(email); k != "" {
		out = append(out, throttleKey{k, t.AccountThreshold})
	}
	if k := IPKey(ip); k != "" {
		out = append(out, throttleKey{k, t.IPThreshold})
	}
	return out
}

// checkThrottle returns a *ThrottledError if any key must still wait.
func (s *Users) checkThrottle(ctx context.Context, ks []throttleKey) error {
	now := s.now()
	var wait time.Duration
	for _, k := range ks {
		a, err := s.Store.LoginAttempts(ctx, k.key)
		if err != nil {
			return err
		}
		if w := a.LastFailureAt.Add(delay(a.Failures, k.threshold, s.Settings.Throttle().MaxDelay)).Sub(now); w > wait {
			wait = w
		}
	}
	if wait > 0 {
		s.Metrics.RateLimited(s.Realm, "login")
		return &ThrottledError{RetryAfter: wait}
	}
	return nil
}

func (s *Users) recordFailure(ctx context.Context, ks []throttleKey) error {
	now := s.now()
	var errs []error
	for _, k := range ks {
		errs = append(errs, s.Store.RecordLoginFailure(ctx, k.key, now, now.Add(s.Settings.Throttle().Window)))
	}
	return errors.Join(errs...)
}

// throttled runs check, a password check, under throttling for the keys:
// it refuses to run while they must wait, counts ErrInvalidCredentials
// as a failure, and clears the account counter on success.
//
// Checks for one account run one at a time, across every instance: first a
// lock in this process (cheap, and it keeps waiters off the database), then a
// lock in the store. Without them parallel requests could all pass the
// throttle before any failure is recorded, and N instances would allow N
// guesses per delay instead of one.
func (s *Users) throttled(ctx context.Context, email, ip string, check func() error) error {
	if k := accountKey(email); k != "" {
		if !s.noLocalLock {
			unlock := accountLocks.lock(k)
			defer unlock()
		}
		release, err := s.lockLogin(ctx, k)
		if err != nil {
			return err
		}
		defer release()
	}
	ks := keys(s.Settings.Throttle(), email, ip)
	if err := s.checkThrottle(ctx, ks); err != nil {
		return err
	}
	err := check()
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		if rerr := s.recordFailure(ctx, ks); rerr != nil {
			return rerr
		}
	case err == nil:
		if k := accountKey(email); k != "" {
			if cerr := s.Store.ClearLoginAttempts(ctx, k); cerr != nil {
				return cerr
			}
		}
	}
	return err
}

const (
	// loginLockLease is how long a held lock stays valid if its process dies:
	// longer than a password check can take (it may queue for a hash slot).
	loginLockLease = 30 * time.Second
	// loginLockWait is how long a check waits for the lock before the caller
	// is told to retry; loginLockPoll is how often it looks.
	loginLockWait = 5 * time.Second
	loginLockPoll = 25 * time.Millisecond
)

// lockLogin takes the account's lock in the store, waiting for the check
// that holds it. It gives up after loginLockWait with a *ThrottledError: an
// account under a parallel flood answers "retry shortly", never a guess.
func (s *Users) lockLogin(ctx context.Context, key string) (release func(), err error) {
	owner := xid.New().String()
	deadline := time.Now().Add(loginLockWait)
	for {
		now := time.Now() // wall clock: the lease is shared by instances, not the service's test clock
		ok, err := s.Store.AcquireLoginLock(ctx, key, owner, now, now.Add(loginLockLease))
		if err != nil {
			return nil, err
		}
		if ok {
			return func() {
				// The caller may be gone (its context ended); the lock must still be freed.
				_ = s.Store.ReleaseLoginLock(context.WithoutCancel(ctx), key, owner)
			}, nil
		}
		if time.Now().After(deadline) {
			s.Metrics.RateLimited(s.Realm, "login")
			return nil, &ThrottledError{RetryAfter: time.Second}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(loginLockPoll):
		}
	}
}

// keyedMutex hands out one mutex per key, dropping it when unused.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	sync.Mutex
	users int
}

// accountLocks serializes password checks per account in this process.
var accountLocks = &keyedMutex{locks: map[string]*keyedLock{}}

func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.users++
	k.mu.Unlock()

	l.Lock()
	return func() {
		l.Unlock()
		k.mu.Lock()
		if l.users--; l.users == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
