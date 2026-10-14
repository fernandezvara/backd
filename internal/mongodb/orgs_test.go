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
