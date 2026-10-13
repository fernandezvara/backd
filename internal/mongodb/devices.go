package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type deviceDoc struct {
	ID        string    `bson:"_id"`
	UserID    string    `bson:"user_id"`
	SessionID string    `bson:"session_id"`
	Platform  string    `bson:"platform"`
	Name      string    `bson:"name,omitempty"`
	TokenHash string    `bson:"token_hash"`
	Token     string    `bson:"token"`
	CreatedAt time.Time `bson:"created_at"`
	LastSeen  time.Time `bson:"last_seen"`
}

func (d deviceDoc) device() auth.Device {
	out := auth.Device(d)
	out.CreatedAt, out.LastSeen = d.CreatedAt.UTC(), d.LastSeen.UTC()
	return out
}

func (s *AuthStore) devices() *mongo.Collection { return s.db.Collection(DevicesCollection) }

// dropDevicesOf removes a user's devices, with their sessions.
func (s *AuthStore) dropDevicesOf(ctx context.Context, userID string) error {
	_, err := s.devices().DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}})
	return err
}

func (s *AuthStore) PutDevice(ctx context.Context, d auth.Device) (auth.Device, bool, error) {
	// A token that belongs to someone else moves: the earlier owner's document goes and a new
	// one (a new id) is made, so an id of the first owner never reaches the second.
	res, err := s.devices().DeleteMany(ctx, bson.D{{Key: "token_hash", Value: d.TokenHash}, {Key: "user_id", Value: bson.D{{Key: "$ne", Value: d.UserID}}}})
	if err != nil {
		return auth.Device{}, false, err
	}
	moved := res.DeletedCount > 0
	set := bson.D{{Key: "session_id", Value: d.SessionID}, {Key: "platform", Value: d.Platform}, {Key: "token", Value: d.Token}, {Key: "last_seen", Value: d.LastSeen}}
	if d.Name != "" {
		set = append(set, bson.E{Key: "name", Value: d.Name})
	}
	update := bson.D{{Key: "$set", Value: set}, {Key: "$setOnInsert", Value: bson.D{{Key: "_id", Value: d.ID}, {Key: "created_at", Value: d.CreatedAt}}}}
	filter := bson.D{{Key: "token_hash", Value: d.TokenHash}, {Key: "user_id", Value: d.UserID}}
	var doc deviceDoc
	for range 2 {
		err := s.devices().FindOneAndUpdate(ctx, filter, update,
			options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&doc)
		if mongo.IsDuplicateKeyError(err) {
			// Two registrations of one token at once: the other one won the insert; update it.
			continue
		}
		if err != nil {
			return auth.Device{}, false, err
		}
		return doc.device(), moved, nil
	}
	return auth.Device{}, false, errors.New("the device could not be registered: try again")
}

func (s *AuthStore) ListDevices(ctx context.Context, userID string) ([]auth.Device, error) {
	cur, err := s.devices().Find(ctx, bson.D{{Key: "user_id", Value: userID}},
		options.Find().SetSort(bson.D{{Key: "last_seen", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var docs []deviceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.Device, len(docs))
	for i, d := range docs {
		out[i] = d.device()
	}
	return out, nil
}

func (s *AuthStore) DeleteDevice(ctx context.Context, userID, id string) error {
	res, err := s.devices().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "user_id", Value: userID}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) DeleteOldestDevices(ctx context.Context, userID string, keep int, keepID string) (int64, error) {
	cur, err := s.devices().Find(ctx, bson.D{{Key: "user_id", Value: userID}},
		options.Find().SetSort(bson.D{{Key: "last_seen", Value: -1}}).SetProjection(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return 0, err
	}
	var all []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &all); err != nil {
		return 0, err
	}
	if len(all) <= keep {
		return 0, nil
	}
	// keepID counts as one of the devices kept; the most recently seen others fill the rest.
	room := keep - 1
	ids := bson.A{}
	for _, d := range all {
		switch {
		case d.ID == keepID:
		case room > 0:
			room--
		default:
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	res, err := s.devices().DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: "user_id", Value: userID}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (s *AuthStore) DeleteStaleDevices(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.devices().DeleteMany(ctx, bson.D{{Key: "last_seen", Value: bson.D{{Key: "$lt", Value: before}}}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// DeleteOrphanedDevices removes the devices whose session is gone or past its expiry, looking
// the sessions up in the same database.
func (s *AuthStore) DeleteOrphanedDevices(ctx context.Context, now time.Time) (int64, error) {
	cur, err := s.devices().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$lookup", Value: bson.D{{Key: "from", Value: SessionsCollection}, {Key: "localField", Value: "session_id"}, {Key: "foreignField", Value: "_id"}, {Key: "as", Value: "s"}}}},
		{{Key: "$match", Value: bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "s", Value: bson.D{{Key: "$size", Value: 0}}}},
			bson.D{{Key: "s.expires_at", Value: bson.D{{Key: "$lte", Value: now}}}},
		}}}}},
		{{Key: "$project", Value: bson.D{{Key: "_id", Value: 1}}}},
	})
	if err != nil {
		return 0, err
	}
	var gone []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &gone); err != nil {
		return 0, err
	}
	if len(gone) == 0 {
		return 0, nil
	}
	ids := make(bson.A, len(gone))
	for i, g := range gone {
		ids[i] = g.ID
	}
	res, err := s.devices().DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
