package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Store hands out repositories backed by one MongoDB client.
type Store struct {
	Client *mongo.Client
}

// Repository returns the repository of a configured collection.
func (s *Store) Repository(c *registry.Collection) storage.Repository {
	return &Repository{coll: s.Client.Database(c.MongoDatabase).Collection(c.Name)}
}

// Transact runs fn once inside a MongoDB transaction: every write made
// through a Repository, using the context fn receives, either all commit
// or none do (roadmap F7). The driver may retry fn on a transient error
// (e.g. a write conflict with another transaction), so fn must have no
// side effects beyond the repositories it's given.
func (s *Store) Transact(ctx context.Context, fn func(ctx context.Context) error) error {
	sess, err := s.Client.StartSession()
	if err != nil {
		return mapError(err)
	}
	defer sess.EndSession(ctx)
	_, err = sess.WithTransaction(ctx, func(sessCtx context.Context) (any, error) {
		return nil, fn(sessCtx)
	})
	return mapError(err)
}

// Repository implements storage.Repository for one MongoDB collection.
type Repository struct {
	coll *mongo.Collection
}

var _ storage.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, doc storage.Document) error {
	_, err := r.coll.InsertOne(ctx, toBSON(doc))
	return mapError(err)
}

func (r *Repository) Get(ctx context.Context, id string) (storage.Document, error) {
	var d bson.D
	if err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d); err != nil {
		return nil, mapError(err)
	}
	return fromBSON(d), nil
}

func (r *Repository) List(ctx context.Context, q storage.Query) (storage.Page, error) {
	filter, err := buildQuery(q)
	if err != nil {
		return storage.Page{}, err
	}
	opts := options.Find().
		SetSort(buildSort(q.Sort)).
		SetSkip(int64(q.Skip)).
		SetLimit(int64(q.Limit) + 1) // one extra to compute HasMore
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return storage.Page{}, mapError(err)
	}
	var docs []bson.D
	if err := cur.All(ctx, &docs); err != nil {
		return storage.Page{}, mapError(err)
	}

	page := storage.Page{Items: make([]storage.Document, 0, min(len(docs), q.Limit))}
	if len(docs) > q.Limit {
		page.HasMore = true
		docs = docs[:q.Limit]
	}
	for _, d := range docs {
		page.Items = append(page.Items, fromBSON(d))
	}
	if q.Count {
		n, err := r.coll.CountDocuments(ctx, filter)
		if err != nil {
			return storage.Page{}, mapError(err)
		}
		page.Total = &n
	}
	return page, nil
}

func (r *Repository) Replace(ctx context.Context, doc storage.Document, ifVersion int64) error {
	id, _ := doc["id"].(string)
	res, err := r.coll.ReplaceOne(ctx, versionFilter(id, &ifVersion), toBSON(doc))
	if err != nil {
		return mapError(err)
	}
	if res.MatchedCount == 0 {
		return r.missOrMismatch(ctx, id)
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id string, ifVersion *int64) error {
	res, err := r.coll.DeleteOne(ctx, versionFilter(id, ifVersion))
	if err != nil {
		return mapError(err)
	}
	if res.DeletedCount == 0 {
		if ifVersion == nil {
			return storage.ErrNotFound
		}
		return r.missOrMismatch(ctx, id)
	}
	return nil
}

// versionFilter selects a document by id and, when ifVersion is set, by
// version. Version 0 matches documents stored without a version.
func versionFilter(id string, ifVersion *int64) bson.D {
	f := bson.D{{Key: "_id", Value: id}}
	switch {
	case ifVersion == nil:
	case *ifVersion == 0:
		f = append(f, bson.E{Key: "_meta.version", Value: nil})
	default:
		f = append(f, bson.E{Key: "_meta.version", Value: *ifVersion})
	}
	return f
}

// missOrMismatch explains why a conditional write matched nothing.
func (r *Repository) missOrMismatch(ctx context.Context, id string) error {
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return mapError(err)
	}
	if n == 0 {
		return storage.ErrNotFound
	}
	return storage.ErrVersionMismatch
}

// mapError translates driver errors into storage errors.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return storage.ErrNotFound
	case mongo.IsDuplicateKeyError(err):
		return &storage.ConflictError{Fields: duplicateKeyFields(err)}
	case errors.Is(err, context.DeadlineExceeded), mongo.IsTimeout(err), mongo.IsNetworkError(err):
		return fmt.Errorf("%w: %v", storage.ErrUnavailable, err)
	}
	return err
}

// duplicateKeyFields returns the fields of the index a duplicate key error
// refers to, taken from the server's keyPattern.
func duplicateKeyFields(err error) []string {
	var we mongo.WriteException
	if !errors.As(err, &we) {
		return nil
	}
	for _, e := range we.WriteErrors {
		pattern, ok := e.Raw.Lookup("keyPattern").DocumentOK()
		if !ok {
			continue
		}
		elems, _ := pattern.Elements()
		fields := make([]string, 0, len(elems))
		for _, el := range elems {
			f := el.Key()
			if f == "_id" {
				f = "id"
			}
			fields = append(fields, f)
		}
		return fields
	}
	return nil
}
