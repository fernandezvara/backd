package mongodb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/storage"
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

// Searching: words, phrases, exclusions and languages, relevance with the score, the same
// search inside an aggregation, the rebuild window, and a text that is never an operator.
func TestSearchOnMongoDB(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	reg := loadRegistry(t, realm, map[string]string{
		"app/articles":              articleSchema,
		"app/articles/indexes.json": `[{"text": {"fields": {"title": 10, "body": 1}, "default_language": "english", "language_field": "lang"}}]`,
	})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "articles")
	repo := (&Store{Client: client}).Repository(c).(*Repository)
	add := func(title, body, lang string) string {
		d := newDoc(map[string]any{"title": title, "body": body})
		if lang != "" {
			d["lang"] = lang
		}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d["id"].(string)
	}
	shoes := add("Running shoes", "Light shoes for the road", "en")
	kids := add("Kids shoes", "Small running shoes for kids", "en")
	boots := add("Boots", "Heavy boots, also for running", "en")
	zapatos := add("Zapatos para correr", "Zapatillas ligeras", "es")
	search := func(text, lang string, relevance bool) []storage.Document {
		t.Helper()
		page, err := repo.List(ctx, storage.Query{Search: &storage.Search{Text: text, Language: lang}, Relevance: relevance, Limit: 20, Sort: []storage.SortField{{Field: "id"}}})
		if err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
		return page.Items
	}
	ids := func(docs []storage.Document) []string {
		var out []string
		for _, d := range docs {
			out = append(out, d["id"].(string))
		}
		return out
	}

	// Relevance: the weighted title beats the body, and the score rides in _meta.score.
	got := search("running", "english", true)
	if len(got) != 3 || got[0]["id"] != shoes && got[0]["id"] != kids {
		t.Fatalf("relevance order = %v", ids(got))
	}
	first, second := got[0]["_meta"].(map[string]any)["score"].(float64), got[2]["_meta"].(map[string]any)["score"].(float64)
	if first <= second || got[2]["id"] != boots {
		t.Errorf("scores %v >= %v, last = %v", first, second, got[2]["id"])
	}
	if _, has := search("running", "english", false)[0]["_meta"].(map[string]any)["score"]; has {
		t.Error("the score is there without relevance sorting")
	}
	// A phrase and an exclusion.
	if got := ids(search(`"running shoes"`, "english", false)); len(got) != 2 {
		t.Errorf("phrase = %v", got)
	}
	if got := ids(search("shoes -kids", "english", false)); len(got) != 1 || got[0] != shoes {
		t.Errorf("exclusion = %v", got)
	}
	// Stemming follows the language: "correr" finds the Spanish document as Spanish.
	if got := ids(search("corriendo", "spanish", false)); len(got) != 1 || got[0] != zapatos {
		t.Errorf("spanish = %v", got)
	}
	// What looks like an operator or a regular expression is only words.
	for _, evil := range []string{`{"$where": "sleep(5000)"}`, `$regex`, `.*`, `\"; db.dropDatabase(); \"`, `-`, `"`} {
		if _, err := repo.List(ctx, storage.Query{Search: &storage.Search{Text: evil}, Limit: 5}); err != nil {
			t.Errorf("search %q: %v", evil, err)
		}
	}

	// In an aggregation: results per language next to a search box.
	res, err := repo.Aggregate(ctx, storage.AggregateQuery{
		Search: &storage.Search{Text: "shoes", Language: "english"}, GroupBy: []storage.GroupField{{Field: "lang"}},
		Metrics: []storage.Metric{{Op: storage.MetricCount}},
	})
	if err != nil || res.TotalGroups != 1 || res.Groups[0].Values["count"] != int64(2) {
		t.Errorf("aggregate = %+v %v", res, err)
	}

	// While the index is being rebuilt there is none: a distinct error, not an outage.
	if err := client.Database(c.MongoDatabase).Collection(c.Name).Indexes().DropOne(ctx, "backd_text"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.List(ctx, storage.Query{Search: &storage.Search{Text: "shoes"}, Limit: 5}); !errors.Is(err, storage.ErrIndexBuilding) {
		t.Errorf("without the index: %v", err)
	}
	if _, err := repo.Aggregate(ctx, storage.AggregateQuery{Search: &storage.Search{Text: "shoes"}, Metrics: []storage.Metric{{Op: storage.MetricCount}}}); !errors.Is(err, storage.ErrIndexBuilding) {
		t.Errorf("aggregate without the index: %v", err)
	}
}
