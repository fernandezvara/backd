package triggers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/registry"
)

// RescanConfirmOver is how many documents a rescan may read before it asks to be confirmed.
const RescanConfirmOver = 10000

// DocSource reads the documents of a collection by when they were last written.
type DocSource interface {
	// Count says how many documents were updated in [since, until).
	Count(ctx context.Context, mongoDatabase, collection string, since, until time.Time) (int64, error)
	// Updated calls fn for each document updated in [since, until), oldest first, in API shape.
	Updated(ctx context.Context, mongoDatabase, collection string, since, until time.Time, fn func(doc map[string]any) error) error
}

// RescanRequest says what to re-emit.
type RescanRequest struct {
	Realm, Database, Collection string
	// Trigger limits it to one trigger; "" is every one that can be rescanned.
	Trigger      string
	Since, Until time.Time
	// Confirm allows a scan over RescanConfirmOver documents.
	Confirm bool
	// Run identifies this rescan in the job ids, so its jobs don't collapse with the originals.
	Run string
}

// RescanResult is what a rescan did.
type RescanResult struct {
	Scanned    int
	Queued     int
	Duplicates int
	// NotRescannable names the triggers left out and why.
	NotRescannable map[string]string
}

// ErrRescanNeedsConfirm means the range holds more documents than a rescan reads unconfirmed.
type ErrRescanNeedsConfirm struct{ Documents int64 }

func (e *ErrRescanNeedsConfirm) Error() string {
	return fmt.Sprintf("this range holds %d documents (more than %d): a rescan reads them all, and without an index on _meta.updated_at that is a scan of the collection; repeat with --confirm", e.Documents, RescanConfirmOver)
}

// Rescan re-emits a collection's changes as synthetic `updated` events, from `_meta.updated_at`,
// for what the stream could not deliver (a gap). It is not a replay of history: a document that
// changed twice in the range is read once, as it is now; deletions can't be found; and a
// trigger whose condition needs `before` or `changed()` can't be evaluated from a document
// alone, so it is listed as not rescannable.
func Rescan(ctx context.Context, reg *registry.Registry, req RescanRequest, src DocSource, q Queue, config func(context.Context, string) map[string]any) (RescanResult, error) {
	res := RescanResult{NotRescannable: map[string]string{}}
	c, ok := reg.Collection(req.Realm, req.Database, req.Collection)
	if !ok {
		return res, fmt.Errorf("no collection %s/%s/%s", req.Realm, req.Database, req.Collection)
	}
	var triggers []*registry.Trigger
	for _, t := range c.Triggers {
		if req.Trigger != "" && t.Name != req.Trigger {
			continue
		}
		switch {
		case !t.FiresOn(registry.TriggerUpdated):
			res.NotRescannable[t.Name] = "it does not listen to `updated`: a rescan can only re-emit documents as updated"
		case t.When != nil && (t.When.References("before") || t.When.References("changed")):
			res.NotRescannable[t.Name] = "its condition needs `before` or `changed()`, which a document alone can't give"
		default:
			triggers = append(triggers, t)
		}
	}
	if req.Trigger != "" && len(c.Triggers) > 0 && !slices.ContainsFunc(c.Triggers, func(t *registry.Trigger) bool { return t.Name == req.Trigger }) {
		return res, fmt.Errorf("collection %s/%s/%s has no trigger %q", req.Realm, req.Database, req.Collection, req.Trigger)
	}
	if len(triggers) == 0 {
		return res, nil
	}
	n, err := src.Count(ctx, c.MongoDatabase, c.Name, req.Since, req.Until)
	if err != nil {
		return res, err
	}
	if n > RescanConfirmOver && !req.Confirm {
		return res, &ErrRescanNeedsConfirm{Documents: n}
	}
	ix := buildIndex(reg)
	ct := ix.byNS[[2]string{c.MongoDatabase, c.Name}]
	if ct == nil {
		return res, nil
	}
	ct = &collectionTriggers{realm: ct.realm, database: ct.database, collection: ct.collection, triggers: triggers, timeouts: ct.timeouts, functions: ct.functions}

	var batch []Job
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		created, err := q.Enqueue(ctx, req.Realm, batch)
		if err != nil {
			return err
		}
		res.Queued += created
		res.Duplicates += len(batch) - created
		batch = batch[:0]
		return nil
	}
	err = src.Updated(ctx, c.MongoDatabase, c.Name, req.Since, req.Until, func(doc map[string]any) error {
		res.Scanned++
		key, _ := doc["id"].(string)
		meta, _ := doc["_meta"].(map[string]any)
		at := time.Time{}
		switch v := meta["updated_at"].(type) {
		case time.Time:
			at = v
		}
		var version int64
		switch v := meta["version"].(type) {
		case int64:
			version = v
		case int32:
			version = int64(v)
		}
		ch := changefeed.Change{Realm: req.Realm, DB: c.MongoDatabase, Coll: c.Name, Op: changefeed.Updated, Key: key, At: at, Version: version, After: doc, Replace: true}
		for _, ev := range ct.evaluate(ctx, ch, config) {
			if ev.outcome != outcomeMatched {
				continue
			}
			job := ev.job
			// Its own id, so it doesn't collapse with the delivery it stands in for; the input
			// says it is a rescan.
			job.ID = JobID(job.EventID+"|rescan|"+req.Run, ev.trigger.Name)
			job.Input = withRescan(job.Input, req.Run)
			batch = append(batch, *job)
			if len(batch) >= 200 {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if err := flush(); err != nil && !errors.Is(err, context.Canceled) {
		return res, err
	}
	return res, nil
}
