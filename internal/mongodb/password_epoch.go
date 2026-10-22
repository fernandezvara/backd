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

const passwordEpochID = "password_epoch"

type passwordEpochDoc struct {
	ValidAfter time.Time `bson:"valid_after"`
	SetBy      string    `bson:"set_by"`
	SetAt      time.Time `bson:"set_at"`
	Reason     string    `bson:"reason,omitempty"`
}

// PasswordEpoch implements auth.Store.
func (s *AuthStore) PasswordEpoch(ctx context.Context) (auth.PasswordEpoch, error) {
	var d passwordEpochDoc
	err := s.db.Collection(SecurityStateCollection).FindOne(ctx, bson.D{{Key: "_id", Value: passwordEpochID}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.PasswordEpoch{}, nil
	}
	if err != nil {
		return auth.PasswordEpoch{}, err
	}
	return auth.PasswordEpoch{ValidAfter: d.ValidAfter.UTC(), SetBy: d.SetBy, SetAt: d.SetAt.UTC(), Reason: d.Reason}, nil
}

// SetPasswordEpoch implements auth.Store: the filter only matches an epoch earlier than the
// new one, so an equal or later one makes the upsert collide on _id and nothing changes.
func (s *AuthStore) SetPasswordEpoch(ctx context.Context, e auth.PasswordEpoch) (bool, error) {
	filter := bson.D{{Key: "_id", Value: passwordEpochID}, {Key: "valid_after", Value: bson.D{{Key: "$lt", Value: e.ValidAfter}}}}
	set := bson.D{{Key: "valid_after", Value: e.ValidAfter}, {Key: "set_by", Value: e.SetBy}, {Key: "set_at", Value: e.SetAt}}
	unset := bson.D{}
	if e.Reason != "" {
		set = append(set, bson.E{Key: "reason", Value: e.Reason})
	} else {
		unset = append(unset, bson.E{Key: "reason", Value: ""})
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	_, err := s.db.Collection(SecurityStateCollection).UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	return err == nil, err
}
