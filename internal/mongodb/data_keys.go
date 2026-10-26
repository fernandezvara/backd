package mongodb

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
)

type dataKeyDoc struct {
	ID          string    `bson:"_id"`
	Database    string    `bson:"database"`
	Collection  string    `bson:"collection"`
	Path        string    `bson:"path"`
	Kind        string    `bson:"kind"`
	Generation  int       `bson:"generation"`
	Key         string    `bson:"key"`
	Wrapped     []byte    `bson:"wrapped"`
	WrapVersion int       `bson:"wrap_version"`
	Status      string    `bson:"status"`
	CreatedAt   time.Time `bson:"created_at"`
}

func (d dataKeyDoc) record() fieldcrypt.Record {
	return fieldcrypt.Record{
		ID: d.ID, Database: d.Database, Collection: d.Collection, Path: d.Path, Kind: d.Kind, Generation: d.Generation,
		Key: d.Key, Wrapped: d.Wrapped, WrapVersion: d.WrapVersion, Status: d.Status, CreatedAt: d.CreatedAt.UTC(),
	}
}

func (s *AuthStore) dataKeys() *mongo.Collection { return s.db.Collection(DataKeysCollection) }

var _ fieldcrypt.Store = (*AuthStore)(nil)

// ActiveDataKey implements fieldcrypt.Store: the newest active generation of a field.
func (s *AuthStore) ActiveDataKey(ctx context.Context, database, collection, path, kind string) (fieldcrypt.Record, bool, error) {
	var d dataKeyDoc
	err := s.dataKeys().FindOne(ctx, bson.D{
		{Key: "database", Value: database}, {Key: "collection", Value: collection}, {Key: "path", Value: path},
		{Key: "kind", Value: kind}, {Key: "status", Value: fieldcrypt.StatusActive},
	}, options.FindOne().SetSort(bson.D{{Key: "generation", Value: -1}})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return fieldcrypt.Record{}, false, nil
	}
	if err != nil {
		return fieldcrypt.Record{}, false, err
	}
	return d.record(), true, nil
}

// DataKeyByID implements fieldcrypt.Store.
func (s *AuthStore) DataKeyByID(ctx context.Context, id string) (fieldcrypt.Record, bool, error) {
	var d dataKeyDoc
	err := s.dataKeys().FindOne(ctx, byID(id)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return fieldcrypt.Record{}, false, nil
	}
	if err != nil {
		return fieldcrypt.Record{}, false, err
	}
	return d.record(), true, nil
}

// CreateDataKey implements fieldcrypt.Store: the unique index on the field and generation makes
// the second of two racing creators lose.
func (s *AuthStore) CreateDataKey(ctx context.Context, r fieldcrypt.Record) (bool, error) {
	_, err := s.dataKeys().InsertOne(ctx, dataKeyDoc{
		ID: r.ID, Database: r.Database, Collection: r.Collection, Path: r.Path, Kind: r.Kind, Generation: r.Generation,
		Key: r.Key, Wrapped: r.Wrapped, WrapVersion: r.WrapVersion, Status: r.Status, CreatedAt: r.CreatedAt,
	})
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	return err == nil, err
}

// DataKeysToRewrap implements fieldcrypt.Store.
func (s *AuthStore) DataKeysToRewrap(ctx context.Context, key string, belowVersion int, afterID string, limit int) ([]fieldcrypt.Record, error) {
	filter := bson.D{{Key: "key", Value: key}, {Key: "wrap_version", Value: bson.D{{Key: "$lt", Value: belowVersion}}}}
	if afterID != "" {
		filter = append(filter, bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}})
	}
	docs, err := findAll[dataKeyDoc](ctx, s.dataKeys(), filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	return mapAll(docs, dataKeyDoc.record), nil
}

// RewrapDataKey implements fieldcrypt.Store: it only replaces a wrapping that still has the
// version the caller read.
func (s *AuthStore) RewrapDataKey(ctx context.Context, id string, wrapped []byte, from, to int) (bool, error) {
	res, err := s.dataKeys().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "wrap_version", Value: from}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "wrapped", Value: wrapped}, {Key: "wrap_version", Value: to}}}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// DataKeysByKey implements fieldcrypt.Store.
func (s *AuthStore) DataKeysByKey(ctx context.Context, key string) (int64, error) {
	return s.dataKeys().CountDocuments(ctx, bson.D{{Key: "key", Value: key}, {Key: "status", Value: bson.D{{Key: "$ne", Value: fieldcrypt.StatusRetired}}}})
}

// DataKeysOf implements fieldcrypt.Store.
func (s *AuthStore) DataKeysOf(ctx context.Context, database, collection, path string) ([]fieldcrypt.Record, error) {
	docs, err := findAll[dataKeyDoc](ctx, s.dataKeys(), bson.D{{Key: "database", Value: database}, {Key: "collection", Value: collection}, {Key: "path", Value: path}},
		options.Find().SetSort(bson.D{{Key: "kind", Value: 1}, {Key: "generation", Value: 1}}))
	if err != nil {
		return nil, err
	}
	return mapAll(docs, dataKeyDoc.record), nil
}

// DataKeyPaths implements fieldcrypt.Store.
func (s *AuthStore) DataKeyPaths(ctx context.Context, database, collection string) ([]string, error) {
	vals, err := s.dataKeys().Distinct(ctx, "path", bson.D{{Key: "database", Value: database}, {Key: "collection", Value: collection}, {Key: "kind", Value: fieldcrypt.KindData}}).Raw()
	if err != nil {
		return nil, err
	}
	elems, err := vals.Values()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		if s, ok := e.StringValueOK(); ok {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, nil
}

// SetDataKeyStatus implements fieldcrypt.Store.
func (s *AuthStore) SetDataKeyStatus(ctx context.Context, id, status string) error {
	_, err := s.dataKeys().UpdateOne(ctx, byID(id), bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: status}}}})
	return err
}
