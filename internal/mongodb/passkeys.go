package mongodb

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/passkey"
)

// PasskeysCollection holds one document per registered passkey (design/passkeys.md §4). Everything
// in it is public: the credential id, the COSE public key and flags; no secret.
const PasskeysCollection = "passkeys"

type passkeyDoc struct {
	ID             string     `bson:"_id"`
	UserID         string     `bson:"user_id"`
	CredentialID   []byte     `bson:"credential_id"`
	PublicKey      []byte     `bson:"public_key"`
	RPID           string     `bson:"rp_id"`
	SignCount      int64      `bson:"sign_count"`
	Transports     []string   `bson:"transports,omitempty"`
	AAGUID         []byte     `bson:"aaguid,omitempty"`
	BackupEligible bool       `bson:"backup_eligible"`
	BackupState    bool       `bson:"backup_state"`
	CloneSuspected bool       `bson:"clone_suspected,omitempty"`
	Name           string     `bson:"name"`
	CreatedAt      time.Time  `bson:"created_at"`
	LastUsedAt     *time.Time `bson:"last_used_at,omitempty"`
}

func (d passkeyDoc) credential() passkey.Credential {
	c := passkey.Credential{
		ID: d.ID, UserID: d.UserID, CredentialID: d.CredentialID, PublicKey: d.PublicKey, RPID: d.RPID,
		SignCount: uint32(d.SignCount), Transports: d.Transports, AAGUID: d.AAGUID,
		BackupEligible: d.BackupEligible, BackupState: d.BackupState, CloneSuspected: d.CloneSuspected,
		Name: d.Name, CreatedAt: d.CreatedAt.UTC(),
	}
	if d.LastUsedAt != nil {
		c.LastUsedAt = d.LastUsedAt.UTC()
	}
	return c
}

func (s *AuthStore) passkeys() *mongo.Collection { return s.db.Collection(PasskeysCollection) }

// Add stores a credential; a credential id registered twice is refused by the unique index.
func (s *AuthStore) Add(ctx context.Context, c passkey.Credential) error {
	d := passkeyDoc{
		ID: c.ID, UserID: c.UserID, CredentialID: c.CredentialID, PublicKey: c.PublicKey, RPID: c.RPID,
		SignCount: int64(c.SignCount), Transports: c.Transports, AAGUID: c.AAGUID,
		BackupEligible: c.BackupEligible, BackupState: c.BackupState, CloneSuspected: c.CloneSuspected,
		Name: c.Name, CreatedAt: c.CreatedAt,
	}
	if !c.LastUsedAt.IsZero() {
		d.LastUsedAt = &c.LastUsedAt
	}
	_, err := s.passkeys().InsertOne(ctx, d)
	if mongo.IsDuplicateKeyError(err) {
		return passkey.ErrInvalid
	}
	return err
}

func (s *AuthStore) ByCredentialID(ctx context.Context, id []byte) (passkey.Credential, error) {
	d, err := getOne[passkeyDoc](ctx, s.passkeys(), bson.D{{Key: "credential_id", Value: id}}, passkey.ErrUnknownCredential)
	if err != nil {
		return passkey.Credential{}, err
	}
	return d.credential(), nil
}

func (s *AuthStore) ListByUser(ctx context.Context, userID string) ([]passkey.Credential, error) {
	cur, err := s.passkeys().Find(ctx, bson.D{{Key: "user_id", Value: userID}}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []passkeyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]passkey.Credential, len(docs))
	for i, d := range docs {
		out[i] = d.credential()
	}
	return out, nil
}

func (s *AuthStore) Update(ctx context.Context, credentialID []byte, u passkey.Update) error {
	set := bson.D{}
	if u.SignCount != nil {
		set = append(set, bson.E{Key: "sign_count", Value: int64(*u.SignCount)})
	}
	if u.LastUsedAt != nil {
		set = append(set, bson.E{Key: "last_used_at", Value: *u.LastUsedAt})
	}
	if u.BackupState != nil {
		set = append(set, bson.E{Key: "backup_state", Value: *u.BackupState})
	}
	if u.CloneSuspected != nil {
		set = append(set, bson.E{Key: "clone_suspected", Value: *u.CloneSuspected})
	}
	if u.Name != nil {
		set = append(set, bson.E{Key: "name", Value: *u.Name})
	}
	if len(set) == 0 {
		return nil
	}
	res, err := s.passkeys().UpdateOne(ctx, bson.D{{Key: "credential_id", Value: credentialID}}, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return passkey.ErrUnknownCredential
	}
	return nil
}

func (s *AuthStore) Delete(ctx context.Context, userID, id string) error {
	res, err := s.passkeys().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "user_id", Value: userID}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return passkey.ErrUnknownCredential
	}
	return nil
}

func (s *AuthStore) CountByUser(ctx context.Context, userID string) (int, error) {
	n, err := s.passkeys().CountDocuments(ctx, bson.D{{Key: "user_id", Value: userID}})
	return int(n), err
}

// UserHandle returns the user's opaque WebAuthn handle (32 random bytes), making it on first use.
// It lives on the user document; two requests racing make one, and both read the same.
func (s *AuthStore) UserHandle(ctx context.Context, userID string) ([]byte, error) {
	read := func() ([]byte, error) {
		var d struct {
			Handle []byte `bson:"passkey_handle"`
		}
		err := s.users().FindOne(ctx, byID(userID), options.FindOne().SetProjection(bson.D{{Key: "passkey_handle", Value: 1}})).Decode(&d)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, auth.ErrNotFound
		}
		return d.Handle, err
	}
	h, err := read()
	if err != nil || len(h) > 0 {
		return h, err
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	if _, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}, {Key: "passkey_handle", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "passkey_handle", Value: fresh}}}}); err != nil {
		return nil, err
	}
	return read()
}

// UserByHandle returns the id of the user with that handle.
func (s *AuthStore) UserByHandle(ctx context.Context, handle []byte) (string, error) {
	var d struct {
		ID string `bson:"_id"`
	}
	err := s.users().FindOne(ctx, bson.D{{Key: "passkey_handle", Value: handle}}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", passkey.ErrUnknownCredential
	}
	return d.ID, err
}

// PasskeyRPIDs lists the relying party ids the stored credentials were registered under, for the
// startup warning when the configured id is not one of them.
func (s *AuthStore) PasskeyRPIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.passkeys().Distinct(ctx, "rp_id", bson.D{}).Decode(&ids)
	return ids, err
}
