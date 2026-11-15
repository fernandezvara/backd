package auth_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func signInAuditSetup(t *testing.T) (*Users, *authtest.MemStore) {
	t.Helper()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Cipher, _ = NewSecretCipher([]byte("01234567890123456789012345678901"))
	svc.Settings.Roles = map[string]registry.Role{"ops": {Admin: registry.RightUsers}}
	return svc, store
}

func recordsOf(store *authtest.MemStore, action string) []AuditRecord {
	var out []AuditRecord
	for _, r := range store.AuditRecords() {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func TestUserLoginIsRecordedOnlyWhenSwitchedOn(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	if _, err := svc.Create(ctx, "bob@example.com", new("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "bob@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(recordsOf(store, AuditUserLogin)); n != 0 {
		t.Fatalf("user.login is off by default, got %d records", n)
	}

	svc.Settings.AuditEntries = map[string]bool{AuditUserLogin: true}
	p, _, err := svc.Login(ctx, "bob@example.com", "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	got := recordsOf(store, AuditUserLogin)
	if len(got) != 1 || got[0].Target != "user:"+p.User.ID || got[0].Details["session_id"] != p.Session.ID {
		t.Fatalf("records = %+v", got)
	}
	if m, _ := got[0].Details["methods"].([]string); len(m) != 1 || m[0] != "password" {
		t.Errorf("methods = %v", got[0].Details["methods"])
	}
}

func TestAdminLoginIsNotAlsoAUserLogin(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	svc.Settings.AuditEntries = map[string]bool{AuditUserLogin: true}
	u, err := svc.Create(ctx, "root@example.com", new("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRole(ctx, u.Email, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "root@example.com", "dev-p4ssw0rd!", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(recordsOf(store, AuditAdminLogin)); n != 1 {
		t.Errorf("admin.login records = %d, want 1", n)
	}
	if n := len(recordsOf(store, AuditUserLogin)); n != 0 {
		t.Errorf("an admin sign-in must not also be user.login, got %d", n)
	}
}

func TestFailedLoginRecordsTheAccountOrAHashNeverTheAddress(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	u, err := svc.Create(ctx, "bob@example.com", new("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = svc.Login(ctx, "bob@example.com", "not the password", "10.0.0.1")
	_, _, _ = svc.Login(ctx, "ghost@example.com", "not the password", "10.0.0.1")

	got := recordsOf(store, AuditUserLoginFailed)
	if len(got) != 2 {
		t.Fatalf("records = %d, want 2: %+v", len(got), got)
	}
	if got[0].Target != "user:"+u.ID || got[0].Details["reason"] != ReasonInvalidCredentials {
		t.Errorf("known account: %+v", got[0])
	}
	if got[1].Target != ActorAnonymous || got[1].Details["address_hash"] == nil || got[1].Details["reason"] != ReasonInvalidCredentials {
		t.Errorf("unknown address: %+v", got[1])
	}
	for _, r := range got {
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "ghost@") || strings.Contains(string(raw), "bob@") || strings.Contains(string(raw), "not the password") {
			t.Errorf("the record holds an address or a password: %s", raw)
		}
	}
}

func TestFailedLoginReasons(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	if _, err := svc.Create(ctx, "off@example.com", new("dev-p4ssw0rd!")); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetDisabled(ctx, "off@example.com", true); err != nil {
		t.Fatal(err)
	}
	_, _, _ = svc.Login(ctx, "off@example.com", "dev-p4ssw0rd!", "10.0.0.2")
	got := recordsOf(store, AuditUserLoginFailed)
	if len(got) != 1 || got[0].Details["reason"] != ReasonDisabled {
		t.Errorf("a disabled account: %+v", got)
	}
}

func TestFailedLoginsCannotFloodTheTrail(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	svc.Settings.LoginThrottle = registry.LoginThrottle{AccountThreshold: 10000, IPThreshold: 10000, MaxDelay: time.Second}
	for range 120 {
		_, _, _ = svc.Login(ctx, "ghost@example.com", "not the password", "10.0.0.3")
	}
	got := recordsOf(store, AuditUserLoginFailed)
	// The 1st, 10th and 100th failure of the window.
	if len(got) != 3 {
		t.Fatalf("120 failures wrote %d records, want 3", len(got))
	}
	if got[1].Details["attempts"] != 10 || got[2].Details["attempts"] != 100 {
		t.Errorf("attempts = %v, %v", got[1].Details["attempts"], got[2].Details["attempts"])
	}
}

func TestFailedLoginIsNotRecordedWhenSwitchedOff(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	svc.Settings.AuditEntries = map[string]bool{AuditUserLoginFailed: false}
	_, _, _ = svc.Login(ctx, "ghost@example.com", "not the password", "10.0.0.4")
	if n := len(recordsOf(store, AuditUserLoginFailed)); n != 0 {
		t.Errorf("records = %d", n)
	}
}

func TestFailedLinkAndCodeRedemptionsAreRecorded(t *testing.T) {
	ctx := context.Background()
	svc, store := signInAuditSetup(t)
	if _, err := svc.VerifyMagicLinkCode(ctx, "no-such-request", "123456", "verifier", "10.0.0.5"); err == nil {
		t.Fatal("an unknown request must fail")
	}
	if _, err := svc.RedeemLoginCode(ctx, "no-such-code", "verifier"); err == nil {
		t.Fatal("an unknown login code must fail")
	}
	got := recordsOf(store, AuditUserLoginFailed)
	if len(got) != 2 || got[0].Details["reason"] != ReasonInvalidLink || got[1].Details["reason"] != ReasonInvalidCode {
		t.Errorf("records = %+v", got)
	}
}
