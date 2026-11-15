package registry

import (
	"fmt"
	"sort"
	"strings"
)

// AuditAction is one entry of the audit trail that a realm can switch on or off with
// `audit.entries` in realm.yaml (design/sign-in-audit.md). Every action the server records is in
// AuditActions; a test fails when one is recorded that is not, which is what lets the setting
// cover actions added later.
type AuditAction struct {
	Name  string
	Group string // the heading the template writes it under
	// Off is true for the actions that are not recorded unless the realm turns them on.
	Off bool
}

// AuditEntriesChanged is recorded at start when `audit.entries` changed since the last start. It
// is how a change to the trail is itself visible, so it can't be switched off and is not in
// AuditActions.
const AuditEntriesChanged = "audit.entries_changed"

// AuditActions lists every configurable audit action, grouped for the template.
var AuditActions = []AuditAction{
	{Name: "admin.login", Group: "sign-in"},
	{Name: "admin.refused", Group: "sign-in"},
	{Name: "identity.signin_refused", Group: "sign-in"},
	{Name: "org.sso.login", Group: "sign-in"},
	{Name: "passkey.login", Group: "sign-in"},
	{Name: "passkey.login_failed", Group: "sign-in"},
	{Name: "user.login", Group: "sign-in", Off: true},
	{Name: "user.login_failed", Group: "sign-in"},
	{Name: "user.step_failed", Group: "sign-in"},
	{Name: "apikey.create", Group: "users, roles, sessions and keys"},
	{Name: "apikey.revoke", Group: "users, roles, sessions and keys"},
	{Name: "invitation.create", Group: "users, roles, sessions and keys"},
	{Name: "invitation.revoke", Group: "users, roles, sessions and keys"},
	{Name: "invitation.sent", Group: "users, roles, sessions and keys"},
	{Name: "password.expire", Group: "users, roles, sessions and keys"},
	{Name: "password.expire_notify", Group: "users, roles, sessions and keys"},
	{Name: "password.reset_link", Group: "users, roles, sessions and keys"},
	{Name: "realm.bootstrap", Group: "users, roles, sessions and keys"},
	{Name: "role.add", Group: "users, roles, sessions and keys"},
	{Name: "role.remove", Group: "users, roles, sessions and keys"},
	{Name: "session.revoke", Group: "users, roles, sessions and keys"},
	{Name: "session.revoke_all", Group: "users, roles, sessions and keys"},
	{Name: "user.claimed", Group: "users, roles, sessions and keys"},
	{Name: "user.consent_recorded", Group: "users, roles, sessions and keys"},
	{Name: "user.create", Group: "users, roles, sessions and keys"},
	{Name: "user.data_export_downloaded", Group: "users, roles, sessions and keys"},
	{Name: "user.data_export_expired", Group: "users, roles, sessions and keys"},
	{Name: "user.data_export_failed", Group: "users, roles, sessions and keys"},
	{Name: "user.data_export_requested", Group: "users, roles, sessions and keys"},
	{Name: "user.data_export_requested_silently", Group: "users, roles, sessions and keys"},
	{Name: "user.data_exported", Group: "users, roles, sessions and keys"},
	{Name: "user.delete", Group: "users, roles, sessions and keys"},
	{Name: "user.delete_account", Group: "users, roles, sessions and keys"},
	{Name: "user.disable", Group: "users, roles, sessions and keys"},
	{Name: "user.email_change_reverted", Group: "users, roles, sessions and keys"},
	{Name: "user.email_changed", Group: "users, roles, sessions and keys"},
	{Name: "user.enable", Group: "users, roles, sessions and keys"},
	{Name: "user.erased", Group: "users, roles, sessions and keys"},
	{Name: "user.networks", Group: "users, roles, sessions and keys"},
	{Name: "user.password", Group: "users, roles, sessions and keys"},
	{Name: "user.password_change", Group: "users, roles, sessions and keys"},
	{Name: "user.password_removed", Group: "users, roles, sessions and keys"},
	{Name: "user.password_reset", Group: "users, roles, sessions and keys"},
	{Name: "user.password_set", Group: "users, roles, sessions and keys"},
	{Name: "user.purged_unverified", Group: "users, roles, sessions and keys"},
	{Name: "user.signup", Group: "users, roles, sessions and keys"},
	{Name: "user.verify_email", Group: "users, roles, sessions and keys"},
	{Name: "passkey.clone_suspected", Group: "second factors and passkeys"},
	{Name: "passkey.registered", Group: "second factors and passkeys"},
	{Name: "passkey.removed", Group: "second factors and passkeys"},
	{Name: "user.mfa_disabled", Group: "second factors and passkeys"},
	{Name: "user.mfa_enabled", Group: "second factors and passkeys"},
	{Name: "user.mfa_reset", Group: "second factors and passkeys"},
	{Name: "user.reauth", Group: "second factors and passkeys"},
	{Name: "user.recovery_code_used", Group: "second factors and passkeys"},
	{Name: "user.recovery_codes_regenerated", Group: "second factors and passkeys"},
	{Name: "identity.apple_revoked", Group: "sign-in methods"},
	{Name: "identity.linked", Group: "sign-in methods"},
	{Name: "identity.unlinked", Group: "sign-in methods"},
	{Name: "org.claims", Group: "organizations"},
	{Name: "org.create", Group: "organizations"},
	{Name: "org.delete", Group: "organizations"},
	{Name: "org.deleted", Group: "organizations"},
	{Name: "org.invitation_accept", Group: "organizations"},
	{Name: "org.invitation_create", Group: "organizations"},
	{Name: "org.invitation_link", Group: "organizations"},
	{Name: "org.invitation_revoke", Group: "organizations"},
	{Name: "org.member_add", Group: "organizations"},
	{Name: "org.member_leave", Group: "organizations"},
	{Name: "org.member_remove", Group: "organizations"},
	{Name: "org.orphaned", Group: "organizations"},
	{Name: "org.owner_assign", Group: "organizations"},
	{Name: "org.reactivate", Group: "organizations"},
	{Name: "org.require_mfa", Group: "organizations"},
	{Name: "org.roles", Group: "organizations"},
	{Name: "org.sso.deleted", Group: "organizations"},
	{Name: "org.sso.disabled", Group: "organizations"},
	{Name: "org.sso.set", Group: "organizations"},
	{Name: "org.sso_required", Group: "organizations"},
	{Name: "org.team_create", Group: "organizations"},
	{Name: "org.team_delete", Group: "organizations"},
	{Name: "org.team_deleted", Group: "organizations"},
	{Name: "org.team_member_add", Group: "organizations"},
	{Name: "org.team_member_remove", Group: "organizations"},
	{Name: "org.team_role_change", Group: "organizations"},
	{Name: "org.team_role_grant", Group: "organizations"},
	{Name: "org.team_role_revoke", Group: "organizations"},
	{Name: "org.team_update", Group: "organizations"},
	{Name: "org.template_delete", Group: "organizations"},
	{Name: "org.template_save", Group: "organizations"},
	{Name: "org.template_test", Group: "organizations"},
	{Name: "org.update", Group: "organizations"},
	{Name: "data.assign_org", Group: "data, export and import"},
	{Name: "data.check", Group: "data, export and import"},
	{Name: "data.create", Group: "data, export and import"},
	{Name: "data.delete", Group: "data, export and import"},
	{Name: "data.migrate", Group: "data, export and import"},
	{Name: "data.migrated", Group: "data, export and import"},
	{Name: "data.purge", Group: "data, export and import"},
	{Name: "data.restore", Group: "data, export and import"},
	{Name: "data.reveal", Group: "data, export and import"},
	{Name: "data.transfer", Group: "data, export and import"},
	{Name: "data.update", Group: "data, export and import"},
	{Name: "export", Group: "data, export and import"},
	{Name: "export.auth", Group: "data, export and import"},
	{Name: "export.done", Group: "data, export and import"},
	{Name: "import", Group: "data, export and import"},
	{Name: "import.done", Group: "data, export and import"},
	{Name: "appconfig.change", Group: "operations"},
	{Name: "consents.notify", Group: "operations"},
	{Name: "device.removed", Group: "operations"},
	{Name: "encrypt.migrate", Group: "operations"},
	{Name: "encrypt.rekey", Group: "operations"},
	{Name: "encrypt.start", Group: "operations"},
	{Name: "files.versions.regenerate", Group: "operations"},
	{Name: "function.invoke_manual", Group: "operations"},
	{Name: "job.cancel", Group: "operations"},
	{Name: "job.rerun", Group: "operations"},
	{Name: "push.devices_removed", Group: "operations"},
	{Name: "push.test", Group: "operations"},
	{Name: "schedule.pause", Group: "operations"},
	{Name: "schedule.resume", Group: "operations"},
	{Name: "secret.delete", Group: "operations"},
	{Name: "secret.key.create", Group: "operations"},
	{Name: "secret.key.delete", Group: "operations"},
	{Name: "secret.key.rotate", Group: "operations"},
	{Name: "secret.rotate_key", Group: "operations"},
	{Name: "secret.set", Group: "operations"},
	{Name: "storage.check", Group: "operations"},
	{Name: "storage.reconcile", Group: "operations"},
	{Name: "triggers.gap_resolve", Group: "operations"},
	{Name: "triggers.rescan", Group: "operations"},
}

// AuditGroups lists the groups of AuditActions in the order the template writes them.
var AuditGroups = []string{
	"sign-in",
	"users, roles, sessions and keys",
	"second factors and passkeys",
	"sign-in methods",
	"organizations",
	"data, export and import",
	"operations",
}

var auditActionByName = func() map[string]AuditAction {
	m := make(map[string]AuditAction, len(AuditActions))
	for _, a := range AuditActions {
		m[a.Name] = a
	}
	return m
}()

// IsAuditAction reports whether name is an audit action that audit.entries can name.
func IsAuditAction(name string) bool {
	_, ok := auditActionByName[name]
	return ok
}

// AuditEnabled reports whether the realm records action: its audit.entries value, else the default
// (on, except for the actions marked Off). Actions that are not configurable (AuditEntriesChanged,
// and any action missing from the table) are always recorded.
func (s RealmSettings) AuditEnabled(action string) bool {
	if on, ok := s.AuditEntries[action]; ok {
		return on
	}
	a, known := auditActionByName[action]
	return !known || !a.Off
}

// closestAuditActions returns up to three audit actions that look like name, for a message about a
// name that is not one.
func closestAuditActions(name string) []string {
	type scored struct {
		name string
		d    int
	}
	var all []scored
	for _, a := range AuditActions {
		d := editDistance(name, a.Name)
		if strings.HasPrefix(a.Name, name) || strings.HasPrefix(name, a.Name) {
			d = min(d, 1)
		}
		all = append(all, scored{a.Name, d})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].d < all[j].d })
	var out []string
	for _, s := range all {
		if len(out) == 3 || s.d > 4 {
			break
		}
		out = append(out, s.name)
	}
	return out
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// auditEntriesError is the message for an unknown name in audit.entries.
func auditEntriesError(name string) error {
	if near := closestAuditActions(name); len(near) > 0 {
		return fmt.Errorf("audit.entries: %q is not an audit action; did you mean %s? (the list is in the audit trail page of the documentation)", name, strings.Join(near, ", "))
	}
	return fmt.Errorf("audit.entries: %q is not an audit action (the list is in the audit trail page of the documentation)", name)
}
