package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
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

const softSchema = `{"properties": {"email": {"type": "string"}, "name": {"type": "string"}}}`

func TestProvisionSoftDeleteIndexes(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()
	files := func(collection string) map[string]string {
		return map[string]string{
			"app/people":                 softSchema,
			"app/people/indexes.json":    `[{"fields": ["email"], "unique": true}]`,
			"app/people/collection.yaml": collection,
		}
	}

	// A collection that already holds a plain unique index when soft delete is
	// turned on: the new index is built next to it, and the old one is flagged.
	before := loadRegistryWith(t, realm, "auth: disabled\n", map[string]string{"app/people": softSchema, "app/people/indexes.json": `[{"fields": ["email"], "unique": true}]`})
	if err := (&Provisioner{Client: client, Registry: before, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	reg := loadRegistryWith(t, realm, "auth: disabled\n", files("soft_delete:\n  retention: 7d\n"))
	c, _ := reg.Collection(realm, "app", "people")
	coll := client.Database(c.MongoDatabase).Collection("people")
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	existing, err := listIndexes(ctx, coll)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]existingIndex{}
	for _, e := range existing {
		got[e.keys] = e
	}
	if e, ok := got["email:1,_meta.deleted_at:1"]; !ok || !e.unique {
		t.Errorf("unique index with the deletion marker missing: %+v", existing)
	}
	if e, ok := got["_meta.purge_at:1"]; !ok || !e.ttl {
		t.Errorf("TTL index on purge_at missing: %+v", existing)
	}
	if !strings.Contains(logs.String(), "a unique index from before soft_delete also covers deleted documents") {
		t.Errorf("no warning about the old unique index: %s", logs.String())
	}

	// Live documents stay unique; a deleted one frees its value; two deleted
	// documents may share one.
	repo := (&Store{Client: client}).Repository(c)
	now := time.Now().UTC().Truncate(time.Millisecond)
	doc := func(id, email string, deleted *time.Time) storage.Document {
		meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
		if deleted != nil {
			meta["deleted_at"] = *deleted
		}
		return storage.Document{"id": id, "_meta": meta, "email": email}
	}
	// The pre-existing plain unique index would refuse a repeated email, even
	// from a deleted document: drop it, as the warning says.
	if err := coll.Indexes().DropOne(ctx, "email_1"); err != nil {
		t.Fatal(err)
	}
	d1, d2 := now.Add(-time.Hour), now.Add(-time.Minute)
	for _, d := range []storage.Document{doc("a", "ada@example.com", &d1), doc("b", "ada@example.com", &d2), doc("c", "ada@example.com", nil)} {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create %v: %v", d["id"], err)
		}
	}
	if err := repo.Create(ctx, doc("d", "ada@example.com", nil)); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("a second live document with the same email: %v, want a conflict", err)
	}
}
