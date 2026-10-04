package mongodb

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginLockOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)

	// Of many parallel owners, exactly one gets a free lock.
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.AcquireLoginLock(ctx, "account:a@example.com", string(rune('a'+i)), now, now.Add(30*time.Second))
			if err != nil {
				t.Error(err)
			}
			if ok {
				won.Add(1)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d owners got the lock, want 1", won.Load())
	}

	// Held: not for others, not released by them; free again once released.
	if ok, _ := s.AcquireLoginLock(ctx, "account:a@example.com", "late", now.Add(time.Second), now.Add(31*time.Second)); ok {
		t.Error("a held lock was acquired")
	}
	_ = s.ReleaseLoginLock(ctx, "account:a@example.com", "late")
	if ok, _ := s.AcquireLoginLock(ctx, "account:a@example.com", "late", now.Add(2*time.Second), now.Add(32*time.Second)); ok {
		t.Error("a lock was released by a different owner")
	}
	// Its lease ends: the lock of a dead process stops blocking.
	if ok, err := s.AcquireLoginLock(ctx, "account:a@example.com", "late", now.Add(31*time.Second), now.Add(61*time.Second)); err != nil || !ok {
		t.Errorf("an expired lock wasn't acquired: %v %v", ok, err)
	}
	if err := s.ReleaseLoginLock(ctx, "account:a@example.com", "late"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AcquireLoginLock(ctx, "account:a@example.com", "next", now.Add(32*time.Second), now.Add(62*time.Second)); !ok {
		t.Error("a released lock wasn't free")
	}
	// The lock isn't a failure counter: it doesn't show in the attempts.
	if a, err := s.LoginAttempts(ctx, "account:a@example.com"); err != nil || a.Failures != 0 {
		t.Errorf("attempts = %+v, %v", a, err)
	}
}
