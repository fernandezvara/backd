package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreOrganizations(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	owner := func(org, user string) auth.OrgMember {
		return auth.OrgMember{Org: org, User: user, JoinedAt: now, Roles: []auth.RoleGrant{{Role: "owner", GrantedAt: now}}}
	}
	if err := s.CreateOrg(ctx, auth.Org{ID: "o1", Name: "One", Slug: "one", CreatedBy: "u1", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now}, owner("o1", "u1")); err != nil {
		t.Fatal(err)
	}
	// A slug is unique; a failed creation leaves nothing behind.
	err := s.CreateOrg(ctx, auth.Org{ID: "o2", Name: "Two", Slug: "one", CreatedBy: "u2", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now}, owner("o2", "u2"))
	if !errors.Is(err, auth.ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v", err)
	}
	if _, err := s.OrgByID(ctx, "o2"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("o2 exists: %v", err)
	}
	// Organizations without a slug don't collide.
	for _, id := range []string{"o3", "o4"} {
		if err := s.CreateOrg(ctx, auth.Org{ID: id, Name: id, CreatedBy: "u1", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now}, owner(id, "u1")); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.CountOrgsCreatedBy(ctx, "u1"); n != 3 {
		t.Errorf("created by u1: %d", n)
	}
	// Updates: rename, take the slug away, set claims, change the status.
	name, empty := "Uno", ""
	o, err := s.UpdateOrg(ctx, "o1", auth.OrgUpdate{Name: &name, Slug: &empty, Claims: map[string]any{"plan": "pro"}, ClaimsSet: true, Status: auth.OrgOrphaned}, now.Add(time.Minute))
	if err != nil || o.Name != "Uno" || o.Slug != "" || o.Claims["plan"] != "pro" || o.Status != auth.OrgOrphaned {
		t.Fatalf("update: %+v %v", o, err)
	}
	// ...which frees the slug for another organization.
	slug := "one"
	if _, err := s.UpdateOrg(ctx, "o3", auth.OrgUpdate{Slug: &slug}, now); err != nil {
		t.Errorf("reuse: %v", err)
	}
	if _, err := s.UpdateOrg(ctx, "o4", auth.OrgUpdate{Slug: &slug}, now); !errors.Is(err, auth.ErrSlugTaken) {
		t.Errorf("taken: %v", err)
	}
	if _, err := s.UpdateOrg(ctx, "nope", auth.OrgUpdate{Name: &name}, now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	list, err := s.ListOrgs(ctx, auth.OrgOrphaned)
	if err != nil || len(list) != 1 || list[0].ID != "o1" {
		t.Errorf("orphaned: %+v %v", list, err)
	}
	// Members: unique per organization and user, replaced by a put.
	m := auth.OrgMember{Org: "o3", User: "u2", JoinedAt: now, Teams: []string{"t1"}, Roles: []auth.RoleGrant{
		{Role: "editor", GrantedAt: now, GrantedBy: "u1", From: now, Until: now.Add(time.Hour)}}}
	if err := s.PutOrgMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.OrgMember(ctx, "o3", "u2")
	if err != nil || len(got.Roles) != 1 || !got.Roles[0].Until.Equal(now.Add(time.Hour)) || got.Teams[0] != "t1" {
		t.Fatalf("member: %+v %v", got, err)
	}
	m.Roles = nil
	if err := s.PutOrgMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountOrgMembershipsOf(ctx, "u2"); n != 1 {
		t.Errorf("memberships: %d", n)
	}
	page, more, err := s.ListOrgMembers(ctx, "o3", "", 1)
	if err != nil || len(page) != 1 || !more || page[0].User != "u1" {
		t.Fatalf("page: %+v %v %v", page, more, err)
	}
	page, more, _ = s.ListOrgMembers(ctx, "o3", "u1", 5)
	if len(page) != 1 || more || page[0].User != "u2" {
		t.Errorf("next page: %+v %v", page, more)
	}
	owners, err := s.OrgMembersWithRoles(ctx, "o3", []string{"owner"})
	if err != nil || len(owners) != 1 || owners[0].User != "u1" {
		t.Errorf("owners: %+v %v", owners, err)
	}
	if err := s.DeleteOrgMember(ctx, "o3", "u2"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrgMember(ctx, "o3", "u2"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("again: %v", err)
	}
	byID, _ := s.OrgsByIDs(ctx, []string{"o1", "o3", "nope"})
	if len(byID) != 2 {
		t.Errorf("by ids: %v", byID)
	}
}

func TestAuthStoreOrgInvitations(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	inv := func(id, hash string, max int) auth.OrgInvitation {
		return auth.OrgInvitation{ID: id, Org: "o1", TokenHash: hash, Binding: auth.BindNone, Roles: []string{"editor"}, MaxUses: max,
			CreatedBy: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	for _, i := range []auth.OrgInvitation{inv("i1", "h1", 2), inv("i2", "h2", 1)} {
		if err := s.CreateOrgInvitation(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.OrgInvitationByHash(ctx, "h1")
	if err != nil || got.ID != "i1" || got.Binding != auth.BindNone || len(got.Teams) != 0 {
		t.Fatalf("by hash: %+v %v", got, err)
	}
	if n, _ := s.CountPendingOrgInvitations(ctx, "o1", now); n != 2 {
		t.Errorf("pending: %d", n)
	}
	// Uses are taken atomically up to the maximum.
	for i := 1; i <= 2; i++ {
		used, err := s.UseOrgInvitation(ctx, "i1", now)
		if err != nil || used.Uses != i {
			t.Fatalf("use %d: %+v %v", i, used, err)
		}
	}
	if _, err := s.UseOrgInvitation(ctx, "i1", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("a third use: %v", err)
	}
	if n, _ := s.CountPendingOrgInvitations(ctx, "o1", now); n != 1 {
		t.Errorf("pending after use: %d", n)
	}
	// Expiry and revocation end it too.
	if _, err := s.UseOrgInvitation(ctx, "i2", now.Add(2*time.Hour)); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("expired: %v", err)
	}
	if err := s.RevokeOrgInvitation(ctx, "o1", "i2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseOrgInvitation(ctx, "i2", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("revoked: %v", err)
	}
	if err := s.RevokeOrgInvitation(ctx, "o2", "i2", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("another organization's: %v", err)
	}
	// A new token replaces the old one, with or without a sealed copy.
	if err := s.ReplaceOrgInvitationToken(ctx, "o1", "i1", "h9", "sealed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OrgInvitationByHash(ctx, "h1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("old hash: %v", err)
	}
	if got, _ := s.OrgInvitationByHash(ctx, "h9"); got.SealedToken != "sealed" {
		t.Errorf("sealed: %+v", got)
	}
	_ = s.ReplaceOrgInvitationToken(ctx, "o1", "i1", "h10", "")
	if got, _ := s.OrgInvitationByHash(ctx, "h10"); got.SealedToken != "" {
		t.Errorf("sealed token kept: %+v", got)
	}
	list, _ := s.ListOrgInvitations(ctx, "o1")
	if len(list) != 2 {
		t.Errorf("list: %d", len(list))
	}
	if err := s.CreateOrg(ctx, auth.Org{ID: "o1", Name: "x", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now}, auth.OrgMember{Org: "o1", User: "u1", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOrgMember(ctx, auth.OrgMember{Org: "o1", User: "u9", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
}
