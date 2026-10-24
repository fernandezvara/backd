package auth_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

// The system data of a user's export: everything the realm holds about them, and none of the
// credentials or other people's data.
func TestAccountExportHoldsTheirDataAndNoSecrets(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)

	u, err := svc.Create(ctx, "ada@example.com", ptr("dev-p4ssw0rd!"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := svc.Create(ctx, "bob@example.com", ptr("dev-p4ssw0rd!"))
	_ = store.CreateSession(ctx, Session{ID: "sess1", UserID: u.ID, TokenHash: "SESSION-TOKEN-HASH", CreatedAt: clock, LastUsedAt: clock, ExpiresAt: clock.Add(time.Hour), Methods: []string{"password"}})
	_ = store.PutMFA(ctx, MFARecord{UserID: u.ID, Secret: "TOTP-SECRET", EnabledAt: clock, LastStep: 1000, RecoveryCodes: []string{"HASH-A", "HASH-B"}})
	_ = store.AppendConsentRecord(ctx, ConsentRecord{ID: "c1", UserID: u.ID, Consent: "terms", Version: "1", Locale: "en", TextHash: "abc", Action: ConsentAccepted, Method: ConsentMethodSignup, At: clock, IP: "203.0.113.9"})
	_ = store.CreateInvitation(ctx, Invitation{ID: "inv1", TokenHash: "INVITATION-TOKEN-HASH", Email: u.Email, CreatedBy: "key:ops", CreatedAt: clock, ExpiresAt: clock.Add(time.Hour)})
	_ = store.CreateInvitation(ctx, Invitation{ID: "inv2", TokenHash: "OTHER", Email: other.Email, CreatedBy: "key:ops", CreatedAt: clock, ExpiresAt: clock.Add(time.Hour)})
	_ = store.AppendAudit(ctx, AuditRecord{ID: "a1", At: clock, Action: "user.login", Actor: "user:" + u.ID, Target: "user:" + u.ID, Details: map[string]any{"ip": "198.51.100.7", "note": "OTHER-PERSON-DETAIL"}})
	_ = store.AppendAudit(ctx, AuditRecord{ID: "a2", At: clock.Add(time.Minute), Action: "user.roles_changed", Actor: "key:ops", Target: "user:" + u.ID})
	_ = store.AppendAudit(ctx, AuditRecord{ID: "a3", At: clock, Action: "user.login", Actor: "user:" + other.ID, Target: "user:" + other.ID})
	_ = store.RecordInvocation(ctx, InvocationRecord{ID: "i1", At: clock, Function: "main/export", Actor: "user:" + u.ID, Status: "ok", Mode: "sync", Origin: "http", Logs: []LogLine{{Level: "info", Line: "SECRET-LOG-LINE"}}})
	_ = store.RecordInvocation(ctx, InvocationRecord{ID: "i2", At: clock, Function: "main/export", Actor: "user:" + other.ID, Status: "ok"})

	parts, err := svc.AccountExport(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]AccountPart{}
	for _, p := range parts {
		byName[p.Name] = p
	}
	for _, name := range []string{"account", "identities", "sessions", "devices", "mfa", "organizations", "invitations", "consents", "audit", "invocations"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("part %s is missing", name)
		}
	}
	if a := byName["account"].Rows[0]; a["email"] != "ada@example.com" || a["id"] != u.ID {
		t.Errorf("account = %v", a)
	}
	if id := byName["identities"].Rows; len(id) != 1 || id[0]["provider"] != ProviderPassword {
		t.Errorf("identities = %v", id)
	}
	if m := byName["mfa"].Rows[0]; m["enabled"] != true || m["recovery_codes_remaining"] != 2 {
		t.Errorf("mfa = %v", m)
	}
	if inv := byName["invitations"].Rows; len(inv) != 1 || inv[0]["id"] != "inv1" {
		t.Errorf("invitations = %v", inv)
	}
	// The creation of the account, the login and the role change; not the other user's login.
	var audited []string
	for _, r := range byName["audit"].Rows {
		audited = append(audited, r["event"].(string))
		if r["id"] == "a3" {
			t.Error("another user's audit record")
		}
	}
	slices.Sort(audited)
	if strings.Join(audited, ",") != "user.create,user.login,user.roles_changed" {
		t.Errorf("audit events = %v", audited)
	}
	if iv := byName["invocations"].Rows; len(iv) != 1 || iv[0]["function"] != "main/export" {
		t.Errorf("invocations = %v", iv)
	}
	if c := byName["consents"].Rows; len(c) != 1 || c[0]["ip"] != "203.0.113.9" || c[0]["action"] != ConsentAccepted {
		t.Errorf("consents = %v", c)
	}

	// Nothing that is a credential, a token, a secret, or somebody else's, is in any part.
	all, _ := json.Marshal(parts)
	for _, forbidden := range []string{"argon2", "SESSION-TOKEN-HASH", "TOTP-SECRET", "HASH-A", "INVITATION-TOKEN-HASH", "OTHER-PERSON-DETAIL", "SECRET-LOG-LINE", "bob@example.com", "198.51.100.7"} {
		if strings.Contains(string(all), forbidden) {
			t.Errorf("the export holds %q", forbidden)
		}
	}
}
