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
	Team      string     `bson:"team,omitempty"` // team roles only
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
	TeamRoles []grantDoc `bson:"team_roles"`
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

func (g grantDoc) grant() auth.RoleGrant {
	rg := auth.RoleGrant{Role: g.Role, GrantedBy: g.GrantedBy, GrantedAt: g.GrantedAt.UTC()}
	if g.From != nil {
		rg.From = g.From.UTC()
	}
	if g.Until != nil {
		rg.Until = g.Until.UTC()
	}
	return rg
}

func grantOf(g auth.RoleGrant) grantDoc {
	return grantDoc{Role: g.Role, From: timePtr(g.From), Until: timePtr(g.Until), GrantedBy: g.GrantedBy, GrantedAt: g.GrantedAt}
}

func (d orgMemberDoc) member() auth.OrgMember {
	m := auth.OrgMember{Org: d.Org, User: d.User, Teams: d.Teams, JoinedAt: d.JoinedAt.UTC(), InvitedBy: d.InvitedBy}
	for _, g := range d.Roles {
		m.Roles = append(m.Roles, g.grant())
	}
	for _, g := range d.TeamRoles {
		m.TeamRoles = append(m.TeamRoles, auth.TeamRoleGrant{Team: g.Team, RoleGrant: g.grant()})
	}
	return m
}

func memberDoc(m auth.OrgMember) orgMemberDoc {
	d := orgMemberDoc{ID: orgMemberID(m.Org, m.User), Org: m.Org, User: m.User, Teams: m.Teams, JoinedAt: m.JoinedAt, InvitedBy: m.InvitedBy, Roles: []grantDoc{}, TeamRoles: []grantDoc{}}
	if d.Teams == nil {
		d.Teams = []string{}
	}
	for _, g := range m.Roles {
		d.Roles = append(d.Roles, grantOf(g))
	}
	for _, g := range m.TeamRoles {
		gd := grantOf(g.RoleGrant)
		gd.Team = g.Team
		d.TeamRoles = append(d.TeamRoles, gd)
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

func (s *AuthStore) ForgetUserInOrgs(ctx context.Context, userID, email string) ([]string, error) {
	cur, err := s.orgMembers().Find(ctx, bson.D{{Key: "user", Value: userID}}, options.Find().SetProjection(bson.D{{Key: "org", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []orgMemberDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	orgs := make([]string, len(docs))
	for i, d := range docs {
		orgs[i] = d.Org
	}
	if _, err := s.orgMembers().DeleteMany(ctx, bson.D{{Key: "user", Value: userID}}); err != nil {
		return nil, err
	}
	bound := bson.A{bson.D{{Key: "user", Value: userID}}}
	if email != "" {
		bound = append(bound, bson.D{{Key: "email", Value: email}})
	}
	if _, err := s.orgInvitations().DeleteMany(ctx, bson.D{{Key: "$or", Value: bound}}); err != nil {
		return nil, err
	}
	clear := bson.D{{Key: "$unset", Value: bson.D{{Key: "created_by", Value: ""}}}}
	if _, err := s.orgInvitations().UpdateMany(ctx, bson.D{{Key: "created_by", Value: userID}}, clear); err != nil {
		return nil, err
	}
	if _, err := s.orgs().UpdateMany(ctx, bson.D{{Key: "created_by", Value: userID}}, clear); err != nil {
		return nil, err
	}
	return orgs, nil
}

func (s *AuthStore) ArchiveOrg(ctx context.Context, id string, now time.Time) (auth.Org, error) {
	var d orgDoc
	err := s.orgs().FindOneAndUpdate(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "status", Value: bson.D{{Key: "$ne", Value: auth.OrgDeleted}}}},
		mongo.Pipeline{
			{{Key: "$set", Value: bson.D{
				{Key: "status", Value: auth.OrgDeleted},
				{Key: "deleted_at", Value: now},
				{Key: "updated_at", Value: now},
				{Key: "former_slug", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$slug", "$former_slug"}}}},
			}}},
			{{Key: "$unset", Value: "slug"}},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		if _, ferr := s.OrgByID(ctx, id); ferr != nil {
			return auth.Org{}, ferr
		}
		return auth.Org{}, auth.ErrOrgNotActive
	}
	if err != nil {
		return auth.Org{}, err
	}
	filter := bson.D{{Key: "org", Value: id}}
	for _, c := range []*mongo.Collection{s.orgMembers(), s.orgTeams(), s.orgInvitations()} {
		if _, err := c.DeleteMany(ctx, filter); err != nil {
			return auth.Org{}, err
		}
	}
	return d.org(), nil
}

func (s *AuthStore) ReactivateOrg(ctx context.Context, id, slug string, now time.Time) (auth.Org, error) {
	set := bson.D{{Key: "status", Value: auth.OrgActive}, {Key: "updated_at", Value: now}}
	if slug != "" {
		set = append(set, bson.E{Key: "slug", Value: slug})
	}
	var d orgDoc
	err := s.orgs().FindOneAndUpdate(ctx, bson.D{{Key: "_id", Value: id}, {Key: "status", Value: auth.OrgDeleted}},
		bson.D{{Key: "$set", Value: set}, {Key: "$unset", Value: bson.D{{Key: "deleted_at", Value: ""}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		if _, ferr := s.OrgByID(ctx, id); ferr != nil {
			return auth.Org{}, ferr
		}
		return auth.Org{}, auth.ErrOrgNotDeleted
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

func (s *AuthStore) CountOrgMembers(ctx context.Context, orgs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(orgs) == 0 {
		return out, nil
	}
	cur, err := s.orgMembers().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.D{{Key: "org", Value: bson.D{{Key: "$in", Value: orgs}}}}}},
		{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$org"}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}}}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID string `bson:"_id"`
		N  int    `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.N
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
	return s.pageOrgMembers(ctx, bson.D{{Key: "org", Value: org}}, after, limit)
}

// pageOrgMembers returns a page of the memberships matching filter, sorted by user id, after
// the user after; more says there are further ones.
func (s *AuthStore) pageOrgMembers(ctx context.Context, filter bson.D, after string, limit int) ([]auth.OrgMember, bool, error) {
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

// NextOrgMemberSeq counts a membership event of a user in an organization.
func (s *AuthStore) NextOrgMemberSeq(ctx context.Context, org, user string) (int64, error) {
	var d struct {
		Seq int64 `bson:"seq"`
	}
	err := s.db.Collection(OrgMemberSeqCollection).FindOneAndUpdate(ctx,
		bson.D{{Key: "_id", Value: orgMemberID(org, user)}},
		bson.D{{Key: "$inc", Value: bson.D{{Key: "seq", Value: int64(1)}}}, {Key: "$setOnInsert", Value: bson.D{{Key: "org", Value: org}, {Key: "user", Value: user}}}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&d)
	return d.Seq, err
}

// PurgeExpiredTeamRoles pulls the grants whose window ended before the instant.
func (s *AuthStore) PurgeExpiredTeamRoles(ctx context.Context, before time.Time) (int64, error) {
	ended := bson.D{{Key: "until", Value: bson.D{{Key: "$lt", Value: before}}}}
	res, err := s.orgMembers().UpdateMany(ctx,
		bson.D{{Key: "team_roles", Value: bson.D{{Key: "$elemMatch", Value: ended}}}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: "team_roles", Value: ended}}}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// OrgTeamHolders pages the members of a team and the holders of a team role in it.
func (s *AuthStore) OrgTeamHolders(ctx context.Context, org, team, role string, now time.Time, after string, limit int) ([]auth.OrgMember, bool, error) {
	held := bson.D{{Key: "team", Value: team}}
	if role != "" {
		held = append(held, bson.E{Key: "role", Value: role})
	}
	// Grants outside their window don't count.
	held = append(held, bson.E{Key: "$and", Value: bson.A{
		bson.D{{Key: "$or", Value: bson.A{bson.D{{Key: "from", Value: bson.D{{Key: "$exists", Value: false}}}}, bson.D{{Key: "from", Value: bson.D{{Key: "$lte", Value: now}}}}}}},
		bson.D{{Key: "$or", Value: bson.A{bson.D{{Key: "until", Value: bson.D{{Key: "$exists", Value: false}}}}, bson.D{{Key: "until", Value: bson.D{{Key: "$gt", Value: now}}}}}}},
	}})
	holder := bson.D{{Key: "team_roles", Value: bson.D{{Key: "$elemMatch", Value: held}}}}
	match := bson.A{holder}
	if role == "" {
		match = append(match, bson.D{{Key: "teams", Value: team}})
	}
	return s.pageOrgMembers(ctx, bson.D{{Key: "org", Value: org}, {Key: "$or", Value: match}}, after, limit)
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

type orgInvitationDoc struct {
	ID          string     `bson:"_id"`
	Org         string     `bson:"org"`
	TokenHash   string     `bson:"token_hash"`
	SealedToken string     `bson:"sealed_token,omitempty"`
	Binding     string     `bson:"binding"`
	User        string     `bson:"user,omitempty"`
	Email       string     `bson:"email,omitempty"`
	Roles       []string   `bson:"roles"`
	Teams       []string   `bson:"teams"`
	MaxUses     int        `bson:"max_uses"`
	Uses        int        `bson:"uses"`
	CreatedBy   string     `bson:"created_by,omitempty"`
	CreatedAt   time.Time  `bson:"created_at"`
	ExpiresAt   time.Time  `bson:"expires_at"`
	RevokedAt   *time.Time `bson:"revoked_at,omitempty"`
}

func (d orgInvitationDoc) invitation() auth.OrgInvitation {
	inv := auth.OrgInvitation{ID: d.ID, Org: d.Org, TokenHash: d.TokenHash, SealedToken: d.SealedToken, Binding: d.Binding,
		User: d.User, Email: d.Email, Roles: d.Roles, Teams: d.Teams, MaxUses: d.MaxUses, Uses: d.Uses,
		CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC()}
	if d.RevokedAt != nil {
		inv.RevokedAt = d.RevokedAt.UTC()
	}
	return inv
}

func (s *AuthStore) orgInvitations() *mongo.Collection {
	return s.db.Collection(OrgInvitationsCollection)
}

func (s *AuthStore) CreateOrgInvitation(ctx context.Context, inv auth.OrgInvitation) error {
	d := orgInvitationDoc{ID: inv.ID, Org: inv.Org, TokenHash: inv.TokenHash, SealedToken: inv.SealedToken, Binding: inv.Binding,
		User: inv.User, Email: inv.Email, Roles: inv.Roles, Teams: inv.Teams, MaxUses: inv.MaxUses, Uses: inv.Uses,
		CreatedBy: inv.CreatedBy, CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt}
	if d.Roles == nil {
		d.Roles = []string{}
	}
	if d.Teams == nil {
		d.Teams = []string{}
	}
	_, err := s.orgInvitations().InsertOne(ctx, d)
	return err
}

func (s *AuthStore) findOrgInvitation(ctx context.Context, filter bson.D) (auth.OrgInvitation, error) {
	var d orgInvitationDoc
	err := s.orgInvitations().FindOne(ctx, filter).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.OrgInvitation{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.OrgInvitation{}, err
	}
	return d.invitation(), nil
}

func (s *AuthStore) OrgInvitationByHash(ctx context.Context, hash string) (auth.OrgInvitation, error) {
	return s.findOrgInvitation(ctx, bson.D{{Key: "token_hash", Value: hash}})
}

func (s *AuthStore) OrgInvitationByID(ctx context.Context, org, id string) (auth.OrgInvitation, error) {
	return s.findOrgInvitation(ctx, bson.D{{Key: "_id", Value: id}, {Key: "org", Value: org}})
}

func (s *AuthStore) ListOrgInvitations(ctx context.Context, org string) ([]auth.OrgInvitation, error) {
	cur, err := s.orgInvitations().Find(ctx, bson.D{{Key: "org", Value: org}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var docs []orgInvitationDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.OrgInvitation, len(docs))
	for i, d := range docs {
		out[i] = d.invitation()
	}
	return out, nil
}

// pendingOrgInvitation matches the invitations that can still be used at now.
func pendingOrgInvitation(now time.Time) bson.D {
	return bson.D{
		{Key: "revoked_at", Value: bson.D{{Key: "$exists", Value: false}}},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
		{Key: "$expr", Value: bson.D{{Key: "$lt", Value: bson.A{"$uses", "$max_uses"}}}},
	}
}

func (s *AuthStore) CountPendingOrgInvitations(ctx context.Context, org string, now time.Time) (int, error) {
	filter := append(bson.D{{Key: "org", Value: org}}, pendingOrgInvitation(now)...)
	n, err := s.orgInvitations().CountDocuments(ctx, filter)
	return int(n), err
}

func (s *AuthStore) UseOrgInvitation(ctx context.Context, id string, now time.Time) (auth.OrgInvitation, error) {
	filter := append(bson.D{{Key: "_id", Value: id}}, pendingOrgInvitation(now)...)
	var d orgInvitationDoc
	err := s.orgInvitations().FindOneAndUpdate(ctx, filter, bson.D{{Key: "$inc", Value: bson.D{{Key: "uses", Value: 1}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return auth.OrgInvitation{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.OrgInvitation{}, err
	}
	return d.invitation(), nil
}

func (s *AuthStore) RevokeOrgInvitation(ctx context.Context, org, id string, now time.Time) error {
	res, err := s.orgInvitations().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "org", Value: org}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "revoked_at", Value: now}}}, {Key: "$unset", Value: bson.D{{Key: "sealed_token", Value: ""}}}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) ReplaceOrgInvitationToken(ctx context.Context, org, id, tokenHash, sealed string) error {
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "token_hash", Value: tokenHash}}}}
	if sealed != "" {
		update[0].Value = bson.D{{Key: "token_hash", Value: tokenHash}, {Key: "sealed_token", Value: sealed}}
	} else {
		update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "sealed_token", Value: ""}}})
	}
	res, err := s.orgInvitations().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "org", Value: org}}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) DeleteOrgInvitation(ctx context.Context, org, id string) error {
	_, err := s.orgInvitations().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "org", Value: org}})
	return err
}

type orgTeamDoc struct {
	ID        string    `bson:"_id"`
	Org       string    `bson:"org"`
	Name      string    `bson:"name"`
	Roles     []string  `bson:"roles"`
	CreatedAt time.Time `bson:"created_at"`
}

func (s *AuthStore) orgTeams() *mongo.Collection { return s.db.Collection(OrgTeamsCollection) }

func (d orgTeamDoc) team() auth.OrgTeam {
	return auth.OrgTeam{ID: d.ID, Org: d.Org, Name: d.Name, Roles: nonNilStrings(d.Roles), CreatedAt: d.CreatedAt.UTC()}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *AuthStore) CreateOrgTeam(ctx context.Context, t auth.OrgTeam) error {
	_, err := s.orgTeams().InsertOne(ctx, orgTeamDoc{ID: t.ID, Org: t.Org, Name: t.Name, Roles: nonNilStrings(t.Roles), CreatedAt: t.CreatedAt})
	if mongo.IsDuplicateKeyError(err) {
		return auth.ErrTeamNameTaken
	}
	return err
}

func (s *AuthStore) UpdateOrgTeam(ctx context.Context, t auth.OrgTeam) error {
	res, err := s.orgTeams().UpdateOne(ctx, bson.D{{Key: "_id", Value: t.ID}, {Key: "org", Value: t.Org}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: t.Name}, {Key: "roles", Value: nonNilStrings(t.Roles)}}}})
	switch {
	case mongo.IsDuplicateKeyError(err):
		return auth.ErrTeamNameTaken
	case err != nil:
		return err
	case res.MatchedCount == 0:
		return auth.ErrNotFound
	}
	return nil
}

func (s *AuthStore) DeleteOrgTeam(ctx context.Context, org, id string) error {
	res, err := s.orgTeams().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "org", Value: org}})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return auth.ErrNotFound
	}
	// The team goes from its members' teams and the grants of its roles go with it.
	_, err = s.orgMembers().UpdateMany(ctx,
		bson.D{{Key: "org", Value: org}, {Key: "$or", Value: bson.A{
			bson.D{{Key: "teams", Value: id}},
			bson.D{{Key: "team_roles.team", Value: id}},
		}}},
		bson.D{{Key: "$pull", Value: bson.D{
			{Key: "teams", Value: id},
			{Key: "team_roles", Value: bson.D{{Key: "team", Value: id}}},
		}}})
	return err
}

func (s *AuthStore) ListOrgTeams(ctx context.Context, org string) ([]auth.OrgTeam, error) {
	cur, err := s.orgTeams().Find(ctx, bson.D{{Key: "org", Value: org}}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []orgTeamDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.OrgTeam, len(docs))
	for i, d := range docs {
		out[i] = d.team()
	}
	return out, nil
}

func (s *AuthStore) OrgMembersInTeams(ctx context.Context, org string, teams []string) ([]auth.OrgMember, error) {
	if len(teams) == 0 {
		return nil, nil
	}
	cur, err := s.orgMembers().Find(ctx, bson.D{{Key: "org", Value: org}, {Key: "teams", Value: bson.D{{Key: "$in", Value: teams}}}})
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

func (s *AuthStore) OrgTeamsByIDs(ctx context.Context, org string, ids []string) ([]auth.OrgTeam, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cur, err := s.orgTeams().Find(ctx, bson.D{{Key: "org", Value: org}, {Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, err
	}
	var docs []orgTeamDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]auth.OrgTeam, len(docs))
	for i, d := range docs {
		out[i] = auth.OrgTeam{ID: d.ID, Org: d.Org, Name: d.Name, Roles: d.Roles, CreatedAt: d.CreatedAt.UTC()}
	}
	return out, nil
}
