package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.Scanner = (*Repository)(nil)

// ScanAfter returns the next documents after afterID, by id.
func (r *Repository) ScanAfter(ctx context.Context, afterID string, limit int) ([]storage.Document, error) {
	filter := bson.D{}
	if afterID != "" {
		filter = bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}}}
	}
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, mapError(err)
	}
	var docs []bson.D
	if err := cur.All(ctx, &docs); err != nil {
		return nil, mapError(err)
	}
	out := make([]storage.Document, len(docs))
	for i, d := range docs {
		out[i] = fromBSON(d)
	}
	return out, nil
}

// EstimatedCount reads the collection's metadata, not its documents.
func (r *Repository) EstimatedCount(ctx context.Context) (int64, error) {
	n, err := r.coll.EstimatedDocumentCount(ctx)
	return n, mapError(err)
}
