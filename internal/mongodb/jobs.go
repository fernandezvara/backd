package mongodb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/jsonnum"
)

type jobResultDoc struct {
	Status     string `bson:"status"`
	Output     any    `bson:"output,omitempty"`
	Code       string `bson:"code,omitempty"`
	Message    string `bson:"message,omitempty"`
	Details    any    `bson:"details,omitempty"`
	DurationMS int64  `bson:"duration_ms"`
	// HTTPStatus is the function's own chosen status when Code is set
	// (roadmap F12's idempotent replay needs it); unused otherwise.
	HTTPStatus int `bson:"http_status,omitempty"`
}

type jobDoc struct {
	ID             string        `bson:"_id"`
	Database       string        `bson:"database"`
	Function       string        `bson:"function"`
	Input          any           `bson:"input"`
	CallerActor    string        `bson:"caller_actor"`
	CallerUserID   string        `bson:"caller_user_id,omitempty"`
	CallerKeyHash  string        `bson:"caller_key_hash,omitempty"`
	Scheduled      bool          `bson:"scheduled,omitempty"`
	ActsAsFunction bool          `bson:"acts_as_function,omitempty"`
	Origin         string        `bson:"origin,omitempty"`
	ParentID       string        `bson:"parent_id,omitempty"`
	Depth          int32         `bson:"depth,omitempty"`
	Status         string        `bson:"status"`
	Attempts       int32         `bson:"attempts"`
	TimeoutMS      int64         `bson:"timeout_ms"`
	RequestID      string        `bson:"request_id,omitempty"`
	LeaseOwner     string        `bson:"lease_owner,omitempty"`
	LeaseExpires   *time.Time    `bson:"lease_expires,omitempty"`
	CreatedAt      time.Time     `bson:"created_at"`
	CompletedAt    *time.Time    `bson:"completed_at,omitempty"`
	ExpiresAt      time.Time     `bson:"expires_at"`
	Result         *jobResultDoc `bson:"result,omitempty"`
}

func (s *AuthStore) jobs() *mongo.Collection { return s.db.Collection(JobsCollection) }

// decodeJSONAny turns a JSON value into a plain Go value (json.Number
// normalized to int64/float64), suitable for BSON encoding directly:
// the driver marshals map[string]any, []any and the scalar types as-is.
func decodeJSONAny(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return jsonnum.Normalize(v), nil
}

// encodeJSONAny is decodeJSONAny's reverse: a value read back from
// MongoDB (bson.D/bson.A for documents/arrays, since it was decoded into
// an `any` field) into JSON. fromBSONValue normalizes it to
// map[string]any/[]any first, which encoding/json marshals directly.
func encodeJSONAny(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(fromBSONValue(v))
	if err != nil {
		return nil
	}
	return data
}

func jobResultToDoc(r *auth.JobResult) (*jobResultDoc, error) {
	if r == nil {
		return nil, nil
	}
	output, err := decodeJSONAny(r.Output)
	if err != nil {
		return nil, err
	}
	details, err := decodeJSONAny(r.Details)
	if err != nil {
		return nil, err
	}
	return &jobResultDoc{Status: r.Status, Output: output, Code: r.Code, Message: r.Message, Details: details, DurationMS: r.DurationMS, HTTPStatus: r.HTTPStatus}, nil
}

func jobResultFromDoc(d *jobResultDoc) *auth.JobResult {
	if d == nil {
		return nil
	}
	return &auth.JobResult{Status: d.Status, Output: encodeJSONAny(d.Output), Code: d.Code, Message: d.Message, Details: encodeJSONAny(d.Details), DurationMS: d.DurationMS, HTTPStatus: d.HTTPStatus}
}

func jobFromDoc(d jobDoc) auth.Job {
	j := auth.Job{
		ID: d.ID, Database: d.Database, Function: d.Function, Input: encodeJSONAny(d.Input),
		CallerActor: d.CallerActor, CallerUserID: d.CallerUserID, CallerKeyHash: d.CallerKeyHash, Scheduled: d.Scheduled,
		ActsAsFunction: d.ActsAsFunction, Origin: d.Origin, ParentID: d.ParentID, Depth: int(d.Depth),
		TimeoutMS: d.TimeoutMS, RequestID: d.RequestID,
		Status: d.Status, Attempts: int(d.Attempts), CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
		Result: jobResultFromDoc(d.Result),
	}
	if d.CompletedAt != nil {
		j.CompletedAt = d.CompletedAt.UTC()
	}
	return j
}

// EnqueueJob inserts a new job, queued for a worker to claim.
func (s *AuthStore) EnqueueJob(ctx context.Context, j auth.Job) error {
	input, err := decodeJSONAny(j.Input)
	if err != nil {
		return err
	}
	_, err = s.jobs().InsertOne(ctx, jobDoc{
		ID: j.ID, Database: j.Database, Function: j.Function, Input: input,
		CallerActor: j.CallerActor, CallerUserID: j.CallerUserID, CallerKeyHash: j.CallerKeyHash, Scheduled: j.Scheduled, Status: j.Status,
		ActsAsFunction: j.ActsAsFunction, Origin: j.Origin, ParentID: j.ParentID, Depth: int32(j.Depth),
		Attempts: 0, TimeoutMS: j.TimeoutMS, RequestID: j.RequestID,
		CreatedAt: j.CreatedAt, ExpiresAt: j.ExpiresAt,
	})
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrJobExists
	}
	return err
}

// ClaimJob atomically claims the oldest claimable job: never claimed
// (status queued, no lease yet), or whose lease has expired (a dead
// worker's jobs are picked up again). The lease extends to at plus the
// job's own timeout_ms plus margin, computed in the same update so a
// claim never needs a second round trip to look the function's timeout
// up.
func (s *AuthStore) ClaimJob(ctx context.Context, workerID string, at time.Time, margin time.Duration) (auth.Job, bool, error) {
	filter := bson.D{
		{Key: "status", Value: bson.D{{Key: "$ne", Value: auth.JobDone}}},
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "lease_expires", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "lease_expires", Value: bson.D{{Key: "$lte", Value: at}}}},
		}},
	}
	leaseExpires := bson.D{{Key: "$add", Value: bson.A{at, "$timeout_ms", margin.Milliseconds()}}}
	pipeline := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "status", Value: auth.JobRunning},
		{Key: "lease_owner", Value: workerID},
		{Key: "lease_expires", Value: leaseExpires},
		{Key: "attempts", Value: bson.D{{Key: "$add", Value: bson.A{"$attempts", int32(1)}}}},
	}}}}
	opts := options.FindOneAndUpdate().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetReturnDocument(options.After)
	var d jobDoc
	err := s.jobs().FindOneAndUpdate(ctx, filter, pipeline, opts).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Job{}, false, nil
	}
	if err != nil {
		return auth.Job{}, false, err
	}
	return jobFromDoc(d), true, nil
}

// CompleteJob records a job's result; expiresAt starts its retention
// countdown. The update is conditional on the job not already being
// done, so a worker whose lease expired and was reclaimed can't finish
// late and overwrite a result another worker already recorded.
func (s *AuthStore) CompleteJob(ctx context.Context, id string, result auth.JobResult, completedAt, expiresAt time.Time) error {
	doc, err := jobResultToDoc(&result)
	if err != nil {
		return err
	}
	filter := bson.D{{Key: "_id", Value: id}, {Key: "status", Value: bson.D{{Key: "$ne", Value: auth.JobDone}}}}
	_, err = s.jobs().UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: auth.JobDone},
		{Key: "completed_at", Value: completedAt},
		{Key: "expires_at", Value: expiresAt},
		{Key: "result", Value: doc},
	}}})
	return err
}

// ListJobs returns the jobs matching f, newest first.
func (s *AuthStore) ListJobs(ctx context.Context, f auth.JobFilter) ([]auth.Job, bool, error) {
	filter := bson.D{}
	for _, c := range []struct{ key, value string }{{"database", f.Database}, {"function", f.Function}, {"status", f.Status}} {
		if c.value != "" {
			filter = append(filter, bson.E{Key: c.key, Value: c.value})
		}
	}
	if f.Scheduled != nil {
		if *f.Scheduled {
			filter = append(filter, bson.E{Key: "scheduled", Value: true})
		} else {
			filter = append(filter, bson.E{Key: "scheduled", Value: bson.D{{Key: "$ne", Value: true}}})
		}
	}
	created := bson.D{}
	if !f.Since.IsZero() {
		created = append(created, bson.E{Key: "$gte", Value: f.Since})
	}
	if !f.Until.IsZero() {
		created = append(created, bson.E{Key: "$lt", Value: f.Until})
	}
	if len(created) > 0 {
		filter = append(filter, bson.E{Key: "created_at", Value: created})
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(f.Skip))
	if f.Limit > 0 {
		opts.SetLimit(int64(f.Limit) + 1)
	}
	cur, err := s.jobs().Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	var docs []jobDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	more := f.Limit > 0 && len(docs) > f.Limit
	if more {
		docs = docs[:f.Limit]
	}
	out := make([]auth.Job, len(docs))
	for i, d := range docs {
		out[i] = jobFromDoc(d)
	}
	return out, more, nil
}

// GetJob returns one job by id.
func (s *AuthStore) GetJob(ctx context.Context, id string) (auth.Job, bool, error) {
	var d jobDoc
	err := s.jobs().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Job{}, false, nil
	}
	if err != nil {
		return auth.Job{}, false, err
	}
	return jobFromDoc(d), true, nil
}
