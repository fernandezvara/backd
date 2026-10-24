package auth_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func ssoUsers(t *testing.T, ssoYAML string) (*Users, *authtest.MemStore) {
	t.Helper()
	root := t.TempDir()
	realm := "signup: open\norganizations:\n  sso: " + ssoYAML + "\n  roles:\n    owner: { manage: [org, members, roles, teams, sso] }\n    member: {}\n  default_roles: [member]\n"
	for p, content := range map[string]string{"acme/realm.yaml": realm, "acme/app/notes/schema.json": `{"type":"object"}`} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewSecretCipher([]byte(testMasterKey))
	if err != nil {
		t.Fatal(err)
	}
	store := authtest.NewMemStore()
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	svc := &Users{Store: store, Realm: "acme", Settings: reg.Realms["acme"].Settings, Cipher: cipher, Now: func() time.Time { return now }}
	ctx := context.Background()
	if err := store.CreateOrg(ctx, Org{ID: "org1", Name: "Acme", Status: OrgActive}, OrgMember{Org: "org1", User: "u1", Roles: []RoleGrant{{Role: "owner"}}}); err != nil {
		t.Fatal(err)
	}
	return svc, store
}

func TestConnectionsAreCappedPerOrganization(t *testing.T) {
	svc, _ := ssoUsers(t, "{ max_per_org: 1 }")
	ctx := context.Background()
	google := ConnectionInput{ClientID: "c-1", HostedDomain: "acme.example", ClientSecret: "s"}
	if _, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", google); err != nil {
		t.Fatal(err)
	}
	// Replacing the one it has is no new connection; a second provider is.
	if _, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", google); err != nil {
		t.Errorf("replace: %v", err)
	}
	ms := ConnectionInput{ClientID: "11111111-2222-3333-4444-555555555555", Tenant: "11111111-2222-3333-4444-555555555555", ClientSecret: "s"}
	if _, err := svc.SetOrgConnection(ctx, "u1", "org1", "microsoft", ms); !errors.Is(err, ErrSSOLimit) {
		t.Errorf("second provider: %v", err)
	}
	if _, ok, _ := svc.OrgSecret(ctx, "org1", "SSO_MICROSOFT_CLIENT_SECRET"); ok {
		t.Error("a refused connection left its secret behind")
	}
}

func TestConnectionNeedsASecretTheFirstTime(t *testing.T) {
	svc, _ := ssoUsers(t, "{ enabled: true }")
	ctx := context.Background()
	in := ConnectionInput{ClientID: "c-1", HostedDomain: "acme.example"}
	var bad *OrgInvalidError
	if _, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", in); !errors.As(err, &bad) || bad.Field != "client_secret" {
		t.Fatalf("no secret: %v", err)
	}
	if list, _ := svc.Store.OrgConnections(ctx, "org1"); len(list) != 0 {
		t.Errorf("a refused connection was saved: %+v", list)
	}
	in.ClientSecret = "s"
	if _, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", in); err != nil {
		t.Fatal(err)
	}
	in.ClientSecret = ""
	v, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", in)
	if err != nil || !v.SecretSet {
		t.Errorf("replace keeping the secret: %+v %v", v, err)
	}
}

// What a replace keeps: the id, the creator, the creation time and the members a manager removed.
func TestReplacingAConnectionKeepsWhatItOwns(t *testing.T) {
	svc, store := ssoUsers(t, "{ enabled: true }")
	ctx := context.Background()
	in := ConnectionInput{ClientID: "c-1", HostedDomain: "acme.example", ClientSecret: "s"}
	first, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", in)
	if err != nil {
		t.Fatal(err)
	}
	have, _ := store.OrgConnection(ctx, "org1", "google")
	have.Blocked = []string{"u9"}
	store.SeedConnection(have)
	in.ClientID = "c-2"
	second, err := svc.SetOrgConnection(ctx, "u1", "org1", "google", in)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) || second.ClientID != "c-2" || second.CreatedBy != "user:u1" {
		t.Errorf("first = %+v, second = %+v", first.OrgConnection, second.OrgConnection)
	}
	stored, _ := store.OrgConnection(ctx, "org1", "google")
	if !stored.HasBlocked("u9") {
		t.Errorf("blocked members were lost: %+v", stored)
	}
}
