package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type logLineDoc struct {
	Level string `bson:"level"`
	Line  string `bson:"line"`
}

type invocationDoc struct {
	ID         string       `bson:"_id"`
	At         time.Time    `bson:"at"`
	ExpiresAt  time.Time    `bson:"expires_at"`
	Function   string       `bson:"function"`
	Actor      string       `bson:"actor"`
	Mode       string       `bson:"mode"`
	Status     string       `bson:"status"`
	Code       string       `bson:"code,omitempty"`
	DurationMS int64        `bson:"duration_ms"`
	RequestID  string       `bson:"request_id,omitempty"`
	JobID      string       `bson:"job_id,omitempty"`
	ParentID   string       `bson:"parent_id,omitempty"`
	Origin     string       `bson:"origin,omitempty"`
	Logs       []logLineDoc `bson:"logs,omitempty"`
}

func (s *AuthStore) invocations() *mongo.Collection { return s.db.Collection(InvocationsCollection) }

// RecordInvocation inserts a record. backd never updates or deletes
// invocation records; MongoDB removes them when they expire.
func (s *AuthStore) RecordInvocation(ctx context.Context, r auth.InvocationRecord) error {
	logs := make([]logLineDoc, len(r.Logs))
	for i, l := range r.Logs {
		logs[i] = logLineDoc{Level: l.Level, Line: l.Line}
	}
	_, err := s.invocations().InsertOne(ctx, invocationDoc{
		ID: r.ID, At: r.At, ExpiresAt: r.ExpiresAt, Function: r.Function, Actor: r.Actor, Mode: r.Mode,
		Status: r.Status, Code: r.Code, DurationMS: r.DurationMS, RequestID: r.RequestID, JobID: r.JobID, ParentID: r.ParentID, Origin: r.Origin, Logs: logs,
	})
	return err
}

func (s *AuthStore) ListInvocations(ctx context.Context, f auth.InvocationFilter) ([]auth.InvocationRecord, bool, error) {
	filter := bson.D{}
	for _, c := range []struct{ key, value string }{{"function", f.Function}, {"request_id", f.RequestID}} {
		if c.value != "" {
			filter = append(filter, bson.E{Key: c.key, Value: c.value})
		}
	}
	at := bson.D{}
	if !f.Since.IsZero() {
		at = append(at, bson.E{Key: "$gte", Value: f.Since})
	}
	if !f.Until.IsZero() {
		at = append(at, bson.E{Key: "$lt", Value: f.Until})
	}
	if len(at) > 0 {
		filter = append(filter, bson.E{Key: "at", Value: at})
	}
	opts := options.Find().SetSort(bson.D{{Key: "at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(f.Skip))
	if f.Limit > 0 {
		opts.SetLimit(int64(f.Limit) + 1)
	}
	cur, err := s.invocations().Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	var docs []invocationDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	more := f.Limit > 0 && len(docs) > f.Limit
	if more {
		docs = docs[:f.Limit]
	}
	out := make([]auth.InvocationRecord, len(docs))
	for i, d := range docs {
		logs := make([]auth.LogLine, len(d.Logs))
		for j, l := range d.Logs {
			logs[j] = auth.LogLine{Level: l.Level, Line: l.Line}
		}
		out[i] = auth.InvocationRecord{
			ID: d.ID, At: d.At.UTC(), ExpiresAt: d.ExpiresAt.UTC(), Function: d.Function, Actor: d.Actor, Mode: d.Mode,
			Status: d.Status, Code: d.Code, DurationMS: d.DurationMS, RequestID: d.RequestID, JobID: d.JobID, ParentID: d.ParentID, Origin: d.Origin, Logs: logs,
		}
	}
	return out, more, nil
}
