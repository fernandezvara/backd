package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.SchemaMigrator = (*Repository)(nil)

// belowVersion selects the documents written under a schema version below current: a missing
// stamp is null, which the index on _meta.schema_version holds for them, and means version 1.
func belowVersion(current int) bson.D {
	versions := bson.A{nil}
	for v := 1; v < current; v++ {
		versions = append(versions, int64(v))
	}
	return bson.D{{Key: "_meta.schema_version", Value: bson.D{{Key: "$in", Value: versions}}}}
}

// CountBelow implements storage.SchemaMigrator.
func (r *Repository) CountBelow(ctx context.Context, current int) (int64, error) {
	if current <= 1 { // nothing is below version 1: a missing stamp is version 1
		return 0, nil
	}
	n, err := r.coll.CountDocuments(ctx, belowVersion(current))
	return n, mapError(err)
}

// ScanBelow implements storage.SchemaMigrator.
func (r *Repository) ScanBelow(ctx context.Context, current int, afterID string, limit int) ([]storage.Document, error) {
	if current <= 1 {
		return nil, nil
	}
	return r.scanWhere(ctx, belowVersion(current), afterID, limit)
}
