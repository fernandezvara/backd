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

type orgDoc struct {
	ID         string         `bson:"_id"`
	Name       string         `bson:"name"`
	Slug       string         `bson:"slug,omitempty"`
	FormerSlug string         `bson:"former_slug,omitempty"`
	CreatedBy  string         `bson:"created_by,omitempty"`
	Status     string         `bson:"status"`
	RequireMFA bool           `bson:"require_mfa"`
	Claims     map[string]any `bson:"claims,omitempty"`
	CreatedAt  time.Time      `bson:"created_at"`
	UpdatedAt  time.Time      `bson:"updated_at"`
	DeletedAt  *time.Time     `bson:"deleted_at,omitempty"`
}

func (d orgDoc) org() auth.Org {
	o := auth.Org{ID: d.ID, Name: d.Name, Slug: d.Slug, FormerSlug: d.FormerSlug, CreatedBy: d.CreatedBy, Status: d.Status,
		RequireMFA: d.RequireMFA, Claims: d.Claims, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC()}
	if d.DeletedAt != nil {
		o.DeletedAt = d.DeletedAt.UTC()
	}
	return o
}

type grantDoc struct {
	Role      string     `bson:"role"`
	From      *time.Time `bson:"from,omitempty"`
	Until     *time.Time `bson:"until,omitempty"`
	GrantedBy string     `bson:"granted_by,omitempty"`
	GrantedAt time.Time  `bson:"granted_at"`
}

type orgMemberDoc struct {
	ID        string     `bson:"_id"` // org/user
	Org       string     `bson:"org"`
	User      string     `bson:"user"`
	Roles     []grantDoc `bson:"roles"`
	Teams     []string   `bson:"teams"`
	JoinedAt  time.Time  `bson:"joined_at"`
	InvitedBy string     `bson:"invited_by,omitempty"`
}

func orgMemberID(org, user string) string { return org + "/" + user }

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (d orgMemberDoc) member() auth.OrgMember {
	m := auth.OrgMember{Org: d.Org, User: d.User, Teams: d.Teams, JoinedAt: d.JoinedAt.UTC(), InvitedBy: d.InvitedBy}
	for _, g := range d.Roles {
		rg := auth.RoleGrant{Role: g.Role, GrantedBy: g.GrantedBy, GrantedAt: g.GrantedAt.UTC()}
		if g.From != nil {
			rg.From = g.From.UTC()
		}
		if g.Until != nil {
			rg.Until = g.Until.UTC()
		}
		m.Roles = append(m.Roles, rg)
	}
	return m
}

func memberDoc(m auth.OrgMember) orgMemberDoc {
	d := orgMemberDoc{ID: orgMemberID(m.Org, m.User), Org: m.Org, User: m.User, Teams: m.Teams, JoinedAt: m.JoinedAt, InvitedBy: m.InvitedBy, Roles: []grantDoc{}}
	if d.Teams == nil {
		d.Teams = []string{}
	}
	for _, g := range m.Roles {
		d.Roles = append(d.Roles, grantDoc{Role: g.Role, From: timePtr(g.From), Until: timePtr(g.Until), GrantedBy: g.GrantedBy, GrantedAt: g.GrantedAt})
	}
	return d
}

func (s *AuthStore) orgs() *mongo.Collection       { return s.db.Collection(OrgsCollection) }
func (s *AuthStore) orgMembers() *mongo.Collection { return s.db.Collection(OrgMembersCollection) }

func (s *AuthStore) CreateOrg(ctx context.Context, o auth.Org, first auth.OrgMember) error {
	d := orgDoc{ID: o.ID, Name: o.Name, Slug: o.Slug, CreatedBy: o.CreatedBy, Status: o.Status, RequireMFA: o.RequireMFA,
		Claims: o.Claims, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
	if _, err := s.orgs().InsertOne(ctx, d); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return auth.ErrSlugTaken
		}
		return err
	}
	if _, err := s.orgMembers().InsertOne(ctx, memberDoc(first)); err != nil {
		_, _ = s.orgs().DeleteOne(context.WithoutCancel(ctx), bson.D{{Key: "_id", Value: o.ID}})
		return err
	}
	return nil
}

func (s *AuthStore) OrgByID(ctx context.Context, id string) (auth.Org, error) {
	var d orgDoc
	err := s.orgs().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.Org{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Org{}, err
	}
	return d.org(), nil
}

func (s *AuthStore) OrgsByIDs(ctx context.Context, ids []string) (map[string]auth.Org, error) {
	out := map[string]auth.Org{}
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := s.orgs().Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, err
	}
	var docs []orgDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.ID] = d.org()
	}
	return out, nil
}

func (s *AuthStore) UpdateOrg(ctx context.Context, id string, upd auth.OrgUpdate, now time.Time) (auth.Org, error) {
	set := bson.D{{Key: "updated_at", Value: now}}
	var unset bson.D
	if upd.Name != nil {
		set = append(set, bson.E{Key: "name", Value: *upd.Name})
	}
	if upd.Slug != nil {
		if *upd.Slug == "" {
			unset = append(unset, bson.E{Key: "slug", Value: ""})
		} else {
			set = append(set, bson.E{Key: "slug", Value: *upd.Slug})
		}
	}
	if upd.RequireMFA != nil {
		set = append(set, bson.E{Key: "require_mfa", Value: *upd.RequireMFA})
	}
	if upd.ClaimsSet {
		if len(upd.Claims) == 0 {
			unset = append(unset, bson.E{Key: "claims", Value: ""})
		} else {
			set = append(set, bson.E{Key: "claims", Value: upd.Claims})
		}
	}
	if upd.Status != "" {
		set = append(set, bson.E{Key: "status", Value: upd.Status})
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	var d orgDoc
	err := s.orgs().FindOneAndUpdate(ctx, bson.D{{Key: "_id", Value: id}}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		return auth.Org{}, auth.ErrNotFound
	case mongo.IsDuplicateKeyError(err):
		return auth.Org{}, auth.ErrSlugTaken
	case err != nil:
		return auth.Org{}, err
	}
	return d.org(), nil
}

func (s *AuthStore) ListOrgs(ctx context.Context, status string) ([]auth.Org, error) {
	filter := bson.D{}
	if status != "" {
		filter = bson.D{{Key: "status", Value: status}}
	}
	cur, err := s.orgs().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var docs []orgDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.Org, len(docs))
	for i, d := range docs {
		out[i] = d.org()
	}
	return out, nil
}

func (s *AuthStore) CountOrgsCreatedBy(ctx context.Context, userID string) (int, error) {
	n, err := s.orgs().CountDocuments(ctx, bson.D{{Key: "created_by", Value: userID}})
	return int(n), err
}

func (s *AuthStore) OrgMember(ctx context.Context, org, user string) (auth.OrgMember, error) {
	var d orgMemberDoc
	err := s.orgMembers().FindOne(ctx, bson.D{{Key: "_id", Value: orgMemberID(org, user)}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.OrgMember{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.OrgMember{}, err
	}
	return d.member(), nil
}

func (s *AuthStore) ListOrgMembers(ctx context.Context, org, after string, limit int) ([]auth.OrgMember, bool, error) {
	filter := bson.D{{Key: "org", Value: org}}
	if after != "" {
		filter = append(filter, bson.E{Key: "user", Value: bson.D{{Key: "$gt", Value: after}}})
	}
	opts := options.Find().SetSort(bson.D{{Key: "user", Value: 1}})
	if limit > 0 {
		opts.SetLimit(int64(limit) + 1)
	}
	cur, err := s.orgMembers().Find(ctx, filter, opts)
	if err != nil {
		return nil, false, err
	}
	var docs []orgMemberDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, false, err
	}
	more := limit > 0 && len(docs) > limit
	if more {
		docs = docs[:limit]
	}
	out := make([]auth.OrgMember, len(docs))
	for i, d := range docs {
		out[i] = d.member()
	}
	return out, more, nil
}

func (s *AuthStore) OrgMembersWithRoles(ctx context.Context, org string, roles []string) ([]auth.OrgMember, error) {
	cur, err := s.orgMembers().Find(ctx, bson.D{{Key: "org", Value: org}, {Key: "roles.role", Value: bson.D{{Key: "$in", Value: roles}}}})
	if err != nil {
		return nil, err
	}
	var docs []orgMemberDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.OrgMember, len(docs))
	for i, d := range docs {
		out[i] = d.member()
	}
	return out, nil
}

func (s *AuthStore) OrgMembershipsOf(ctx context.Context, user string, limit int) ([]auth.OrgMember, error) {
	opts := options.Find().SetSort(bson.D{{Key: "org", Value: 1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := s.orgMembers().Find(ctx, bson.D{{Key: "user", Value: user}}, opts)
	if err != nil {
		return nil, err
	}
	var docs []orgMemberDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.OrgMember, len(docs))
	for i, d := range docs {
		out[i] = d.member()
	}
	return out, nil
}

func (s *AuthStore) CountOrgMembershipsOf(ctx context.Context, user string) (int, error) {
	n, err := s.orgMembers().CountDocuments(ctx, bson.D{{Key: "user", Value: user}})
	return int(n), err
}

func (s *AuthStore) PutOrgMember(ctx context.Context, m auth.OrgMember) error {
	d := memberDoc(m)
	_, err := s.orgMembers().ReplaceOne(ctx, bson.D{{Key: "_id", Value: d.ID}}, d, options.Replace().SetUpsert(true))
	return err
}

func (s *AuthStore) DeleteOrgMember(ctx context.Context, org, user string) error {
	res, err := s.orgMembers().DeleteOne(ctx, bson.D{{Key: "_id", Value: orgMemberID(org, user)}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}
