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

type oauthStateDoc struct {
	ID            string    `bson:"_id"`
	Provider      string    `bson:"provider"`
	Intent        string    `bson:"intent"`
	CodeChallenge string    `bson:"code_challenge"`
	Verifier      string    `bson:"verifier"`
	Nonce         string    `bson:"nonce"`
	RedirectTo    string    `bson:"redirect_to"`
	InvitationID  string    `bson:"invitation_id,omitempty"`
	LinkUserID    string    `bson:"link_user_id,omitempty"`
	Locales       []string  `bson:"locales,omitempty"`
	CreatedAt     time.Time `bson:"created_at"`
	ExpiresAt     time.Time `bson:"expires_at"`
}

type loginCodeDoc struct {
	ID            string    `bson:"_id"`
	UserID        string    `bson:"user_id"`
	CodeChallenge string    `bson:"code_challenge"`
	CreatedAt     time.Time `bson:"created_at"`
	ExpiresAt     time.Time `bson:"expires_at"`
}

// PutOAuthState stores a sign-in attempt.
func (s *AuthStore) PutOAuthState(ctx context.Context, st auth.OAuthState) error {
	_, err := s.db.Collection(OAuthStatesCollection).InsertOne(ctx, oauthStateDoc(st))
	return err
}

// ClaimOAuthState deletes and returns the unexpired attempt with that id: one
// atomic step, so a state can't be used twice. TTL removal is lazy, so the expiry
// is checked here too.
func (s *AuthStore) ClaimOAuthState(ctx context.Context, id string, now time.Time) (auth.OAuthState, error) {
	var d oauthStateDoc
	err := s.db.Collection(OAuthStatesCollection).FindOneAndDelete(ctx, bson.D{{Key: "_id", Value: id}, {Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}}},
		options.FindOneAndDelete()).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.OAuthState{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.OAuthState{}, err
	}
	st := auth.OAuthState(d)
	st.CreatedAt, st.ExpiresAt = d.CreatedAt.UTC(), d.ExpiresAt.UTC()
	return st, nil
}

// PutLoginCode stores a one-time login code.
func (s *AuthStore) PutLoginCode(ctx context.Context, lc auth.LoginCode) error {
	_, err := s.db.Collection(OAuthCodesCollection).InsertOne(ctx, loginCodeDoc(lc))
	return err
}

// ClaimLoginCode deletes and returns the unexpired code with that id.
func (s *AuthStore) ClaimLoginCode(ctx context.Context, id string, now time.Time) (auth.LoginCode, error) {
	var d loginCodeDoc
	err := s.db.Collection(OAuthCodesCollection).FindOneAndDelete(ctx, bson.D{{Key: "_id", Value: id}, {Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.LoginCode{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.LoginCode{}, err
	}
	lc := auth.LoginCode(d)
	lc.CreatedAt, lc.ExpiresAt = d.CreatedAt.UTC(), d.ExpiresAt.UTC()
	return lc, nil
}
