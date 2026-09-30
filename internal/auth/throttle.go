package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/registry"
)

// Login throttling: failures are counted per account and per client IP
// in a sliding window. Past a threshold, each new attempt must wait an
// exponentially growing delay. There is no lockout: waiting always works.
const (
	accountThreshold = 5
	ipThreshold      = 50
	attemptWindow    = 15 * time.Minute
	maxDelay         = 15 * time.Minute
)

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
func delay(failures, threshold int) time.Duration {
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

func keys(email, ip string) []throttleKey {
	var out []throttleKey
	if k := accountKey(email); k != "" {
		out = append(out, throttleKey{k, accountThreshold})
	}
	if k := IPKey(ip); k != "" {
		out = append(out, throttleKey{k, ipThreshold})
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
		if w := a.LastFailureAt.Add(delay(a.Failures, k.threshold)).Sub(now); w > wait {
			wait = w
		}
	}
	if wait > 0 {
		return &ThrottledError{RetryAfter: wait}
	}
	return nil
}

func (s *Users) recordFailure(ctx context.Context, ks []throttleKey) error {
	now := s.now()
	var errs []error
	for _, k := range ks {
		errs = append(errs, s.Store.RecordLoginFailure(ctx, k.key, now, now.Add(attemptWindow)))
	}
	return errors.Join(errs...)
}

// throttled runs check, a password check, under throttling for the keys:
// it refuses to run while they must wait, counts ErrInvalidCredentials
// as a failure, and clears the account counter on success.
//
// Checks for one account run one at a time in this process, so parallel
// requests can't all pass the throttle before any failure is recorded.
func (s *Users) throttled(ctx context.Context, email, ip string, check func() error) error {
	if k := accountKey(email); k != "" {
		unlock := accountLocks.lock(k)
		defer unlock()
	}
	ks := keys(email, ip)
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
