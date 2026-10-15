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
// realtime, or (later) it has triggers. Such a collection needs pre-change images.
func (c *Collection) NeedsEvents() bool { return c.Realtime }

// EventCollection names a collection whose changes the change feed watches.
type EventCollection struct {
	Realm         string
	Database      string
	MongoDatabase string
	Name          string
	Realtime      bool
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
					Realm: realm.Name, Database: db.Name, MongoDatabase: db.MongoName, Name: c.Name, Realtime: c.Realtime,
				})
			}
		}
	}
	slices.SortFunc(out, func(a, b EventCollection) int {
		return strings.Compare(a.MongoDatabase+"/"+a.Name, b.MongoDatabase+"/"+b.Name)
	})
	return out
}
