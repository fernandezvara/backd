// Package changefeed reads a database's change history for the two features built on it:
// triggers (a dispatcher with a durable position) and realtime (consumers that open when a
// client subscribes). It is driver-neutral: a Source (internal/mongodb) opens the streams;
// this package owns what is shared — the vocabulary of a change, the watched set, resuming
// after errors, history that is gone, and the lazy open/close of a consumer.
//
// The design is design/triggers-and-realtime.md §3 and §4.
package changefeed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// Op is what happened to a document, in the vocabulary of both features.
type Op string

const (
	Created Op = "created" // an insert
	Updated Op = "updated" // an update or a replace
	Removed Op = "removed" // a delete
)

// Namespace is a collection of a realm's databases.
type Namespace struct {
	Realm string
	DB    string // the MongoDB database
	Coll  string
}

func (n Namespace) key() string { return n.DB + "\x00" + n.Coll }

// WatchSet is what a consumer reads.
type WatchSet struct {
	// Collections are application collections, read with their pre- and post-change images.
	Collections []Namespace
	// System are backd's own collections. They are projected where the data is read, down to
	// document keys and the names of the fields that changed — never values — so secrets
	// never leave the database.
	System []Namespace
}

// Empty reports whether nothing is watched.
func (w WatchSet) Empty() bool { return len(w.Collections) == 0 && len(w.System) == 0 }

// Equal reports whether both sets watch the same namespaces, whatever their order.
func (w WatchSet) Equal(o WatchSet) bool {
	return sameNamespaces(w.Collections, o.Collections) && sameNamespaces(w.System, o.System)
}

// Count is how many namespaces the set watches.
func (w WatchSet) Count() int { return len(w.Collections) + len(w.System) }

func sameNamespaces(a, b []Namespace) bool {
	if len(a) != len(b) {
		return false
	}
	ka, kb := keys(a), keys(b)
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

func keys(ns []Namespace) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.key()
	}
	return out
}

// Position is an opaque place in the change history. A stream resumed at it continues
// exactly after it.
type Position []byte

// Change is one change of one document.
type Change struct {
	Realm  string
	DB     string
	Coll   string
	System bool // a change of a system collection: only Key, Op and Fields are filled
	Op     Op
	Key    string // the document's id
	// At is the cluster time of the change.
	At time.Time
	// Version is the document's `_meta.version` after the change (before it, for a removal);
	// 0 when the images are not available.
	Version int64
	// Before and After are the document before and after, in API shape; nil when the image is
	// not available (a created document has no Before, a removed one has no After; a missing
	// image on an update or removal is the case that must fail closed).
	Before, After map[string]any
	// Fields are the names of the fields an update reported as changed (dot paths); empty for
	// creations, removals and replacements.
	Fields []string
	// Replace is true when the change was a replacement of the whole document.
	Replace bool
}

// Event is what a stream hands back: a Change, or — with Change nil — only the news that the
// position moved with nothing to deliver (a quiet period).
type Event struct {
	Change   *Change
	Position Position
}

// Stream is one open read of the change history.
type Stream interface {
	// Next blocks until there is a change, the position moves, or ctx is done.
	Next(ctx context.Context) (Event, error)
	// Position is where the stream is now.
	Position() Position
	Close()
}

// Source opens streams over a watched set, after a position (nil: from now).
type Source interface {
	Open(ctx context.Context, set WatchSet, after Position) (Stream, error)
}

// ErrHistoryLost means the history no longer reaches the position asked for: events between
// it and now are gone.
var ErrHistoryLost = errors.New("changefeed: the change history no longer reaches the position")

// Gap reports ErrHistoryLost to a handler.
type Gap struct {
	// From is the position that could not be resumed; the consumer continues from now.
	From Position
	At   time.Time
}

// Handler receives what a consumer reads. Calls are sequential; a slow handler slows the
// consumer, so handlers that can't keep up must buffer or drop on their own.
type Handler interface {
	Change(ctx context.Context, c Change, at Position)
	// Idle reports a position moved with nothing delivered.
	Idle(ctx context.Context, at Position)
	Gap(ctx context.Context, g Gap)
}

// Observer receives the reader-level metrics. internal/metrics implements it; every method
// may be called on a nil receiver there.
type Observer interface {
	FeedState(consumer string, open bool, watched int)
	FeedReopened(consumer, reason string)
	FeedEvent(consumer string, op Op, system bool)
	FeedLag(consumer string, lag time.Duration)
}

// Reopen reasons, as the label of the reopen metric.
const (
	ReasonSetChanged = "set_changed"
	ReasonError      = "error"
	ReasonGap        = "history_lost"
)

// describe renders a set for logs: counts and the databases, never documents.
func (w WatchSet) describe() string {
	dbs := map[string]bool{}
	for _, n := range append(slices.Clone(w.Collections), w.System...) {
		dbs[n.DB] = true
	}
	names := make([]string, 0, len(dbs))
	for d := range dbs {
		names = append(names, d)
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}
