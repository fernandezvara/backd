package auth_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

func TestIPKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":                "ip:203.0.113.9",
		"::ffff:203.0.113.9":         "ip:203.0.113.9",
		"2001:db8:1:2:aaaa::1":       "ip:2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb:cccc::ff": "ip:2001:db8:1:2::/64",
		"":                           "",
		"not-an-ip":                  "",
	} {
		if got := IPKey(in); got != want {
			t.Errorf("IPKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// failLogins makes n failed login attempts that must not be throttled.
func failLogins(t *testing.T, svc *Users, email, ip string, n int) {
	t.Helper()
	for i := range n {
		if _, _, err := svc.Login(context.Background(), email, "dev-p4ssw0rd!0", ip); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
}

func retryAfter(err error) time.Duration {
	var te *ThrottledError
	if errors.As(err, &te) {
		return te.RetryAfter
	}
	return -1
}

func TestLoginThrottlePerAccount(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	clock := start
	svc := newUsers(store, &clock)
	if _, err := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}

	// Four failures are free; the fifth starts the backoff at 1s.
	failLogins(t, svc, "ada@example.com", "203.0.113.1", 5)
	_, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "203.0.113.1")
	if got := retryAfter(err); got != time.Second {
		t.Fatalf("after 5 failures: %v (retry after %v), want a 1s wait", err, got)
	}
	// Blocked attempts aren't counted: waiting is always enough.
	for range 3 {
		_, _, _ = svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!0", "203.0.113.1")
	}

	// The wait applies from any address: it's per account.
	_, _, err = svc.Login(ctx, "ADA@example.com", "dev-p4ssw0rd!", "198.51.100.7")
	if retryAfter(err) != time.Second {
		t.Errorf("from another IP: %v", err)
	}

	// Each further failure doubles the wait.
	for i, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		clock = clock.Add(time.Minute)
		failLogins(t, svc, "ada@example.com", "203.0.113.1", 1)
		if _, _, err := svc.Login(ctx, "ada@example.com", "x", "203.0.113.1"); retryAfter(err) != want {
			t.Errorf("after %d failures: wait %v, want %v", 6+i, retryAfter(err), want)
		}
	}

	// After the wait, the right password works and resets the account counter.
	clock = clock.Add(8 * time.Second)
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "203.0.113.1"); err != nil {
		t.Fatalf("login after waiting: %v", err)
	}
	failLogins(t, svc, "ada@example.com", "203.0.113.1", 4)
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "203.0.113.1"); err != nil {
		t.Errorf("counter not reset by success: %v", err)
	}

	// Unknown emails are throttled the same way, so throttling doesn't
	// reveal which emails are registered.
	failLogins(t, svc, "ghost@example.com", "203.0.113.2", 5)
	if _, _, err := svc.Login(ctx, "ghost@example.com", "x", "203.0.113.2"); retryAfter(err) != time.Second {
		t.Errorf("unknown email: %v", err)
	}

	// The window: failures older than 15 minutes don't count.
	failLogins(t, svc, "bob@example.com", "203.0.113.3", 4)
	clock = clock.Add(16 * time.Minute)
	failLogins(t, svc, "bob@example.com", "203.0.113.3", 4)
	if _, _, err := svc.Login(ctx, "bob@example.com", "x", "203.0.113.3"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old failures still counted: %v", err)
	}
}

func TestLoginThrottleCap(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	for range 30 {
		_, _, err := svc.Login(ctx, "ada@example.com", "x", "")
		if w := retryAfter(err); w > 0 {
			clock = clock.Add(w)
			_, _, err = svc.Login(ctx, "ada@example.com", "x", "")
		}
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if _, _, err := svc.Login(ctx, "ada@example.com", "x", ""); retryAfter(err) != 15*time.Minute {
		t.Errorf("wait after many failures = %v, want the 15m cap", retryAfter(err))
	}
}

func TestLoginThrottlePerIP(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	if _, err := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	// 50 failures spread over many accounts (below each account's threshold).
	for i := range 50 {
		failLogins(t, svc, fmt.Sprintf("user%d@example.com", i), "2001:db8:1:2::1", 1)
	}
	// The whole /64 is blocked, even for a correct password...
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "2001:db8:1:2::99"); retryAfter(err) != time.Second {
		t.Errorf("from the same /64: %v", err)
	}
	// ...but other addresses are not.
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "2001:db8:1:3::1"); err != nil {
		t.Errorf("from another /64: %v", err)
	}
	// A successful login doesn't clear the IP counter.
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", "2001:db8:1:2::99"); retryAfter(err) <= 0 {
		t.Errorf("IP counter cleared by another address's success: %v", err)
	}
}

func TestPasswordChecksThrottled(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	me, _, _ := svc.Signup(ctx, "ada@example.com", "dev-p4ssw0rd!", "")
	for range 5 {
		if err := svc.ChangePassword(ctx, me, "dev-p4ssw0rd!0", "dev-p4ssw0rd!2"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if err := svc.DeleteAccount(ctx, me, "dev-p4ssw0rd!"); retryAfter(err) != time.Second {
		t.Errorf("delete account after 5 wrong passwords: %v", err)
	}
	// The same counter guards login.
	if _, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!", ""); retryAfter(err) != time.Second {
		t.Errorf("login after 5 wrong passwords in a session: %v", err)
	}
}

// Parallel attempts on one account can't slip past the throttle: checks
// for an account run one at a time.
func TestLoginThrottleParallel(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	var wg sync.WaitGroup
	var invalid, throttled atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := svc.Login(ctx, "ada@example.com", "dev-p4ssw0rd!0", "")
			switch {
			case errors.Is(err, ErrInvalidCredentials):
				invalid.Add(1)
			case retryAfter(err) > 0:
				throttled.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if invalid.Load() != 5 || throttled.Load() != 5 {
		t.Errorf("%d checked, %d throttled; want 5 and 5", invalid.Load(), throttled.Load())
	}
}
