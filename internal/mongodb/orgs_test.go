package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/email"
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
	// The invitations that name a user, or an address, whichever organization made them.
	bound := func(id, org, binding, user, email string) auth.OrgInvitation {
		i := inv(id, "hb-"+id, 1)
		i.Org, i.Binding, i.User, i.Email = org, binding, user, email
		return i
	}
	for _, i := range []auth.OrgInvitation{
		bound("b1", "o1", auth.BindUser, "u7", ""),
		bound("b2", "o2", auth.BindEmail, "", "ada@example.com"),
		bound("b3", "o2", auth.BindEmail, "", "bob@example.com"),
		bound("b4", "o2", auth.BindUser, "u8", ""),
	} {
		if err := s.CreateOrgInvitation(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	addressed, err := s.OrgInvitationsAddressedTo(ctx, "u7", "ada@example.com")
	var addressedIDs []string
	for _, a := range addressed {
		addressedIDs = append(addressedIDs, a.ID)
	}
	slices.Sort(addressedIDs)
	if err != nil || !slices.Equal(addressedIDs, []string{"b1", "b2"}) {
		t.Errorf("addressed: %v %v", addressedIDs, err)
	}
	if none, _ := s.OrgInvitationsAddressedTo(ctx, "", ""); len(none) != 0 {
		t.Errorf("addressed to nobody: %v", none)
	}
	if err := s.CreateOrg(ctx, auth.Org{ID: "o1", Name: "x", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now}, auth.OrgMember{Org: "o1", User: "u1", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOrgMember(ctx, auth.OrgMember{Org: "o1", User: "u9", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestAuthStoreOrgTeams(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreateOrgTeam(ctx, auth.OrgTeam{ID: "t1", Org: "o1", Name: "Editors", Roles: []string{"editor"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrgTeam(ctx, auth.OrgTeam{ID: "t2", Org: "o1", Name: "Editors", CreatedAt: now}); !errors.Is(err, auth.ErrTeamNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	// The same name in another organization is fine.
	if err := s.CreateOrgTeam(ctx, auth.OrgTeam{ID: "t3", Org: "o2", Name: "Editors", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrgTeam(ctx, auth.OrgTeam{ID: "t4", Org: "o1", Name: "Admins", Roles: []string{"admin"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListOrgTeams(ctx, "o1")
	if err != nil || len(list) != 2 || list[0].Name != "Admins" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := s.UpdateOrgTeam(ctx, auth.OrgTeam{ID: "t4", Org: "o1", Name: "Editors"}); !errors.Is(err, auth.ErrTeamNameTaken) {
		t.Errorf("rename to a taken name: %v", err)
	}
	if err := s.UpdateOrgTeam(ctx, auth.OrgTeam{ID: "t4", Org: "o1", Name: "Leads", Roles: []string{"editor", "admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOrgTeam(ctx, auth.OrgTeam{ID: "t4", Org: "o2", Name: "x"}); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("another organization's team: %v", err)
	}
	by, _ := s.OrgTeamsByIDs(ctx, "o1", []string{"t1", "t3", "t4"})
	if len(by) != 2 {
		t.Errorf("by ids: %+v", by)
	}
	// Deleting a team takes it out of the memberships.
	for _, u := range []string{"u1", "u2"} {
		if err := s.PutOrgMember(ctx, auth.OrgMember{Org: "o1", User: u, JoinedAt: now, Teams: []string{"t1", "t4"}}); err != nil {
			t.Fatal(err)
		}
	}
	in, _ := s.OrgMembersInTeams(ctx, "o1", []string{"t1"})
	if len(in) != 2 {
		t.Errorf("in teams: %+v", in)
	}
	if err := s.DeleteOrgTeam(ctx, "o1", "t1"); err != nil {
		t.Fatal(err)
	}
	m, _ := s.OrgMember(ctx, "o1", "u1")
	if len(m.Teams) != 1 || m.Teams[0] != "t4" {
		t.Errorf("teams after delete: %v", m.Teams)
	}
	if err := s.DeleteOrgTeam(ctx, "o1", "t1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("again: %v", err)
	}
}

func TestAuthStoreArchiveAndReactivateOrg(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreateOrg(ctx, auth.Org{ID: "o1", Name: "Gone", Slug: "gone", CreatedBy: "u1", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now},
		auth.OrgMember{Org: "o1", User: "u1", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = s.CreateOrgTeam(ctx, auth.OrgTeam{ID: "t1", Org: "o1", Name: "T", CreatedAt: now})
	_ = s.CreateOrgInvitation(ctx, auth.OrgInvitation{ID: "i1", Org: "o1", TokenHash: "h", Binding: auth.BindNone, MaxUses: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	org, err := s.ArchiveOrg(ctx, "o1", now)
	if err != nil || org.Status != auth.OrgDeleted || org.Slug != "" || org.FormerSlug != "gone" || org.DeletedAt.IsZero() {
		t.Fatalf("archive: %+v %v", org, err)
	}
	if n, _ := s.CountOrgMembershipsOf(ctx, "u1"); n != 0 {
		t.Errorf("memberships left: %d", n)
	}
	if teams, _ := s.ListOrgTeams(ctx, "o1"); len(teams) != 0 {
		t.Errorf("teams left: %v", teams)
	}
	if invs, _ := s.ListOrgInvitations(ctx, "o1"); len(invs) != 0 {
		t.Errorf("invitations left: %v", invs)
	}
	if _, err := s.ArchiveOrg(ctx, "o1", now); !errors.Is(err, auth.ErrOrgNotActive) {
		t.Errorf("again: %v", err)
	}
	if _, err := s.ArchiveOrg(ctx, "nosuch", now); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	// The slug is free for another organization; the deleted one comes back under another.
	if err := s.CreateOrg(ctx, auth.Org{ID: "o2", Name: "New", Slug: "gone", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now},
		auth.OrgMember{Org: "o2", User: "u2", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReactivateOrg(ctx, "o1", "gone", now); !errors.Is(err, auth.ErrSlugTaken) {
		t.Errorf("slug taken: %v", err)
	}
	back, err := s.ReactivateOrg(ctx, "o1", "other", now)
	if err != nil || back.Status != auth.OrgActive || back.Slug != "other" || !back.DeletedAt.IsZero() {
		t.Errorf("reactivate: %+v %v", back, err)
	}
	if _, err := s.ReactivateOrg(ctx, "o1", "", now); !errors.Is(err, auth.ErrOrgNotDeleted) {
		t.Errorf("an active one: %v", err)
	}
}

func TestRepositoryOrgPurge(t *testing.T) {
	_, repo := provisionOneWith(t, `{"type": "object", "properties": {"n": {"type": "integer"}}}`, "")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, org := range []any{"o1", "o1", "o1", "o2", nil} {
		meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1), "owner": "u1"}
		if org != nil {
			meta["org"] = org
		}
		if i == 2 {
			meta["deleted_at"] = now // soft-deleted ones are counted and removed too
		}
		if err := repo.Create(ctx, map[string]any{"id": string(rune('a' + i)), "n": int64(i), "_meta": meta}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.CountInOrg(ctx, "o1"); err != nil || n != 3 {
		t.Fatalf("count: %d %v", n, err)
	}
	n, _, err := repo.DeleteInOrg(ctx, "o1", nil, 2)
	if err != nil || n != 2 {
		t.Fatalf("first batch: %d %v", n, err)
	}
	n, _, err = repo.DeleteInOrg(ctx, "o1", nil, 2)
	if err != nil || n != 1 {
		t.Fatalf("second batch: %d %v", n, err)
	}
	if n, _ := repo.CountInOrg(ctx, "o2"); n != 1 {
		t.Errorf("another organization lost documents: %d", n)
	}
}

func TestAuthStoreForgetUserInOrgs(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"o1", "o2"} {
		if err := s.CreateOrg(ctx, auth.Org{ID: id, Name: id, CreatedBy: "u1", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now},
			auth.OrgMember{Org: id, User: "u1", JoinedAt: now, Teams: []string{"t"}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.PutOrgMember(ctx, auth.OrgMember{Org: "o1", User: "u2", JoinedAt: now})
	inv := func(id, user, email, by string) auth.OrgInvitation {
		return auth.OrgInvitation{ID: id, Org: "o1", TokenHash: id, Binding: auth.BindUser, User: user, Email: email, MaxUses: 1, CreatedBy: by, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	for _, i := range []auth.OrgInvitation{inv("a", "u1", "", "u2"), inv("b", "", "u1@example.com", "u2"), inv("c", "u3", "", "u1"), inv("d", "u3", "", "u2")} {
		if err := s.CreateOrgInvitation(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	orgs, err := s.ForgetUserInOrgs(ctx, "u1", "u1@example.com")
	if err != nil || len(orgs) != 2 {
		t.Fatalf("forget: %v %v", orgs, err)
	}
	if n, _ := s.CountOrgMembershipsOf(ctx, "u1"); n != 0 {
		t.Errorf("memberships left: %d", n)
	}
	if m, _ := s.OrgMember(ctx, "o1", "u2"); m.User != "u2" {
		t.Errorf("another member was removed")
	}
	left, _ := s.ListOrgInvitations(ctx, "o1")
	if len(left) != 2 {
		t.Fatalf("invitations: %+v", left)
	}
	for _, i := range left {
		if i.CreatedBy == "u1" {
			t.Errorf("created_by kept: %+v", i)
		}
	}
	org, _ := s.OrgByID(ctx, "o1")
	if org.CreatedBy != "" {
		t.Errorf("organization created_by kept: %q", org.CreatedBy)
	}
}

func TestRepositoryClearOwnerInOrgs(t *testing.T) {
	_, repo := provisionOneWith(t, `{"type": "object", "properties": {"n": {"type": "integer"}}}`, "")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, org := range []any{"o1", "o1", nil} {
		meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1), "owner": "u1"}
		if org != nil {
			meta["org"] = org
		}
		if err := repo.Create(ctx, map[string]any{"id": string(rune('a' + i)), "n": int64(i), "_meta": meta}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.CountOwnedInOrgs(ctx, "u1"); err != nil || n != 2 {
		t.Fatalf("count: %d %v", n, err)
	}
	n, err := repo.ClearOwnerInOrgs(ctx, "u1", 10, now)
	if err != nil || n != 2 {
		t.Fatalf("clear: %d %v", n, err)
	}
	doc, _ := repo.Get(ctx, "a")
	meta, _ := doc["_meta"].(map[string]any)
	if meta["owner"] != nil || meta["version"] != int64(2) || meta["org"] != "o1" {
		t.Errorf("document: %v", meta)
	}
	if own, _ := repo.CountOwned(ctx, "u1"); own != 1 {
		t.Errorf("the one outside an organization keeps its owner: %d", own)
	}
}

func TestAuthStoreTeamRoles(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	grant := func(team, role string, from, until time.Time) auth.TeamRoleGrant {
		return auth.TeamRoleGrant{Team: team, RoleGrant: auth.RoleGrant{Role: role, From: from, Until: until, GrantedBy: "u9", GrantedAt: now}}
	}
	put := func(user string, teams []string, grants ...auth.TeamRoleGrant) {
		t.Helper()
		if err := s.PutOrgMember(ctx, auth.OrgMember{Org: "o1", User: user, JoinedAt: now, Teams: teams, TeamRoles: grants}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateOrgTeam(ctx, auth.OrgTeam{ID: "sales", Org: "o1", Name: "Sales", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	put("u1", []string{"sales"})                                                             // a member
	put("u2", nil, grant("sales", "manager", time.Time{}, time.Time{}))                      // a holder outside the team
	put("u3", []string{"sales"}, grant("sales", "manager", now.Add(time.Hour), time.Time{})) // not yet
	put("u4", nil, grant("sales", "delegate", now.Add(-2*time.Hour), now.Add(-time.Hour)))   // expired
	put("u5", nil, grant("support", "manager", time.Time{}, time.Time{}))                    // another team
	put("u6", nil, grant("sales", "delegate", now.Add(-time.Hour), now.Add(time.Hour)))      // in its window

	users := func(role string, after string, limit int) (out []string, more bool) {
		t.Helper()
		list, more, err := s.OrgTeamHolders(ctx, "o1", "sales", role, now, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range list {
			out = append(out, m.User)
		}
		return out, more
	}
	if got, _ := users("", "", 0); fmt.Sprint(got) != "[u1 u2 u3 u6]" {
		t.Errorf("members and holders: %v", got)
	}
	if got, _ := users("manager", "", 0); fmt.Sprint(got) != "[u2]" {
		t.Errorf("managers: %v", got)
	}
	if got, _ := users("delegate", "", 0); fmt.Sprint(got) != "[u6]" {
		t.Errorf("delegates: %v", got)
	}
	// Pages.
	got, more := users("", "", 2)
	if fmt.Sprint(got) != "[u1 u2]" || !more {
		t.Errorf("page 1: %v %v", got, more)
	}
	if got, more := users("", "u2", 2); fmt.Sprint(got) != "[u3 u6]" || more {
		t.Errorf("page 2: %v %v", got, more)
	}
	// The grant comes back as it went in.
	m, _ := s.OrgMember(ctx, "o1", "u6")
	if len(m.TeamRoles) != 1 || m.TeamRoles[0].Team != "sales" || m.TeamRoles[0].GrantedBy != "u9" || !m.TeamRoles[0].Until.Equal(now.Add(time.Hour)) {
		t.Errorf("stored grant: %+v", m.TeamRoles)
	}
	// Ended grants are purged once they are older than the cut.
	if n, err := s.PurgeExpiredTeamRoles(ctx, now.Add(-3*time.Hour)); err != nil || n != 0 {
		t.Errorf("too early: %d %v", n, err)
	}
	if n, err := s.PurgeExpiredTeamRoles(ctx, now); err != nil || n != 1 {
		t.Errorf("purge: %d %v", n, err)
	}
	if m, _ := s.OrgMember(ctx, "o1", "u4"); len(m.TeamRoles) != 0 {
		t.Errorf("u4 after the purge: %+v", m.TeamRoles)
	}
	if m, _ := s.OrgMember(ctx, "o1", "u6"); len(m.TeamRoles) != 1 {
		t.Errorf("u6 after the purge: %+v", m.TeamRoles)
	}
	// Deleting the team takes its grants and keeps the others.
	if err := s.DeleteOrgTeam(ctx, "o1", "sales"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.OrgMember(ctx, "o1", "u6"); len(m.TeamRoles) != 0 {
		t.Errorf("grants after the team went: %+v", m.TeamRoles)
	}
	if m, _ := s.OrgMember(ctx, "o1", "u5"); len(m.TeamRoles) != 1 {
		t.Errorf("another team's grant: %+v", m.TeamRoles)
	}
}

func TestAuthStoreOrgMemberSeq(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	next := func(org, user string) int64 {
		t.Helper()
		n, err := s.NextOrgMemberSeq(ctx, org, user)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// It counts per organization and user, from one, and never goes back.
	if a, b, c := next("o1", "u1"), next("o1", "u1"), next("o1", "u2"); a != 1 || b != 2 || c != 1 {
		t.Errorf("seq: %d %d %d", a, b, c)
	}
	if n := next("o2", "u1"); n != 1 {
		t.Errorf("another organization: %d", n)
	}
	if n := next("o1", "u1"); n != 3 {
		t.Errorf("after the others: %d", n)
	}
}

func TestAuthStoreOrgTemplates(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	put := func(org, kind, locale string, version int, subject string) {
		t.Helper()
		tpl := auth.OrgTemplate{Org: org, Kind: kind, Locale: locale, Override: email.Override{Subject: subject, Text: "t", HTML: "h"}, Version: version, UpdatedBy: "u1", UpdatedAt: now}
		if version > 1 {
			tpl.Versions = []auth.OrgTemplateVersion{{Version: version - 1, Override: email.Override{Subject: "old", Text: "t0", HTML: "h0"}, By: "u2", At: now.Add(-time.Hour)}}
		}
		if err := s.PutOrgTemplate(ctx, tpl); err != nil {
			t.Fatal(err)
		}
	}
	put("o1", "reset-password", "en", 1, "one")
	put("o1", "reset-password", "es", 2, "dos")
	put("o2", "reset-password", "en", 1, "other")
	got, err := s.OrgTemplate(ctx, "o1", "reset-password", "es")
	if err != nil || got.Subject != "dos" || got.Version != 2 || len(got.Versions) != 1 || got.Versions[0].Subject != "old" || got.Versions[0].By != "u2" || got.UpdatedBy != "u1" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := s.OrgTemplate(ctx, "o1", "reset-password", "fr"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	list, _ := s.ListOrgTemplates(ctx, "o1")
	if len(list) != 2 || list[0].Locale != "en" || list[1].Locale != "es" {
		t.Errorf("list: %+v", list)
	}
	// Saving again replaces it; deleting removes only that one.
	put("o1", "reset-password", "en", 2, "uno")
	if got, _ := s.OrgTemplate(ctx, "o1", "reset-password", "en"); got.Subject != "uno" || got.Version != 2 {
		t.Errorf("replace: %+v", got)
	}
	if err := s.DeleteOrgTemplate(ctx, "o1", "reset-password", "en"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrgTemplate(ctx, "o1", "reset-password", "en"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
	if list, _ := s.ListOrgTemplates(ctx, "o2"); len(list) != 1 {
		t.Errorf("another organization's: %+v", list)
	}
}

// sso_required is stored and cleared, and the sessions that did not come through the
// organization's connection are the ones DeleteOrgSessionsNotOpenedBy ends.
func TestAuthStoreSSORequiredAndItsSessions(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreateOrg(ctx, auth.Org{ID: "o1", Name: "One", Status: auth.OrgActive, CreatedAt: now, UpdatedAt: now},
		auth.OrgMember{Org: "o1", User: "u1", JoinedAt: now}); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	if o, err := s.UpdateOrg(ctx, "o1", auth.OrgUpdate{SSORequired: &on}, now); err != nil || !o.SSORequired {
		t.Fatalf("on: %+v %v", o, err)
	}
	if o, _ := s.OrgByID(ctx, "o1"); !o.SSORequired {
		t.Errorf("not stored: %+v", o)
	}
	if o, err := s.UpdateOrg(ctx, "o1", auth.OrgUpdate{SSORequired: &off}, now); err != nil || o.SSORequired {
		t.Fatalf("off: %+v %v", o, err)
	}

	mk := func(id, org string, methods ...string) {
		t.Helper()
		if err := s.CreateSession(ctx, auth.Session{ID: id, UserID: "u-" + id, TokenHash: "t-" + id, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour), Org: org, Methods: methods}); err != nil {
			t.Fatal(err)
		}
	}
	mk("pw", "o1", "password")
	mk("realm-google", "o1", "google")
	mk("conn", "o1", auth.ConnectionMethod("o1", "google"), "totp")
	mk("other-conn", "o1", auth.ConnectionMethod("o2", "google"))
	mk("elsewhere", "o2", "password")
	n, err := s.DeleteOrgSessionsNotOpenedBy(ctx, "o1")
	if err != nil || n != 3 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	left, _ := s.ListSessions(ctx, "u-conn")
	other, _ := s.ListSessions(ctx, "u-elsewhere")
	gone, _ := s.ListSessions(ctx, "u-pw")
	if len(left) != 1 || len(other) != 1 || len(gone) != 0 {
		t.Errorf("left %d, elsewhere %d, pw %d", len(left), len(other), len(gone))
	}
}
