package registry

import (
	"fmt"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

// The _meta fields of a soft-deleted document: when it was deleted, by whom,
// and when it goes for good (only with a retention).
const (
	MetaDeletedAt = "_meta.deleted_at"
	MetaDeletedBy = "_meta.deleted_by"
	MetaPurgeAt   = "_meta.purge_at"
)

const minSoftDeleteRetention = time.Hour

// SoftDelete is collection.yaml's soft_delete: DELETE hides a document
// instead of removing it, so it can be restored.
type SoftDelete struct {
	// Retention is how long a deleted document is kept before MongoDB's TTL
	// index removes it; 0 keeps it until it is purged by hand.
	Retention time.Duration
}

// softDeleteDoc is soft_delete in collection.yaml: true, false, or a mapping
// of settings.
type softDeleteDoc struct {
	On        bool
	Retention *string
}

func (d *softDeleteDoc) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if err := n.Decode(&d.On); err != nil {
			return fmt.Errorf("line %d: soft_delete must be true, false or a mapping (retention)", n.Line)
		}
		return nil
	case yaml.MappingNode:
		d.On = true
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch key := n.Content[i].Value; key {
			case "retention":
				var s string
				if err := n.Content[i+1].Decode(&s); err != nil {
					return fmt.Errorf("line %d: soft_delete.retention must be a duration such as 30d", n.Content[i+1].Line)
				}
				d.Retention = &s
			default:
				return fmt.Errorf("line %d: unknown key %q in soft_delete (want retention)", n.Content[i].Line, key)
			}
		}
		return nil
	}
	return fmt.Errorf("line %d: soft_delete must be true, false or a mapping (retention)", n.Line)
}

// settings validates the section; nil means soft delete is off.
func (d *softDeleteDoc) settings(path string) (*SoftDelete, error) {
	if d == nil || !d.On {
		return nil, nil
	}
	sd := &SoftDelete{}
	if d.Retention != nil {
		r, err := ParseDuration(*d.Retention)
		switch {
		case err != nil:
			return nil, fmt.Errorf("%s: soft_delete.retention: %w", path, err)
		case r < minSoftDeleteRetention:
			return nil, fmt.Errorf("%s: soft_delete.retention: must be at least %s, got %s (leave it out to keep deleted documents until they are purged by hand)", path, minSoftDeleteRetention, *d.Retention)
		}
		sd.Retention = r
	}
	return sd, nil
}

// addSoftDeleteIndexes makes the declared indexes fit a collection that
// keeps deleted documents. Every unique index gets the deletion time as its
// last key: live documents (no deleted_at) stay unique among themselves, a
// deleted one never clashes with anything, and a value can be reused as soon
// as its document is deleted. A document stored before soft delete was turned
// on counts as live (a missing field is null in an index), so it works on
// existing collections. A retention adds the TTL index that removes documents
// when their purge_at passes.
func (c *Collection) addSoftDeleteIndexes() {
	if c.SoftDelete == nil {
		return
	}
	for i, ix := range c.Indexes {
		if ix.Unique && !slices.ContainsFunc(ix.Keys, func(k IndexKey) bool { return k.Field == MetaDeletedAt }) {
			c.Indexes[i].Keys = append(slices.Clone(ix.Keys), IndexKey{Field: MetaDeletedAt})
		}
	}
	if c.SoftDelete.Retention > 0 {
		c.Indexes = append(c.Indexes, Index{Keys: []IndexKey{{Field: MetaPurgeAt}}, TTL: true})
	}
}
