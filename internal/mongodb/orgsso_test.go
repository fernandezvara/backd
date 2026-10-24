package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreOrgConnections(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	g := auth.OrgConnection{ID: "o1:google", Org: "o1", Provider: "google", ClientID: "c-1", HostedDomain: "acme.example", JoinRoles: []string{"member"}, Enabled: true, CreatedBy: "user:u1", UpdatedBy: "user:u1", CreatedAt: now, UpdatedAt: now}
	if err := s.PutOrgConnection(ctx, g); err != nil {
		t.Fatal(err)
	}
	m := auth.OrgConnection{ID: "o1:microsoft", Org: "o1", Provider: "microsoft", ClientID: "c-2", Tenant: "t-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := s.PutOrgConnection(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Another organization's connection is not listed.
	if err := s.PutOrgConnection(ctx, auth.OrgConnection{ID: "o2:google", Org: "o2", Provider: "google", ClientID: "c-3", HostedDomain: "other.example", Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	list, err := s.OrgConnections(ctx, "o1")
	if err != nil || len(list) != 2 || list[0].Provider != "google" || list[1].Provider != "microsoft" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	got, err := s.OrgConnection(ctx, "o1", "google")
	if err != nil || got.HostedDomain != "acme.example" || got.Blocked == nil || len(got.JoinRoles) != 1 || !got.Enabled {
		t.Fatalf("get = %+v, %v", got, err)
	}

	// A replace changes what the manager controls and keeps the rest.
	later := now.Add(time.Hour)
	if _, err := s.db.Collection(OrgConnectionsCollection).UpdateByID(ctx, "o1:google", map[string]any{"$set": map[string]any{"blocked": []string{"u9"}}}); err != nil {
		t.Fatal(err)
	}
	g2 := g
	g2.ID, g2.ClientID, g2.Enabled, g2.UpdatedBy, g2.UpdatedAt, g2.CreatedBy, g2.CreatedAt = "other", "c-1b", false, "admin", later, "admin", later
	if err := s.PutOrgConnection(ctx, g2); err != nil {
		t.Fatal(err)
	}
	got, err = s.OrgConnection(ctx, "o1", "google")
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "c-1b" || got.Enabled || got.UpdatedBy != "admin" || !got.UpdatedAt.Equal(later) {
		t.Errorf("not replaced: %+v", got)
	}
	if got.ID != "o1:google" || got.CreatedBy != "user:u1" || !got.CreatedAt.Equal(now) || len(got.Blocked) != 1 || got.Blocked[0] != "u9" {
		t.Errorf("what a replace keeps was changed: %+v", got)
	}

	if err := s.DeleteOrgConnection(ctx, "o1", "google"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrgConnection(ctx, "o1", "google"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
	if _, err := s.OrgConnection(ctx, "o1", "google"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestAuthStoreOrganizationIdentitiesAndSlugs(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	put := func(id, user, org, subject string) error {
		return s.PutIdentity(ctx, auth.Identity{ID: id, UserID: user, Provider: "google", Org: org, Subject: subject, CreatedAt: now, UpdatedAt: now})
	}
	if err := put("i1", "u1", "", "sub"); err != nil {
		t.Fatal(err)
	}
	if err := put("i2", "u1", "o1", "sub"); err != nil {
		t.Fatalf("the same person at an organization: %v", err)
	}
	if err := put("i3", "u1", "o1", "sub-2"); !errors.Is(err, auth.ErrIdentityExists) {
		t.Errorf("a second identity of the user at the organization: %v", err)
	}
	if err := put("i4", "u2", "o1", "sub"); !errors.Is(err, auth.ErrIdentityExists) {
		t.Errorf("the subject of another user at the organization: %v", err)
	}
	// The lookups keep them apart.
	if id, err := s.Identity(ctx, "google", "sub"); err != nil || id.ID != "i1" || id.Org != "" {
		t.Errorf("realm identity = %+v, %v", id, err)
	}
	if id, err := s.IdentityIn(ctx, "google", "o1", "sub"); err != nil || id.ID != "i2" || id.Org != "o1" {
		t.Errorf("organization identity = %+v, %v", id, err)
	}
	if _, err := s.IdentityIn(ctx, "google", "o2", "sub"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("another organization: %v", err)
	}
	if id, err := s.IdentityOf(ctx, "u1", "google"); err != nil || id.ID != "i1" {
		t.Errorf("IdentityOf = %+v, %v", id, err)
	}
	if id, err := s.IdentityOfIn(ctx, "u1", "google", "o1"); err != nil || id.ID != "i2" {
		t.Errorf("IdentityOfIn = %+v, %v", id, err)
	}
	if ids, _ := s.ListIdentities(ctx, "u1"); len(ids) != 2 {
		t.Errorf("list = %+v", ids)
	}
	if err := s.DeleteIdentityIn(ctx, "u1", "google", "o1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity(ctx, "google", "sub"); err != nil {
		t.Errorf("the realm identity went with the organization's: %v", err)
	}
	if err := s.DeleteIdentityIn(ctx, "u1", "google", "o1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}

	// Organizations by slug: not a deleted one.
	if err := s.CreateOrg(ctx, auth.Org{ID: "o1", Name: "One", Slug: "one", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now}, auth.OrgMember{Org: "o1", User: "u1", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	if o, err := s.OrgBySlug(ctx, "one"); err != nil || o.ID != "o1" {
		t.Errorf("OrgBySlug = %+v, %v", o, err)
	}
	if _, err := s.OrgBySlug(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown slug: %v", err)
	}
	if _, err := s.ArchiveOrg(ctx, "o1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OrgBySlug(ctx, "one"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("a deleted organization's slug: %v", err)
	}
}
