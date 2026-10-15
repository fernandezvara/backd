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
	text   bool // a text index (its keys are _fts and _ftsx)
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
			text:   hasKey(s.KeysDocument, "_fts"),
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
		// The text index has its own comparison (see applyTextIndex).
		if e.name != "_id_" && !e.text && !matched[e.name] {
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
	return p.applyTextIndex(ctx, c, coll)
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
	textDiffs, err := p.verifyTextIndex(ctx, c, coll)
	return append(diffs, textDiffs...), err
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

func hasKey(raw bson.Raw, key string) bool {
	_, err := raw.LookupErr(key)
	return err == nil
}

// textState is a text index found in MongoDB.
type textState struct {
	name string
	sig  string // canonical form, comparable with declaredTextSig
}

// findTextIndex returns the collection's text index, or nil.
func findTextIndex(ctx context.Context, coll *mongo.Collection) (*textState, error) {
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list indexes of %s: %w", coll.Name(), err)
	}
	var specs []bson.Raw
	if err := cur.All(ctx, &specs); err != nil {
		return nil, fmt.Errorf("list indexes of %s: %w", coll.Name(), err)
	}
	for _, s := range specs {
		key, _ := s.Lookup("key").DocumentOK()
		if !hasKey(key, "_fts") {
			continue
		}
		st := &textState{}
		st.name, _ = s.Lookup("name").StringValueOK()
		var parts []string
		if w, ok := s.Lookup("weights").DocumentOK(); ok {
			elems, _ := w.Elements()
			for _, e := range elems {
				n, _ := e.Value().AsInt64OK()
				parts = append(parts, fmt.Sprintf("%s:%d", e.Key(), n))
			}
		}
		slices.Sort(parts)
		lang, _ := s.Lookup("default_language").StringValueOK()
		override, _ := s.Lookup("language_override").StringValueOK()
		st.sig = fmt.Sprintf("weights=%s;language=%s;override=%s", strings.Join(parts, ","), lang, override)
		return st, nil
	}
	return nil, nil
}

// sortedSig puts the field list of a declared signature in the order the database reports.
func sortedSig(t *registry.TextIndex) string {
	parts := make([]string, len(t.Fields))
	for i, f := range t.Fields {
		parts[i] = fmt.Sprintf("%s:%d", f.Path, f.Weight)
	}
	slices.Sort(parts)
	return fmt.Sprintf("weights=%s;language=%s;override=%s", strings.Join(parts, ","), t.DefaultLanguage, registry.TextSearchField)
}

// applyTextIndex makes the collection's text index what indexes.json declares: it creates a
// missing one and replaces one backd made when the declaration changed, logging the rebuild.
// A text index backd didn't make is never touched: there can be only one, so it is an error.
func (p *Provisioner) applyTextIndex(ctx context.Context, c *registry.Collection, coll *mongo.Collection) error {
	have, err := findTextIndex(ctx, coll)
	if err != nil {
		return err
	}
	if c.Text == nil {
		if have != nil && have.name == registry.IndexTextName {
			p.Log.Warn("a text index exists in MongoDB but indexes.json declares none; it is kept",
				"database", c.MongoDatabase, "collection", c.Name, "index", have.name)
		}
		return nil
	}
	if have != nil && have.name != registry.IndexTextName {
		return fmt.Errorf("%s.%s has a text index named %q that backd didn't create, and a collection can have only one: drop it (db.%s.dropIndex(%q)) so backd can create the one indexes.json declares",
			c.MongoDatabase, c.Name, have.name, c.Name, have.name)
	}
	if have != nil && have.sig == sortedSig(c.Text) {
		return nil
	}
	if have != nil {
		p.Log.Warn("the text index changed: rebuilding it; searches answer 503 index_building until it is built",
			"database", c.MongoDatabase, "collection", c.Name, "index", have.name)
		if err := coll.Indexes().DropOne(ctx, have.name); err != nil {
			return fmt.Errorf("drop the text index of %s.%s: %w", c.MongoDatabase, c.Name, err)
		}
	}
	keys := make(bson.D, len(c.Text.Fields))
	weights := make(bson.D, len(c.Text.Fields))
	for i, f := range c.Text.Fields {
		keys[i] = bson.E{Key: f.Path, Value: "text"}
		weights[i] = bson.E{Key: f.Path, Value: f.Weight}
	}
	opts := options.Index().SetName(registry.IndexTextName).SetWeights(weights).
		SetDefaultLanguage(c.Text.DefaultLanguage).SetLanguageOverride(registry.TextSearchField)
	if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: keys, Options: opts}); err != nil {
		return fmt.Errorf("create the text index of %s.%s: %w", c.MongoDatabase, c.Name, err)
	}
	p.Log.Info("text index created", "database", c.MongoDatabase, "collection", c.Name, "index", registry.IndexTextName, "fields", len(c.Text.Fields), "default_language", c.Text.DefaultLanguage)
	return p.backfillSearchLanguage(ctx, c, coll)
}

// backfillSearchLanguage gives documents written before the text index the stored language
// the index reads, from their language_field.
func (p *Provisioner) backfillSearchLanguage(ctx context.Context, c *registry.Collection, coll *mongo.Collection) error {
	if c.Text.LanguageField == "" {
		return nil
	}
	field := mongoField(c.Text.LanguageField)
	var values []string
	if err := coll.Distinct(ctx, field, bson.D{{Key: registry.TextSearchField, Value: bson.D{{Key: "$exists", Value: false}}}}).Decode(&values); err != nil {
		return fmt.Errorf("read the languages of %s.%s: %w", c.MongoDatabase, c.Name, err)
	}
	for _, v := range values {
		res, err := coll.UpdateMany(ctx,
			bson.D{{Key: field, Value: v}, {Key: registry.TextSearchField, Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "$set", Value: bson.D{{Key: registry.TextSearchField, Value: registry.SearchLanguage(v)}}}})
		if err != nil {
			return fmt.Errorf("set the search language of %s.%s: %w", c.MongoDatabase, c.Name, err)
		}
		p.Log.Info("search language set on existing documents", "database", c.MongoDatabase, "collection", c.Name, "locale", v, "documents", res.ModifiedCount)
	}
	return nil
}

// verifyTextIndex reports how the text index differs from the declaration.
func (p *Provisioner) verifyTextIndex(ctx context.Context, c *registry.Collection, coll *mongo.Collection) ([]string, error) {
	have, err := findTextIndex(ctx, coll)
	if err != nil {
		return nil, err
	}
	switch {
	case c.Text == nil:
		return nil, nil
	case have == nil:
		return []string{"the text index is missing"}, nil
	case have.name != registry.IndexTextName:
		return []string{fmt.Sprintf("the text index %q wasn't made by backd; drop it so backd can create its own", have.name)}, nil
	case have.sig != sortedSig(c.Text):
		return []string{"the text index differs from indexes.json (weights or languages changed); run `backd provision` to rebuild it"}, nil
	}
	return nil, nil
}
