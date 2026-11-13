// Package dbnames names the MongoDB databases and collections backd keeps for itself, for the
// tools that list them without connecting (`backd databases`, for least-privilege grants).
// internal/mongodb, which creates them, is checked against it.
package dbnames

import "github.com/fernandezvara/backd/internal/registry"

// The deployment database records what each realm was last provisioned with.
const (
	DeploymentDatabase   = "backd___deployment"
	DeploymentCollection = "realms"
)

// SystemDatabase is the database of a realm's own data: users, sessions, jobs, audit.
func SystemDatabase(realm string) string { return realm + registry.SystemDatabaseSuffix }

// SystemCollections are the collections of every system database.
func SystemCollections() []string {
	return []string{
		"users", "identities", "sessions", "api_keys", "invitations", "login_attempts", "audit", "secrets",
		"invocations", "jobs", "file_journal", "storage_usage", "file_deletions", "schema_checks", "schedules",
		"email_tokens", "idempotency", "oauth_states", "auth_challenges", "passkeys", "mfa", "devices", "orgs", "org_members",
		"org_templates", "org_member_seq", "org_teams", "org_invitations", "org_connections", "user_exports",
		"security_state", "data_keys", "app_config", "app_config_history", "consent_records", "push_outbox",
		"push_sends", "login_codes",
	}
}
