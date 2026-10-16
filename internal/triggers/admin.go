package triggers

import (
	"context"
	"time"
)

// DispatcherStatus is what the deployment's dispatcher state says right now.
type DispatcherStatus struct {
	// Found is false before any instance has ever dispatched.
	Found bool
	// Held is whether some instance holds the lease at the moment.
	Held         bool
	Epoch        int64
	Holder       string
	LeaseExpires time.Time
	// WatermarkAt is when the position was last stored: its age is what to watch.
	WatermarkAt time.Time
}

// GapRecord is a stored gap.
type GapRecord struct {
	Gap
	ID         string
	RecordedAt time.Time
	// ResolvedAt is zero while the gap is open.
	ResolvedAt time.Time
}

// Admin reads and closes what the dispatcher recorded; MongoDB's TriggerState implements it.
type Admin interface {
	Status(ctx context.Context) (DispatcherStatus, error)
	// Gaps lists the gaps of a realm (those of every realm included: a window gap covers all),
	// newest first; resolved ones only when all is true.
	Gaps(ctx context.Context, realm string, all bool, limit int) ([]GapRecord, error)
	// ResolveGap closes an open gap of the realm; ok is false when there is none by that id.
	ResolveGap(ctx context.Context, realm, id string, at time.Time) (ok bool, err error)
}
