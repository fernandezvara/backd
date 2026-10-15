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
	_, err := s.state().UpdateOne(ctx, bson.D{{Key: "_id", Value: s.id()}},
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
	err := s.state().FindOne(ctx, bson.D{{Key: "_id", Value: s.id()}}).Decode(&d)
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
