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

const articleSchema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string"},
    "body":  {"type": "string"},
    "tags":  {"type": "array", "items": {"type": "string"}},
    "lang":  {"type": "string"}
  },
  "additionalProperties": false
}`

func TestTextIndexLifecycle(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, logs := testLogger()
	coll := client.Database(realm + "__app").Collection("articles")

	provision := func(indexes string) error {
		t.Helper()
		reg := loadRegistry(t, realm, map[string]string{"app/articles": articleSchema, "app/articles/indexes.json": indexes})
		return (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx)
	}
	verify := func(indexes string) error {
		t.Helper()
		reg := loadRegistry(t, realm, map[string]string{"app/articles": articleSchema, "app/articles/indexes.json": indexes})
		return (&Provisioner{Client: client, Registry: reg, Log: log}).Verify(ctx)
	}

	v1 := `[{"text": {"fields": {"title": 10, "tags": 5, "body": 1}, "default_language": "english", "language_field": "lang"}}]`
	// Provisioned without the text entry first: verifying then names the missing index.
	if err := provision(`[{"fields": ["title"]}]`); err != nil {
		t.Fatal(err)
	}
	if err := verify(v1); err == nil || !strings.Contains(err.Error(), "text index is missing") {
		t.Fatalf("verify before the index: %v", err)
	}
	if err := provision(v1); err != nil {
		t.Fatal(err)
	}
	have, err := findTextIndex(ctx, coll)
	if err != nil || have == nil || have.name != "backd_text" || !strings.Contains(have.sig, "title:10") || !strings.Contains(have.sig, "override=_search_lang") {
		t.Fatalf("index = %+v, %v", have, err)
	}
	if !strings.Contains(logs.String(), "text index created") {
		t.Errorf("log = %s", logs)
	}
	if err := verify(v1); err != nil {
		t.Errorf("verify after: %v", err)
	}
	// Provisioning again changes nothing and doesn't rebuild.
	logs.Reset()
	if err := provision(v1); err != nil || strings.Contains(logs.String(), "rebuilding") {
		t.Fatalf("again: %v %s", err, logs)
	}

	// A changed declaration is rebuilt, and said so.
	v2 := `[{"text": {"fields": {"title": 20, "body": 1}, "default_language": "spanish", "language_field": "lang"}}]`
	if err := verify(v2); err == nil || !strings.Contains(err.Error(), "differs from indexes.json") {
		t.Errorf("verify a changed declaration: %v", err)
	}
	if err := provision(v2); err != nil || !strings.Contains(logs.String(), "rebuilding it; searches answer 503 index_building") {
		t.Fatalf("rebuild: %v %s", err, logs)
	}
	if have, _ = findTextIndex(ctx, coll); have == nil || !strings.Contains(have.sig, "language=spanish") || strings.Contains(have.sig, "tags") {
		t.Errorf("rebuilt = %+v", have)
	}

	// A text index backd didn't make is never dropped.
	if err := coll.Indexes().DropOne(ctx, "backd_text"); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "body", Value: "text"}}, Options: options.Index().SetName("mine")}); err != nil {
		t.Fatal(err)
	}
	if err := provision(v1); err == nil || !strings.Contains(err.Error(), `text index named "mine" that backd didn't create`) {
		t.Errorf("foreign index: %v", err)
	}
}

// The language a document is indexed in is the language_field's locale, mapped, and stays out
// of the document; documents written before the index get it when the index is created.
func TestSearchLanguageIsStoredAndHidden(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	coll := client.Database(realm + "__app").Collection("articles")

	// Documents from before the index, written with no hidden field.
	for i, lang := range []string{"es-MX", "pt_BR", "xx", ""} {
		d := bson.D{{Key: "_id", Value: string(rune('a' + i))}, {Key: "title", Value: "t"},
			{Key: "_meta", Value: bson.D{{Key: "created_at", Value: bson.NewDateTimeFromTime(time.Now())}, {Key: "updated_at", Value: bson.NewDateTimeFromTime(time.Now())}, {Key: "version", Value: int64(1)}}}}
		if lang != "" {
			d = append(d, bson.E{Key: "lang", Value: lang})
		}
		if _, err := coll.InsertOne(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	reg := loadRegistry(t, realm, map[string]string{
		"app/articles":              `{"type":"object","properties":{"title":{"type":"string"},"lang":{"type":"string"}},"additionalProperties":false}`,
		"app/articles/indexes.json": `[{"text": {"fields": {"title": 1}, "language_field": "lang"}}]`,
	})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	stored := func(id string) any {
		var raw bson.M
		if err := coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		return raw["_search_lang"]
	}
	for id, want := range map[string]any{"a": "spanish", "b": "portuguese", "c": "none", "d": nil} {
		if got := stored(id); got != want {
			t.Errorf("backfill %s: %v, want %v", id, got, want)
		}
	}

	// New writes keep it in step, and a read never shows it.
	c, _ := reg.Collection(realm, "app", "articles")
	repo := (&Store{Client: client}).Repository(c).(*Repository)
	doc := newDoc(map[string]any{"title": "hola", "lang": "fr-CA"})
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if got := stored(doc["id"].(string)); got != "french" {
		t.Errorf("created: %v", got)
	}
	got, err := repo.Get(ctx, doc["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["_search_lang"]; has {
		t.Error("the hidden field is in the document")
	}
	doc["lang"] = "de"
	if err := repo.Replace(ctx, doc, 1); err != nil {
		t.Fatal(err)
	}
	if got := stored(doc["id"].(string)); got != "german" {
		t.Errorf("replaced: %v", got)
	}
}
