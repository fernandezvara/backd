package mongodb

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const strictSchema = `{
  "type": "object",
  "properties": {
    "name":  {"type": "string", "format": "email"},
    "age":   {"type": "integer", "minimum": 0},
    "score": {"type": "number"}
  },
  "required": ["name"],
  "additionalProperties": false
}`

func TestProvisionApplyAndVerify(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()
	reg := loadRegistry(t, realm, map[string]string{"app/people": strictSchema, "app/notes": `{}`})
	p := &Provisioner{Client: client, Registry: reg, Log: log}

	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), realm+"__app.people: collection missing") {
		t.Fatalf("Verify before apply = %v, want missing collection", err)
	}

	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify after apply: %v", err)
	}
	if !strings.Contains(logs.String(), `"keyword":"format"`) {
		t.Errorf("omitted format keyword not logged: %s", logs)
	}

	// A second apply changes nothing.
	before := strings.Count(logs.String(), "collection created")
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	after := logs.String()
	if strings.Count(after, "collection created") != before || strings.Contains(after, "validator updated") {
		t.Errorf("second apply was not a no-op: %s", after)
	}
}

func TestProvisionSchemaChange(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()

	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/people": strictSchema}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	changed := strings.Replace(strictSchema, `"minimum": 0`, `"minimum": 18`, 1)
	p.Registry = loadRegistry(t, realm, map[string]string{"app/people": changed})
	err := p.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "validator differs") {
		t.Fatalf("Verify after schema change = %v, want validator difference", err)
	}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(logs.String(), "collection validator updated") {
		t.Error("validator update not logged")
	}
	if err := p.Verify(ctx); err != nil {
		t.Fatalf("Verify after re-apply: %v", err)
	}
}

func TestProvisionWarnsAboutUnconfiguredObjects(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()

	if _, err := client.Database(realm+"__app").Collection("stray").InsertOne(ctx, bson.D{{Key: "x", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Database(realm+"__old").Collection("c").InsertOne(ctx, bson.D{{Key: "x", Value: 1}}); err != nil {
		t.Fatal(err)
	}

	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/people": `{}`}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, `"collection":"stray"`) {
		t.Errorf("unconfigured collection not reported: %s", out)
	}
	if !strings.Contains(out, `"database":"`+realm+`__old"`) {
		t.Errorf("unconfigured database not reported: %s", out)
	}
	// Nothing is dropped.
	n, err := client.Database(realm+"__app").Collection("stray").CountDocuments(ctx, bson.D{})
	if err != nil || n != 1 {
		t.Errorf("stray collection count = %d, %v; want 1", n, err)
	}
}

func TestDatabaseValidatorEnforcesSchema(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()

	p := &Provisioner{Client: client, Registry: loadRegistry(t, realm, map[string]string{"app/people": strictSchema}), Log: log}
	if err := p.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	coll := client.Database(realm + "__app").Collection("people")
	now := time.Now().UTC()
	meta := bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: int64(1)}}
	noVersion := bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}}
	badVersion := bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: "1"}}

	valid := bson.D{
		{Key: "_id", Value: "d2fjps30h2ipb32i2hg0"}, {Key: "_meta", Value: meta},
		{Key: "name", Value: "a@b.c"}, {Key: "age", Value: int64(3)}, {Key: "score", Value: 2.5},
	}
	if _, err := coll.InsertOne(ctx, valid); err != nil {
		t.Errorf("valid document rejected despite additionalProperties false: %v", err)
	}

	invalid := []bson.D{
		{{Key: "_id", Value: "x1"}, {Key: "_meta", Value: meta}},                                                               // missing name
		{{Key: "_id", Value: "x2"}, {Key: "_meta", Value: meta}, {Key: "name", Value: "a"}, {Key: "extra", Value: 1}},          // additional property
		{{Key: "_id", Value: "x3"}, {Key: "_meta", Value: meta}, {Key: "name", Value: "a"}, {Key: "age", Value: 1.5}},          // not an integer
		{{Key: "_id", Value: "x4"}, {Key: "name", Value: "a"}},                                                                 // no _meta
		{{Key: "_id", Value: "x5"}, {Key: "_meta", Value: bson.D{{Key: "created_at", Value: "x"}}}, {Key: "name", Value: "a"}}, // bad _meta
		{{Key: "_id", Value: "x6"}, {Key: "_meta", Value: noVersion}, {Key: "name", Value: "a"}},                               // no version
		{{Key: "_id", Value: "x7"}, {Key: "_meta", Value: badVersion}, {Key: "name", Value: "a"}},                              // string version
	}
	for i, doc := range invalid {
		if _, err := coll.InsertOne(ctx, doc); err == nil {
			t.Errorf("invalid document %d accepted", i)
		}
	}
}
