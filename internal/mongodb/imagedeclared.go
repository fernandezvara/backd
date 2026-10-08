package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type imageDeclaredDoc struct {
	Key         string `bson:"_id"`
	Fingerprint string `bson:"fingerprint"`
}

func (s *AuthStore) imageDeclared() *mongo.Collection {
	return s.db.Collection(ImageDeclaredCollection)
}

// ImageDeclarations returns the fingerprints recorded for the declared image versions.
func (s *AuthStore) ImageDeclarations(ctx context.Context) (map[string]string, error) {
	cur, err := s.imageDeclared().Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var docs []imageDeclaredDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(docs))
	for _, d := range docs {
		out[d.Key] = d.Fingerprint
	}
	return out, nil
}

// SetImageDeclaration records a declared version's fingerprint.
func (s *AuthStore) SetImageDeclaration(ctx context.Context, key, fingerprint string) error {
	_, err := s.imageDeclared().ReplaceOne(ctx, bson.D{{Key: "_id", Value: key}}, imageDeclaredDoc{Key: key, Fingerprint: fingerprint}, options.Replace().SetUpsert(true))
	return err
}
