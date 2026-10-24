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
	if err := r.open(ctx, out...); err != nil {
		return nil, err
	}
	return out, nil
}

var _ storage.Exporter = (*Repository)(nil)

// ScanOwnedAfter reads the documents a user owns, soft-deleted ones included, in id order.
func (r *Repository) ScanOwnedAfter(ctx context.Context, owner, afterID string, limit int) ([]storage.Document, error) {
	return r.scanWhere(ctx, ownedBy(owner), afterID, limit)
}

// ScanReferencingAfter reads the documents where field holds value (an equality on an array
// field matches the documents whose array contains it), in id order.
func (r *Repository) ScanReferencingAfter(ctx context.Context, field, value, afterID string, limit int) ([]storage.Document, error) {
	return r.scanWhere(ctx, bson.D{{Key: mongoField(field), Value: value}}, afterID, limit)
}

func (r *Repository) scanWhere(ctx context.Context, filter bson.D, afterID string, limit int) ([]storage.Document, error) {
	if afterID != "" {
		filter = append(append(bson.D(nil), filter...), bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}})
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
	if err := r.open(ctx, out...); err != nil {
		return nil, err
	}
	return out, nil
}

// EstimatedCount reads the collection's metadata, not its documents.
func (r *Repository) EstimatedCount(ctx context.Context) (int64, error) {
	n, err := r.coll.EstimatedDocumentCount(ctx)
	return n, mapError(err)
}
