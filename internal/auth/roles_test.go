package auth_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func newRoleUsers(t *testing.T) (*Users, *authtest.MemStore) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Settings.Roles = map[string]registry.Role{
		"admin":  {Users: []string{"ada@example.com"}},
		"editor": {Users: []string{"ada@example.com", "bob@example.com"}},
		"viewer": {},
	}
	return svc, store
}

func rolesOf(t *testing.T, store *authtest.MemStore, email string) []string {
	t.Helper()
	u, err := store.UserByEmail(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	r := slices.Clone(u.Roles)
	slices.Sort(r)
	return r
}

func TestRoleSeedsOnCreate(t *testing.T) {
	svc, store := newRoleUsers(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "ADA@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Signup(ctx, "bob@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	if got := rolesOf(t, store, "ada@example.com"); !reflect.DeepEqual(got, []string{"admin", "editor"}) {
		t.Errorf("ada = %v", got)
	}
	if got := rolesOf(t, store, "bob@example.com"); !reflect.DeepEqual(got, []string{"editor"}) {
		t.Errorf("bob = %v", got)
	}
}

func TestApplyRoleSeeds(t *testing.T) {
	svc, store := newRoleUsers(t)
	ctx := context.Background()
	// Users created before the seeds existed.
	plain := *svc
	plain.Settings.Roles = nil
	for _, e := range []string{"ada@example.com", "carl@example.com"} {
		if _, err := plain.Create(ctx, e, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.AddRole(ctx, "carl@example.com", "viewer"); err != nil {
		t.Fatal(err)
	}

	n, err := svc.ApplyRoleSeeds(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ApplyRoleSeeds = %d, %v; want 1 user updated", n, err)
	}
	if got := rolesOf(t, store, "ada@example.com"); !reflect.DeepEqual(got, []string{"admin", "editor"}) {
		t.Errorf("ada = %v", got)
	}
	// Idempotent, and other assignments are kept.
	if n, _ := svc.ApplyRoleSeeds(ctx); n != 0 {
		t.Errorf("second run updated %d users", n)
	}
	if got := rolesOf(t, store, "carl@example.com"); !reflect.DeepEqual(got, []string{"viewer"}) {
		t.Errorf("carl = %v", got)
	}
}

func TestAddRemoveRole(t *testing.T) {
	svc, store := newRoleUsers(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "carl@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRole(ctx, "carl@example.com", "root"); !errors.Is(err, ErrUndeclaredRole) {
		t.Errorf("undeclared role: %v", err)
	}
	if err := svc.AddRole(ctx, "nobody@example.com", "viewer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	for range 2 { // adding twice keeps one
		if err := svc.AddRole(ctx, "carl@example.com", "viewer"); err != nil {
			t.Fatal(err)
		}
	}
	if got := rolesOf(t, store, "carl@example.com"); !reflect.DeepEqual(got, []string{"viewer"}) {
		t.Errorf("after add = %v", got)
	}
	if err := svc.RemoveRole(ctx, "carl@example.com", "viewer"); err != nil {
		t.Fatal(err)
	}
	if got := rolesOf(t, store, "carl@example.com"); len(got) != 0 {
		t.Errorf("after remove = %v", got)
	}
	if !svc.IsSeeded("ada@example.com", "admin") || svc.IsSeeded("carl@example.com", "viewer") {
		t.Error("IsSeeded")
	}
}

func TestReportRoles(t *testing.T) {
	svc, _ := newRoleUsers(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "ada@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "carl@example.com", nil); err != nil {
		t.Fatal(err)
	}
	_ = svc.AddRole(ctx, "carl@example.com", "viewer")
	// A role realm.yaml no longer declares.
	old := *svc
	old.Settings.Roles = map[string]registry.Role{"legacy": {}}
	_ = old.AddRole(ctx, "ada@example.com", "legacy")

	rep, err := svc.ReportRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.DBOnly, map[string][]string{"carl@example.com": {"viewer"}}) ||
		!reflect.DeepEqual(rep.Undeclared, map[string][]string{"ada@example.com": {"legacy"}}) {
		t.Errorf("report = %+v", rep)
	}
}
