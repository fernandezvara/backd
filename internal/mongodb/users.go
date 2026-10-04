package mongodb

import (
	"context"
	"errors"
	"github.com/fernandezvara/backd/internal/registry"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

// AuthStore implements auth.Store on a realm's system database.
type AuthStore struct {
	db *mongo.Database
}

// NewAuthStore returns the auth store of realm.
func NewAuthStore(client *mongo.Client, realm string) *AuthStore {
	return &AuthStore{db: client.Database(SystemDatabaseName(realm))}
}

type userDoc struct {
	ID            string     `bson:"_id"`
	Email         string     `bson:"email"`
	EmailVerified bool       `bson:"email_verified"`
	ErasedAt      *time.Time `bson:"erased_at,omitempty"`
	PendingEmail  string     `bson:"pending_email,omitempty"`
	PreviousEmail string     `bson:"previous_email,omitempty"`
	Roles         []string   `bson:"roles"`
	Disabled      bool       `bson:"disabled"`
	Locale        string     `bson:"locale,omitempty"`
	AdminNetworks []string   `bson:"admin_networks,omitempty"`
	LoginNetworks []string   `bson:"login_networks,omitempty"`
	CreatedAt     time.Time  `bson:"created_at"`
	UpdatedAt     time.Time  `bson:"updated_at"`
}

func (d userDoc) user() auth.User {
	return auth.User{
		ID: d.ID, Email: d.Email, EmailVerified: d.EmailVerified, PendingEmail: d.PendingEmail, PreviousEmail: d.PreviousEmail, ErasedAt: utcOrZero(d.ErasedAt), Roles: d.Roles, Disabled: d.Disabled, Locale: d.Locale,
		AdminNetworks: networks(d.AdminNetworks), LoginNetworks: networks(d.LoginNetworks),
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
	}
}

// networks parses stored networks. They were validated when written, so
// an unparsable entry means someone edited the database by hand: it is
// dropped here, and the list becomes empty only if all were bad.
func networks(stored []string) registry.Networks {
	var out registry.Networks
	for _, s := range stored {
		if n, err := registry.ParseNetworks([]string{s}); err == nil {
			out = append(out, n...)
		}
	}
	return out
}

type identityDoc struct {
	ID           string    `bson:"_id"`
	UserID       string    `bson:"user_id"`
	Provider     string    `bson:"provider"`
	Subject      string    `bson:"subject"`
	PasswordHash string    `bson:"password_hash,omitempty"`
	CreatedAt    time.Time `bson:"created_at"`
	UpdatedAt    time.Time `bson:"updated_at"`
}

func (s *AuthStore) users() *mongo.Collection      { return s.db.Collection(UsersCollection) }
func (s *AuthStore) identities() *mongo.Collection { return s.db.Collection(IdentitiesCollection) }
func (s *AuthStore) sessions() *mongo.Collection   { return s.db.Collection(SessionsCollection) }
func (s *AuthStore) apiKeys() *mongo.Collection    { return s.db.Collection(APIKeysCollection) }
func (s *AuthStore) attempts() *mongo.Collection   { return s.db.Collection(LoginAttemptsCollection) }
func (s *AuthStore) invitations() *mongo.Collection {
	return s.db.Collection(InvitationsCollection)
}

func (s *AuthStore) CreateUser(ctx context.Context, u auth.User) error {
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	_, err := s.users().InsertOne(ctx, userDoc{
		ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerified, Roles: roles, Disabled: u.Disabled, Locale: u.Locale,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	})
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrEmailTaken
	}
	return err
}

func (s *AuthStore) UserByEmail(ctx context.Context, email string) (auth.User, error) {
	var d userDoc
	err := s.users().FindOne(ctx, bson.D{{Key: "email", Value: email}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	return d.user(), nil
}

func (s *AuthStore) UserByID(ctx context.Context, id string) (auth.User, error) {
	var d userDoc
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	return d.user(), nil
}

func (s *AuthStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	cur, err := s.users().Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "email", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []userDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.User, len(docs))
	for i, d := range docs {
		out[i] = d.user()
	}
	return out, nil
}

func (s *AuthStore) ListUsersPage(ctx context.Context, contains, after string, skip, limit int) ([]auth.User, bool, error) {
	email := bson.D{}
	if after != "" {
		email = append(email, bson.E{Key: "$gt", Value: after})
	}
	if contains != "" {
		// Emails are stored lower-case, so the match is case-sensitive on a
		// lower-cased text and can walk the email index instead of the documents.
		email = append(email, bson.E{Key: "$regex", Value: regexp.QuoteMeta(contains)})
	}
	filter := bson.D{}
	if len(email) > 0 {
		filter = bson.D{{Key: "email", Value: email}}
	}
	opts := options.Find().SetSort(bson.D{{Key: "email", Value: 1}}).SetSkip(int64(skip)).SetLimit(int64(limit) + 1)
	cur, err := s.users().Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	var docs []userDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	hasMore := len(docs) > limit
	docs = docs[:min(limit, len(docs))]
	out := make([]auth.User, len(docs))
	for i, d := range docs {
		out[i] = d.user()
	}
	return out, hasMore, nil
}

func (s *AuthStore) AddRoles(ctx context.Context, userID string, roles []string, now time.Time) error {
	return s.updateByID(ctx, userID, bson.D{
		{Key: "$addToSet", Value: bson.D{{Key: "roles", Value: bson.D{{Key: "$each", Value: roles}}}}},
		{Key: "$set", Value: bson.D{{Key: "updated_at", Value: now}}},
	})
}

func (s *AuthStore) RemoveRole(ctx context.Context, userID, role string, now time.Time) error {
	return s.updateByID(ctx, userID, bson.D{
		{Key: "$pull", Value: bson.D{{Key: "roles", Value: role}}},
		{Key: "$set", Value: bson.D{{Key: "updated_at", Value: now}}},
	})
}

func (s *AuthStore) updateByID(ctx context.Context, id string, update bson.D) error {
	res, err := s.users().UpdateByID(ctx, id, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) UpdateUser(ctx context.Context, id string, upd auth.UserUpdate, now time.Time) error {
	set := bson.D{{Key: "updated_at", Value: now}}
	unset := bson.D{}
	if upd.Email != nil {
		set = append(set, bson.E{Key: "email", Value: *upd.Email})
	}
	for key, v := range map[string]*string{"pending_email": upd.PendingEmail, "previous_email": upd.PreviousEmail} {
		switch {
		case v == nil:
		case *v == "":
			unset = append(unset, bson.E{Key: key, Value: ""})
		default:
			set = append(set, bson.E{Key: key, Value: *v})
		}
	}
	if upd.EmailVerified != nil {
		set = append(set, bson.E{Key: "email_verified", Value: *upd.EmailVerified})
	}
	if upd.Disabled != nil {
		set = append(set, bson.E{Key: "disabled", Value: *upd.Disabled})
	}
	if upd.Locale != nil {
		set = append(set, bson.E{Key: "locale", Value: *upd.Locale})
	}
	for key, n := range map[string]*registry.Networks{"admin_networks": upd.AdminNetworks, "login_networks": upd.LoginNetworks} {
		switch {
		case n == nil:
		case len(*n) == 0:
			unset = append(unset, bson.E{Key: key, Value: ""})
		default:
			set = append(set, bson.E{Key: key, Value: n.Strings()})
		}
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	err := s.updateByID(ctx, id, update)
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrEmailTaken
	}
	return err
}

func utcOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// EraseUser turns the user into a tombstone. Sessions and sign-in methods go
// first, like DeleteUser, so an interrupted erase never leaves credentials
// for a user without their identity.
func (s *AuthStore) EraseUser(ctx context.Context, id, placeholderEmail string, now time.Time) error {
	if _, err := s.DeleteSessions(ctx, id); err != nil {
		return err
	}
	if _, err := s.identities().DeleteMany(ctx, bson.D{{Key: "user_id", Value: id}}); err != nil {
		return err
	}
	return s.updateByID(ctx, id, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "email", Value: placeholderEmail}, {Key: "erased_at", Value: now}, {Key: "disabled", Value: true},
			{Key: "email_verified", Value: true}, {Key: "roles", Value: bson.A{}}, {Key: "updated_at", Value: now},
		}},
		{Key: "$unset", Value: bson.D{
			{Key: "locale", Value: ""}, {Key: "pending_email", Value: ""}, {Key: "previous_email", Value: ""},
			{Key: "admin_networks", Value: ""}, {Key: "login_networks", Value: ""},
		}},
	})
}

// ListUnverifiedUsers returns the oldest users that never verified their address.
func (s *AuthStore) ListUnverifiedUsers(ctx context.Context, before time.Time, limit int) ([]auth.User, error) {
	cur, err := s.users().Find(ctx, bson.D{{Key: "email_verified", Value: false}, {Key: "created_at", Value: bson.D{{Key: "$lt", Value: before}}}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []userDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.User, len(docs))
	for i, d := range docs {
		out[i] = d.user()
	}
	return out, nil
}

// DeleteUser removes sessions and identities first, so an interrupted
// delete never leaves credentials without their user.
func (s *AuthStore) DeleteUser(ctx context.Context, id string) error {
	if _, err := s.DeleteSessions(ctx, id); err != nil {
		return err
	}
	if _, err := s.identities().DeleteMany(ctx, bson.D{{Key: "user_id", Value: id}}); err != nil {
		return err
	}
	res, err := s.users().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) PutIdentity(ctx context.Context, id auth.Identity) error {
	filter := bson.D{{Key: "provider", Value: id.Provider}, {Key: "subject", Value: id.Subject}}
	set := bson.D{{Key: "updated_at", Value: id.UpdatedAt}}
	if id.PasswordHash != "" {
		set = append(set, bson.E{Key: "password_hash", Value: id.PasswordHash})
	}
	update := bson.D{
		{Key: "$set", Value: set},
		{Key: "$setOnInsert", Value: bson.D{
			{Key: "_id", Value: id.ID}, {Key: "user_id", Value: id.UserID}, {Key: "created_at", Value: id.CreatedAt},
		}},
	}
	_, err := s.identities().UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *AuthStore) Identity(ctx context.Context, provider, subject string) (auth.Identity, error) {
	var d identityDoc
	err := s.identities().FindOne(ctx, bson.D{{Key: "provider", Value: provider}, {Key: "subject", Value: subject}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Identity{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Identity{}, err
	}
	return auth.Identity{
		ID: d.ID, UserID: d.UserID, Provider: d.Provider, Subject: d.Subject, PasswordHash: d.PasswordHash,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
	}, nil
}

func (s *AuthStore) DeleteSessions(ctx context.Context, userID string) (int64, error) {
	res, err := s.sessions().DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

type sessionDoc struct {
	ID         string    `bson:"_id"`
	TokenHash  string    `bson:"token_hash"`
	UserID     string    `bson:"user_id"`
	CreatedAt  time.Time `bson:"created_at"`
	LastUsedAt time.Time `bson:"last_used_at"`
	ExpiresAt  time.Time `bson:"expires_at"`
}

func (d sessionDoc) session() auth.Session {
	return auth.Session{
		ID: d.ID, UserID: d.UserID, TokenHash: d.TokenHash,
		CreatedAt: d.CreatedAt.UTC(), LastUsedAt: d.LastUsedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
	}
}

func (s *AuthStore) CreateSession(ctx context.Context, x auth.Session) error {
	_, err := s.sessions().InsertOne(ctx, sessionDoc{
		ID: x.ID, TokenHash: x.TokenHash, UserID: x.UserID,
		CreatedAt: x.CreatedAt, LastUsedAt: x.LastUsedAt, ExpiresAt: x.ExpiresAt,
	})
	return err
}

// SessionByTokenHash loads the session and its user in one round trip.
func (s *AuthStore) SessionByTokenHash(ctx context.Context, hash string) (auth.Session, auth.User, error) {
	cur, err := s.sessions().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "token_hash", Value: hash}}}},
		{{Key: "$limit", Value: 1}},
		{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: UsersCollection}, {Key: "localField", Value: "user_id"},
			{Key: "foreignField", Value: "_id"}, {Key: "as", Value: "user"},
		}}},
		{{Key: "$unwind", Value: "$user"}},
	})
	if err != nil {
		return auth.Session{}, auth.User{}, err
	}
	var rows []struct {
		Session sessionDoc `bson:",inline"`
		User    userDoc    `bson:"user"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return auth.Session{}, auth.User{}, err
	}
	if len(rows) == 0 {
		return auth.Session{}, auth.User{}, auth.ErrNotFound
	}
	return rows[0].Session.session(), rows[0].User.user(), nil
}

func (s *AuthStore) TouchSession(ctx context.Context, id string, lastUsed, expires time.Time) error {
	_, err := s.sessions().UpdateByID(ctx, id, bson.D{{Key: "$set", Value: bson.D{
		{Key: "last_used_at", Value: lastUsed}, {Key: "expires_at", Value: expires},
	}}})
	return err
}

func (s *AuthStore) ListSessions(ctx context.Context, userID string) ([]auth.Session, error) {
	cur, err := s.sessions().Find(ctx, bson.D{{Key: "user_id", Value: userID}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var docs []sessionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.Session, len(docs))
	for i, d := range docs {
		out[i] = d.session()
	}
	return out, nil
}

func (s *AuthStore) DeleteSession(ctx context.Context, userID, id string) error {
	res, err := s.sessions().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "user_id", Value: userID}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) DeleteOtherSessions(ctx context.Context, userID, keepID string) (int64, error) {
	res, err := s.sessions().DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}, {Key: "_id", Value: bson.D{{Key: "$ne", Value: keepID}}}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// apiKeyDoc is an API key; _id is the key's hash.
type apiKeyDoc struct {
	Hash       string     `bson:"_id"`
	Name       string     `bson:"name"`
	Role       string     `bson:"role"`
	Networks   []string   `bson:"networks,omitempty"`
	Scopes     []string   `bson:"scopes,omitempty"`
	Prefix     string     `bson:"prefix"`
	CreatedAt  time.Time  `bson:"created_at"`
	LastUsedAt *time.Time `bson:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `bson:"expires_at,omitempty"`
}

// scopes reads a key's stored grants. A grant that doesn't parse (the document
// was edited by hand) leaves the key with a scope that allows nothing: failing
// closed, never open.
func scopes(stored []string) auth.Scopes {
	if len(stored) == 0 {
		return nil
	}
	s, err := auth.ParseScopes(stored)
	if err != nil {
		return auth.Scopes{{Op: "invalid"}}
	}
	return s
}

func (d apiKeyDoc) key() auth.APIKey {
	utc := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	return auth.APIKey{Name: d.Name, Role: auth.KeyRole(d.Role), Networks: networks(d.Networks), Scopes: scopes(d.Scopes), Prefix: d.Prefix, Hash: d.Hash, CreatedAt: d.CreatedAt.UTC(), LastUsedAt: utc(d.LastUsedAt), ExpiresAt: utc(d.ExpiresAt)}
}

func (s *AuthStore) CreateAPIKey(ctx context.Context, k auth.APIKey) error {
	_, err := s.apiKeys().InsertOne(ctx, apiKeyDoc{
		Hash: k.Hash, Name: k.Name, Role: string(k.Role), Networks: k.Networks.Strings(), Scopes: k.Scopes.Strings(), Prefix: k.Prefix, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt, ExpiresAt: k.ExpiresAt,
	})
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrKeyNameTaken
	}
	return err
}

func (s *AuthStore) APIKeyByHash(ctx context.Context, hash string) (auth.APIKey, error) {
	var d apiKeyDoc
	err := s.apiKeys().FindOne(ctx, bson.D{{Key: "_id", Value: hash}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.APIKey{}, auth.ErrKeyNotFound
	}
	if err != nil {
		return auth.APIKey{}, err
	}
	return d.key(), nil
}

func (s *AuthStore) ListAPIKeys(ctx context.Context) ([]auth.APIKey, error) {
	cur, err := s.apiKeys().Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []apiKeyDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.APIKey, len(docs))
	for i, d := range docs {
		out[i] = d.key()
	}
	return out, nil
}

func (s *AuthStore) DeleteAPIKey(ctx context.Context, name string) error {
	res, err := s.apiKeys().DeleteOne(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrKeyNotFound
	}
	return nil
}

func (s *AuthStore) TouchAPIKey(ctx context.Context, hash string, at time.Time) error {
	_, err := s.apiKeys().UpdateByID(ctx, hash, bson.D{{Key: "$set", Value: bson.D{{Key: "last_used_at", Value: at}}}})
	return err
}

func (s *AuthStore) LoginAttempts(ctx context.Context, key string) (auth.Attempts, error) {
	var d struct {
		Failures      int       `bson:"failures"`
		LastFailureAt time.Time `bson:"last_failure_at"`
	}
	err := s.attempts().FindOne(ctx, bson.D{{Key: "_id", Value: key}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Attempts{}, nil
	}
	if err != nil {
		return auth.Attempts{}, err
	}
	return auth.Attempts{Failures: d.Failures, LastFailureAt: d.LastFailureAt.UTC()}, nil
}

// RecordLoginFailure increments the counter in one atomic update. A
// counter whose window ended (but that the TTL monitor, which runs about
// once a minute, hasn't deleted yet) restarts at 1.
func (s *AuthStore) RecordLoginFailure(ctx context.Context, key string, at, expires time.Time) error {
	live := bson.D{{Key: "$gt", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$expires_at", at}}}, at}}}
	failures := bson.D{{Key: "$cond", Value: bson.A{live, bson.D{{Key: "$add", Value: bson.A{"$failures", int32(1)}}}, int32(1)}}}
	pipeline := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "failures", Value: failures},
		{Key: "last_failure_at", Value: at},
		{Key: "expires_at", Value: expires},
	}}}}
	_, err := s.attempts().UpdateOne(ctx, bson.D{{Key: "_id", Value: key}}, pipeline, options.UpdateOne().SetUpsert(true))
	return err
}

// AcquireLoginLock stores the lock as a document of the attempts collection
// (so it also expires by TTL if a process dies holding it). The upsert only
// matches a lock whose lease ended, or inserts one: against a live lock it
// would insert a second document with the same _id, which fails as a
// duplicate key.
func (s *AuthStore) AcquireLoginLock(ctx context.Context, key, owner string, now, until time.Time) (bool, error) {
	filter := bson.D{{Key: "_id", Value: "lock:" + key}, {Key: "expires_at", Value: bson.D{{Key: "$lte", Value: now}}}}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "owner", Value: owner}, {Key: "expires_at", Value: until},
		{Key: "failures", Value: int32(0)}, {Key: "last_failure_at", Value: now},
	}}}
	_, err := s.attempts().UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	return err == nil, err
}

func (s *AuthStore) ReleaseLoginLock(ctx context.Context, key, owner string) error {
	_, err := s.attempts().DeleteOne(ctx, bson.D{{Key: "_id", Value: "lock:" + key}, {Key: "owner", Value: owner}})
	return err
}

func (s *AuthStore) ClearLoginAttempts(ctx context.Context, key string) error {
	_, err := s.attempts().DeleteOne(ctx, bson.D{{Key: "_id", Value: key}})
	return err
}

// IncrementCounter reuses the login-throttling collection and pipeline
// (see RecordLoginFailure) but returns the resulting count and reset
// time, which a caller that must decide "over the limit or not" needs
// and login throttling itself doesn't.
func (s *AuthStore) IncrementCounter(ctx context.Context, key string, at, expires time.Time) (int, time.Time, error) {
	live := bson.D{{Key: "$gt", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$expires_at", at}}}, at}}}
	failures := bson.D{{Key: "$cond", Value: bson.A{live, bson.D{{Key: "$add", Value: bson.A{"$failures", int32(1)}}}, int32(1)}}}
	resetAt := bson.D{{Key: "$cond", Value: bson.A{live, bson.D{{Key: "$ifNull", Value: bson.A{"$expires_at", expires}}}, expires}}}
	pipeline := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "failures", Value: failures},
		{Key: "last_failure_at", Value: at},
		{Key: "expires_at", Value: resetAt},
	}}}}
	var d struct {
		Failures  int       `bson:"failures"`
		ExpiresAt time.Time `bson:"expires_at"`
	}
	err := s.attempts().FindOneAndUpdate(ctx, bson.D{{Key: "_id", Value: key}}, pipeline,
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&d)
	if err != nil {
		return 0, time.Time{}, err
	}
	return d.Failures, d.ExpiresAt.UTC(), nil
}

type invitationDoc struct {
	ID        string    `bson:"_id"`
	TokenHash string    `bson:"token_hash"`
	Email     string    `bson:"email,omitempty"`
	CreatedBy string    `bson:"created_by"`
	CreatedAt time.Time `bson:"created_at"`
	ExpiresAt time.Time `bson:"expires_at"`
}

func (d invitationDoc) invitation() auth.Invitation {
	return auth.Invitation{ID: d.ID, TokenHash: d.TokenHash, Email: d.Email, CreatedBy: d.CreatedBy,
		CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC()}
}

func (s *AuthStore) CreateInvitation(ctx context.Context, inv auth.Invitation) error {
	_, err := s.invitations().InsertOne(ctx, invitationDoc{
		ID: inv.ID, TokenHash: inv.TokenHash, Email: inv.Email, CreatedBy: inv.CreatedBy, CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt,
	})
	return err
}

func (s *AuthStore) ClaimInvitation(ctx context.Context, hash string, now time.Time) (auth.Invitation, error) {
	var d invitationDoc
	err := s.invitations().FindOneAndDelete(ctx, bson.D{
		{Key: "token_hash", Value: hash}, {Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Invitation{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Invitation{}, err
	}
	return d.invitation(), nil
}

// ClaimInvitationByID deletes and returns an unexpired invitation by id.
func (s *AuthStore) ClaimInvitationByID(ctx context.Context, id string, now time.Time) (auth.Invitation, error) {
	var d invitationDoc
	err := s.invitations().FindOneAndDelete(ctx, bson.D{
		{Key: "_id", Value: id}, {Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Invitation{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Invitation{}, err
	}
	return d.invitation(), nil
}

func (s *AuthStore) ListInvitations(ctx context.Context) ([]auth.Invitation, error) {
	cur, err := s.invitations().Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var docs []invitationDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.Invitation, len(docs))
	for i, d := range docs {
		out[i] = d.invitation()
	}
	return out, nil
}

func (s *AuthStore) DeleteInvitation(ctx context.Context, id string) error {
	res, err := s.invitations().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}
