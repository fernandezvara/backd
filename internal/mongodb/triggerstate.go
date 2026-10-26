package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/changefeed"
	"github.com/fernandezvara/backd/internal/triggers"
)

// TriggerStateCollection and TriggerGapsCollection live in the deployment database
// (design/triggers-and-realtime.md §9): the lease and the position of the trigger dispatcher
// in one document, and the gaps it recorded. They are runtime state, written by serving
// instances in every provision mode.
const (
	TriggerStateCollection = "trigger_state"
	TriggerGapsCollection  = "trigger_gaps"
)

// TriggerState implements triggers.State on MongoDB.
//
// The lease and the position are one document, so that moving the position is atomic with
// still being the leaseholder: a holder that lost the lease can't advance it, whatever it
// believes. All times come from the database's clock ($$NOW), never from the instances'.
type TriggerState struct {
	Client *mongo.Client
	// Deployment keeps backd deployments that share one MongoDB cluster apart.
	Deployment string
}

func (s *TriggerState) state() *mongo.Collection {
	return s.Client.Database(DeploymentDatabase).Collection(TriggerStateCollection)
}

func (s *TriggerState) gaps() *mongo.Collection {
	return s.Client.Database(DeploymentDatabase).Collection(TriggerGapsCollection)
}

func (s *TriggerState) id() string { return "dispatcher:" + s.Deployment }

func ttlMillis(ttl time.Duration) int64 { return ttl.Milliseconds() }

func leaseExpiry(ttl time.Duration) bson.D {
	return bson.D{{Key: "$dateAdd", Value: bson.D{
		{Key: "startDate", Value: "$$NOW"}, {Key: "unit", Value: "millisecond"}, {Key: "amount", Value: ttlMillis(ttl)},
	}}}
}

type leaseDoc struct {
	Holder string `bson:"holder"`
	Epoch  int64  `bson:"epoch"`
}

// AcquireLease takes the lease when it has expired or is already ours.
func (s *TriggerState) AcquireLease(ctx context.Context, holder string, ttl time.Duration) (triggers.Lease, bool, error) {
	// The document is made once, empty and expired; taking it is then a plain conditional
	// update (an upsert can't carry the $expr that compares with the database's clock).
	_, err := s.state().UpdateOne(ctx, byID(s.id()),
		bson.D{{Key: "$setOnInsert", Value: bson.D{
			{Key: "holder", Value: ""}, {Key: "epoch", Value: int64(0)}, {Key: "expires_at", Value: time.Unix(0, 0).UTC()},
		}}}, options.UpdateOne().SetUpsert(true))
	if err != nil && !mongo.IsDuplicateKeyError(err) { // two instances making it at once
		return triggers.Lease{}, false, err
	}
	filter := bson.D{
		{Key: "_id", Value: s.id()},
		{Key: "$expr", Value: bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "$lte", Value: bson.A{"$expires_at", "$$NOW"}}},
			bson.D{{Key: "$eq", Value: bson.A{"$holder", holder}}},
		}}}},
	}
	update := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "holder", Value: holder},
		{Key: "epoch", Value: bson.D{{Key: "$add", Value: bson.A{"$epoch", int64(1)}}}},
		{Key: "expires_at", Value: leaseExpiry(ttl)},
	}}}}
	var d leaseDoc
	err = s.state().FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return triggers.Lease{}, false, nil // somebody else holds it
	}
	if err != nil {
		return triggers.Lease{}, false, err
	}
	return triggers.Lease{Holder: holder, Epoch: d.Epoch}, true, nil
}

// RenewLease extends the lease while it is still the holder's, at the same epoch.
func (s *TriggerState) RenewLease(ctx context.Context, l triggers.Lease, ttl time.Duration) (bool, error) {
	res, err := s.state().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: s.id()}, {Key: "holder", Value: l.Holder}, {Key: "epoch", Value: l.Epoch}},
		mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "expires_at", Value: leaseExpiry(ttl)}}}}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

// ReleaseLease expires the lease now, keeping its epoch and position.
func (s *TriggerState) ReleaseLease(ctx context.Context, l triggers.Lease) error {
	_, err := s.state().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: s.id()}, {Key: "holder", Value: l.Holder}, {Key: "epoch", Value: l.Epoch}},
		mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "expires_at", Value: "$$NOW"}}}}})
	return err
}

// Watermark reads the stored position.
func (s *TriggerState) Watermark(ctx context.Context) (changefeed.Position, time.Time, bool, error) {
	var d struct {
		Token bson.Binary `bson:"token"`
		At    time.Time   `bson:"at"`
	}
	err := s.state().FindOne(ctx, byID(s.id())).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	if len(d.Token.Data) == 0 {
		return nil, time.Time{}, false, nil
	}
	return changefeed.Position(d.Token.Data), d.At.UTC(), true, nil
}

// SetWatermark stores the position when l is still the lease: the filter names holder and
// epoch, so a holder that was replaced matches nothing.
func (s *TriggerState) SetWatermark(ctx context.Context, l triggers.Lease, pos changefeed.Position, at time.Time) (bool, error) {
	res, err := s.state().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: s.id()}, {Key: "holder", Value: l.Holder}, {Key: "epoch", Value: l.Epoch}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "token", Value: bson.Binary{Data: pos}}, {Key: "at", Value: at}}}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

type gapDoc struct {
	ID         string    `bson:"_id"`
	Deployment string    `bson:"deployment"`
	Realm      string    `bson:"realm,omitempty"`
	Collection string    `bson:"collection,omitempty"`
	Trigger    string    `bson:"trigger,omitempty"`
	Reason     string    `bson:"reason"`
	From       time.Time `bson:"from"`
	To         time.Time `bson:"to"`
	Count      int       `bson:"count"`
	Keys       []string  `bson:"keys,omitempty"`
	RecordedAt time.Time `bson:"recorded_at"`
	ResolvedAt time.Time `bson:"resolved_at,omitempty"`
}

// RecordGap stores a gap.
func (s *TriggerState) RecordGap(ctx context.Context, g triggers.Gap) error {
	count := g.Count
	if count == 0 {
		count = 1
	}
	_, err := s.gaps().InsertOne(ctx, gapDoc{
		ID: xid.New().String(), Deployment: s.Deployment, Realm: g.Realm, Collection: g.Collection, Trigger: g.Trigger,
		Reason: g.Reason, From: g.From.UTC(), To: g.To.UTC(), Count: count, Keys: g.Keys, RecordedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("record trigger gap: %w", err)
	}
	return nil
}

// Status reads the lease and the position as they are stored.
func (s *TriggerState) Status(ctx context.Context) (triggers.DispatcherStatus, error) {
	var d struct {
		Holder  string      `bson:"holder"`
		Epoch   int64       `bson:"epoch"`
		Expires time.Time   `bson:"expires_at"`
		Token   bson.Binary `bson:"token"`
		At      time.Time   `bson:"at"`
	}
	err := s.state().FindOne(ctx, byID(s.id())).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return triggers.DispatcherStatus{}, nil
	}
	if err != nil {
		return triggers.DispatcherStatus{}, err
	}
	st := triggers.DispatcherStatus{Found: true, Epoch: d.Epoch, Holder: d.Holder, LeaseExpires: d.Expires.UTC(), WatermarkAt: d.At.UTC()}
	st.Held = d.Holder != "" && time.Now().Before(d.Expires)
	return st, nil
}

// realmFilter selects the gaps of a realm: its own and those that cover every realm.
func (s *TriggerState) realmFilter(realm string) bson.D {
	return bson.D{{Key: "deployment", Value: s.Deployment}, {Key: "$or", Value: bson.A{
		bson.D{{Key: "realm", Value: realm}},
		bson.D{{Key: "realm", Value: bson.D{{Key: "$exists", Value: false}}}},
	}}}
}

// Gaps lists a realm's gaps, newest first.
func (s *TriggerState) Gaps(ctx context.Context, realm string, all bool, limit int) ([]triggers.GapRecord, error) {
	filter := s.realmFilter(realm)
	if !all {
		filter = append(filter, bson.E{Key: "resolved_at", Value: bson.D{{Key: "$exists", Value: false}}})
	}
	if limit <= 0 {
		limit = 100
	}
	docs, err := findAll[gapDoc](ctx, s.gaps(), filter, options.Find().SetSort(bson.D{{Key: "recorded_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	out := make([]triggers.GapRecord, len(docs))
	for i, d := range docs {
		out[i] = triggers.GapRecord{
			Realm: d.Realm, Collection: d.Collection, Trigger: d.Trigger, Reason: d.Reason,
			From: d.From.UTC(), To: d.To.UTC(), Count: d.Count, Keys: d.Keys,
			ID: d.ID, RecordedAt: d.RecordedAt.UTC(), ResolvedAt: d.ResolvedAt.UTC(),
		}
		if d.ResolvedAt.IsZero() {
			out[i].ResolvedAt = time.Time{}
		}
	}
	return out, nil
}

// ResolveGap closes an open gap of the realm.
func (s *TriggerState) ResolveGap(ctx context.Context, realm, id string, at time.Time) (bool, error) {
	filter := append(s.realmFilter(realm), bson.E{Key: "_id", Value: id}, bson.E{Key: "resolved_at", Value: bson.D{{Key: "$exists", Value: false}}})
	res, err := s.gaps().UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{{Key: "resolved_at", Value: at.UTC()}}}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

// OpenGaps counts a realm's unresolved gaps.
func (s *TriggerState) OpenGaps(ctx context.Context, realm string) (int, error) {
	filter := append(s.realmFilter(realm), bson.E{Key: "resolved_at", Value: bson.D{{Key: "$exists", Value: false}}})
	n, err := s.gaps().CountDocuments(ctx, filter)
	return int(n), err
}
