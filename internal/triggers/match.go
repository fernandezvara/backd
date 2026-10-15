package triggers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/rules"
)

// WatchSet is what the dispatcher reads: the collections of every realm that declare triggers.
func WatchSet(reg *registry.Registry) changefeed.WatchSet {
	var set changefeed.WatchSet
	for _, c := range reg.EventCollections() {
		if c.Triggers > 0 {
			set.Collections = append(set.Collections, changefeed.Namespace{Realm: c.Realm, DB: c.MongoDatabase, Coll: c.Name})
		}
	}
	return set
}

// index finds a changed collection's triggers by namespace.
type index struct {
	reg  *registry.Registry
	byNS map[[2]string]*collectionTriggers
}

type collectionTriggers struct {
	realm, database, collection string
	triggers                    []*registry.Trigger
	timeouts                    map[string]time.Duration // by trigger name: the function's timeout
	functions                   map[string][2]string     // by trigger name: database, function
}

// empty reports whether no collection declares a trigger.
func (ix *index) empty() bool { return ix == nil || len(ix.byNS) == 0 }

func buildIndex(reg *registry.Registry) *index {
	ix := &index{reg: reg, byNS: map[[2]string]*collectionTriggers{}}
	for _, db := range reg.Databases() {
		for _, c := range db.SortedCollections() {
			if len(c.Triggers) == 0 {
				continue
			}
			ct := &collectionTriggers{
				realm: c.Realm, database: c.Database, collection: c.Name, triggers: c.Triggers,
				timeouts: map[string]time.Duration{}, functions: map[string][2]string{},
			}
			for _, t := range c.Triggers {
				fdb, fn := splitFunction(t.Function)
				ct.functions[t.Name] = [2]string{fdb, fn}
				if rl := reg.Realms[c.Realm]; rl != nil {
					if d := rl.Databases[fdb]; d != nil && d.Functions != nil {
						if f := d.Functions.Functions[fn]; f != nil {
							ct.timeouts[t.Name] = f.Timeout
						}
					}
				}
			}
			ix.byNS[[2]string{c.MongoDatabase, c.Name}] = ct
		}
	}
	return ix
}

func splitFunction(ref string) (db, fn string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:]
		}
	}
	return "", ref
}

// outcome of a trigger considering one change.
const (
	outcomeMatched        = "matched"
	outcomeNoMatch        = "no_match"
	outcomeMissingPreImge = "missing_preimage"
	outcomeError          = "error"
)

// input is the job input of a trigger run (design §10, base envelope). The documents ride
// only when the trigger's `fields` asks, which the execution step adds.
type input struct {
	ID         string   `json:"id"`
	Event      string   `json:"event"`
	Collection string   `json:"collection"`
	Doc        inputDoc `json:"doc"`
	Version    int64    `json:"version"`
	Changed    []string `json:"changed"`
	Depth      int      `json:"depth"`
	Root       string   `json:"root"`
	At         string   `json:"at"`
}

type inputDoc struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// evaluation is what one change produced for one trigger.
type evaluation struct {
	trigger *registry.Trigger
	outcome string
	job     *Job
	err     error
}

// evaluate runs every trigger of the collection against the change. config is the realm's
// app configuration, read only when a condition asks for it.
func (ct *collectionTriggers) evaluate(ctx context.Context, c changefeed.Change, config func(context.Context, string) map[string]any) []evaluation {
	eventID := EventID(c)
	event := string(c.Op)
	var out []evaluation
	var cfg map[string]any
	for _, t := range ct.triggers {
		if !t.FiresOn(event) {
			continue
		}
		ev := evaluation{trigger: t}
		needBefore := t.When != nil && (t.When.References("before") || t.When.References("changed"))
		missing := false
		switch c.Op {
		case changefeed.Created:
			missing = c.After == nil
		case changefeed.Updated:
			missing = c.After == nil || (needBefore && c.Before == nil)
		case changefeed.Removed:
			missing = needBefore && c.Before == nil
		}
		if missing {
			ev.outcome = outcomeMissingPreImge
			out = append(out, ev)
			continue
		}
		if t.When != nil {
			if t.When.UsesConfig() && cfg == nil && config != nil {
				cfg = config(ctx, ct.realm)
			}
			ok, err := t.When.Fires(c.Before, c.After, c.At, cfg)
			if err != nil {
				ev.outcome, ev.err = outcomeError, err
				out = append(out, ev)
				continue
			}
			if !ok {
				ev.outcome = outcomeNoMatch
				out = append(out, ev)
				continue
			}
		}
		ev.outcome = outcomeMatched
		ev.job = ct.job(t, c, eventID)
		out = append(out, ev)
	}
	return out
}

func (ct *collectionTriggers) job(t *registry.Trigger, c changefeed.Change, eventID string) *Job {
	var changed []string
	if c.Op == changefeed.Updated && c.Before != nil && c.After != nil {
		changed = rules.ChangedFields(c.Before, c.After)
	}
	if changed == nil {
		changed = []string{}
	}
	in := input{
		ID: eventID, Event: string(c.Op), Collection: ct.collection,
		Doc: inputDoc{ID: c.Key, Version: c.Version}, Version: c.Version, Changed: changed,
		Depth: 0, Root: eventID, At: c.At.UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(in)
	fn := ct.functions[t.Name]
	return &Job{
		ID: JobID(eventID, t.Name), Realm: ct.realm, Database: fn[0], Function: fn[1],
		Input: raw, Origin: "trigger:" + ct.database + "/" + ct.collection + "/" + t.Name,
		Timeout: ct.timeouts[t.Name], RequestID: eventID, EventID: eventID,
	}
}
