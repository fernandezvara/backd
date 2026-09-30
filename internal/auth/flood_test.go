package auth_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

// TestLoginFloodMemoryBounded fires concurrent logins with production
// hashing parameters (64 MiB each) and checks that the concurrency cap
// keeps memory bounded: queued, not crashed.
func TestLoginFloodMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: hashes with production parameters")
	}
	const logins, capacity = 40, 2
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Hasher = NewHasher(capacity, DefaultArgon2Params)

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()

	var wg sync.WaitGroup
	var ok, busy atomic.Int32
	for i := range logins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Distinct accounts: logins for one account are serialized, and
			// unknown emails still hash (against the dummy hash).
			_, _, err := svc.Login(ctx, fmt.Sprintf("user%d@example.com", i), "dev-p4ssw0rd!", "")
			switch {
			case errors.Is(err, ErrInvalidCredentials):
				ok.Add(1)
			case errors.Is(err, ErrBusy):
				busy.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-sampled

	growth := int64(peak.Load()) - int64(base.HeapInuse)
	const limit = 600 << 20 // uncapped, 40 hashes would need about 2.5 GiB
	t.Logf("%d logins answered, %d busy; peak heap growth %d MiB", ok.Load(), busy.Load(), growth>>20)
	if ok.Load() != logins {
		t.Errorf("%d of %d logins were answered without a deadline", ok.Load(), logins)
	}
	if growth > limit {
		t.Errorf("peak heap growth %d MiB exceeds %d MiB: hashing isn't bounded", growth>>20, limit>>20)
	}
}
