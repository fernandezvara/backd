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
