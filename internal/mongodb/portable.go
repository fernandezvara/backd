package mongodb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/exportpkg"
)

var _ auth.PortableStore = (*AuthStore)(nil)

func (s *AuthStore) portableCollection(name string) (*mongo.Collection, error) {
	if !slices.Contains(auth.PortableCollections, name) {
		return nil, fmt.Errorf("%q is not a portable collection", name)
	}
	return s.db.Collection(name), nil
}

// ExportSystem implements auth.PortableStore.
func (s *AuthStore) ExportSystem(ctx context.Context, collection, afterID string, limit int) ([]map[string]any, error) {
	coll, err := s.portableCollection(collection)
	if err != nil {
		return nil, err
	}
	filter := bson.D{}
	if afterID != "" {
		filter = bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}}}
	}
	cur, err := coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, mapError(err)
	}
	var raw []bson.D
	if err := cur.All(ctx, &raw); err != nil {
		return nil, mapError(err)
	}
	out := make([]map[string]any, len(raw))
	for i, d := range raw {
		out[i] = portableFromBSON(d)
	}
	return out, nil
}

// portableFromBSON is fromBSON for system documents: "_id" becomes "id", binary values stay
// bytes, and nothing is dropped.
func portableFromBSON(d bson.D) map[string]any {
	out := make(map[string]any, len(d))
	for _, e := range d {
		key := e.Key
		if key == "_id" {
			key = "id"
		}
		out[key] = portableValue(e.Value)
	}
	return out
}

func portableValue(v any) any {
	switch t := v.(type) {
	case bson.Binary:
		return t.Data
	case bson.D:
		m := make(map[string]any, len(t))
		for _, e := range t {
			m[e.Key] = portableValue(e.Value)
		}
		return m
	case bson.A:
		a := make([]any, len(t))
		for i, e := range t {
			a[i] = portableValue(e)
		}
		return a
	}
	return fromBSONValue(v)
}

// GetSystem implements auth.PortableStore.
func (s *AuthStore) GetSystem(ctx context.Context, collection, id string) (map[string]any, bool, error) {
	coll, err := s.portableCollection(collection)
	if err != nil {
		return nil, false, err
	}
	var d bson.D
	switch err := coll.FindOne(ctx, byID(id)).Decode(&d); {
	case errors.Is(err, mongo.ErrNoDocuments):
		return nil, false, nil
	case err != nil:
		return nil, false, mapError(err)
	}
	return portableFromBSON(d), true, nil
}

// ImportSystem implements auth.PortableStore.
func (s *AuthStore) ImportSystem(ctx context.Context, collection string, docs []map[string]any, mode string, write bool) ([]auth.PortableResult, error) {
	coll, err := s.portableCollection(collection)
	if err != nil {
		return nil, err
	}
	var unique [][]string
	for _, sc := range systemCollections {
		if sc.name != collection {
			continue
		}
		for _, ix := range sc.indexes {
			if !ix.unique {
				continue
			}
			var keys []string
			for _, k := range ix.keys {
				keys = append(keys, k.Key)
			}
			unique = append(unique, keys)
		}
	}
	results := make([]auth.PortableResult, len(docs))
	for i, d := range docs {
		id, _ := d["id"].(string)
		res := auth.PortableResult{ID: id}
		doc := toBSON(d)
		var have bson.D
		err := coll.FindOne(ctx, byID(id)).Decode(&have)
		switch err {
		case nil:
			x, _ := exportpkg.EncodeDocument(portableFromBSON(have))
			y, _ := exportpkg.EncodeDocument(d)
			switch {
			case bytes.Equal(x, y):
				res.Outcome = auth.PortableUnchanged
			case mode == "skip":
				res.Outcome = auth.PortableSkipped
			case mode == "replace":
				res.Outcome = auth.PortableReplaced
				if write {
					if _, rerr := coll.ReplaceOne(ctx, byID(id), doc); rerr != nil {
						res.Outcome, res.Reason = writeFailure(rerr)
					}
				}
			default:
				res.Outcome, res.Reason = auth.PortableConflict, "a different document with this id exists in the target"
			}
		case mongo.ErrNoDocuments:
			res.Outcome = auth.PortableCreated
			if field := clashingUnique(ctx, coll, unique, doc, id); field != "" {
				res.Outcome, res.Reason = auth.PortableConflict, "a document with this "+field+" exists in the target"
			} else if write {
				if _, ierr := coll.InsertOne(ctx, doc); ierr != nil {
					res.Outcome, res.Reason = writeFailure(ierr)
				}
			}
		default:
			return nil, mapError(err)
		}
		results[i] = res
	}
	return results, nil
}

// writeFailure explains a failed write of one document: a unique value that is taken is a
// conflict, a document the collection's validator refuses is invalid.
func writeFailure(err error) (outcome, reason string) {
	switch {
	case mongo.IsDuplicateKeyError(err):
		return auth.PortableConflict, "a unique value of this document is already taken in the target"
	case strings.Contains(err.Error(), "Document failed validation"):
		return auth.PortableInvalid, "the document doesn't have the shape this version of backd stores"
	}
	return auth.PortableInvalid, err.Error()
}

// clashingUnique returns the fields of the first unique index another document of the
// collection already holds the same values for, or "".
func clashingUnique(ctx context.Context, coll *mongo.Collection, unique [][]string, doc bson.D, id string) string {
	values := map[string]any{}
	for _, e := range doc {
		values[e.Key] = e.Value
	}
	for _, keys := range unique {
		filter := bson.D{{Key: "_id", Value: bson.D{{Key: "$ne", Value: id}}}}
		complete := true
		for _, k := range keys {
			v, ok := values[k]
			if !ok || v == nil {
				complete = false
				break
			}
			filter = append(filter, bson.E{Key: k, Value: v})
		}
		if !complete || len(keys) == 0 || keys[0] == "_id" {
			continue
		}
		if n, err := coll.CountDocuments(ctx, filter, options.Count().SetLimit(1)); err == nil && n > 0 {
			return strings.Join(keys, "+")
		}
	}
	return ""
}
