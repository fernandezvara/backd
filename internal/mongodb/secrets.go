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

type secretDoc struct {
	ID             string    `bson:"_id"`
	Database       string    `bson:"database"`
	Name           string    `bson:"name"`
	Type           string    `bson:"type,omitempty"`
	Fingerprint    string    `bson:"fingerprint,omitempty"`
	Version        int       `bson:"version,omitempty"`
	PrevCiphertext string    `bson:"prev_ciphertext,omitempty"`
	PrevNonce      string    `bson:"prev_nonce,omitempty"`
	PrevVersion    int       `bson:"prev_version,omitempty"`
	Ciphertext     string    `bson:"ciphertext"`
	Nonce          string    `bson:"nonce"`
	KeyID          string    `bson:"key_id"`
	CreatedAt      time.Time `bson:"created_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
	UpdatedBy      string    `bson:"updated_by"`
}

func (d secretDoc) secret() auth.Secret {
	return auth.Secret{
		Database: d.Database, Name: d.Name, Type: d.Type, Fingerprint: d.Fingerprint,
		Version: d.Version, PrevCiphertext: d.PrevCiphertext, PrevNonce: d.PrevNonce, PrevVersion: d.PrevVersion, Ciphertext: d.Ciphertext, Nonce: d.Nonce, KeyID: d.KeyID,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), UpdatedBy: d.UpdatedBy,
	}
}

func (s *AuthStore) secrets() *mongo.Collection { return s.db.Collection(SecretsCollection) }

// secretID is the deterministic document id of a secret: unique by
// construction, so UpsertSecret needs no separate existence check.
func secretID(database, name string) string {
	scope := database
	if scope == "" {
		scope = "realm"
	}
	return scope + ":" + name
}

// UpsertSecret creates or replaces the secret at (s.Database, s.Name),
// keeping its original created_at on an update.
func (s *AuthStore) UpsertSecret(ctx context.Context, sec auth.Secret) error {
	id := secretID(sec.Database, sec.Name)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "database", Value: sec.Database},
			{Key: "name", Value: sec.Name},
			{Key: "type", Value: sec.Type},
			{Key: "fingerprint", Value: sec.Fingerprint},
			{Key: "version", Value: sec.Version},
			{Key: "prev_ciphertext", Value: sec.PrevCiphertext},
			{Key: "prev_nonce", Value: sec.PrevNonce},
			{Key: "prev_version", Value: sec.PrevVersion},
			{Key: "ciphertext", Value: sec.Ciphertext},
			{Key: "nonce", Value: sec.Nonce},
			{Key: "key_id", Value: sec.KeyID},
			{Key: "updated_at", Value: sec.UpdatedAt},
			{Key: "updated_by", Value: sec.UpdatedBy},
		}},
		{Key: "$setOnInsert", Value: bson.D{{Key: "created_at", Value: sec.CreatedAt}}},
	}
	_, err := s.secrets().UpdateByID(ctx, id, update, options.UpdateOne().SetUpsert(true))
	return err
}

// CreateSecret stores a secret that must not exist yet: the deterministic id makes the insert
// the existence check.
func (s *AuthStore) CreateSecret(ctx context.Context, sec auth.Secret) error {
	_, err := s.secrets().InsertOne(ctx, secretDoc{
		ID: secretID(sec.Database, sec.Name), Database: sec.Database, Name: sec.Name, Type: sec.Type, Fingerprint: sec.Fingerprint, Version: sec.Version,
		Ciphertext: sec.Ciphertext, Nonce: sec.Nonce, KeyID: sec.KeyID, CreatedAt: sec.CreatedAt, UpdatedAt: sec.UpdatedAt, UpdatedBy: sec.UpdatedBy,
	})
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrSecretExists
	}
	return err
}

// ReplaceKeySecret implements auth.Store: the filter holds the version the rotation started from,
// so two rotations can't both win.
func (s *AuthStore) ReplaceKeySecret(ctx context.Context, sec auth.Secret, ifVersion int) (bool, error) {
	versionIs := bson.D{{Key: "version", Value: ifVersion}}
	if ifVersion == 1 {
		versionIs = bson.D{{Key: "$or", Value: bson.A{bson.D{{Key: "version", Value: 1}}, bson.D{{Key: "version", Value: bson.D{{Key: "$exists", Value: false}}}}}}}
	}
	filter := append(bson.D{{Key: "_id", Value: secretID(sec.Database, sec.Name)}}, versionIs...)
	res, err := s.secrets().UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{
		{Key: "fingerprint", Value: sec.Fingerprint}, {Key: "version", Value: sec.Version},
		{Key: "ciphertext", Value: sec.Ciphertext}, {Key: "nonce", Value: sec.Nonce}, {Key: "key_id", Value: sec.KeyID},
		{Key: "prev_ciphertext", Value: sec.PrevCiphertext}, {Key: "prev_nonce", Value: sec.PrevNonce}, {Key: "prev_version", Value: sec.PrevVersion},
		{Key: "updated_at", Value: sec.UpdatedAt}, {Key: "updated_by", Value: sec.UpdatedBy},
	}}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

func (s *AuthStore) SecretByScope(ctx context.Context, database, name string) (auth.Secret, error) {
	var d secretDoc
	err := s.secrets().FindOne(ctx, bson.D{{Key: "_id", Value: secretID(database, name)}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Secret{}, auth.ErrSecretNotFound
	}
	if err != nil {
		return auth.Secret{}, err
	}
	return d.secret(), nil
}

func (s *AuthStore) ListSecrets(ctx context.Context) ([]auth.Secret, error) {
	opts := options.Find().SetSort(bson.D{{Key: "database", Value: 1}, {Key: "name", Value: 1}})
	cur, err := s.secrets().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, err
	}
	var docs []secretDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.Secret, len(docs))
	for i, d := range docs {
		out[i] = d.secret()
	}
	return out, nil
}

func (s *AuthStore) DeleteSecret(ctx context.Context, database, name string) error {
	res, err := s.secrets().DeleteOne(ctx, bson.D{{Key: "_id", Value: secretID(database, name)}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrSecretNotFound
	}
	return nil
}
