package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/auth"
)

type idempotencyDoc struct {
	ID          string        `bson:"_id"`
	Function    string        `bson:"function"`
	CallerActor string        `bson:"caller_actor"`
	InputHash   string        `bson:"input_hash"`
	Mode        string        `bson:"mode"`
	Status      string        `bson:"status"`
	Result      *jobResultDoc `bson:"result,omitempty"`
	JobID       string        `bson:"job_id,omitempty"`
	CreatedAt   time.Time     `bson:"created_at"`
	ExpiresAt   time.Time     `bson:"expires_at"`
}

func (s *AuthStore) idempotency() *mongo.Collection { return s.db.Collection(IdempotencyCollection) }

func idempotencyFromDoc(d idempotencyDoc) auth.IdempotencyRecord {
	return auth.IdempotencyRecord{
		ID: d.ID, Function: d.Function, CallerActor: d.CallerActor, InputHash: d.InputHash, Mode: d.Mode,
		Status: d.Status, Result: jobResultFromDoc(d.Result), JobID: d.JobID,
		CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
	}
}

// ClaimIdempotency inserts rec if its id isn't already used; a duplicate
// key means someone already claimed it, so the existing record is
// fetched and returned instead, claimed false.
func (s *AuthStore) ClaimIdempotency(ctx context.Context, rec auth.IdempotencyRecord) (auth.IdempotencyRecord, bool, error) {
	_, err := s.idempotency().InsertOne(ctx, idempotencyDoc{
		ID: rec.ID, Function: rec.Function, CallerActor: rec.CallerActor, InputHash: rec.InputHash,
		Mode: rec.Mode, Status: rec.Status, CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt,
	})
	if err == nil {
		return rec, true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return auth.IdempotencyRecord{}, false, err
	}
	var d idempotencyDoc
	if err := s.idempotency().FindOne(ctx, bson.D{{Key: "_id", Value: rec.ID}}).Decode(&d); err != nil {
		return auth.IdempotencyRecord{}, false, err
	}
	return idempotencyFromDoc(d), false, nil
}

// CompleteIdempotency stores a claimed key's outcome: result for a sync
// call, jobID for async (mutually exclusive). Only the one that applies
// is set — the collection's validator types "result" as an object, so
// setting it to an explicit BSON null (rather than leaving it absent)
// would fail validation.
func (s *AuthStore) CompleteIdempotency(ctx context.Context, id string, result *auth.JobResult, jobID string, completedAt, expiresAt time.Time) error {
	set := bson.D{
		{Key: "status", Value: auth.IdempotencyDone},
		{Key: "expires_at", Value: expiresAt},
	}
	if result != nil {
		doc, err := jobResultToDoc(result)
		if err != nil {
			return err
		}
		set = append(set, bson.E{Key: "result", Value: doc})
	}
	if jobID != "" {
		set = append(set, bson.E{Key: "job_id", Value: jobID})
	}
	_, err := s.idempotency().UpdateByID(ctx, id, bson.D{{Key: "$set", Value: set}})
	return err
}

// ReleaseIdempotency removes a claim that shouldn't be remembered.
func (s *AuthStore) ReleaseIdempotency(ctx context.Context, id string) error {
	_, err := s.idempotency().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	return err
}
