package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

// Organization sign-in connections (design/org-sso.md §4): one document per organization and
// provider, with a deterministic id from the pair so a replace needs no existence check.
type orgConnectionDoc struct {
	ID              string    `bson:"_id"`
	Org             string    `bson:"org"`
	Provider        string    `bson:"provider"`
	ClientID        string    `bson:"client_id"`
	Tenant          string    `bson:"tenant,omitempty"`
	HostedDomain    string    `bson:"hosted_domain,omitempty"`
	EmailDomains    []string  `bson:"email_domains"`
	NativeClientIDs []string  `bson:"native_client_ids"`
	JoinRoles       []string  `bson:"join_roles"`
	Enabled         bool      `bson:"enabled"`
	Blocked         []string  `bson:"blocked"`
	CreatedBy       string    `bson:"created_by,omitempty"`
	UpdatedBy       string    `bson:"updated_by,omitempty"`
	CreatedAt       time.Time `bson:"created_at"`
	UpdatedAt       time.Time `bson:"updated_at"`
}

func (d orgConnectionDoc) connection() auth.OrgConnection {
	return auth.OrgConnection{
		ID: d.ID, Org: d.Org, Provider: d.Provider, ClientID: d.ClientID, Tenant: d.Tenant, HostedDomain: d.HostedDomain,
		EmailDomains: d.EmailDomains, NativeClientIDs: d.NativeClientIDs, JoinRoles: d.JoinRoles, Enabled: d.Enabled, Blocked: d.Blocked,
		CreatedBy: d.CreatedBy, UpdatedBy: d.UpdatedBy, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
	}
}

func (s *AuthStore) orgConnections() *mongo.Collection {
	return s.db.Collection(OrgConnectionsCollection)
}

func orgConnectionID(org, provider string) string { return org + ":" + provider }

// PutOrgConnection implements auth.Store. A replace sets the fields a manager controls and
// leaves the id, the creator, the creation time and the blocked members as they are: the last
// is written by the sign-in code, not by this call.
func (s *AuthStore) PutOrgConnection(ctx context.Context, c auth.OrgConnection) error {
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "org", Value: c.Org},
			{Key: "provider", Value: c.Provider},
			{Key: "client_id", Value: c.ClientID},
			{Key: "tenant", Value: c.Tenant},
			{Key: "hosted_domain", Value: c.HostedDomain},
			{Key: "email_domains", Value: nonNilStrings(c.EmailDomains)},
			{Key: "native_client_ids", Value: nonNilStrings(c.NativeClientIDs)},
			{Key: "join_roles", Value: nonNilStrings(c.JoinRoles)},
			{Key: "enabled", Value: c.Enabled},
			{Key: "updated_by", Value: c.UpdatedBy},
			{Key: "updated_at", Value: c.UpdatedAt},
		}},
		{Key: "$setOnInsert", Value: bson.D{
			{Key: "blocked", Value: nonNilStrings(c.Blocked)},
			{Key: "created_by", Value: c.CreatedBy},
			{Key: "created_at", Value: c.CreatedAt},
		}},
	}
	_, err := s.orgConnections().UpdateByID(ctx, orgConnectionID(c.Org, c.Provider), update, options.UpdateOne().SetUpsert(true))
	return err
}

// OrgConnection implements auth.Store.
func (s *AuthStore) OrgConnection(ctx context.Context, org, provider string) (auth.OrgConnection, error) {
	d, err := getOne[orgConnectionDoc](ctx, s.orgConnections(), byID(orgConnectionID(org, provider)), auth.ErrNotFound)
	if err != nil {
		return auth.OrgConnection{}, err
	}
	return d.connection(), nil
}

// OrgConnections implements auth.Store.
func (s *AuthStore) OrgConnections(ctx context.Context, org string) ([]auth.OrgConnection, error) {
	docs, err := findAll[orgConnectionDoc](ctx, s.orgConnections(), bson.D{{Key: "org", Value: org}}, options.Find().SetSort(bson.D{{Key: "provider", Value: 1}}))
	if err != nil {
		return nil, err
	}
	return mapAll(docs, orgConnectionDoc.connection), nil
}

// DeleteOrgConnection implements auth.Store.
func (s *AuthStore) DeleteOrgConnection(ctx context.Context, org, provider string) error {
	res, err := s.orgConnections().DeleteOne(ctx, byID(orgConnectionID(org, provider)))
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// BlockOrgMember implements auth.Store: the members a manager removed stay out of the
// connection until invited again. The newest are kept when the list is full.
func (s *AuthStore) BlockOrgMember(ctx context.Context, org, user string) error {
	_, err := s.orgConnections().UpdateMany(ctx,
		bson.D{{Key: "org", Value: org}, {Key: "blocked", Value: bson.D{{Key: "$ne", Value: user}}}},
		bson.D{{Key: "$push", Value: bson.D{{Key: "blocked", Value: bson.D{{Key: "$each", Value: bson.A{user}}, {Key: "$slice", Value: -auth.MaxConnectionBlocked}}}}}})
	return err
}

// UnblockOrgMember implements auth.Store.
func (s *AuthStore) UnblockOrgMember(ctx context.Context, org, user string) error {
	_, err := s.orgConnections().UpdateMany(ctx, bson.D{{Key: "org", Value: org}}, bson.D{{Key: "$pull", Value: bson.D{{Key: "blocked", Value: user}}}})
	return err
}
