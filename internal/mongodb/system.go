package mongodb

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/registry"
)

// System collections of a realm's system database.
const (
	UsersCollection         = "users"
	IdentitiesCollection    = "identities"
	SessionsCollection      = "sessions"
	APIKeysCollection       = "api_keys"
	InvitationsCollection   = "invitations"
	LoginAttemptsCollection = "login_attempts"
	// AuditCollection is append-only: backd inserts and reads, and MongoDB
	// removes records when they expire.
	AuditCollection = "audit"
	// SecretsCollection holds encrypted values for functions (roadmap F5).
	SecretsCollection = "secrets"
	// InvocationsCollection is append-only, like AuditCollection: one
	// record per function call (roadmap F10).
	InvocationsCollection = "invocations"
	// JobsCollection holds async function calls (roadmap F11), from
	// enqueue through a worker's claim to their result, until it's
	// removed by expiry.
	JobsCollection = "jobs"
	// IdempotencyCollection ties an Idempotency-Key to its outcome
	// (roadmap F12), scoped to the function and the caller.
	IdempotencyCollection = "idempotency"
	// OAuthStatesCollection and LoginCodesCollection hold the short-lived records of
	// the redirect sign-in flow: an attempt waiting for the provider, and a one-time
	// login code (any first factor's) waiting to be redeemed. Both are keyed by a hash
	// and expire by TTL.
	OAuthStatesCollection = "oauth_states"
	LoginCodesCollection  = "login_codes"
	// AuthChallengesCollection holds the steps between a first factor and a session
	// (a second factor to give, a TOTP being set up, later magic links and
	// re-authentication codes), keyed by a hash and expiring by TTL; MFACollection
	// holds one document per user with a second factor.
	AuthChallengesCollection = "auth_challenges"
	MFACollection            = "mfa"
	// DevicesCollection holds the devices registered for push notifications: one document per
	// device, with its token sealed and looked up by the hash of the token (or, for a web
	// subscription, of its endpoint).
	DevicesCollection = "devices"
	// ConsentRecordsCollection is the append-only history of consents accepted and
	// withdrawn: the proof, with the hash of the text shown.
	ConsentRecordsCollection = "consent_records"
	// AppConfigCollection holds the realm's app configuration values (one document) and
	// AppConfigHistoryCollection the versions that led to them (design/app-config.md §4).
	AppConfigCollection        = "app_config"
	AppConfigHistoryCollection = "app_config_history"
	// SecurityStateCollection holds the realm's operator decisions that outlive deployments:
	// the password epoch (design/breach-response.md §3).
	SecurityStateCollection = "security_state"
	// UserExportsCollection holds the records of personal data exports (design/compliance.md §4):
	// who asked, the state of the job that builds them, and where the package is stored.
	UserExportsCollection = "user_exports"
	// DataKeysCollection holds the wrapped data keys of encrypted fields, one per field and
	// generation (design/field-encryption.md §4.2).
	DataKeysCollection = "data_keys"
	// OrgsCollection, OrgMembersCollection, OrgTeamsCollection and OrgInvitationsCollection hold
	// the realm's organizations, their memberships, teams and invitations
	// (design/organizations.md §4).
	OrgsCollection           = "orgs"
	OrgMembersCollection     = "org_members"
	OrgTeamsCollection       = "org_teams"
	OrgInvitationsCollection = "org_invitations"
	// OrgConnectionsCollection holds the sign-in connections organizations set up (design/org-sso.md).
	OrgConnectionsCollection = "org_connections"
	// OrgMemberSeqCollection holds, per organization and user, the sequence number of the last
	// membership event handed to a hook (design/team-access.md §7).
	OrgMemberSeqCollection = "org_member_seq"
	// OrgTemplatesCollection holds the email templates organizations wrote (design/team-access.md §11).
	OrgTemplatesCollection = "org_templates"
	// PushSendsCollection holds one record per push send: its totals, never a message.
	PushSendsCollection = "push_sends"
	// PushOutboxCollection holds what the capture provider took instead of sending: development
	// only, with the message.
	PushOutboxCollection = "push_outbox"
	// SchemaChecksCollection holds the latest schema check report of each
	// collection (roadmap #26), replaced by the next finished check.
	SchemaChecksCollection = "schema_checks"
	// FileJournalCollection records every upload of a realm's files and what became of it;
	// FileDeletionsCollection queues the objects to delete from the bucket (roadmap #149).
	FileJournalCollection   = "file_journal"
	FileDeletionsCollection = "file_deletions"
	// StorageUsageCollection keeps running totals of the bytes and files documents
	// reference: one document for the realm ("realm") and one per user ("user:<id>").
	StorageUsageCollection = "storage_usage"
	// ImageDeclaredCollection keeps the fingerprint of each declared image version as a worker
	// last saw it, to tell a changed declaration at startup.
	ImageDeclaredCollection = "image_declared"
	// SchedulesCollection holds the runtime state of scheduled functions:
	// whether an administrator paused one.
	SchedulesCollection = "schedules"
	// EmailTokensCollection holds the hashes of the tokens sent in emails.
	EmailTokensCollection = "email_tokens"
)

// SystemDatabaseName is the MongoDB database holding a realm's users,
// sessions and keys. Three underscores can't come from a valid database
// name, so it never collides with <realm>__<database>.
func SystemDatabaseName(realm string) string { return realm + registry.SystemDatabaseSuffix }

// systemIndex is an index backd maintains on a system collection.
type systemIndex struct {
	keys   bson.D
	unique bool
	sparse bool // skip documents without the field
	ttl    bool // expire documents at the date in the (single) key field
}

type systemCollection struct {
	name      string
	validator map[string]any
	indexes   []systemIndex
	// obsolete are the keys of unique indexes an earlier version created and the current
	// indexes replace: provisioning drops them after creating the new ones, and verifying
	// reports them.
	obsolete []bson.D
}

// SystemCollectionNames returns the collections of every system database.
func SystemCollectionNames() []string {
	names := make([]string, len(systemCollections))
	for i, sc := range systemCollections {
		names[i] = sc.name
	}
	return names
}

// systemCollections is the fixed layout of every system database.
var systemCollections = []systemCollection{
	{
		name: UsersCollection,
		validator: jsonSchema([]string{"_id", "email", "email_verified", "roles", "disabled", "created_at", "updated_at"}, map[string]any{
			"_id":                  str(),
			"email":                str(),
			"email_verified":       typ("bool"),
			"roles":                map[string]any{"bsonType": "array", "items": str()},
			"disabled":             typ("bool"),
			"locale":               str(),
			"consents":             typ("object"),
			"password_expired_at":  typ("date"),
			"password_notified_at": typ("date"),
			"admin_networks":       map[string]any{"bsonType": "array", "items": str()},
			"login_networks":       map[string]any{"bsonType": "array", "items": str()},
			"created_at":           typ("date"),
			"updated_at":           typ("date"),
		}),
		// Sparse, so users from future providers that give no email don't
		// collide on a missing value.
		indexes: []systemIndex{{keys: bson.D{{Key: "email", Value: 1}}, unique: true, sparse: true}},
	},
	{
		name: IdentitiesCollection,
		validator: jsonSchema([]string{"_id", "user_id", "provider", "subject", "created_at", "updated_at"}, map[string]any{
			"_id":           str(),
			"user_id":       str(),
			"provider":      map[string]any{"bsonType": "string", "pattern": "^[a-z][a-z0-9-]{0,31}$"},
			"subject":       str(),
			"org":           str(),
			"password_hash": str(),
			// What an external provider reported (never for a password), and the
			// last sign-in; an Apple identity also holds its encrypted refresh token.
			"email":               str(),
			"email_verified":      typ("bool"),
			"last_used_at":        typ("date"),
			"password_changed_at": typ("date"),
			"apple_refresh_token": str(),
			"apple_client_id":     str(),
			"created_at":          typ("date"),
			"updated_at":          typ("date"),
		}),
		indexes: []systemIndex{
			// An organization's connection adds its id to the key (a realm identity has none).
			{keys: bson.D{{Key: "provider", Value: 1}, {Key: "org", Value: 1}, {Key: "subject", Value: 1}}, unique: true},
			// One identity per provider per user, and per organization.
			{keys: bson.D{{Key: "user_id", Value: 1}, {Key: "provider", Value: 1}, {Key: "org", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "user_id", Value: 1}}},
		},
		// The keys before organization sign-in: stricter than the ones above, they would refuse
		// a person's second identity with a provider. Dropped once the new ones exist.
		obsolete: []bson.D{
			{{Key: "provider", Value: 1}, {Key: "subject", Value: 1}},
			{{Key: "user_id", Value: 1}, {Key: "provider", Value: 1}},
		},
	},
	{
		name: SessionsCollection,
		// _id is the public session id; the token is only stored hashed.
		validator: jsonSchema([]string{"_id", "token_hash", "user_id", "created_at", "last_used_at", "expires_at"}, map[string]any{
			"_id":          str(),
			"token_hash":   str(),
			"user_id":      str(),
			"created_at":   typ("date"),
			"last_used_at": typ("date"),
			"expires_at":   typ("date"),
			"methods":      map[string]any{"bsonType": "array", "items": str()},
			"mfa":          typ("bool"),
			"org":          str(),
			"max_lifetime": typ("long"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "token_hash", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "user_id", Value: 1}}},
			{keys: bson.D{{Key: "org", Value: 1}}, sparse: true},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		name: APIKeysCollection,
		validator: jsonSchema([]string{"_id", "name", "role", "prefix", "created_at"}, map[string]any{
			"_id":          str(),
			"name":         str(),
			"role":         map[string]any{"enum": bson.A{"data", "admin"}},
			"networks":     map[string]any{"bsonType": "array", "items": str()},
			"scopes":       map[string]any{"bsonType": "array", "items": str()},
			"prefix":       str(),
			"created_at":   typ("date"),
			"last_used_at": typ("date"),
			"expires_at":   typ("date"),
			"org":          str(),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "name", Value: 1}}, unique: true}},
	},
	{
		name: InvitationsCollection,
		// _id is the public invitation id; the token is only stored hashed.
		validator: jsonSchema([]string{"_id", "token_hash", "created_by", "created_at", "expires_at"}, map[string]any{
			"_id":        str(),
			"token_hash": str(),
			"email":      str(),
			"created_by": str(),
			"created_at": typ("date"),
			"expires_at": typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "token_hash", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		name: LoginAttemptsCollection,
		validator: jsonSchema([]string{"_id", "failures", "last_failure_at", "expires_at"}, map[string]any{
			"_id":             str(),
			"failures":        typ("int"),
			"last_failure_at": typ("date"),
			"expires_at":      typ("date"),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true}},
	},
	{
		name: AuditCollection,
		validator: jsonSchema([]string{"_id", "at", "expires_at", "action", "actor"}, map[string]any{
			"_id":        str(),
			"at":         typ("date"),
			"expires_at": typ("date"),
			"action":     str(),
			"actor":      str(),
			"target":     str(),
			"details":    typ("object"),
			"request_id": str(),
			"client_ip":  str(),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "at", Value: 1}}},
			{keys: bson.D{{Key: "target", Value: 1}, {Key: "at", Value: 1}}},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		name: SecretsCollection,
		// _id is "<database>:<name>", or "realm:<name>" for the realm
		// scope; database is "" for the realm scope, never absent.
		validator: jsonSchema([]string{"_id", "database", "name", "ciphertext", "nonce", "key_id", "created_at", "updated_at", "updated_by"}, map[string]any{
			"_id":        str(),
			"database":   str(),
			"name":       str(),
			"ciphertext": str(),
			"nonce":      str(),
			"key_id":     str(),
			"created_at": typ("date"),
			"updated_at": typ("date"),
			"updated_by": str(),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "database", Value: 1}, {Key: "name", Value: 1}}, unique: true}},
	},
	{
		name: InvocationsCollection,
		validator: jsonSchema([]string{"_id", "at", "expires_at", "function", "actor", "mode", "status", "duration_ms"}, map[string]any{
			"_id":         str(),
			"at":          typ("date"),
			"expires_at":  typ("date"),
			"function":    str(), // <database>/<name>
			"actor":       str(),
			"mode":        str(),
			"status":      str(),
			"code":        str(),
			"duration_ms": typ("long"),
			"request_id":  str(),
			"job_id":      str(),
			"logs":        typ("array"), // [{level, line}], already masked by the executor
			"steps":       typ("array"), // [{n, name, status, ...}] from ctx.step
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "function", Value: 1}, {Key: "at", Value: 1}}},
			{keys: bson.D{{Key: "request_id", Value: 1}}, sparse: true},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		name: JobsCollection,
		validator: jsonSchema([]string{"_id", "database", "function", "input", "caller_actor", "status", "attempts", "timeout_ms", "created_at", "expires_at"}, map[string]any{
			"_id":             str(),
			"database":        str(),
			"function":        str(),
			"input":           map[string]any{}, // any JSON value: object, array, string, number, bool or null
			"caller_actor":    str(),
			"caller_user_id":  str(),
			"caller_org":      str(),
			"caller_key_hash": str(),
			"status":          map[string]any{"enum": bson.A{"queued", "running", "done"}},
			"attempts":        typ("int"),
			"timeout_ms":      typ("long"),
			"request_id":      str(),
			"lease_owner":     str(),
			"lease_expires":   typ("date"),
			"created_at":      typ("date"),
			"completed_at":    typ("date"),
			"expires_at":      typ("date"),
			"result":          typ("object"),
			"check":           typ("object"),
			"password_notice": typ("object"),
			"exclusive":       str(),        // only one unfinished job holds a name (unique index below)
			"steps":           typ("array"), // [{n, name, status, ...}] the running attempt reported (ctx.step)
			"steps_omitted":   typ("int"),
		}),
		indexes: []systemIndex{
			// The worker's claim query: the oldest job that's queued, or
			// whose lease has expired.
			{keys: bson.D{{Key: "status", Value: 1}, {Key: "lease_expires", Value: 1}, {Key: "created_at", Value: 1}}},
			// Listing a function's jobs, newest first (GET _admin/jobs).
			{keys: bson.D{{Key: "database", Value: 1}, {Key: "function", Value: 1}, {Key: "created_at", Value: -1}}},
			// Ordered jobs: the head of each serial key (its oldest unfinished job) is the only
			// one a worker may claim.
			{keys: bson.D{{Key: "serial_key", Value: 1}, {Key: "serial_order", Value: 1}}, sparse: true},
			// The trigger dispatcher counts a realm's unfinished trigger jobs by origin prefix.
			{keys: bson.D{{Key: "origin", Value: 1}, {Key: "status", Value: 1}}},
			// The erase jobs only (the metrics refresher counts the failed ones
			// without scanning every finished job).
			{keys: bson.D{{Key: "erase.user_id", Value: 1}}, sparse: true},
			// One unfinished job per exclusive name (a schema check): a finished job
			// loses the field, so the name is free again.
			{keys: bson.D{{Key: "exclusive", Value: 1}}, unique: true, sparse: true},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		// _id is the file's id.
		name: FileJournalCollection,
		validator: jsonSchema([]string{"_id", "database", "collection", "field", "key", "size", "status", "created_at", "updated_at", "expires_at"}, map[string]any{
			"_id":         str(),
			"database":    str(),
			"collection":  str(),
			"field":       str(),
			"document_id": str(),
			"key":         str(),
			"caller":      str(),
			"size":        typ("long"),
			"status":      str(),
			"created_at":  typ("date"),
			"updated_at":  typ("date"),
			"expires_at":  typ("date"),
			// Pending uploads (made before the document that holds them).
			"pending":       typ("bool"),
			"direct":        typ("bool"),
			"token_hash":    str(),
			"owner":         str(),
			"caller_key":    str(),
			"pending_until": typ("date"),
			"name":          str(),
			"type":          str(),
			"sha256":        str(),
			"uploaded_at":   typ("date"),
		}),
		indexes: []systemIndex{
			// What a worker looks for: uploads stuck writing or stored.
			{keys: bson.D{{Key: "status", Value: 1}, {Key: "updated_at", Value: 1}}},
			// How many open pending uploads a caller holds.
			{keys: bson.D{{Key: "caller_key", Value: 1}, {Key: "status", Value: 1}}, sparse: true},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		// _id is "realm", or "user:<id>".
		name: StorageUsageCollection,
		validator: jsonSchema([]string{"_id", "bytes", "files", "updated_at"}, map[string]any{
			"_id":        str(),
			"bytes":      typ("long"),
			"files":      typ("long"),
			"updated_at": typ("date"),
		}),
		indexes: []systemIndex{
			// The users holding the most.
			{keys: bson.D{{Key: "bytes", Value: -1}}},
		},
	},
	{
		// _id is the object's key.
		name: FileDeletionsCollection,
		validator: jsonSchema([]string{"_id", "reason", "attempts", "not_before", "created_at"}, map[string]any{
			"_id":        str(),
			"reason":     str(),
			"attempts":   typ("int"),
			"not_before": typ("date"),
			"created_at": typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "not_before", Value: 1}}},
		},
	},
	{
		// _id is "<database>/<collection>".
		name: SchemaChecksCollection,
		validator: jsonSchema([]string{"_id", "database", "collection", "job_id", "started_at", "finished_at", "scanned", "invalid", "complete", "limit", "documents"}, map[string]any{
			"_id":         str(),
			"database":    str(),
			"collection":  str(),
			"job_id":      str(),
			"started_at":  typ("date"),
			"finished_at": typ("date"),
			"scanned":     typ("long"),
			"invalid":     typ("long"),
			"complete":    typ("bool"),
			"stopped_by":  str(),
			"limit":       typ("int"),
			"schema_hash": str(),
			"documents":   typ("array"),
		}),
	},
	{
		// _id is "<database>/<function>".
		name: SchedulesCollection,
		validator: jsonSchema([]string{"_id", "paused", "changed_at"}, map[string]any{
			"_id":        str(),
			"paused":     typ("bool"),
			"changed_at": typ("date"),
			"changed_by": str(),
		}),
	},
	{
		name: EmailTokensCollection,
		// _id is the SHA-256 of the token: the token itself is never stored.
		validator: jsonSchema([]string{"_id", "purpose", "user_id", "created_at", "expires_at"}, map[string]any{
			"_id":         str(),
			"purpose":     str(),
			"user_id":     str(),
			"redirect_to": str(),
			"created_at":  typ("date"),
			"expires_at":  typ("date"),
			"used_at":     typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "user_id", Value: 1}, {Key: "purpose", Value: 1}}},
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
		},
	},
	{
		name: IdempotencyCollection,
		// _id is auth.IdempotencyID(database, name, callerSubject, key):
		// unique per function, per caller, per key, which is exactly
		// what "claim if absent, else return the existing record" needs
		// — no separate index, the default _id uniqueness is the lock.
		validator: jsonSchema([]string{"_id", "function", "caller_actor", "input_hash", "mode", "status", "created_at", "expires_at"}, map[string]any{
			"_id":          str(),
			"function":     str(), // <database>/<name>
			"caller_actor": str(),
			"input_hash":   str(),
			"mode":         map[string]any{"enum": bson.A{"sync", "async"}},
			"status":       map[string]any{"enum": bson.A{"running", "done"}},
			"result":       typ("object"), // a sync call's outcome, once done
			"job_id":       str(),         // an async call's job, once done
			"created_at":   typ("date"),
			"expires_at":   typ("date"),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true}},
	},
	{
		name: OAuthStatesCollection,
		// _id is the SHA-256 of the state value the provider echoes.
		validator: jsonSchema([]string{"_id", "provider", "intent", "code_challenge", "verifier", "nonce", "redirect_to", "created_at", "expires_at"}, map[string]any{
			"_id":            str(),
			"provider":       str(),
			"org":            str(),
			"scope_org":      str(),
			"intent":         map[string]any{"enum": bson.A{"signin", "link"}},
			"code_challenge": str(),
			"verifier":       str(),
			"nonce":          str(),
			"redirect_to":    str(),
			"invitation_id":  str(),
			"link_user_id":   str(),
			"locales":        typ("array"),
			"created_at":     typ("date"),
			"expires_at":     typ("date"),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true}},
	},
	{
		name: AuthChallengesCollection,
		// _id is the SHA-256 of the value given to the caller.
		validator: jsonSchema([]string{"_id", "kind", "user_id", "created_at", "expires_at"}, map[string]any{
			"_id":            str(),
			"kind":           map[string]any{"enum": bson.A{"mfa", "enroll", "totp_setup", "reauth", "reauth_code", "magic_link", "consent"}},
			"token_hash":     str(),
			"code_challenge": str(),
			"redirect_to":    str(),
			"email":          str(),
			"purpose":        str(),
			"locale":         str(),
			"secret":         str(),
			"session_id":     str(),
			"code_hash":      str(),
			"user_id":        str(),
			"factor":         str(),
			"methods":        map[string]any{"bsonType": "array", "items": str()},
			"consents":       typ("object"),
			"new_user":       typ("bool"),
			"profile":        typ("object"),
			"via_org":        str(),
			"attempts":       typ("int"),
			"created_at":     typ("date"),
			"expires_at":     typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
			{keys: bson.D{{Key: "user_id", Value: 1}}},
			// The token of a magic link, to find its request when the link is opened.
			{keys: bson.D{{Key: "token_hash", Value: 1}}, unique: true, sparse: true},
		},
	},
	{
		name: MFACollection,
		// _id is the user id; the TOTP secret is sealed and recovery codes are keyed hashes.
		validator: jsonSchema([]string{"_id", "secret", "enabled_at"}, map[string]any{
			"_id":            str(),
			"secret":         str(),
			"enabled_at":     typ("date"),
			"last_step":      map[string]any{"bsonType": bson.A{"int", "long"}},
			"recovery_codes": map[string]any{"bsonType": "array", "items": str()},
		}),
	},
	{
		name: DevicesCollection,
		validator: jsonSchema([]string{"_id", "user_id", "session_id", "platform", "token_hash", "token", "created_at", "last_seen"}, map[string]any{
			"_id":        str(),
			"user_id":    str(),
			"session_id": str(),
			"platform":   map[string]any{"enum": bson.A{"android", "ios", "web"}},
			"token_hash": str(),
			"token":      str(),
			"name":       str(),
			"created_at": typ("date"),
			"last_seen":  typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "token_hash", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "user_id", Value: 1}, {Key: "last_seen", Value: -1}}},
			{keys: bson.D{{Key: "session_id", Value: 1}}},
			{keys: bson.D{{Key: "last_seen", Value: 1}}},
		},
	},
	{
		name: OrgsCollection,
		validator: jsonSchema([]string{"_id", "name", "status", "require_mfa", "created_at", "updated_at"}, map[string]any{
			"_id":             str(),
			"name":            str(),
			"slug":            str(),
			"former_slug":     str(),
			"created_by":      str(),
			"status":          map[string]any{"enum": bson.A{"active", "orphaned", "deleted"}},
			"require_mfa":     typ("bool"),
			"sso_required":    typ("bool"),
			"session_max_age": typ("long"),
			"created_at":      typ("date"),
			"updated_at":      typ("date"),
			"deleted_at":      typ("date"),
		}),
		indexes: []systemIndex{
			// Unique among organizations that have one: a deleted organization gives it up.
			{keys: bson.D{{Key: "slug", Value: 1}}, unique: true, sparse: true},
			{keys: bson.D{{Key: "created_by", Value: 1}}},
			{keys: bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}}},
		},
	},
	{
		name: OrgMembersCollection,
		validator: jsonSchema([]string{"_id", "org", "user", "roles", "teams", "joined_at"}, map[string]any{
			"_id":        str(),
			"org":        str(),
			"user":       str(),
			"roles":      map[string]any{"bsonType": "array"},
			"teams":      map[string]any{"bsonType": "array", "items": str()},
			"team_roles": map[string]any{"bsonType": "array"},
			"joined_at":  typ("date"),
			"invited_by": str(),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "org", Value: 1}, {Key: "user", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "user", Value: 1}}},
			{keys: bson.D{{Key: "org", Value: 1}, {Key: "roles.role", Value: 1}}},
			{keys: bson.D{{Key: "org", Value: 1}, {Key: "team_roles.team", Value: 1}}},
		},
	},
	{
		name: OrgTemplatesCollection,
		validator: jsonSchema([]string{"_id", "org", "kind", "locale", "subject", "text", "html", "version", "updated_at"}, map[string]any{
			"_id":      str(),
			"org":      str(),
			"kind":     str(),
			"locale":   str(),
			"subject":  str(),
			"text":     str(),
			"html":     str(),
			"version":  map[string]any{"bsonType": bson.A{"int", "long"}},
			"versions": map[string]any{"bsonType": "array"},
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "org", Value: 1}}}},
	},
	{
		name: OrgMemberSeqCollection,
		validator: jsonSchema([]string{"_id", "org", "user", "seq"}, map[string]any{
			"_id":  str(),
			"org":  str(),
			"user": str(),
			"seq":  map[string]any{"bsonType": bson.A{"int", "long"}},
		}),
	},
	{
		name: OrgTeamsCollection,
		validator: jsonSchema([]string{"_id", "org", "name", "roles", "created_at"}, map[string]any{
			"_id":        str(),
			"org":        str(),
			"name":       str(),
			"roles":      map[string]any{"bsonType": "array", "items": str()},
			"created_at": typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "org", Value: 1}, {Key: "name", Value: 1}}, unique: true},
		},
	},
	{
		name: OrgInvitationsCollection,
		validator: jsonSchema([]string{"_id", "org", "token_hash", "binding", "created_at", "expires_at"}, map[string]any{
			"_id":        str(),
			"org":        str(),
			"token_hash": str(),
			"binding":    map[string]any{"enum": bson.A{"user", "email", "none"}},
			"user":       str(),
			"email":      str(),
			"roles":      map[string]any{"bsonType": "array", "items": str()},
			"teams":      map[string]any{"bsonType": "array", "items": str()},
			"max_uses":   map[string]any{"bsonType": bson.A{"int", "long"}},
			"uses":       map[string]any{"bsonType": bson.A{"int", "long"}},
			"created_by": str(),
			"created_at": typ("date"),
			"expires_at": typ("date"),
			"revoked_at": typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "token_hash", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "org", Value: 1}, {Key: "created_at", Value: -1}}},
		},
	},
	{
		name: OrgConnectionsCollection,
		validator: jsonSchema([]string{"_id", "org", "provider", "client_id", "enabled", "created_at", "updated_at"}, map[string]any{
			"_id":               str(),
			"org":               str(),
			"provider":          map[string]any{"enum": bson.A{"google", "microsoft"}},
			"client_id":         str(),
			"tenant":            str(),
			"hosted_domain":     str(),
			"email_domains":     map[string]any{"bsonType": "array", "items": str()},
			"native_client_ids": map[string]any{"bsonType": "array", "items": str()},
			"join_roles":        map[string]any{"bsonType": "array", "items": str()},
			"enabled":           typ("bool"),
			"blocked":           map[string]any{"bsonType": "array", "items": str()},
			"created_by":        str(),
			"updated_by":        str(),
			"created_at":        typ("date"),
			"updated_at":        typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "org", Value: 1}, {Key: "provider", Value: 1}}, unique: true},
		},
	},
	{
		name: UserExportsCollection,
		validator: jsonSchema([]string{"_id", "user", "status", "created_at"}, map[string]any{
			"_id":         str(),
			"user":        str(),
			"status":      map[string]any{"enum": bson.A{"queued", "running", "ready", "failed", "expired"}},
			"job":         str(),
			"admin":       str(),
			"ticket":      str(),
			"notify":      typ("bool"),
			"silent":      typ("bool"),
			"reason":      str(),
			"locale":      str(),
			"created_at":  typ("date"),
			"started_at":  typ("date"),
			"finished_at": typ("date"),
			"expires_at":  typ("date"),
			"key":         str(),
			"size":        map[string]any{"bsonType": bson.A{"int", "long"}},
			"counts":      typ("object"),
			"error":       str(),
			"downloads":   map[string]any{"bsonType": bson.A{"int", "long"}},
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "user", Value: 1}, {Key: "created_at", Value: -1}}},
			{keys: bson.D{{Key: "status", Value: 1}, {Key: "expires_at", Value: 1}}},
		},
	},
	{
		name: SecurityStateCollection,
		// One document per decision; _id names it ("password_epoch").
		validator: jsonSchema([]string{"_id"}, map[string]any{
			"_id":         str(),
			"valid_after": typ("date"),
			"set_by":      str(),
			"set_at":      typ("date"),
			"reason":      str(),
		}),
	},
	{
		name: DataKeysCollection,
		validator: jsonSchema([]string{"_id", "database", "collection", "path", "kind", "generation", "key", "wrapped", "wrap_version", "status", "created_at"}, map[string]any{
			"_id":          str(),
			"database":     str(),
			"collection":   str(),
			"path":         str(),
			"kind":         map[string]any{"enum": bson.A{"data", "index"}},
			"generation":   map[string]any{"bsonType": bson.A{"int", "long"}},
			"key":          str(),
			"wrapped":      typ("binData"),
			"wrap_version": map[string]any{"bsonType": bson.A{"int", "long"}},
			"status":       map[string]any{"enum": bson.A{"active", "decrypt_only", "retired"}},
			"created_at":   typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "path", Value: 1}, {Key: "kind", Value: 1}, {Key: "generation", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "key", Value: 1}, {Key: "wrap_version", Value: 1}}},
		},
	},
	{
		name: AppConfigCollection,
		validator: jsonSchema([]string{"_id", "version", "values", "updated_at"}, map[string]any{
			"_id":        str(),
			"version":    typ("long"),
			"values":     str(),
			"updated_at": typ("date"),
			"updated_by": str(),
		}),
	},
	{
		name: AppConfigHistoryCollection,
		validator: jsonSchema([]string{"_id", "at", "actor", "kind", "changes", "values"}, map[string]any{
			"_id":            typ("long"),
			"at":             typ("date"),
			"actor":          str(),
			"kind":           map[string]any{"enum": bson.A{"set", "rollback"}},
			"rolled_back_to": typ("long"),
			"changes":        str(),
			"values":         str(),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "at", Value: 1}}}},
	},
	{
		name: ConsentRecordsCollection,
		validator: jsonSchema([]string{"_id", "user", "consent", "version", "action", "method", "at"}, map[string]any{
			"_id":        str(),
			"user":       str(),
			"consent":    str(),
			"version":    str(),
			"locale":     str(),
			"text_hash":  str(),
			"action":     map[string]any{"enum": bson.A{"accepted", "withdrawn"}},
			"method":     map[string]any{"enum": bson.A{"signup", "prompt", "api", "admin"}},
			"evidence":   str(),
			"at":         typ("date"),
			"ip":         str(),
			"user_agent": str(),
			"expires_at": typ("date"),
		}),
		indexes: []systemIndex{
			// Set at erasure: the retention after which a worker-less TTL removes the record.
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true, sparse: true},
			{keys: bson.D{{Key: "user", Value: 1}, {Key: "at", Value: 1}}},
			{keys: bson.D{{Key: "consent", Value: 1}, {Key: "version", Value: 1}}},
		},
	},
	{
		name: PushOutboxCollection,
		validator: jsonSchema([]string{"_id", "send_id", "device_id", "platform", "status", "message", "created_at", "expires_at"}, map[string]any{
			"_id":        str(),
			"send_id":    str(),
			"user_id":    str(),
			"device_id":  str(),
			"platform":   str(),
			"status":     str(),
			"attempt":    typ("int"),
			"message":    str(),
			"created_at": typ("date"),
			"expires_at": typ("date"),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true}},
	},
	{
		name: PushSendsCollection,
		validator: jsonSchema([]string{"_id", "kind", "sender", "created_at", "expires_at"}, map[string]any{
			"_id":            str(),
			"kind":           str(),
			"sender":         str(),
			"users":          typ("int"),
			"planned":        typ("bool"),
			"batches":        typ("int"),
			"units":          typ("int"),
			"batches_done":   typ("int"),
			"units_done":     typ("int"),
			"queued":         typ("int"),
			"limited":        typ("int"),
			"no_devices":     typ("int"),
			"no_consent":     typ("int"),
			"sent":           typ("int"),
			"invalid":        typ("int"),
			"failed":         typ("int"),
			"batch_ids_done": map[string]any{"bsonType": "array", "items": str()},
			"unit_ids_done":  map[string]any{"bsonType": "array", "items": str()},
			"created_at":     typ("date"),
			"expires_at":     typ("date"),
			"completed_at":   typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true},
			{keys: bson.D{{Key: "created_at", Value: -1}}},
		},
	},
	{
		name: LoginCodesCollection,
		// _id is the SHA-256 of the login code.
		validator: jsonSchema([]string{"_id", "user_id", "method", "code_challenge", "created_at", "expires_at"}, map[string]any{
			"_id":            str(),
			"user_id":        str(),
			"method":         str(),
			"code_challenge": str(),
			"new_user":       typ("bool"),
			"profile":        typ("object"),
			"org":            str(),
			"created_at":     typ("date"),
			"expires_at":     typ("date"),
		}),
		indexes: []systemIndex{{keys: bson.D{{Key: "expires_at", Value: 1}}, ttl: true}},
	},
}

func jsonSchema(required []string, props map[string]any) map[string]any {
	req := make(bson.A, len(required))
	for i, r := range required {
		req[i] = r
	}
	return map[string]any{"$jsonSchema": map[string]any{"bsonType": "object", "required": req, "properties": props}}
}

func typ(t string) map[string]any { return map[string]any{"bsonType": t} }
func str() map[string]any         { return typ("string") }

// authRealms returns the realms with authentication enabled, sorted.
func (p *Provisioner) authRealms() []string {
	var out []string
	for _, rl := range p.Registry.SortedRealms() {
		if rl.Settings.AuthEnabled {
			out = append(out, rl.Name)
		}
	}
	return out
}

// applySystem creates or updates the system database of every realm with
// authentication enabled.
func (p *Provisioner) applySystem(ctx context.Context) error {
	for _, realm := range p.authRealms() {
		dbName := SystemDatabaseName(realm)
		existing, err := p.collectionOptions(ctx, dbName)
		if err != nil {
			return err
		}
		mdb := p.Client.Database(dbName)
		for _, sc := range systemCollections {
			if err := p.ensureCollection(ctx, mdb, existing, sc.name, sc.validator); err != nil {
				return err
			}
			coll := mdb.Collection(sc.name)
			missing, mismatched, err := compareSystemIndexes(ctx, coll, sc.indexes)
			if err != nil {
				return err
			}
			if len(mismatched) > 0 {
				return fmt.Errorf("%s.%s: %s", dbName, sc.name, strings.Join(mismatched, "; "))
			}
			for _, ix := range missing {
				opts := options.Index().SetUnique(ix.unique).SetSparse(ix.sparse)
				if ix.ttl {
					opts.SetExpireAfterSeconds(0)
				}
				name, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: ix.keys, Options: opts})
				if err != nil {
					return fmt.Errorf("create index on %s.%s: %w", dbName, sc.name, err)
				}
				p.Log.Info("index created", "database", dbName, "collection", sc.name, "index", name)
			}
			if err := dropObsoleteIndexes(ctx, coll, sc.obsolete, func(name string) {
				p.Log.Info("obsolete index dropped", "database", dbName, "collection", sc.name, "index", name)
			}); err != nil {
				return fmt.Errorf("%s.%s: %w", dbName, sc.name, err)
			}
		}
		p.warnUnknownSystemCollections(dbName, existing)
	}
	return nil
}

// verifySystem lists the differences between each system database and
// the layout backd expects.
func (p *Provisioner) verifySystem(ctx context.Context) ([]string, error) {
	var diffs []string
	for _, realm := range p.authRealms() {
		d, existing, err := p.systemDiffs(ctx, realm)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, d...)
		p.warnUnknownSystemCollections(SystemDatabaseName(realm), existing)
	}
	return diffs, nil
}

// VerifySystem checks one realm's system database, for commands that
// write to it without provisioning first.
func (p *Provisioner) VerifySystem(ctx context.Context, realm string) error {
	diffs, _, err := p.systemDiffs(ctx, realm)
	if err != nil {
		return err
	}
	if len(diffs) > 0 {
		return fmt.Errorf("the system database of realm %q is not provisioned (run `backd provision`):\n  %s", realm, strings.Join(diffs, "\n  "))
	}
	return nil
}

// systemDiffs compares one realm's system database with the expected
// layout. It also returns the existing collections' options.
func (p *Provisioner) systemDiffs(ctx context.Context, realm string) ([]string, map[string]bson.Raw, error) {
	dbName := SystemDatabaseName(realm)
	existing, err := p.collectionOptions(ctx, dbName)
	if err != nil {
		return nil, nil, err
	}
	var diffs []string
	for _, sc := range systemCollections {
		ns := dbName + "." + sc.name
		opts, ok := existing[sc.name]
		if !ok {
			diffs = append(diffs, ns+": collection missing")
			continue
		}
		if d := validatorDiff(opts, sc.validator); d != "" {
			diffs = append(diffs, ns+": "+d)
		}
		missing, mismatched, err := compareSystemIndexes(ctx, p.Client.Database(dbName).Collection(sc.name), sc.indexes)
		if err != nil {
			return nil, nil, err
		}
		diffs = append(diffs, prefixed(ns, mismatched)...)
		for _, ix := range missing {
			diffs = append(diffs, fmt.Sprintf("%s: index (%s) missing", ns, keyList(ix.keys)))
		}
		if len(sc.obsolete) > 0 {
			have, err := listIndexes(ctx, p.Client.Database(dbName).Collection(sc.name))
			if err != nil {
				return nil, nil, err
			}
			for _, k := range sc.obsolete {
				for _, e := range have {
					if e.keys == keyList(k) {
						diffs = append(diffs, fmt.Sprintf("%s: index %s (%s) is obsolete and would refuse sign-ins of organizations", ns, e.name, e.keys))
					}
				}
			}
		}
	}
	return diffs, existing, nil
}

func prefixed(prefix string, msgs []string) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = prefix + ": " + m
	}
	return out
}

// compareSystemIndexes returns the wanted indexes that don't exist and
// descriptions of existing ones whose options differ. Extra indexes are
// left alone.
// dropObsoleteIndexes removes the indexes with the given keys, if they exist.
func dropObsoleteIndexes(ctx context.Context, coll *mongo.Collection, keys []bson.D, dropped func(name string)) error {
	if len(keys) == 0 {
		return nil
	}
	existing, err := listIndexes(ctx, coll)
	if err != nil {
		return err
	}
	for _, k := range keys {
		for _, e := range existing {
			if e.keys == keyList(k) {
				if err := coll.Indexes().DropOne(ctx, e.name); err != nil {
					return fmt.Errorf("drop the obsolete index %s: %w", e.name, err)
				}
				dropped(e.name)
			}
		}
	}
	return nil
}

func compareSystemIndexes(ctx context.Context, coll *mongo.Collection, want []systemIndex) ([]systemIndex, []string, error) {
	existing, err := listIndexes(ctx, coll)
	if err != nil {
		return nil, nil, err
	}
	var missing []systemIndex
	var mismatched []string
	for _, ix := range want {
		keys := keyList(ix.keys)
		i := -1
		for j, e := range existing {
			if e.keys == keys {
				i = j
				break
			}
		}
		switch {
		case i < 0:
			missing = append(missing, ix)
		case existing[i].unique != ix.unique || existing[i].sparse != ix.sparse || existing[i].ttl != ix.ttl:
			e := existing[i]
			mismatched = append(mismatched, fmt.Sprintf(
				"index %q (%s) has unique=%t sparse=%t ttl=%t, want unique=%t sparse=%t ttl=%t; drop it manually so backd can recreate it",
				e.name, keys, e.unique, e.sparse, e.ttl, ix.unique, ix.sparse, ix.ttl))
		}
	}
	return missing, mismatched, nil
}

// keyList renders a key document like canonicalKeys, e.g. "provider:1,subject:1".
func keyList(keys bson.D) string {
	parts := make([]string, len(keys))
	for i, e := range keys {
		parts[i] = fmt.Sprintf("%s:%v", e.Key, e.Value)
	}
	return strings.Join(parts, ",")
}

func (p *Provisioner) warnUnknownSystemCollections(dbName string, existing map[string]bson.Raw) {
	for name := range existing {
		known := false
		for _, sc := range systemCollections {
			known = known || sc.name == name
		}
		if !known {
			p.Log.Warn("unexpected collection in system database; it is ignored", "database", dbName, "collection", name)
		}
	}
}
