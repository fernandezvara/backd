package mongodb

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/docutil"
	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.Migrator = (*Repository)(nil)

// migratePaths lists the fields a migration looks at: the configured ones it was asked for and
// the ones that are no longer encrypted.
func (r *Repository) migratePaths(ctx context.Context, o storage.MigrateOptions) (configured []string, removed []string, err error) {
	for _, ef := range r.c.Encrypted {
		if len(o.Fields) == 0 || slices.Contains(o.Fields, ef.Path) {
			configured = append(configured, ef.Path)
		}
	}
	if r.ring != nil {
		removed, err = r.ring.HistoricPaths(ctx, r.c)
	}
	return configured, removed, err
}

// rawMap reads a hidden map (_enc, _bidx) of a stored document; binary values stay bytes.
func rawMap(d bson.D, root string) map[string]any {
	out := map[string]any{}
	for _, e := range d {
		if e.Key != root {
			continue
		}
		sub, _ := e.Value.(bson.D)
		for _, x := range sub {
			if b, ok := x.Value.(bson.Binary); ok {
				out[x.Key] = b.Data
			} else {
				out[x.Key] = fromBSONValue(x.Value)
			}
		}
	}
	return out
}

func versionOfRaw(d bson.D) int64 {
	for _, e := range d {
		if e.Key != "_meta" {
			continue
		}
		if meta, ok := fromBSONValue(e.Value).(map[string]any); ok {
			v, _ := meta["version"].(int64)
			return v
		}
	}
	return 0
}

// MigrateBatch implements storage.Migrator.
func (r *Repository) MigrateBatch(ctx context.Context, afterID string, limit int, o storage.MigrateOptions) (storage.MigrateResult, error) {
	var res storage.MigrateResult
	if r.ring == nil {
		return res, fmt.Errorf("%w: this instance has no field encryption for the realm", fieldcrypt.ErrUnavailable)
	}
	configured, removed, err := r.migratePaths(ctx, o)
	if err != nil {
		return res, err
	}
	active := map[string]string{}
	for _, p := range configured {
		if active[p], err = r.ring.ActiveID(ctx, r.c, p); err != nil {
			return res, err
		}
	}
	filter := bson.D{}
	if afterID != "" {
		filter = bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}}}
	}
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return res, mapError(err)
	}
	var docs []bson.D
	if err := cur.All(ctx, &docs); err != nil {
		return res, mapError(err)
	}
	for _, d := range docs {
		doc := fromBSON(d)
		id, _ := doc["id"].(string)
		res.Last = id
		res.Counts.Scanned++
		counts, update, err := r.migrateDocument(ctx, d, doc, configured, removed, active)
		if err != nil {
			return res, err
		}
		if len(update) == 0 {
			continue
		}
		if o.DryRun {
			res.Counts = res.Counts.Add(counts)
			continue
		}
		v := versionOfRaw(d)
		out, err := r.coll.UpdateOne(ctx, versionFilter(id, &v), update)
		if err != nil {
			return res, mapError(err)
		}
		if out.MatchedCount == 0 {
			// Someone wrote the document meanwhile; their write went through the current configuration.
			res.Counts.Conflicts++
			continue
		}
		res.Counts = res.Counts.Add(counts)
	}
	return res, nil
}

// migrateDocument works out what one stored document needs: the update that makes it follow the
// configuration, empty when it already does.
func (r *Repository) migrateDocument(ctx context.Context, raw bson.D, doc storage.Document, configured, removed []string, active map[string]string) (c storage.MigrateCounts, update bson.D, err error) {
	id, _ := doc["id"].(string)
	enc, bidx := rawMap(raw, registry.EncRoot), rawMap(raw, registry.BlindRoot)
	set := bson.D{}
	changedEnc, changedBidx := false, false
	value := func(p string) (v any, sealedWith string, present bool, err error) {
		x, ok := docutil.GetPath(doc, p)
		if !ok {
			return nil, "", false, nil
		}
		ct, sealed := x.(fieldcrypt.Ciphertext)
		if !sealed {
			return x, "", true, nil
		}
		kid, err := fieldcrypt.ParseID(ct)
		if err != nil {
			return nil, "", true, err
		}
		plain, err := r.ring.OpenAt(ctx, r.c, p, id, ct)
		return plain, fmt.Sprintf("%x", kid), true, err
	}
	for _, p := range removed {
		v, sealedWith, present, err := value(p)
		if err != nil {
			return c, nil, err
		}
		if present && sealedWith != "" {
			set = append(set, bson.E{Key: p, Value: toBSONValue(v)})
			c.Decrypted++
		}
		if _, has := enc[registry.MarkerField(p)]; has {
			delete(enc, registry.MarkerField(p))
			changedEnc = true
		}
		if _, has := bidx[registry.MarkerField(p)]; has {
			delete(bidx, registry.MarkerField(p))
			changedBidx = true
		}
	}
	for _, p := range configured {
		v, sealedWith, present, err := value(p)
		if err != nil {
			return c, nil, err
		}
		if !present {
			continue
		}
		k := registry.MarkerField(p)
		switch {
		case sealedWith == "" || sealedWith != active[p]:
			ct, kid, err := r.ring.SealAt(ctx, r.c, p, id, v)
			if err != nil {
				return c, nil, err
			}
			set = append(set, bson.E{Key: p, Value: toBSONValue(ct)})
			enc[k], changedEnc = kid, true
			if sealedWith == "" {
				c.Encrypted++
			} else {
				c.Reencrypted++
			}
		case enc[k] != sealedWith:
			enc[k], changedEnc = sealedWith, true
			c.Marked++
		}
		if ef, _ := r.c.EncryptedAt(p); ef.Search != "" {
			want, err := r.ring.Blind(ctx, r.c, p, v)
			if err != nil {
				return c, nil, err
			}
			if have, _ := bidx[k].([]byte); !bytes.Equal(have, want) {
				bidx[k], changedBidx = want, true
				c.Indexed++
			}
		}
	}
	// The hidden maps are set whole, or removed when nothing is left, so a strict validator that
	// no longer knows them (a collection that stopped encrypting) never sees an empty one.
	for _, m := range []struct {
		root    string
		values  map[string]any
		changed bool
	}{{registry.EncRoot, enc, changedEnc}, {registry.BlindRoot, bidx, changedBidx}} {
		if m.changed {
			if len(m.values) == 0 {
				continue
			}
			set = append(set, bson.E{Key: m.root, Value: toBSONValue(m.values)})
		}
	}
	unset := bson.D{}
	if changedEnc && len(enc) == 0 {
		unset = append(unset, bson.E{Key: registry.EncRoot, Value: ""})
	}
	if changedBidx && len(bidx) == 0 {
		unset = append(unset, bson.E{Key: registry.BlindRoot, Value: ""})
	}
	if len(set) == 0 && len(unset) == 0 {
		return c, nil, nil
	}
	if len(set) > 0 {
		update = append(update, bson.E{Key: "$set", Value: set})
	}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	return c, update, nil
}

// ---- status ----

// EncryptionStatus implements storage.Migrator.
func (r *Repository) EncryptionStatus(ctx context.Context) ([]storage.FieldStatus, error) {
	paths := make([]string, 0, len(r.c.Encrypted))
	for _, ef := range r.c.Encrypted {
		paths = append(paths, ef.Path)
	}
	if r.ring != nil {
		removed, err := r.ring.HistoricPaths(ctx, r.c)
		if err != nil {
			return nil, err
		}
		paths = append(paths, removed...)
	}
	count := func(f bson.D) (int64, error) {
		n, err := r.coll.CountDocuments(ctx, f)
		return n, mapError(err)
	}
	var out []storage.FieldStatus
	for _, p := range paths {
		ef, configured := r.c.EncryptedAt(p)
		st := storage.FieldStatus{Path: p, Configured: configured, Search: ef.Search != ""}
		var err error
		if configured && r.ring != nil {
			if st.ActiveID, err = r.ring.ActiveID(ctx, r.c, p); err != nil {
				return nil, err
			}
		}
		if st.Plaintext, err = count(bson.D{{Key: p, Value: bson.D{{Key: "$exists", Value: true}, {Key: "$not", Value: bson.D{{Key: "$type", Value: "binData"}}}}}}); err != nil {
			return nil, err
		}
		if st.Encrypted, err = count(bson.D{{Key: p, Value: bson.D{{Key: "$type", Value: "binData"}}}}); err != nil {
			return nil, err
		}
		if st.ActiveID != "" {
			if st.Current, err = r.CountSealedWith(ctx, p, st.ActiveID); err != nil {
				return nil, err
			}
		}
		st.Old = st.Encrypted - st.Current
		if st.Search {
			if st.Indexed, err = count(bson.D{{Key: registry.BlindPrefix + registry.MarkerField(p), Value: bson.D{{Key: "$type", Value: "binData"}}}}); err != nil {
				return nil, err
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// CountNotCurrent implements storage.Migrator.
func (r *Repository) CountNotCurrent(ctx context.Context, path string) (int64, error) {
	filter := bson.D{{Key: path, Value: bson.D{{Key: "$type", Value: "binData"}}}}
	if _, configured := r.c.EncryptedAt(path); configured && r.ring != nil {
		active, err := r.ring.ActiveID(ctx, r.c, path)
		if err != nil {
			return 0, err
		}
		filter = append(filter, bson.E{Key: registry.EncRoot + "." + registry.MarkerField(path), Value: bson.D{{Key: "$ne", Value: active}}})
	}
	n, err := r.coll.CountDocuments(ctx, filter)
	return n, mapError(err)
}

// CountSealedWith implements storage.Migrator.
func (r *Repository) CountSealedWith(ctx context.Context, path, keyID string) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: registry.EncRoot + "." + registry.MarkerField(path), Value: keyID}})
	return n, mapError(err)
}

// Prepare implements storage.Migrator.
func (r *Repository) Prepare(ctx context.Context) error {
	if r.ring == nil {
		return fmt.Errorf("%w: this instance has no field encryption for the realm", fieldcrypt.ErrUnavailable)
	}
	return r.ring.Ensure(ctx, r.c)
}

// Retire implements storage.Migrator: a key is retired when no value is left that is not sealed
// with the active key (or, for a field no longer encrypted, no encrypted value is left at all).
func (r *Repository) Retire(ctx context.Context) ([]string, error) {
	if r.ring == nil {
		return nil, nil
	}
	paths := make([]string, 0, len(r.c.Encrypted))
	for _, ef := range r.c.Encrypted {
		paths = append(paths, ef.Path)
	}
	removed, err := r.ring.HistoricPaths(ctx, r.c)
	if err != nil {
		return nil, err
	}
	paths = append(paths, removed...)
	var all []string
	for _, p := range paths {
		ids, err := r.ring.Retire(ctx, r.c, p, func(string) (int64, error) { return r.CountNotCurrent(ctx, p) })
		if err != nil {
			return all, err
		}
		all = append(all, ids...)
	}
	return all, nil
}

// Generations implements storage.Migrator.
func (r *Repository) Generations(ctx context.Context, path string) ([]storage.KeyGeneration, error) {
	if r.ring == nil {
		return nil, nil
	}
	recs, err := r.ring.Store.DataKeysOf(ctx, r.c.Database, r.c.Name, path)
	if err != nil {
		return nil, err
	}
	out := make([]storage.KeyGeneration, 0, len(recs))
	for _, rec := range recs {
		g := storage.KeyGeneration{ID: rec.ID, Kind: rec.Kind, Generation: rec.Generation, Key: rec.Key, Status: rec.Status, WrapVersion: rec.WrapVersion}
		if rec.Kind == fieldcrypt.KindData {
			if g.Values, err = r.CountSealedWith(ctx, path, rec.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, g)
	}
	return out, nil
}

// Rekey implements storage.Migrator.
func (r *Repository) Rekey(ctx context.Context, path string) (storage.KeyGeneration, error) {
	if r.ring == nil {
		return storage.KeyGeneration{}, fmt.Errorf("%w: this instance has no field encryption for the realm", fieldcrypt.ErrUnavailable)
	}
	rec, err := r.ring.Rekey(ctx, r.c, path)
	if err != nil {
		return storage.KeyGeneration{}, err
	}
	return storage.KeyGeneration{ID: rec.ID, Kind: rec.Kind, Generation: rec.Generation, Key: rec.Key, Status: rec.Status, WrapVersion: rec.WrapVersion}, nil
}
