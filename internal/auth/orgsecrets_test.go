package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// The same name at the realm, a database and an organization is three secrets.
func TestOrganizationSecretsAreAScopeOfTheirOwn(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "acme", &clock, NewSecretCache())

	for scope, set := range map[string]func() error{
		"realm":    func() error { return svc.SetSecret(ctx, "", "SSO_KEY", "realm-value", "user:a") },
		"database": func() error { return svc.SetSecret(ctx, "app", "SSO_KEY", "database-value", "user:a") },
		"org1":     func() error { return svc.SetOrgSecret(ctx, "org1", "SSO_KEY", "org1-value", "user:a") },
		"org2":     func() error { return svc.SetOrgSecret(ctx, "org2", "SSO_KEY", "org2-value", "user:a") },
	} {
		if err := set(); err != nil {
			t.Fatalf("%s: %v", scope, err)
		}
	}
	for org, want := range map[string]string{"org1": "org1-value", "org2": "org2-value"} {
		got, ok, err := svc.OrgSecret(ctx, org, "SSO_KEY")
		if err != nil || !ok || got != want {
			t.Errorf("%s = %q, %v, %v; want %q", org, got, ok, err, want)
		}
	}
	if _, ok, _ := svc.OrgSecret(ctx, "org3", "SSO_KEY"); ok {
		t.Error("an organization without the secret read another's")
	}

	// A function declaring the name reads the database's and the realm's, never an organization's.
	values, missing, err := svc.ResolveSecrets(ctx, []registry.SecretRef{{Name: "SSO_KEY"}, {Realm: true, Name: "SSO_KEY"}}, "app")
	if err != nil || len(missing) != 0 || values["SSO_KEY"] != "database-value" || values["realm.SSO_KEY"] != "realm-value" {
		t.Errorf("values = %v, missing = %v, err = %v", values, missing, err)
	}

	list, err := svc.ListSecrets(ctx)
	if err != nil || len(list) != 4 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	var orgs []string
	for _, m := range list {
		if m.Org != "" {
			orgs = append(orgs, m.Org)
			if m.Database != "" {
				t.Errorf("an organization secret also names a database: %+v", m)
			}
		}
	}
	if strings.Join(orgs, ",") != "org1,org2" {
		t.Errorf("organization secrets listed = %v", orgs)
	}

	if err := svc.DeleteOrgSecret(ctx, "org1", "SSO_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteOrgSecret(ctx, "org1", "SSO_KEY"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, ok, _ := svc.OrgSecret(ctx, "org1", "SSO_KEY"); ok {
		t.Error("a deleted secret is still read (cache not cleared)")
	}
	// The others stay.
	if got, ok, _ := svc.OrgSecret(ctx, "org2", "SSO_KEY"); !ok || got != "org2-value" {
		t.Errorf("org2 = %q, %v", got, ok)
	}
	if got, _, _ := svc.ResolveSecrets(ctx, []registry.SecretRef{{Name: "SSO_KEY"}}, "app"); got["SSO_KEY"] != "database-value" {
		t.Error("deleting an organization's secret touched the database's")
	}
}

// An organization id can't make a database scope: the ids differ even when the names look alike.
func TestOrganizationScopeNeverEqualsADatabaseName(t *testing.T) {
	if registry.ValidName(OrgScope("abc")) {
		t.Fatalf("%q is a valid database name: an organization secret could be read as a database's", OrgScope("abc"))
	}
	for _, id := range []string{"x", "org", "a-b_c"} {
		if registry.ValidName(OrgScope(id)) {
			t.Errorf("OrgScope(%q) is a valid name", id)
		}
	}
}

// Setting is audited without the value; a change reaches the reader within the same call.
func TestOrganizationSecretAuditAndChange(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	svc := newSecretsUsers(t, store, "acme", &clock, NewSecretCache())

	if err := svc.SetOrgSecret(ctx, "org1", "SSO_GOOGLE_CLIENT_SECRET", "first", "user:a"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := svc.OrgSecret(ctx, "org1", "SSO_GOOGLE_CLIENT_SECRET"); v != "first" {
		t.Fatalf("v = %q", v)
	}
	if err := svc.SetOrgSecret(ctx, "org1", "SSO_GOOGLE_CLIENT_SECRET", "second", "user:b"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := svc.OrgSecret(ctx, "org1", "SSO_GOOGLE_CLIENT_SECRET"); v != "second" {
		t.Errorf("after a change v = %q", v)
	}
	if err := svc.SetOrgSecret(ctx, "org1", "not-valid", "x", "user:a"); err == nil || !strings.Contains(err.Error(), "invalid secret name") {
		t.Errorf("invalid name accepted: %v", err)
	}

	recs, _, err := svc.AuditTrail(ctx, AuditFilter{Action: AuditSecretSet, Limit: 10})
	if err != nil || len(recs) != 2 {
		t.Fatalf("audit = %+v, %v", recs, err)
	}
	for _, r := range recs {
		if r.Target != "secret:org:org1/SSO_GOOGLE_CLIENT_SECRET" || r.Details["org"] != "org1" {
			t.Errorf("record = %+v", r)
		}
		for _, v := range r.Details {
			if s, _ := v.(string); s == "first" || s == "second" {
				t.Errorf("the value is in the audit record: %+v", r)
			}
		}
	}
}

func TestOrganizationSecretsNeedTheMasterKey(t *testing.T) {
	svc := &Users{Store: authtest.NewMemStore(), Realm: "acme"}
	if err := svc.SetOrgSecret(context.Background(), "org1", "SSO_KEY", "x", "user:a"); !errors.Is(err, ErrSecretsNotConfigured) {
		t.Errorf("err = %v", err)
	}
	if _, ok, err := svc.OrgSecret(context.Background(), "org1", "SSO_KEY"); ok || err != nil {
		t.Errorf("ok = %v, err = %v", ok, err)
	}
}
