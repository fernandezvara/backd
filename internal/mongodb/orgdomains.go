package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/auth"
)

// Verified domains (design/sso.md §10): one document per organization and domain, with a
// deterministic id from the pair. A partial unique index on the domain, for the verified ones only,
// is what makes a verified domain belong to one organization: two may both be pending, one can win.
type orgDomainDoc struct {
	ID            string    `bson:"_id"`
	Org           string    `bson:"org"`
	Domain        string    `bson:"domain"`
	State         string    `bson:"state"`
	Challenge     string    `bson:"challenge"`
	Discoverable  bool      `bson:"discoverable"`
	LinkAccounts  bool      `bson:"link_accounts"`
	Failures      int       `bson:"failures"`
	Checks        int       `bson:"checks"`
	CreatedBy     string    `bson:"created_by,omitempty"`
	CreatedAt     time.Time `bson:"created_at"`
	VerifiedAt    time.Time `bson:"verified_at,omitempty"`
	LastCheckedAt time.Time `bson:"last_checked_at,omitempty"`
	NextCheckAt   time.Time `bson:"next_check_at,omitempty"`
}

func (d orgDomainDoc) domain() auth.OrgDomain {
	return auth.OrgDomain{
		ID: d.ID, Org: d.Org, Domain: d.Domain, State: d.State, Challenge: d.Challenge, Discoverable: d.Discoverable,
		LinkAccounts: d.LinkAccounts, Failures: d.Failures, Checks: d.Checks, CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt.UTC(),
		VerifiedAt: d.VerifiedAt.UTC(), LastCheckedAt: d.LastCheckedAt.UTC(), NextCheckAt: d.NextCheckAt.UTC(),
	}
}

func (s *AuthStore) orgDomains() *mongo.Collection { return s.db.Collection(OrgDomainsCollection) }

// PutOrgDomain implements auth.Store.
func (s *AuthStore) PutOrgDomain(ctx context.Context, d auth.OrgDomain) error {
	set := bson.D{
		{Key: "org", Value: d.Org}, {Key: "domain", Value: d.Domain}, {Key: "state", Value: d.State}, {Key: "challenge", Value: d.Challenge},
		{Key: "discoverable", Value: d.Discoverable}, {Key: "link_accounts", Value: d.LinkAccounts},
		{Key: "failures", Value: d.Failures}, {Key: "checks", Value: d.Checks},
	}
	unset := bson.D{}
	for _, t := range []struct {
		key string
		at  time.Time
	}{{"verified_at", d.VerifiedAt}, {"last_checked_at", d.LastCheckedAt}, {"next_check_at", d.NextCheckAt}} {
		if t.at.IsZero() {
			unset = append(unset, bson.E{Key: t.key, Value: ""})
		} else {
			set = append(set, bson.E{Key: t.key, Value: t.at})
		}
	}
	update := bson.D{
		{Key: "$set", Value: set},
		{Key: "$setOnInsert", Value: bson.D{{Key: "created_by", Value: d.CreatedBy}, {Key: "created_at", Value: d.CreatedAt}}},
	}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	_, err := s.orgDomains().UpdateByID(ctx, d.ID, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrDomainTaken
	}
	return err
}

// OrgDomain implements auth.Store.
func (s *AuthStore) OrgDomain(ctx context.Context, org, domain string) (auth.OrgDomain, error) {
	d, err := getOne[orgDomainDoc](ctx, s.orgDomains(), byID(org+":"+domain), auth.ErrNotFound)
	if err != nil {
		return auth.OrgDomain{}, err
	}
	return d.domain(), nil
}

// OrgDomains implements auth.Store.
func (s *AuthStore) OrgDomains(ctx context.Context, org string) ([]auth.OrgDomain, error) {
	docs, err := findAll[orgDomainDoc](ctx, s.orgDomains(), bson.D{{Key: "org", Value: org}}, options.Find().SetSort(bson.D{{Key: "domain", Value: 1}}))
	if err != nil {
		return nil, err
	}
	return mapAll(docs, orgDomainDoc.domain), nil
}

// VerifiedOrgDomain implements auth.Store.
func (s *AuthStore) VerifiedOrgDomain(ctx context.Context, domain string) (auth.OrgDomain, error) {
	d, err := getOne[orgDomainDoc](ctx, s.orgDomains(), bson.D{{Key: "domain", Value: domain}, {Key: "state", Value: auth.DomainVerified}}, auth.ErrNotFound)
	if err != nil {
		return auth.OrgDomain{}, err
	}
	return d.domain(), nil
}

// DueOrgDomains implements auth.Store.
func (s *AuthStore) DueOrgDomains(ctx context.Context, now time.Time, limit int) ([]auth.OrgDomain, error) {
	docs, err := findAll[orgDomainDoc](ctx, s.orgDomains(), bson.D{{Key: "next_check_at", Value: bson.D{{Key: "$lte", Value: now}}}},
		options.Find().SetSort(bson.D{{Key: "next_check_at", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	return mapAll(docs, orgDomainDoc.domain), nil
}

// DeleteOrgDomain implements auth.Store.
func (s *AuthStore) DeleteOrgDomain(ctx context.Context, org, domain string) error {
	res, err := s.orgDomains().DeleteOne(ctx, byID(org+":"+domain))
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// DeleteOrgDomains implements auth.Store.
func (s *AuthStore) DeleteOrgDomains(ctx context.Context, org string) (int, error) {
	res, err := s.orgDomains().DeleteMany(ctx, bson.D{{Key: "org", Value: org}})
	if err != nil {
		return 0, err
	}
	return int(res.DeletedCount), nil
}
