// Package triggers dispatches the changes of a database to the functions collections declare
// (design/triggers-and-realtime.md §9): one dispatcher per deployment holds a lease, reads the
// change stream from a durable position, evaluates each trigger's condition and queues one
// async job per match. It is driver-neutral: MongoDB lives behind State, Queue and a
// changefeed.Source.
package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// Job is one queued run of a trigger's function.
type Job struct {
	// ID is derived from the change and the trigger, so a replay of the change produces the
	// same job and the queue collapses it.
	ID        string
	Realm     string
	Database  string // the function's database
	Function  string // the function's name
	Input     json.RawMessage
	Origin    string // trigger:<database>/<collection>/<trigger>
	Timeout   time.Duration
	RequestID string
	EventID   string
	// ChainRoot and ChainDepth place the run in a chain of writes caused by trigger runs: its
	// own writes are stamped with them.
	ChainRoot  string
	ChainDepth int
}

// Queue stores jobs for workers to claim.
type Queue interface {
	// Enqueue inserts the jobs of one realm. A job whose ID exists already is a replay and
	// is not an error: created counts the new ones.
	Enqueue(ctx context.Context, realm string, jobs []Job) (created int, err error)
}

// Lease is the right to dispatch. Epoch grows with every acquisition, so a holder that lost
// the lease (a pause, a partition) is fenced: its writes fail on the epoch.
type Lease struct {
	Holder string
	Epoch  int64
}

// State is the dispatcher's durable state: the lease, the position it has reached and the
// gaps it recorded. The position lives with the lease so that only the current holder can
// move it.
type State interface {
	// AcquireLease takes the lease when nobody holds it or the holder's has expired, with
	// the database's clock. ok is false when someone else holds it.
	AcquireLease(ctx context.Context, holder string, ttl time.Duration) (l Lease, ok bool, err error)
	// RenewLease extends it; ok is false when it is no longer this lease.
	RenewLease(ctx context.Context, l Lease, ttl time.Duration) (ok bool, err error)
	// ReleaseLease gives it up so another instance may take it at once.
	ReleaseLease(ctx context.Context, l Lease) error
	// Watermark returns the stored position and when it was stored; found is false before
	// the first.
	Watermark(ctx context.Context) (pos changefeed.Position, at time.Time, found bool, err error)
	// SetWatermark stores the position, only while l is still the lease; ok is false when
	// it is not (the caller must stop).
	SetWatermark(ctx context.Context, l Lease, pos changefeed.Position, at time.Time) (ok bool, err error)
	// RecordGap stores a gap for an operator to see.
	RecordGap(ctx context.Context, g Gap) error
}

// Gap reasons.
const (
	// GapWindow: the dispatcher was away longer than the change history reaches.
	GapWindow = "window"
	// GapPreImage: a change arrived without the image its condition needs.
	GapPreImage = "preimage_missing"
)

// MaxGapKeys is how many document ids one gap record lists.
const MaxGapKeys = 1000

// Gap is a stretch of changes the dispatcher could not turn into jobs.
type Gap struct {
	Realm      string
	Collection string // "" for a window gap: it covers every collection
	Trigger    string // "" when it is not about one trigger
	Reason     string
	From, To   time.Time
	Count      int
	Keys       []string // document ids, at most MaxGapKeys
}

// Observer receives the dispatcher's metrics. internal/metrics implements it.
type Observer interface {
	// Event counts a change a trigger considered, by outcome: matched, no_match, missing_preimage or error.
	Event(realm, collection, trigger, outcome string)
	// Enqueued counts the jobs queued and the replays the queue collapsed.
	Enqueued(realm string, created, duplicates int)
	// Lag is the time from a change to the jobs it made being queued.
	Lag(d time.Duration)
	// Watermark records when the position was last stored.
	Watermark(at time.Time)
	// Lease records whether this instance holds the lease, and its epoch.
	Lease(held bool, epoch int64)
	// Gap counts a gap recorded.
	Gap(realm, reason string)
}

// EventID identifies one change of one document, the same wherever and however often it is
// read. It does not use the resume token (token bytes are not a stable identity) and does
// not rely on (id, version) alone: a document deleted and created again with the same id
// repeats its first version.
func EventID(c changefeed.Change) string {
	h := sha256.New()
	for _, part := range []string{
		c.DB, c.Coll, c.Key, string(c.Op), strconv.FormatBool(c.Replace),
		strconv.FormatInt(c.At.Unix(), 10), strconv.FormatUint(uint64(c.Increment), 10), strconv.FormatInt(c.Version, 10),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// JobID is the job a trigger makes of an event. One change can match several triggers, so
// the event's id alone would collapse their jobs; the trigger's name is part of it. The id
// has the shape of any job id, and is the unique key that makes a replay a no-op.
func JobID(eventID, trigger string) string {
	sum := sha256.Sum256([]byte(eventID + "\x00" + trigger))
	id, err := xid.FromBytes(sum[:12])
	if err != nil {
		// 12 bytes always fit.
		return hex.EncodeToString(sum[:12])
	}
	return id.String()
}
