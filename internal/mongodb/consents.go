package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

type consentRecordDoc struct {
	ID        string    `bson:"_id"`
	UserID    string    `bson:"user"`
	Consent   string    `bson:"consent"`
	Version   string    `bson:"version"`
	Locale    string    `bson:"locale,omitempty"`
	TextHash  string    `bson:"text_hash,omitempty"`
	Action    string    `bson:"action"`
	Method    string    `bson:"method"`
	Evidence  string    `bson:"evidence,omitempty"`
	At        time.Time `bson:"at"`
	DeleteAt  time.Time `bson:"expires_at,omitempty"`
	IP        string    `bson:"ip,omitempty"`
	UserAgent string    `bson:"user_agent,omitempty"`
}

func (s *AuthStore) consentRecords() *mongo.Collection {
	return s.db.Collection(ConsentRecordsCollection)
}

func (s *AuthStore) AppendConsentRecord(ctx context.Context, r auth.ConsentRecord) error {
	_, err := s.consentRecords().InsertOne(ctx, consentRecordDoc{
		ID: r.ID, UserID: r.UserID, Consent: r.Consent, Version: r.Version, Locale: r.Locale, TextHash: r.TextHash,
		Action: r.Action, Method: r.Method, Evidence: r.Evidence, At: r.At, DeleteAt: r.DeleteAt, IP: r.IP, UserAgent: r.UserAgent,
	})
	return err
}

func (s *AuthStore) ListConsentRecords(ctx context.Context, userID string) ([]auth.ConsentRecord, error) {
	docs, err := findAll[consentRecordDoc](ctx, s.consentRecords(), bson.D{{Key: "user", Value: userID}},
		options.Find().SetSort(bson.D{{Key: "at", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := make([]auth.ConsentRecord, len(docs))
	for i, d := range docs {
		out[i] = auth.ConsentRecord{
			ID: d.ID, UserID: d.UserID, Consent: d.Consent, Version: d.Version, Locale: d.Locale, TextHash: d.TextHash,
			Action: d.Action, Method: d.Method, Evidence: d.Evidence, At: d.At.UTC(), DeleteAt: d.DeleteAt.UTC(), IP: d.IP, UserAgent: d.UserAgent,
		}
	}
	return out, nil
}

func (s *AuthStore) SetUserConsent(ctx context.Context, userID, consent, version string) error {
	field := "consents." + consent
	var update bson.D
	if version == "" {
		update = bson.D{{Key: "$unset", Value: bson.D{{Key: field, Value: ""}}}}
	} else {
		update = bson.D{{Key: "$set", Value: bson.D{{Key: field, Value: version}}}}
	}
	res, err := s.users().UpdateOne(ctx, byID(userID), update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) ConsentVersionsInUse(ctx context.Context) ([]auth.ConsentRef, error) {
	cur, err := s.consentRecords().Aggregate(ctx, bson.A{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "consent", Value: "$consent"}, {Key: "version", Value: "$version"}}}}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID struct {
			Consent string `bson:"consent"`
			Version string `bson:"version"`
		} `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	var out []auth.ConsentRef
	for _, r := range rows {
		if r.ID.Version != "" {
			out = append(out, auth.ConsentRef{Consent: r.ID.Consent, Version: r.ID.Version})
		}
	}
	return out, nil
}

func (s *AuthStore) ReduceConsentRecords(ctx context.Context, userID, pseudonym string, deleteAt time.Time) (int64, error) {
	set := bson.D{{Key: "user", Value: pseudonym}}
	if !deleteAt.IsZero() {
		set = append(set, bson.E{Key: "expires_at", Value: deleteAt})
	}
	res, err := s.consentRecords().UpdateMany(ctx, bson.D{{Key: "user", Value: userID}}, bson.D{
		{Key: "$set", Value: set},
		{Key: "$unset", Value: bson.D{{Key: "ip", Value: ""}, {Key: "user_agent", Value: ""}}},
	})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

func (s *AuthStore) DeleteConsentRecordsOf(ctx context.Context, userID string) (int64, error) {
	res, err := s.consentRecords().DeleteMany(ctx, bson.D{{Key: "user", Value: userID}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
