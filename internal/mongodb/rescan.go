package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DocScanner implements triggers.DocSource: documents by when they were last written.
type DocScanner struct{ Client *mongo.Client }

func updatedBetween(since, until time.Time) bson.D {
	rng := bson.D{}
	if !since.IsZero() {
		rng = append(rng, bson.E{Key: "$gte", Value: since})
	}
	if !until.IsZero() {
		rng = append(rng, bson.E{Key: "$lt", Value: until})
	}
	if len(rng) == 0 {
		return bson.D{}
	}
	return bson.D{{Key: "_meta.updated_at", Value: rng}}
}

// Count counts the documents updated in [since, until).
func (d *DocScanner) Count(ctx context.Context, mongoDatabase, collection string, since, until time.Time) (int64, error) {
	return d.Client.Database(mongoDatabase).Collection(collection).CountDocuments(ctx, updatedBetween(since, until))
}

// Updated reads the documents updated in [since, until), oldest first.
func (d *DocScanner) Updated(ctx context.Context, mongoDatabase, collection string, since, until time.Time, fn func(map[string]any) error) error {
	cur, err := d.Client.Database(mongoDatabase).Collection(collection).Find(ctx, updatedBetween(since, until),
		options.Find().SetSort(bson.D{{Key: "_meta.updated_at", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx) //nolint:errcheck
	for cur.Next(ctx) {
		var raw bson.D
		if err := cur.Decode(&raw); err != nil {
			return err
		}
		if err := fn(fromBSON(raw)); err != nil {
			return err
		}
	}
	return cur.Err()
}
