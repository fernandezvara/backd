package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/fernandezvara/backd/internal/rules"
)

// loadRealtime reads collection.yaml's `realtime:` into c: true marks the collection as one
// whose changes may be streamed to clients (design/triggers-and-realtime.md §17). Anything but
// a boolean is an error, so a typo is never a silently dead opt-in.
func loadRealtime(c *Collection, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var doc struct {
		Realtime any `yaml:"realtime"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		// Another section's mistake is reported by the loader that reads it.
		return nil
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
