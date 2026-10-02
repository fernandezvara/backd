package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/storage"
)

var _ storage.Eraser = (*Repository)(nil)

// ids returns the ids of up to limit documents matching filter.
func (r *Repository) ids(ctx context.Context, filter bson.D, limit int) (bson.A, error) {
	cur, err := r.coll.Find(ctx, filter, options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, mapError(err)
	}
	var docs []bson.D
	if err := cur.All(ctx, &docs); err != nil {
		return nil, mapError(err)
	}
	out := make(bson.A, 0, len(docs))
	for _, d := range docs {
		out = append(out, d[0].Value)
	}
	return out, nil
}

func ownedBy(owner string) bson.D { return bson.D{{Key: "_meta.owner", Value: owner}} }

// systemWrite is the part of an update that records an erase as the writer.
func systemWrite(now time.Time) (set bson.D, inc bson.D) {
	return bson.D{{Key: "_meta.updated_at", Value: bson.NewDateTimeFromTime(now)}, {Key: "_meta.updated_by", Value: storage.ErasedBy}},
		bson.D{{Key: "_meta.version", Value: 1}}
}

func (r *Repository) CountOwned(ctx context.Context, owner string) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, ownedBy(owner))
	return n, mapError(err)
}

func (r *Repository) CountReferences(ctx context.Context, field, value string, array bool) (int64, error) {
	// An equality on an array field matches the documents whose array contains
	// the value, so one query serves both shapes.
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: mongoField(field), Value: value}})
	return n, mapError(err)
}

func (r *Repository) DeleteOwned(ctx context.Context, owner string, limit int) (int64, error) {
	ids, err := r.ids(ctx, ownedBy(owner), limit)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	res, err := r.coll.DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: "_meta.owner", Value: owner}})
	if err != nil {
		return 0, mapError(err)
	}
	return res.DeletedCount, nil
}

func (r *Repository) AnonymizeOwned(ctx context.Context, owner string, remove []string, replace map[string]any, limit int, now time.Time) (int64, error) {
	ids, err := r.ids(ctx, ownedBy(owner), limit)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	set, inc := systemWrite(now)
	set = append(set, bson.E{Key: "_meta.owner", Value: nil})
	for _, f := range sortedKeys(replace) {
		set = append(set, bson.E{Key: mongoField(f), Value: toBSONValue(replace[f])})
	}
	update := bson.D{{Key: "$set", Value: set}, {Key: "$inc", Value: inc}}
	if len(remove) > 0 {
		unset := make(bson.D, len(remove))
		for i, f := range remove {
			unset[i] = bson.E{Key: mongoField(f), Value: ""}
		}
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	res, err := r.coll.UpdateMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: "_meta.owner", Value: owner}}, update)
	if err != nil {
		return 0, mapError(err)
	}
	return res.ModifiedCount, nil
}

func (r *Repository) PullReference(ctx context.Context, field, value string, limit int, now time.Time) (int64, error) {
	f := mongoField(field)
	ids, err := r.ids(ctx, bson.D{{Key: f, Value: value}}, limit)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	set, inc := systemWrite(now)
	res, err := r.coll.UpdateMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: f, Value: value}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: f, Value: value}}}, {Key: "$set", Value: set}, {Key: "$inc", Value: inc}})
	if err != nil {
		return 0, mapError(err)
	}
	return res.ModifiedCount, nil
}

func (r *Repository) ClearReference(ctx context.Context, field, value string, limit int, now time.Time) (int64, error) {
	f := mongoField(field)
	ids, err := r.ids(ctx, bson.D{{Key: f, Value: value}}, limit)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	set, inc := systemWrite(now)
	res, err := r.coll.UpdateMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: f, Value: value}},
		bson.D{{Key: "$unset", Value: bson.D{{Key: f, Value: ""}}}, {Key: "$set", Value: set}, {Key: "$inc", Value: inc}})
	if err != nil {
		return 0, mapError(err)
	}
	return res.ModifiedCount, nil
}
