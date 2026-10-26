package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// byID is the filter of the document whose _id is id.
func byID(id any) bson.D {
	return bson.D{{Key: "_id", Value: id}}
}

// findAll runs a find and decodes every document it returns into T. No match is an empty slice.
func findAll[T any](ctx context.Context, c *mongo.Collection, filter any, opts ...options.Lister[options.FindOptions]) ([]T, error) {
	cur, err := c.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	var docs []T
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

// mapAll converts each element of in; the result is never nil.
func mapAll[T, U any](in []T, convert func(T) U) []U {
	out := make([]U, len(in))
	for i, d := range in {
		out[i] = convert(d)
	}
	return out
}

// findOne decodes the first document that matches into T; found is false when none does.
func findOne[T any](ctx context.Context, c *mongo.Collection, filter any, opts ...options.Lister[options.FindOneOptions]) (d T, found bool, err error) {
	err = c.FindOne(ctx, filter, opts...).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return d, false, nil
	}
	return d, err == nil, err
}

// getOne is findOne for a document that must exist: when none matches it returns notFound.
func getOne[T any](ctx context.Context, c *mongo.Collection, filter any, notFound error) (T, error) {
	d, found, err := findOne[T](ctx, c, filter)
	if err == nil && !found {
		err = notFound
	}
	return d, err
}

// withTransaction runs fn in a transaction of client, which the driver may run again on a
// transient error.
func withTransaction(ctx context.Context, client *mongo.Client, fn func(ctx context.Context) error) error {
	sess, err := client.StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(ctx)
	_, err = sess.WithTransaction(ctx, func(sessCtx context.Context) (any, error) {
		return nil, fn(sessCtx)
	})
	return err
}

// utcPtr is t in UTC, nil when t is nil.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// withinTime adds to filter the condition that field is at or after since and before until;
// a zero bound adds nothing.
func withinTime(filter bson.D, field string, since, until time.Time) bson.D {
	rng := bson.D{}
	if !since.IsZero() {
		rng = append(rng, bson.E{Key: "$gte", Value: since})
	}
	if !until.IsZero() {
		rng = append(rng, bson.E{Key: "$lt", Value: until})
	}
	if len(rng) > 0 {
		filter = append(filter, bson.E{Key: field, Value: rng})
	}
	return filter
}

// pageOpts sorts a find and skips skip documents. A positive limit asks for one document more
// than the page, so trimPage can tell whether there is a next one.
func pageOpts(sort bson.D, skip, limit int) *options.FindOptionsBuilder {
	opts := options.Find().SetSort(sort).SetSkip(int64(skip))
	if limit > 0 {
		opts.SetLimit(int64(limit) + 1)
	}
	return opts
}

// trimPage cuts docs, fetched with pageOpts, to the page and says whether more follow it.
func trimPage[T any](docs []T, limit int) ([]T, bool) {
	if limit > 0 && len(docs) > limit {
		return docs[:limit], true
	}
	return docs, false
}
