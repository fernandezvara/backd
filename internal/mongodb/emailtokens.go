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

type emailTokenDoc struct {
	ID         string     `bson:"_id"`
	Purpose    string     `bson:"purpose"`
	UserID     string     `bson:"user_id"`
	RedirectTo string     `bson:"redirect_to,omitempty"`
	Address    string     `bson:"address,omitempty"`
	Invitation string     `bson:"invitation_id,omitempty"`
	CreatedAt  time.Time  `bson:"created_at"`
	ExpiresAt  time.Time  `bson:"expires_at"`
	UsedAt     *time.Time `bson:"used_at,omitempty"`
}

func (s *AuthStore) emailTokens() *mongo.Collection { return s.db.Collection(EmailTokensCollection) }

// CreateEmailToken stores a token's hash.
func (s *AuthStore) CreateEmailToken(ctx context.Context, t auth.EmailToken) error {
	_, err := s.emailTokens().InsertOne(ctx, emailTokenDoc{
		ID: t.Hash, Purpose: t.Purpose, UserID: t.UserID, RedirectTo: t.RedirectTo, Address: t.Address, Invitation: t.InvitationID, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
	})
	return err
}

// GetEmailToken returns a token by hash, used or not.
func (s *AuthStore) GetEmailToken(ctx context.Context, hash string) (auth.EmailToken, error) {
	var d emailTokenDoc
	err := s.emailTokens().FindOne(ctx, bson.D{{Key: "_id", Value: hash}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.EmailToken{}, auth.ErrInvalidToken
	}
	if err != nil {
		return auth.EmailToken{}, err
	}
	t := auth.EmailToken{Hash: d.ID, Purpose: d.Purpose, UserID: d.UserID, RedirectTo: d.RedirectTo, Address: d.Address, InvitationID: d.Invitation, CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC()}
	if d.UsedAt != nil {
		t.UsedAt = d.UsedAt.UTC()
	}
	return t, nil
}

// RedeemEmailToken marks the token used in one update that matches only an
// unused, unexpired token of this purpose, so two parallel redemptions can't
// both succeed.
func (s *AuthStore) RedeemEmailToken(ctx context.Context, hash, purpose string, now time.Time) (auth.EmailToken, error) {
	filter := bson.D{
		{Key: "_id", Value: hash}, {Key: "purpose", Value: purpose},
		{Key: "used_at", Value: bson.D{{Key: "$exists", Value: false}}},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}
	var d emailTokenDoc
	err := s.emailTokens().FindOneAndUpdate(ctx, filter, bson.D{{Key: "$set", Value: bson.D{{Key: "used_at", Value: now}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.EmailToken{}, auth.ErrInvalidToken
	}
	if err != nil {
		return auth.EmailToken{}, err
	}
	return auth.EmailToken{Hash: d.ID, Purpose: d.Purpose, UserID: d.UserID, RedirectTo: d.RedirectTo, Address: d.Address, InvitationID: d.Invitation,
		CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(), UsedAt: now}, nil
}

// DeleteEmailTokensOfUser removes every token of the user.
func (s *AuthStore) DeleteEmailTokensOfUser(ctx context.Context, userID string) error {
	_, err := s.emailTokens().DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}})
	return err
}

// InvalidateEmailTokens marks used every unused token of the user and purpose.
func (s *AuthStore) InvalidateEmailTokens(ctx context.Context, userID, purpose string, now time.Time) error {
	_, err := s.emailTokens().UpdateMany(ctx,
		bson.D{{Key: "user_id", Value: userID}, {Key: "purpose", Value: purpose}, {Key: "used_at", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "used_at", Value: now}}}})
	return err
}
