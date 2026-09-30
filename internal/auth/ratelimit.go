package auth

import (
	"context"
	"time"
)

// RateLimit increments key's counter and returns *ThrottledError if the
// resulting count is over limit within window: shared across every
// backd instance (roadmap F9), unlike concurrency limits (F8), because
// the counter lives in Store, not in process memory. Reuses the same
// "N events per window" primitive as login throttling (Store's
// IncrementCounter), under its own key namespace so the two never
// collide.
func (s *Users) RateLimit(ctx context.Context, key string, limit int, window time.Duration) error {
	now := s.now()
	count, resetAt, err := s.Store.IncrementCounter(ctx, key, now, now.Add(window))
	if err != nil {
		return err
	}
	if count > limit {
		return &ThrottledError{RetryAfter: resetAt.Sub(now)}
	}
	return nil
}
