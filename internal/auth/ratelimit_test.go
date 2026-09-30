package auth_test

import (
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

func TestRateLimit(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	svc := &Users{Store: authtest.NewMemStore(), Now: func() time.Time { return now }}
	ctx := t.Context()

	for i := range 3 {
		if err := svc.RateLimit(ctx, "func:shop/app/send:user:u1", 3, time.Minute); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}
	err := svc.RateLimit(ctx, "func:shop/app/send:user:u1", 3, time.Minute)
	var te *ThrottledError
	if !errors.As(err, &te) {
		t.Fatalf("4th call: err = %v, want *ThrottledError", err)
	}
	if te.RetryAfter <= 0 || te.RetryAfter > time.Minute {
		t.Errorf("RetryAfter = %v, want (0, 1m]", te.RetryAfter)
	}

	// A different key has its own counter.
	if err := svc.RateLimit(ctx, "func:shop/app/send:user:u2", 3, time.Minute); err != nil {
		t.Errorf("different key: %v", err)
	}

	// The window resets after it ends: a steady stream of calls doesn't
	// push the deadline forward forever (unlike login throttling's
	// cleanup deadline — see decisions.md).
	now = now.Add(90 * time.Second)
	if err := svc.RateLimit(ctx, "func:shop/app/send:user:u1", 3, time.Minute); err != nil {
		t.Errorf("after the window reset: %v", err)
	}
}

func TestRateLimitSharedAcrossInstances(t *testing.T) {
	// Two *Users acting as two backd instances, sharing one Store: this
	// is the point of F9 (unlike F8's in-memory, per-instance concurrency
	// limiter).
	store := authtest.NewMemStore()
	now := time.Now()
	a := &Users{Store: store, Now: func() time.Time { return now }}
	b := &Users{Store: store, Now: func() time.Time { return now }}
	ctx := t.Context()

	if err := a.RateLimit(ctx, "func:shop/app/send:ip:203.0.113.1", 2, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := b.RateLimit(ctx, "func:shop/app/send:ip:203.0.113.1", 2, time.Minute); err != nil {
		t.Fatal(err)
	}
	var te *ThrottledError
	if err := a.RateLimit(ctx, "func:shop/app/send:ip:203.0.113.1", 2, time.Minute); !errors.As(err, &te) {
		t.Fatalf("third call (on the first instance): err = %v, want *ThrottledError", err)
	}
}
