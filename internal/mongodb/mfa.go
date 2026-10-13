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

type challengeDoc struct {
	ID        string            `bson:"_id"`
	Kind      string            `bson:"kind"`
	UserID    string            `bson:"user_id"`
	Factor    string            `bson:"factor,omitempty"`
	NewUser   bool              `bson:"new_user,omitempty"`
	Profile   map[string]string `bson:"profile,omitempty"`
	Secret    string            `bson:"secret,omitempty"`
	Attempts  int               `bson:"attempts"`
	CreatedAt time.Time         `bson:"created_at"`
	ExpiresAt time.Time         `bson:"expires_at"`
}

func (d challengeDoc) challenge() auth.AuthChallenge {
	c := auth.AuthChallenge(d)
	c.CreatedAt, c.ExpiresAt = d.CreatedAt.UTC(), d.ExpiresAt.UTC()
	return c
}

type mfaDoc struct {
	ID            string    `bson:"_id"`
	Secret        string    `bson:"secret"`
	EnabledAt     time.Time `bson:"enabled_at"`
	LastStep      int64     `bson:"last_step"`
	RecoveryCodes []string  `bson:"recovery_codes"`
}

func (s *AuthStore) challenges() *mongo.Collection { return s.db.Collection(AuthChallengesCollection) }
func (s *AuthStore) mfa() *mongo.Collection        { return s.db.Collection(MFACollection) }

// dropSecondFactors removes a user's second factors and pending steps.
func (s *AuthStore) dropSecondFactors(ctx context.Context, userID string) error {
	if _, err := s.mfa().DeleteOne(ctx, bson.D{{Key: "_id", Value: userID}}); err != nil {
		return err
	}
	_, err := s.challenges().DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}})
	return err
}

func unexpired(id string, now time.Time) bson.D {
	return bson.D{{Key: "_id", Value: id}, {Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}}}
}

func (s *AuthStore) PutChallenge(ctx context.Context, c auth.AuthChallenge) error {
	_, err := s.challenges().InsertOne(ctx, challengeDoc(c))
	return err
}

func (s *AuthStore) GetChallenge(ctx context.Context, id string, now time.Time) (auth.AuthChallenge, error) {
	var d challengeDoc
	err := s.challenges().FindOne(ctx, unexpired(id, now)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.AuthChallenge{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.AuthChallenge{}, err
	}
	return d.challenge(), nil
}

func (s *AuthStore) ClaimChallenge(ctx context.Context, id string, now time.Time) (auth.AuthChallenge, error) {
	var d challengeDoc
	err := s.challenges().FindOneAndDelete(ctx, unexpired(id, now)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.AuthChallenge{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.AuthChallenge{}, err
	}
	return d.challenge(), nil
}

func (s *AuthStore) CountChallengeAttempt(ctx context.Context, id string, now time.Time) (auth.AuthChallenge, error) {
	var d challengeDoc
	err := s.challenges().FindOneAndUpdate(ctx, unexpired(id, now), bson.D{{Key: "$inc", Value: bson.D{{Key: "attempts", Value: 1}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.AuthChallenge{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.AuthChallenge{}, err
	}
	return d.challenge(), nil
}

func (s *AuthStore) DeleteChallenge(ctx context.Context, id string) error {
	_, err := s.challenges().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	return err
}

func (s *AuthStore) DeleteChallengesOfUser(ctx context.Context, userID string, kinds ...string) error {
	filter := bson.D{{Key: "user_id", Value: userID}}
	if len(kinds) > 0 {
		filter = append(filter, bson.E{Key: "kind", Value: bson.D{{Key: "$in", Value: kinds}}})
	}
	_, err := s.challenges().DeleteMany(ctx, filter)
	return err
}

func (s *AuthStore) MFAOf(ctx context.Context, userID string) (auth.MFARecord, error) {
	var d mfaDoc
	err := s.mfa().FindOne(ctx, bson.D{{Key: "_id", Value: userID}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.MFARecord{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.MFARecord{}, err
	}
	return auth.MFARecord{UserID: d.ID, Secret: d.Secret, EnabledAt: d.EnabledAt.UTC(), LastStep: d.LastStep, RecoveryCodes: d.RecoveryCodes}, nil
}

func (s *AuthStore) PutMFA(ctx context.Context, r auth.MFARecord) error {
	codes := r.RecoveryCodes
	if codes == nil {
		codes = []string{}
	}
	_, err := s.mfa().ReplaceOne(ctx, bson.D{{Key: "_id", Value: r.UserID}},
		mfaDoc{ID: r.UserID, Secret: r.Secret, EnabledAt: r.EnabledAt, LastStep: r.LastStep, RecoveryCodes: codes},
		options.Replace().SetUpsert(true))
	return err
}

func (s *AuthStore) DeleteMFA(ctx context.Context, userID string) error {
	_, err := s.mfa().DeleteOne(ctx, bson.D{{Key: "_id", Value: userID}})
	return err
}

func (s *AuthStore) MFAUserIDs(ctx context.Context) ([]string, error) {
	cur, err := s.mfa().Find(ctx, bson.D{}, options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out, nil
}

// AdvanceMFAStep moves last_step forward in one conditional update: a step that is not
// greater than the stored one matches nothing, which is the replay.
func (s *AuthStore) AdvanceMFAStep(ctx context.Context, userID string, step int64) (bool, error) {
	res, err := s.mfa().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}, {Key: "last_step", Value: bson.D{{Key: "$lt", Value: step}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "last_step", Value: step}}}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// UseRecoveryCode pulls the code from the array in one atomic update: two requests with
// the same code can't both succeed.
func (s *AuthStore) UseRecoveryCode(ctx context.Context, userID, hash string) (int, bool, error) {
	var d mfaDoc
	err := s.mfa().FindOneAndUpdate(ctx,
		bson.D{{Key: "_id", Value: userID}, {Key: "recovery_codes", Value: hash}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: "recovery_codes", Value: hash}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return len(d.RecoveryCodes), true, nil
}
