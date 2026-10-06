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
			"_id":            str(),
			"email":          str(),
			"email_verified": typ("bool"),
			"roles":          map[string]any{"bsonType": "array", "items": str()},
			"disabled":       typ("bool"),
			"locale":         str(),
			"admin_networks": map[string]any{"bsonType": "array", "items": str()},
			"login_networks": map[string]any{"bsonType": "array", "items": str()},
			"created_at":     typ("date"),
			"updated_at":     typ("date"),
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
			"provider":      map[string]any{"enum": bson.A{"password"}},
			"subject":       str(),
			"password_hash": str(),
			"created_at":    typ("date"),
			"updated_at":    typ("date"),
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "provider", Value: 1}, {Key: "subject", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "user_id", Value: 1}}},
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
		}),
		indexes: []systemIndex{
			{keys: bson.D{{Key: "token_hash", Value: 1}}, unique: true},
			{keys: bson.D{{Key: "user_id", Value: 1}}},
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
