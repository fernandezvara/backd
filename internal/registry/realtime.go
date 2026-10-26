package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fernandezvara/backd/internal/rules"
	"github.com/fernandezvara/backd/internal/storage"
)

// loadRealtime reads collection.yaml's `realtime:` into c: true marks the collection as one
// whose changes may be streamed to clients (design/triggers-and-realtime.md §17). Anything but
// a boolean is an error, so a typo is never a silently dead opt-in.
func loadRealtime(c *Collection, path string, data []byte) error {
	if data == nil {
		return nil
	}
	var doc struct {
		Realtime any `yaml:"realtime"`
	}
	if ok, err := decodeSection(data, path, &doc); !ok || err != nil {
		return err
	}
	switch v := doc.Realtime.(type) {
	case nil:
	case bool:
		c.Realtime = v
	default:
		return fmt.Errorf("%s: realtime must be true or false, got %v", path, v)
	}
	return nil
}

// NeedsEvents reports whether the collection's changes are read from the change stream: it is
// realtime, or it has triggers. Such a collection needs pre-change images.
func (c *Collection) NeedsEvents() bool { return c.Realtime || len(c.Triggers) > 0 }

// EventCollection names a collection whose changes the change feed watches.
type EventCollection struct {
	Realm         string
	Database      string
	MongoDatabase string
	Name          string
	Realtime      bool
	Triggers      int // how many triggers it declares
}

// EventCollections lists every collection of every realm that needs the change feed, in a
// stable order.
func (r *Registry) EventCollections() []EventCollection {
	var out []EventCollection
	for _, realm := range r.Realms {
		for _, db := range realm.Databases {
			for _, c := range db.Collections {
				if !c.NeedsEvents() {
					continue
				}
				out = append(out, EventCollection{
					Realm: realm.Name, Database: db.Name, MongoDatabase: db.MongoName, Name: c.Name, Realtime: c.Realtime, Triggers: len(c.Triggers),
				})
			}
		}
	}
	slices.SortFunc(out, func(a, b EventCollection) int {
		return strings.Compare(a.MongoDatabase+"/"+a.Name, b.MongoDatabase+"/"+b.Name)
	})
	return out
}

// RealtimeSettings are a realm's own lower limits for streams; zero means the instance's.
type RealtimeSettings struct {
	MaxSubscriptionsPerStream int
	MaxStreamsPerUser         int
}

// visibilityKey summarizes what decides who a realtime stream shows a document to: the
// opt-in, the read rule and soft deletion. A subscription made under another key is stale.
func (c *Collection) visibilityKey() string {
	src := ""
	if rule := c.Rules.For(rules.Read); rule != nil {
		src = rule.Source
	}
	return fmt.Sprintf("%t|%t|%s", c.Realtime, c.SoftDelete != nil, src)
}

// RealtimeChanged lists the realtime collections of old (as "<mongo database>\x00<name>")
// whose subscriptions no longer follow the configuration in next: they lost the opt-in, were
// removed, or what shows their documents changed.
func RealtimeChanged(old, next *Registry) map[string]bool {
	out := map[string]bool{}
	for _, ec := range old.EventCollections() {
		if !ec.Realtime {
			continue
		}
		oc, _ := old.Collection(ec.Realm, ec.Database, ec.Name)
		nc, ok := next.Collection(ec.Realm, ec.Database, ec.Name)
		if !ok || oc == nil || nc.visibilityKey() != oc.visibilityKey() {
			out[ec.MongoDatabase+"\x00"+ec.Name] = true
		}
	}
	return out
}

// checkRealtimeRead refuses a realtime collection whose read rule can't be decided in memory.
// A stream evaluates the rule per subscriber, per change, with storage.Match; an operator it
// doesn't implement would be guessed. The rule is translated for a few probe callers (none,
// and one with every role), and the result must stay inside what Match evaluates.
func checkRealtimeRead(c *Collection, path string) error {
	rule := c.Rules.For(rules.Read)
	if rule == nil {
		return nil
	}
	for _, u := range []*rules.User{nil, {ID: "probe", Roles: []string{"probe"}}} {
		f, err := rule.Filter(rules.Values{User: u})
		if err != nil {
			continue // a value the probe lacks: decided per request, like any rule
		}
		if err := storage.MatchSupported(f); err != nil {
			return fmt.Errorf("%s: realtime: true needs a read rule that can be evaluated per change: %v", path, err)
		}
	}
	return nil
}
