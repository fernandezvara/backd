package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type auditDoc struct {
	ID        string         `bson:"_id"`
	At        time.Time      `bson:"at"`
	ExpiresAt time.Time      `bson:"expires_at"`
	Action    string         `bson:"action"`
	Actor     string         `bson:"actor"`
	Target    string         `bson:"target,omitempty"`
	Details   map[string]any `bson:"details,omitempty"`
	RequestID string         `bson:"request_id,omitempty"`
	ClientIP  string         `bson:"client_ip,omitempty"`
}

func (s *AuthStore) audit() *mongo.Collection { return s.db.Collection(AuditCollection) }

// AppendAudit inserts a record. backd never updates or deletes audit
// records; MongoDB removes them when they expire.
func (s *AuthStore) AppendAudit(ctx context.Context, r auth.AuditRecord) error {
	_, err := s.audit().InsertOne(ctx, auditDoc{
		ID: r.ID, At: r.At, ExpiresAt: r.ExpiresAt, Action: r.Action, Actor: r.Actor, Target: r.Target,
		Details: r.Details, RequestID: r.RequestID, ClientIP: r.ClientIP,
	})
	return err
}

func (s *AuthStore) ListAudit(ctx context.Context, f auth.AuditFilter) ([]auth.AuditRecord, bool, error) {
	filter := bson.D{}
	for _, c := range []struct{ key, value string }{{"action", f.Action}, {"actor", f.Actor}, {"target", f.Target}} {
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
	cur, err := s.audit().Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	var docs []auditDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	more := f.Limit > 0 && len(docs) > f.Limit
	if more {
		docs = docs[:f.Limit]
	}
	out := make([]auth.AuditRecord, len(docs))
	for i, d := range docs {
		out[i] = auth.AuditRecord{
			ID: d.ID, At: d.At.UTC(), ExpiresAt: d.ExpiresAt.UTC(), Action: d.Action, Actor: d.Actor, Target: d.Target,
			Details: plain(d.Details), RequestID: d.RequestID, ClientIP: d.ClientIP,
		}
	}
	return out, more, nil
}

// plain turns decoded BSON arrays and documents into plain Go values.
func plain(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = plainValue(v)
	}
	return out
}

func plainValue(v any) any {
	switch x := v.(type) {
	case bson.A:
		list := make([]any, len(x))
		for i, e := range x {
			list[i] = plainValue(e)
		}
		return list
	case bson.D:
		m := make(map[string]any, len(x))
		for _, e := range x {
			m[e.Key] = plainValue(e.Value)
		}
		return m
	case bson.M:
		return plain(x)
	}
	return v
}
