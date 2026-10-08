package mongodb

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestProvisionSystemDatabase(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()
	p := &Provisioner{Client: client, Registry: loadRegistryWith(t, realm, "", map[string]string{"app/notes": `{}`}), Log: log}
	sys := realm + "___system"

	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), sys+".users: collection missing") || !strings.Contains(err.Error(), sys+".sessions: collection missing") {
		t.Fatalf("Verify before apply = %v, want missing system collections", err)
	}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify after apply: %v", err)
	}

	names, err := client.Database(sys).ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"users", "identities", "sessions", "api_keys", "invitations", "login_attempts"} {
		if !contains(names, want) {
			t.Errorf("collection %s not created (have %v)", want, names)
		}
	}

	// Expected indexes, including TTL and unique ones.
	wantIdx := map[string][]string{
		"users":          {"email:1 unique sparse"},
		"identities":     {"provider:1,subject:1 unique", "user_id:1"},
		"sessions":       {"token_hash:1 unique", "user_id:1", "expires_at:1 ttl"},
		"api_keys":       {"name:1 unique"},
		"invitations":    {"token_hash:1 unique", "expires_at:1 ttl"},
		"login_attempts": {"expires_at:1 ttl"},
	}
	for coll, want := range wantIdx {
		got := describeIndexes(t, client.Database(sys).Collection(coll))
		for _, w := range want {
			if !contains(got, w) {
				t.Errorf("%s: index %q missing (have %v)", coll, w, got)
			}
		}
	}

	// A second apply changes nothing.
	before := logs.String()
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if extra := strings.TrimPrefix(logs.String(), before); strings.Contains(extra, "created") || strings.Contains(extra, "updated") {
		t.Errorf("second apply was not a no-op: %s", extra)
	}

	// The validator rejects malformed documents.
	_, err = client.Database(sys).Collection("users").InsertOne(ctx, bson.D{{Key: "_id", Value: "u1"}, {Key: "email", Value: 42}})
	if err == nil || !strings.Contains(err.Error(), "Document failed validation") {
		t.Errorf("invalid user insert: err = %v, want a validation failure", err)
	}
	now := time.Now()
	_, err = client.Database(sys).Collection("users").InsertOne(ctx, bson.D{
		{Key: "_id", Value: "u1"}, {Key: "email", Value: "a@x.io"}, {Key: "email_verified", Value: false},
		{Key: "roles", Value: bson.A{}}, {Key: "disabled", Value: false}, {Key: "created_at", Value: now}, {Key: "updated_at", Value: now},
	})
	if err != nil {
		t.Errorf("valid user insert: %v", err)
	}
}

func TestProvisionSystemSkipsAuthDisabled(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/notes": `{}`}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	names, err := client.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: realm + "___system"}})
	if err != nil || len(names) != 0 {
		t.Errorf("system database created for an auth-disabled realm: %v, %v", names, err)
	}
}

func TestProvisionSystemIndexMismatch(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	sys := realm + "___system"

	// A plain (non-TTL) index on sessions.expires_at would never expire sessions.
	_, err := client.Database(sys).Collection("sessions").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	p := &Provisioner{Client: client, Registry: loadRegistryWith(t, realm, "", map[string]string{"app/notes": `{}`}), Log: log}
	err = p.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), `index "expires_at_1" (expires_at:1) has unique=false sparse=false ttl=false, want unique=false sparse=false ttl=true`) {
		t.Fatalf("Apply = %v, want TTL mismatch", err)
	}
	err = p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), sys+".sessions: index \"expires_at_1\"") {
		t.Fatalf("Verify = %v, want TTL mismatch", err)
	}
}

func TestProvisionWarnsAboutUnusedSystemDatabase(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()
	if _, err := client.Database(realm+"___system").Collection("users").InsertOne(ctx, bson.D{{Key: "x", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/notes": `{}`}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `system database of a realm that is not configured or has auth disabled`) {
		t.Errorf("unused system database not reported: %s", logs)
	}
}

// describeIndexes lists a collection's indexes as "keys[ unique][ sparse][ ttl]".
func describeIndexes(t *testing.T, coll *mongo.Collection) []string {
	t.Helper()
	specs, err := coll.Indexes().ListSpecifications(context.Background(), options.ListIndexes())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range specs {
		d := canonicalKeys(s.KeysDocument)
		if s.Unique != nil && *s.Unique {
			d += " unique"
		}
		if s.Sparse != nil && *s.Sparse {
			d += " sparse"
		}
		if s.ExpireAfterSeconds != nil {
			d += " ttl"
		}
		out = append(out, d)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// A realm provisioned by v0.8.0 has identities that only allow the password provider and no
// (user_id, provider) index: provisioning updates both, keeps the identities it has, and then
// providers fit.
func TestProvisionUpgradesIdentitiesFromV080(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	p := &Provisioner{Client: client, Registry: loadRegistryWith(t, realm, "", map[string]string{"app/notes": `{}`}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ids := client.Database(realm + "___system").Collection("identities")

	// Back to how v0.8.0 left it.
	old := bson.D{{Key: "$jsonSchema", Value: bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"_id", "user_id", "provider", "subject", "created_at", "updated_at"}},
		{Key: "properties", Value: bson.D{
			{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "user_id", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "provider", Value: bson.D{{Key: "enum", Value: bson.A{"password"}}}},
			{Key: "subject", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "password_hash", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "created_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
			{Key: "updated_at", Value: bson.D{{Key: "bsonType", Value: "date"}}},
		}},
	}}}
	if err := client.Database(realm+"___system").RunCommand(ctx, bson.D{{Key: "collMod", Value: "identities"}, {Key: "validator", Value: old}}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := ids.Indexes().DropOne(ctx, "user_id_1_provider_1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := ids.InsertOne(ctx, bson.D{{Key: "_id", Value: "i1"}, {Key: "user_id", Value: "u1"}, {Key: "provider", Value: "password"}, {Key: "subject", Value: "u1"},
		{Key: "password_hash", Value: "h"}, {Key: "created_at", Value: now}, {Key: "updated_at", Value: now}}); err != nil {
		t.Fatal(err)
	}
	google := bson.D{{Key: "_id", Value: "i2"}, {Key: "user_id", Value: "u1"}, {Key: "provider", Value: "google"}, {Key: "subject", Value: "g"}, {Key: "created_at", Value: now}, {Key: "updated_at", Value: now}}
	if _, err := ids.InsertOne(ctx, google); err == nil {
		t.Fatal("the old validator accepted a provider")
	}

	// verify mode refuses to start until provisioning has run; applying fixes it.
	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "identities") {
		t.Fatalf("Verify before the upgrade = %v", err)
	}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify after the upgrade: %v", err)
	}
	if _, err := ids.InsertOne(ctx, google); err != nil {
		t.Errorf("a provider identity after the upgrade: %v", err)
	}
	if n, _ := ids.CountDocuments(ctx, bson.D{{Key: "user_id", Value: "u1"}}); n != 2 {
		t.Errorf("identities: %d", n)
	}
	// The index now enforces one identity per provider per user.
	dup := bson.D{{Key: "_id", Value: "i3"}, {Key: "user_id", Value: "u1"}, {Key: "provider", Value: "google"}, {Key: "subject", Value: "g2"}, {Key: "created_at", Value: now}, {Key: "updated_at", Value: now}}
	if _, err := ids.InsertOne(ctx, dup); !mongo.IsDuplicateKeyError(err) {
		t.Errorf("a second google identity for the user: %v", err)
	}
}
