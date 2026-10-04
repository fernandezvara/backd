package mongodb

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/registry"
)

// existingIndex is an index found in MongoDB.
type existingIndex struct {
	name   string
	keys   string // canonical key list, e.g. "status:1,_meta.created_at:-1"; "" for special indexes
	unique bool
	sparse bool
	ttl    bool // has expireAfterSeconds
}

// indexKeys builds the MongoDB key document of a declared index.
func indexKeys(ix registry.Index) bson.D {
	d := make(bson.D, len(ix.Keys))
	for i, k := range ix.Keys {
		dir := 1
		if k.Desc {
			dir = -1
		}
		d[i] = bson.E{Key: mongoField(k.Field), Value: dir}
	}
	return d
}

func declaredKeys(ix registry.Index) string {
	parts := make([]string, len(ix.Keys))
	for i, e := range indexKeys(ix) {
		parts[i] = fmt.Sprintf("%s:%d", e.Key, e.Value)
	}
	return strings.Join(parts, ",")
}

// canonicalKeys renders a stored key document, or "" when it isn't a plain
// ascending/descending index (text, hashed, 2dsphere, …).
func canonicalKeys(raw bson.Raw) string {
	elems, err := raw.Elements()
	if err != nil {
		return ""
	}
	parts := make([]string, len(elems))
	for i, e := range elems {
		n, ok := e.Value().AsInt64OK()
		if !ok {
			f, isFloat := e.Value().DoubleOK()
			if !isFloat {
				return ""
			}
			n = int64(f)
		}
		if n != 1 && n != -1 {
			return ""
		}
		parts[i] = fmt.Sprintf("%s:%d", e.Key(), n)
	}
	return strings.Join(parts, ",")
}

func listIndexes(ctx context.Context, coll *mongo.Collection) ([]existingIndex, error) {
	specs, err := coll.Indexes().ListSpecifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("list indexes of %s: %w", coll.Name(), err)
	}
	out := make([]existingIndex, 0, len(specs))
	for _, s := range specs {
		out = append(out, existingIndex{
			name:   s.Name,
			keys:   canonicalKeys(s.KeysDocument),
			unique: s.Unique != nil && *s.Unique,
			sparse: s.Sparse != nil && *s.Sparse,
			ttl:    s.ExpireAfterSeconds != nil,
		})
	}
	return out, nil
}

// indexState compares declared and existing indexes.
type indexState struct {
	missing    []registry.Index
	mismatched []string // human-readable differences
	undeclared []existingIndex
}

func compareIndexes(c *registry.Collection, existing []existingIndex) indexState {
	var st indexState
	matched := map[string]bool{}
	for _, ix := range c.Indexes {
		keys := declaredKeys(ix)
		found := false
		for _, e := range existing {
			if e.keys != keys {
				continue
			}
			found = true
			matched[e.name] = true
			if e.unique != ix.Unique {
				st.mismatched = append(st.mismatched, fmt.Sprintf(
					"index %q (%s) has unique=%t, indexes.json declares unique=%t; drop it manually to change it",
					e.name, ix, e.unique, ix.Unique))
			}
			if e.ttl != ix.TTL {
				st.mismatched = append(st.mismatched, fmt.Sprintf(
					"index %q (%s) has ttl=%t, but the collection's soft_delete.retention needs ttl=%t; drop it manually to change it",
					e.name, ix, e.ttl, ix.TTL))
			}
		}
		if !found {
			st.missing = append(st.missing, ix)
		}
	}
	for _, e := range existing {
		if e.name != "_id_" && !matched[e.name] {
			st.undeclared = append(st.undeclared, e)
		}
	}
	return st
}

// applyIndexes creates missing declared indexes. Existing indexes are never
// dropped or modified; a uniqueness mismatch is an error.
func (p *Provisioner) applyIndexes(ctx context.Context, c *registry.Collection, coll *mongo.Collection) error {
	existing, err := listIndexes(ctx, coll)
	if err != nil {
		return err
	}
	st := compareIndexes(c, existing)
	if len(st.mismatched) > 0 {
		return fmt.Errorf("%s.%s: %s", c.MongoDatabase, c.Name, strings.Join(st.mismatched, "; "))
	}
	for _, ix := range st.missing {
		opts := options.Index().SetUnique(ix.Unique)
		if ix.TTL {
			// A document goes when the date in its key (purge_at) has passed.
			opts.SetExpireAfterSeconds(0)
		}
		model := mongo.IndexModel{Keys: indexKeys(ix), Options: opts}
		name, err := coll.Indexes().CreateOne(ctx, model)
		if err != nil {
			return fmt.Errorf("create index (%s) on %s.%s: %w", ix, c.MongoDatabase, c.Name, err)
		}
		p.Log.Info("index created", "database", c.MongoDatabase, "collection", c.Name, "index", name, "fields", ix.String(), "unique", ix.Unique)
	}
	p.warnUndeclaredIndexes(c, st.undeclared)
	return nil
}

// verifyIndexes returns the differences between declared and existing indexes.
func (p *Provisioner) verifyIndexes(ctx context.Context, c *registry.Collection, coll *mongo.Collection) ([]string, error) {
	existing, err := listIndexes(ctx, coll)
	if err != nil {
		return nil, err
	}
	st := compareIndexes(c, existing)
	diffs := st.mismatched
	for _, ix := range st.missing {
		diffs = append(diffs, fmt.Sprintf("index (%s) missing", ix))
	}
	p.warnUndeclaredIndexes(c, st.undeclared)
	return diffs, nil
}

func (p *Provisioner) warnUndeclaredIndexes(c *registry.Collection, undeclared []existingIndex) {
	for _, e := range undeclared {
		if c.SoftDelete != nil && e.unique && slices.ContainsFunc(c.Indexes, func(ix registry.Index) bool {
			return ix.Unique && declaredKeys(ix) == e.keys+","+mongoField(registry.MetaDeletedAt)+":1"
		}) {
			p.Log.Warn("a unique index from before soft_delete also covers deleted documents, so a deleted document's values can't be reused; backd keeps it, drop it once the collection's new unique index exists",
				"database", c.MongoDatabase, "collection", c.Name, "index", e.name)
			continue
		}
		p.Log.Warn("index exists in MongoDB but not in indexes.json; it is kept",
			"database", c.MongoDatabase, "collection", c.Name, "index", e.name)
	}
}
