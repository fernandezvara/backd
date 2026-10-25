package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestAuthStoreBlocksRemovedMembers(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, c := range []auth.OrgConnection{
		{ID: "o1:google", Org: "o1", Provider: "google", ClientID: "c", HostedDomain: "a.example", Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "o1:microsoft", Org: "o1", Provider: "microsoft", ClientID: "c", Tenant: "t", Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "o2:google", Org: "o2", Provider: "google", ClientID: "c", HostedDomain: "b.example", Enabled: true, CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.PutOrgConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// Every connection of the organization, once, and no other organization's.
	for range 2 {
		if err := s.BlockOrgMember(ctx, "o1", "u1"); err != nil {
			t.Fatal(err)
		}
	}
	for provider, want := range map[string]int{"google": 1, "microsoft": 1} {
		if c, _ := s.OrgConnection(ctx, "o1", provider); len(c.Blocked) != want || !c.HasBlocked("u1") {
			t.Errorf("%s blocked = %v", provider, c.Blocked)
		}
	}
	if c, _ := s.OrgConnection(ctx, "o2", "google"); len(c.Blocked) != 0 {
		t.Errorf("another organization's connection blocks: %v", c.Blocked)
	}
	if err := s.UnblockOrgMember(ctx, "o1", "u1"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.OrgConnection(ctx, "o1", "google"); len(c.Blocked) != 0 {
		t.Errorf("unblocked: %v", c.Blocked)
	}
	// The list keeps the newest.
	for i := range auth.MaxConnectionBlocked + 5 {
		if err := s.BlockOrgMember(ctx, "o2", fmt.Sprintf("u%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := s.OrgConnection(ctx, "o2", "google")
	if len(c.Blocked) != auth.MaxConnectionBlocked || c.HasBlocked("u0000") || !c.HasBlocked(fmt.Sprintf("u%04d", auth.MaxConnectionBlocked+4)) {
		t.Errorf("blocked has %d entries, first %q", len(c.Blocked), c.Blocked[0])
	}
}

func TestAuthStoreKeepsTheOrganizationOfASignIn(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "u1@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, auth.Session{ID: "s1", UserID: "u1", TokenHash: "h1", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour), Org: "o1"}); err != nil {
		t.Fatal(err)
	}
	sess, _, err := s.SessionByTokenHash(ctx, "h1")
	if err != nil || sess.Org != "o1" {
		t.Errorf("session = %+v, %v", sess, err)
	}
	if err := s.PutLoginCode(ctx, auth.LoginCode{ID: "lc1", UserID: "u1", Method: "google", CodeChallenge: "c", Org: "o1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if lc, err := s.ClaimLoginCode(ctx, "lc1", now); err != nil || lc.Org != "o1" {
		t.Errorf("login code = %+v, %v", lc, err)
	}
	if err := s.PutChallenge(ctx, auth.AuthChallenge{ID: "ch1", Kind: auth.ChallengeMFA, UserID: "u1", Factor: "google", ViaOrg: "o1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if c, err := s.GetChallenge(ctx, "ch1", now); err != nil || c.ViaOrg != "o1" || c.Org != "" {
		t.Errorf("challenge = %+v, %v", c, err)
	}
	if err := s.PutOAuthState(ctx, auth.OAuthState{ID: "st1", Provider: "google", Org: "o1", Intent: "signin", CodeChallenge: "c", Verifier: "v", Nonce: "n", RedirectTo: "https://x", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if st, err := s.ClaimOAuthState(ctx, "st1", now); err != nil || st.Org != "o1" {
		t.Errorf("state = %+v, %v", st, err)
	}
}

// A connection moves between realms through the portable collections, without any secret.
func TestOrgConnectionsAreExportedAndImported(t *testing.T) {
	src, _ := authFixture(t)
	dst, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	conn := auth.OrgConnection{ID: "o1:google", Org: "o1", Provider: "google", ClientID: "c-1", HostedDomain: "acme.example", EmailDomains: []string{"acme.example"},
		NativeClientIDs: []string{"n-1"}, JoinRoles: []string{"member"}, Enabled: true, CreatedBy: "user:u1", UpdatedBy: "user:u1", CreatedAt: now, UpdatedAt: now}
	if err := src.PutOrgConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := src.BlockOrgMember(ctx, "o1", "u9"); err != nil {
		t.Fatal(err)
	}
	cipher, _ := auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc := &auth.Users{Store: src, Cipher: cipher}
	if err := svc.SetOrgSecret(ctx, "o1", "SSO_GOOGLE_CLIENT_SECRET", "the-secret", "test"); err != nil {
		t.Fatal(err)
	}

	docs, err := src.ExportSystem(ctx, "org_connections", "", 100)
	if err != nil || len(docs) != 1 {
		t.Fatalf("export: %v %v", docs, err)
	}
	if strings.Contains(fmt.Sprint(docs), "the-secret") || docs[0]["client_id"] != "c-1" {
		t.Fatalf("export = %v", docs)
	}
	// Importing: created once, unchanged after, and the connection reads as it was.
	res, err := dst.ImportSystem(ctx, "org_connections", docs, "create", true)
	if err != nil || res[0].Outcome != auth.PortableCreated {
		t.Fatalf("import: %+v %v", res, err)
	}
	if res, err = dst.ImportSystem(ctx, "org_connections", docs, "create", true); err != nil || res[0].Outcome != auth.PortableUnchanged {
		t.Fatalf("import again: %+v %v", res, err)
	}
	got, err := dst.OrgConnection(ctx, "o1", "google")
	if err != nil || got.ClientID != "c-1" || got.HostedDomain != "acme.example" || !got.HasBlocked("u9") || !got.Enabled || got.UpdatedBy != "user:u1" {
		t.Fatalf("imported = %+v, %v", got, err)
	}
	// No secret came with it: the connection is unavailable until one is set.
	dsvc := &auth.Users{Store: dst, Cipher: cipher}
	if _, ok, _ := dsvc.OrgSecret(ctx, "o1", "SSO_GOOGLE_CLIENT_SECRET"); ok {
		t.Error("a secret travelled")
	}
	// A different document with the same id is a conflict; a bad shape is invalid.
	changed := map[string]any{}
	for k, v := range docs[0] {
		changed[k] = v
	}
	changed["client_id"] = "c-2"
	if res, _ := dst.ImportSystem(ctx, "org_connections", []map[string]any{changed}, "create", true); res[0].Outcome != auth.PortableConflict {
		t.Errorf("a different connection: %+v", res)
	}
	if res, _ := dst.ImportSystem(ctx, "org_connections", []map[string]any{{"id": "o1:microsoft", "org": "o1", "provider": "apple", "client_id": "x", "enabled": true, "created_at": now, "updated_at": now}}, "create", true); res[0].Outcome != auth.PortableInvalid {
		t.Errorf("a provider organizations don't have: %+v", res)
	}
}

// Erasing a user removes every identity they have, the ones through an organization's connection too.
func TestErasingAUserRemovesTheirOrganizationIdentities(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreateUser(ctx, auth.User{ID: "u1", Email: "u1@example.com", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for id, org := range map[string]string{"i1": "", "i2": "o1", "i3": "o2"} {
		if err := s.PutIdentity(ctx, auth.Identity{ID: id, UserID: "u1", Provider: "google", Org: org, Subject: "sub-" + org, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EraseUser(ctx, "u1", "erased-u1@example.invalid", now); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.ListIdentities(ctx, "u1"); len(ids) != 0 {
		t.Errorf("identities after erasure: %+v", ids)
	}
	for _, org := range []string{"", "o1", "o2"} {
		if _, err := s.IdentityIn(ctx, "google", org, "sub-"+org); err == nil {
			t.Errorf("the identity of %q can still sign in", org)
		}
	}
}

// Every organization keeps a secret of the same name, next to the realm's and a database's: the
// collection's unique index is (database, name), so an organization's scope must not look like
// the realm's.
func TestOrganizationsKeepSecretsOfTheSameName(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	cipher, _ := auth.NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc := &auth.Users{Store: s, Cipher: cipher}
	const name = "SSO_GOOGLE_CLIENT_SECRET"
	for scope, set := range map[string]func() error{
		"realm":    func() error { return svc.SetSecret(ctx, "", name, "realm", "t") },
		"database": func() error { return svc.SetSecret(ctx, "app", name, "database", "t") },
		"org1":     func() error { return svc.SetOrgSecret(ctx, "org1", name, "one", "t") },
		"org2":     func() error { return svc.SetOrgSecret(ctx, "org2", name, "two", "t") },
	} {
		if err := set(); err != nil {
			t.Fatalf("%s: %v", scope, err)
		}
	}
	// Replacing one doesn't touch the others.
	if err := svc.SetOrgSecret(ctx, "org1", name, "one-again", "t"); err != nil {
		t.Fatal(err)
	}
	for org, want := range map[string]string{"org1": "one-again", "org2": "two"} {
		if v, ok, err := svc.OrgSecret(ctx, org, name); err != nil || !ok || v != want {
			t.Errorf("%s = %q, %v, %v", org, v, ok, err)
		}
	}
	list, err := s.ListSecrets(ctx)
	if err != nil || len(list) != 4 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, sec := range list {
		if sec.Org != "" && sec.Database != "" {
			t.Errorf("an organization's secret reads as a database's: %+v", sec)
		}
		if sec.Org == "" && sec.Database == "" && sec.Scope() != "" {
			t.Errorf("the realm's secret has a scope: %+v", sec)
		}
	}
	if err := svc.DeleteOrgSecret(ctx, "org1", name); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.OrgSecret(ctx, "org2", name); !ok {
		t.Error("deleting one organization's secret removed another's")
	}
	if _, err := s.SecretByScope(ctx, "", name); err != nil {
		t.Errorf("the realm's secret went: %v", err)
	}
}
