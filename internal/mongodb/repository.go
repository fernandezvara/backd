package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/fieldcrypt"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// Store hands out repositories backed by one MongoDB client.
type Store struct {
	Client *mongo.Client
	// Crypt returns the realm's field encryption; nil (or a nil result) means the realm has none,
	// which is only valid while none of its collections encrypts a field.
	Crypt func(realm string) *fieldcrypt.Ring
}

// Repository returns the repository of a configured collection.
func (s *Store) Repository(c *registry.Collection) storage.Repository {
	r := &Repository{coll: s.Client.Database(c.MongoDatabase).Collection(c.Name), c: c}
	if s.Crypt != nil {
		// Also for a collection that encrypts nothing now: values of a field that once was
		// encrypted are opened on read until a migration opens them for good.
		r.ring = s.Crypt(c.Realm)
	}
	if c.Text != nil {
		r.languageField = c.Text.LanguageField
	}
	return r
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
	c    *registry.Collection
	// ring seals and opens the collection's encrypted fields; nil when it has none.
	ring *fieldcrypt.Ring
	// languageField is the document field (API path) holding a locale code, for a collection
	// whose text index has a language_field: the stored copy the index reads is kept in step
	// on every write.
	languageField string
}

// scoreField is where a search's relevance is projected, a name no document can use (names
// starting with an underscore are server-owned).
const scoreField = "_backd_score"

// mapSearchError maps a failed read: a search without its text index is a rebuild in
// progress, not an outage of the database.
func mapSearchError(parent, run context.Context, searching bool, err error) error {
	if searching && run.Err() != nil && parent.Err() == nil {
		return storage.ErrSearchTooExpensive
	}
	var ce mongo.CommandError
	if searching && errors.As(err, &ce) {
		switch ce.Code {
		case 27: // IndexNotFound: "text index required for $text query"
			return storage.ErrIndexBuilding
		case 50: // MaxTimeMSExpired
			return storage.ErrSearchTooExpensive
		}
	}
	return mapError(err)
}

// bsonOf converts a document for storage, adding the hidden search language the text index
// reads when the collection declares a language_field.
func (r *Repository) bsonOf(doc storage.Document) bson.D {
	out := toBSON(doc)
	if r.languageField == "" {
		return out
	}
	if v, ok := lookupPath(doc, r.languageField).(string); ok && v != "" {
		out = append(out, bson.E{Key: registry.TextSearchField, Value: registry.SearchLanguage(v)})
	}
	return out
}

// lookupPath reads a dot path of a document; a missing field is nil.
func lookupPath(doc map[string]any, path string) any {
	var cur any = map[string]any(doc)
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

var _ storage.Repository = (*Repository)(nil)

// seal returns doc as it is stored: its encrypted fields sealed. doc itself is not changed.
func (r *Repository) seal(ctx context.Context, doc storage.Document) (storage.Document, error) {
	if r.c == nil || len(r.c.Encrypted) == 0 {
		return doc, nil
	}
	if r.ring == nil {
		return nil, fmt.Errorf("%w: this instance has no field encryption for the realm", fieldcrypt.ErrUnavailable)
	}
	return r.ring.Encrypt(ctx, r.c, doc)
}

// open decrypts the encrypted fields of documents just read, in place.
func (r *Repository) open(ctx context.Context, docs ...storage.Document) error {
	if r.c == nil || storage.RawEncrypted(ctx) {
		return nil
	}
	if r.ring == nil {
		if len(r.c.Encrypted) == 0 {
			return nil
		}
		return fmt.Errorf("%w: this instance has no field encryption for the realm", fieldcrypt.ErrUnavailable)
	}
	for _, d := range docs {
		if err := r.ring.Decrypt(ctx, r.c, d); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, doc storage.Document) error {
	doc, err := r.seal(ctx, doc)
	if err != nil {
		return err
	}
	_, err = r.coll.InsertOne(ctx, r.bsonOf(doc))
	return mapError(err)
}

func (r *Repository) Get(ctx context.Context, id string) (storage.Document, error) {
	var d bson.D
	if err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d); err != nil {
		return nil, mapError(err)
	}
	doc := fromBSON(d)
	if err := r.open(ctx, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (r *Repository) List(ctx context.Context, q storage.Query) (storage.Page, error) {
	var err error
	if q.Filter, err = r.blindFilter(ctx, q.Filter); err != nil {
		return storage.Page{}, err
	}
	if q.Access, err = r.blindFilter(ctx, q.Access); err != nil {
		return storage.Page{}, err
	}
	filter, err := buildQuery(q)
	if err != nil {
		return storage.Page{}, err
	}
	find := filter
	if q.After != nil {
		after, err := buildTree(storage.Simplify(q.After))
		if err != nil {
			return storage.Page{}, err
		}
		if len(find) == 0 {
			find = after
		} else {
			find = bson.D{{Key: "$and", Value: bson.A{find, after}}}
		}
	}
	opts := options.Find().
		SetSort(buildSort(q.Sort)).
		SetSkip(int64(q.Skip)).
		SetLimit(int64(q.Limit) + 1) // one extra to compute HasMore
	if q.Relevance {
		// Best match first, ties by id; the score rides along under a name no document can
		// have and is moved into _meta.score below.
		score := bson.D{{Key: "$meta", Value: "textScore"}}
		opts.SetProjection(bson.D{{Key: scoreField, Value: score}}).
			SetSort(bson.D{{Key: scoreField, Value: score}, {Key: "_id", Value: 1}})
	}
	runCtx := ctx
	if q.Search != nil && q.MaxTime > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, q.MaxTime)
		defer cancel()
	}
	cur, err := r.coll.Find(runCtx, find, opts)
	if err != nil {
		return storage.Page{}, mapSearchError(ctx, runCtx, q.Search != nil, err)
	}
	var docs []bson.D
	if err := cur.All(runCtx, &docs); err != nil {
		return storage.Page{}, mapSearchError(ctx, runCtx, q.Search != nil, err)
	}

	page := storage.Page{Items: make([]storage.Document, 0, min(len(docs), q.Limit))}
	if len(docs) > q.Limit {
		page.HasMore = true
		docs = docs[:q.Limit]
	}
	for _, d := range docs {
		var score any
		if q.Relevance {
			for i, e := range d {
				if e.Key == scoreField {
					score = e.Value
					d = slices.Delete(slices.Clone(d), i, i+1)
					break
				}
			}
		}
		doc := fromBSON(d)
		if score != nil {
			if meta, ok := doc["_meta"].(map[string]any); ok {
				meta["score"] = score
			}
		}
		page.Items = append(page.Items, doc)
	}
	if err := r.open(ctx, page.Items...); err != nil {
		return storage.Page{}, err
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
	doc, err := r.seal(ctx, doc)
	if err != nil {
		return err
	}
	res, err := r.coll.ReplaceOne(ctx, versionFilter(id, &ifVersion), r.bsonOf(doc))
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
			if rest, ok := strings.CutPrefix(f, registry.BlindPrefix); ok {
				f = strings.ReplaceAll(rest, "~", ".") // the encrypted field a blind index stands for
			}
			fields = append(fields, f)
		}
		return fields
	}
	return nil
}
