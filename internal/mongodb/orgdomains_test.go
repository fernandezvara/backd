package mongodb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuthStoreOrgDomains(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 11, 16, 9, 0, 0, 0, time.UTC)
	claim := func(org, domain, state string) auth.OrgDomain {
		return auth.OrgDomain{ID: org + ":" + domain, Org: org, Domain: domain, State: state, Challenge: "backd-verify=" + org, LinkAccounts: true, CreatedBy: "user:u1", CreatedAt: now}
	}
	// Two organizations may both be pending for a domain.
	for _, org := range []string{"o1", "o2"} {
		if err := s.PutOrgDomain(ctx, claim(org, "acme.example", auth.DomainPending)); err != nil {
			t.Fatalf("pending %s: %v", org, err)
		}
	}
	// Only one can be verified.
	v1 := claim("o1", "acme.example", auth.DomainVerified)
	v1.VerifiedAt, v1.NextCheckAt, v1.Discoverable = now, now.Add(time.Hour), true
	if err := s.PutOrgDomain(ctx, v1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOrgDomain(ctx, claim("o2", "acme.example", auth.DomainVerified)); !errors.Is(err, auth.ErrDomainTaken) {
		t.Fatalf("a second verified claim: %v", err)
	}
	// The one that lost is still there, pending; the holder reads back whole.
	if d, err := s.OrgDomain(ctx, "o2", "acme.example"); err != nil || d.State != auth.DomainPending {
		t.Errorf("loser = %+v, %v", d, err)
	}
	got, err := s.VerifiedOrgDomain(ctx, "acme.example")
	if err != nil || got.Org != "o1" || !got.Discoverable || !got.LinkAccounts || !got.VerifiedAt.Equal(now) || got.CreatedBy != "user:u1" {
		t.Fatalf("holder = %+v, %v", got, err)
	}
	// A replace keeps who created it and when.
	again := v1
	again.CreatedBy, again.CreatedAt, again.Failures = "admin", now.Add(time.Hour), 2
	if err := s.PutOrgDomain(ctx, again); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.OrgDomain(ctx, "o1", "acme.example"); d.CreatedBy != "user:u1" || !d.CreatedAt.Equal(now) || d.Failures != 2 {
		t.Errorf("after a replace: %+v", d)
	}
	// A time cleared stays cleared.
	cleared := again
	cleared.NextCheckAt = time.Time{}
	if err := s.PutOrgDomain(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.OrgDomain(ctx, "o1", "acme.example"); !d.NextCheckAt.IsZero() {
		t.Errorf("next check = %v", d.NextCheckAt)
	}
	// Lapsed holds nothing: the other can be verified.
	lapsed := cleared
	lapsed.State = auth.DomainLapsed
	if err := s.PutOrgDomain(ctx, lapsed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifiedOrgDomain(ctx, "acme.example"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("a lapsed domain is held: %v", err)
	}
	if err := s.PutOrgDomain(ctx, claim("o2", "acme.example", auth.DomainVerified)); err != nil {
		t.Errorf("the other after a lapse: %v", err)
	}
	// Listing, due checks and deleting.
	if err := s.PutOrgDomain(ctx, claim("o1", "b.example", auth.DomainPending)); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.OrgDomains(ctx, "o1"); len(list) != 2 || list[0].Domain != "acme.example" || list[1].Domain != "b.example" {
		t.Errorf("list = %+v", list)
	}
	due := claim("o1", "c.example", auth.DomainPending)
	due.NextCheckAt = now.Add(-time.Minute)
	later := claim("o1", "d.example", auth.DomainPending)
	later.NextCheckAt = now.Add(time.Hour)
	for _, d := range []auth.OrgDomain{due, later} {
		if err := s.PutOrgDomain(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := s.DueOrgDomains(ctx, now, 10); err != nil || len(list) != 1 || list[0].Domain != "c.example" {
		t.Errorf("due = %+v, %v", list, err)
	}
	if err := s.DeleteOrgDomain(ctx, "o1", "b.example"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrgDomain(ctx, "o1", "b.example"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if n, err := s.DeleteOrgDomains(ctx, "o1"); err != nil || n != 3 {
		t.Errorf("DeleteOrgDomains = %d, %v", n, err)
	}
	// The validator refuses a state it doesn't know.
	bad := claim("o3", "x.example", "approved")
	if err := s.PutOrgDomain(ctx, bad); err == nil {
		t.Error("an unknown state was stored")
	}
}
