package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/registry"
)

const indexedSchema = `{"properties": {"email": {"type": "string"}, "status": {"type": "string"}, "address": {"type": "object", "properties": {"city": {"type": "string"}}}}}`

func TestCanonicalKeys(t *testing.T) {
	tests := []struct {
		doc  bson.D
		want string
	}{
		{bson.D{{Key: "a", Value: int32(1)}, {Key: "b", Value: int64(-1)}}, "a:1,b:-1"},
		{bson.D{{Key: "a", Value: 1.0}}, "a:1"},
		{bson.D{{Key: "a", Value: "text"}}, ""},
		{bson.D{{Key: "a", Value: "hashed"}}, ""},
	}
	for _, tt := range tests {
		raw, _ := bson.Marshal(tt.doc)
		if got := canonicalKeys(raw); got != tt.want {
			t.Errorf("canonicalKeys(%v) = %q, want %q", tt.doc, got, tt.want)
		}
	}
}

func indexNames(t *testing.T, coll *mongo.Collection) map[string]bool {
	t.Helper()
	existing, err := listIndexes(context.Background(), coll)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range existing {
		out[e.name+" unique="+map[bool]string{true: "true", false: "false"}[e.unique]] = true
	}
	return out
}

func TestProvisionIndexes(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()

	reg := loadRegistry(t, realm, map[string]string{
		"app/people": indexedSchema,
		"app/people/indexes.json": `[
		  {"fields": ["email"], "unique": true},
		  {"fields": ["status", "-_meta.created_at"]},
		  {"fields": ["id", "address.city"]}
		]`,
	})
	p := &Provisioner{Client: client, Registry: reg, Log: log}
	coll := client.Database(realm + "__app").Collection("people")

	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := indexNames(t, coll)
	for _, want := range []string{"_id_ unique=false", "email_1 unique=true", "status_1__meta.created_at_-1 unique=false", "_id_1_address.city_1 unique=false"} {
		if !got[want] {
			t.Errorf("index %s missing; have %v", want, got)
		}
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Idempotent.
	before := strings.Count(logs.String(), "index created")
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if strings.Count(logs.String(), "index created") != before {
		t.Error("second apply created indexes again")
	}

	// An undeclared index is reported and kept.
	if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "manual", Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify with undeclared index: %v", err)
	}
	if !strings.Contains(logs.String(), `"index":"manual_1"`) {
		t.Errorf("undeclared index not reported: %s", logs)
	}
	if !indexNames(t, coll)["manual_1 unique=false"] {
		t.Error("undeclared index was dropped")
	}
}

func TestVerifyReportsIndexDifferences(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()

	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/people": indexedSchema}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	coll := client.Database(realm + "__app").Collection("people")
	// email exists as a non-unique index; status is not there at all.
	if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "email", Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	p.Registry = loadRegistry(t, realm, map[string]string{
		"app/people":              indexedSchema,
		"app/people/indexes.json": `[{"fields": ["email"], "unique": true}, {"fields": ["-status"]}]`,
	})

	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "index (-status) missing") || !strings.Contains(err.Error(), `"email_1" (email) has unique=false`) {
		t.Fatalf("Verify = %v", err)
	}
	// Apply refuses to silently change uniqueness.
	if err := p.Apply(ctx); err == nil || !strings.Contains(err.Error(), "drop it manually") {
		t.Fatalf("Apply = %v, want uniqueness conflict", err)
	}
}

func TestApplyFailsOnDuplicatesForUniqueIndex(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()

	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/people": indexedSchema}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	repo := (&Store{Client: client}).Repository(mustCollection(t, p, realm, "app", "people"))
	for range 2 {
		if err := repo.Create(ctx, newDoc(map[string]any{"email": "same@x"})); err != nil {
			t.Fatal(err)
		}
	}
	p.Registry = loadRegistry(t, realm, map[string]string{
		"app/people":              indexedSchema,
		"app/people/indexes.json": `[{"fields": ["email"], "unique": true}]`,
	})
	if err := p.Apply(ctx); err == nil || !strings.Contains(err.Error(), "create index (email)") {
		t.Fatalf("Apply = %v, want duplicate key failure", err)
	}
}

func mustCollection(t *testing.T, p *Provisioner, realm, db, coll string) *registry.Collection {
	t.Helper()
	c, ok := p.Registry.Collection(realm, db, coll)
	if !ok {
		t.Fatalf("collection %s/%s/%s not configured", realm, db, coll)
	}
	return c
}
