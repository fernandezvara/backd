package auth_test

import (
	"context"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestAuditEntriesGateWhatIsRecorded(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)
	svc.Settings.AuditEntries = map[string]bool{AuditRoleAdd: false, "user.login": true}

	svc.Audit(ctx, AuditRoleAdd, "user:u1", nil)    // switched off
	svc.Audit(ctx, AuditRoleRemove, "user:u1", nil) // default: on
	svc.Audit(ctx, "user.login", "user:u1", nil)    // switched on, off by default

	var got []string
	for _, r := range store.AuditRecords() {
		got = append(got, r.Action)
	}
	if len(got) != 2 || got[0] != AuditRoleRemove || got[1] != "user.login" {
		t.Errorf("recorded %v, want [role.remove user.login]", got)
	}
}

func TestSyncAuditEntriesRecordsOnlyChanges(t *testing.T) {
	ctx := context.Background()
	store := authtest.NewMemStore()
	clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	svc := newUsers(store, &clock)

	count := func() int {
		n := 0
		for _, r := range store.AuditRecords() {
			if r.Action == registry.AuditEntriesChanged {
				n++
			}
		}
		return n
	}
	sync := func() {
		t.Helper()
		if err := svc.SyncAuditEntries(ctx); err != nil {
			t.Fatal(err)
		}
	}

	sync()
	if count() != 0 {
		t.Fatal("a realm that never touched audit.entries must record nothing")
	}
	svc.Settings.AuditEntries = map[string]bool{AuditRoleAdd: false}
	sync()
	if count() != 1 {
		t.Fatalf("turning an entry off must be recorded once, got %d", count())
	}
	sync() // restart with the same settings
	if count() != 1 {
		t.Fatalf("a restart with no change must record nothing, got %d", count())
	}
	svc.Settings.AuditEntries = map[string]bool{AuditRoleAdd: true} // back to the default
	sync()
	if count() != 2 {
		t.Fatalf("turning it back on must be recorded, got %d", count())
	}
	last := store.AuditRecords()[len(store.AuditRecords())-1]
	if changed, _ := last.Details["changed"].([]string); len(changed) != 1 || changed[0] != AuditRoleAdd {
		t.Errorf("details = %v", last.Details)
	}
	if last.Actor != ActorConfig || last.Target != "audit.entries" {
		t.Errorf("actor/target = %s / %s", last.Actor, last.Target)
	}
}
