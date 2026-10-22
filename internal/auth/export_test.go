package auth

import (
	"context"

	"github.com/fernandezvara/backd/internal/registry"
)

// DisableLocalLock lets the external tests show that the store's lock alone
// serializes the password checks of an account (see noLocalLock).
func (s *Users) DisableLocalLock() { s.noLocalLock = true }

// AccountThreshold is the number of failures after which attempts must wait.
const AccountThreshold = registry.DefaultAccountThreshold

// RefuseExpiredFactor exposes refuseExpiredFactor to the external tests.
func (s *Users) RefuseExpiredFactor(ctx context.Context, u User, factor string) error {
	return s.refuseExpiredFactor(ctx, u, factor)
}
