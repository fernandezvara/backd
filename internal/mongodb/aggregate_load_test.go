package mongodb

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/storage"
)

// A load test of aggregations and searches on a large collection, to see what the defaults
// (5 s, 1,000 groups, 10,000 distinct values) mean in practice. It is skipped unless
// BACKD_LOAD_TEST is set (BACKD_LOAD_DOCS sets the size, 300,000 by default); the numbers it
// prints are what docs/auth/security.md reports.
func TestAggregateAndSearchLoad(t *testing.T) {
	if os.Getenv("BACKD_LOAD_TEST") == "" {
		t.Skip("set BACKD_LOAD_TEST=1 to run the load test")
	}
	n := 300_000
	if v := os.Getenv("BACKD_LOAD_DOCS"); v != "" {
		fmt.Sscan(v, &n)
	}
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	schema := `{"type":"object","properties":{"category":{"type":"string"},"status":{"type":"string"},"owner_ref":{"type":"string"},"total":{"type":"number"},"paid_at":{"type":"string","format":"date-time","x-backd-store":"date"},"title":{"type":"string"},"body":{"type":"string"}}}`
	reg := loadRegistry(t, realm, map[string]string{
		"app/orders": schema,
		"app/orders/indexes.json": `[{"fields": ["category"]}, {"fields": ["paid_at"]},
		  {"text": {"fields": {"title": 10, "body": 1}, "default_language": "english"}}]`,
	})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "orders")
	coll := client.Database(c.MongoDatabase).Collection(c.Name)
	repo := &Repository{coll: coll}

	words := make([]string, 2000)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range words {
		words[i] = fmt.Sprintf("w%04d", i)
	}
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	started := time.Now()
	for done := 0; done < n; {
		batch := make([]any, 0, 5000)
		for i := 0; i < 5000 && done < n; i++ {
			title := make([]string, 4)
			body := make([]string, 40)
			for j := range title {
				title[j] = words[int(rng.Float64()*rng.Float64()*float64(len(words)))] // a few words are common
			}
			for j := range body {
				body[j] = words[int(rng.Float64()*rng.Float64()*float64(len(words)))]
			}
			at := base.Add(time.Duration(rng.IntN(3*365*24)) * time.Hour)
			batch = append(batch, bson.D{
				{Key: "_id", Value: fmt.Sprintf("o%08d", done)},
				{Key: "_meta", Value: bson.D{{Key: "created_at", Value: bson.NewDateTimeFromTime(at)}, {Key: "updated_at", Value: bson.NewDateTimeFromTime(at)}, {Key: "version", Value: int64(1)}, {Key: "owner", Value: fmt.Sprintf("u%d", rng.IntN(5000))}}},
				{Key: "category", Value: fmt.Sprintf("c%02d", rng.IntN(50))},
				{Key: "status", Value: []string{"new", "paid", "shipped", "returned", "lost"}[rng.IntN(5)]},
				{Key: "owner_ref", Value: fmt.Sprintf("u%d", rng.IntN(50000))},
				{Key: "total", Value: float64(rng.IntN(100000)) / 100},
				{Key: "paid_at", Value: bson.NewDateTimeFromTime(at)},
				{Key: "title", Value: strings.Join(title, " ")},
				{Key: "body", Value: strings.Join(body, " ")},
			})
			done++
		}
		if _, err := coll.InsertMany(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("loaded %d documents in %s", n, time.Since(started).Round(time.Second))

	run := func(name string, q storage.AggregateQuery) {
		q.MaxTime = 30 * time.Second // measure, don't refuse
		q.MaxDistinct = 10000
		start := time.Now()
		res, err := repo.Aggregate(ctx, q)
		took := time.Since(start).Round(time.Millisecond)
		if err != nil {
			t.Logf("%-52s %8s  error: %v", name, took, err)
			return
		}
		t.Logf("%-52s %8s  %d groups", name, took, res.TotalGroups)
	}
	count := []storage.Metric{{Op: storage.MetricCount}}
	all := []storage.Metric{{Op: storage.MetricCount}, {Op: storage.MetricSum, Field: "total"}, {Op: storage.MetricAvg, Field: "total"}}
	lastMonth := storage.Condition{Field: "paid_at", Op: storage.OpGte, Value: base.AddDate(2, 11, 0)}
	lastYear := storage.Condition{Field: "paid_at", Op: storage.OpGte, Value: base.AddDate(2, 0, 0)}
	run("total count, whole collection", storage.AggregateQuery{Metrics: count})
	run("count by category (indexed), whole", storage.AggregateQuery{GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: count})
	run("count+sum+avg by status (unindexed), whole", storage.AggregateQuery{GroupBy: []storage.GroupField{{Field: "status"}}, Metrics: all})
	run("by month (tz Europe/Madrid), whole", storage.AggregateQuery{GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: "month", TZ: "Europe/Madrid"}}, Metrics: all})
	run("by day, whole (1,095 groups)", storage.AggregateQuery{GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: "day"}}, Metrics: count, Limit: 1000})
	run("category × status × month, whole", storage.AggregateQuery{GroupBy: []storage.GroupField{{Field: "category"}, {Field: "status"}, {Field: "paid_at", Bucket: "month"}}, Metrics: all, Limit: 1000})
	run("count_distinct owner_ref (50k), total", storage.AggregateQuery{Metrics: []storage.Metric{{Op: storage.MetricCountDistinct, Field: "owner_ref"}}})
	run("by month, narrow where (last year, indexed)", storage.AggregateQuery{Filter: lastYear, GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: "month"}}, Metrics: all})
	run("by category, narrow where (last month, indexed)", storage.AggregateQuery{Filter: lastMonth, GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: all})
	run("by status, where on unindexed field", storage.AggregateQuery{Filter: storage.Condition{Field: "owner_ref", Op: storage.OpEq, Value: "u7"}, GroupBy: []storage.GroupField{{Field: "status"}}, Metrics: count})
	run("rule-style access filter (owner), by category", storage.AggregateQuery{Access: storage.Condition{Field: "_meta.owner", Op: storage.OpEq, Value: "u7"}, GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: count})

	search := func(name, text string, rel bool) {
		start := time.Now()
		page, err := repo.List(ctx, storage.Query{Search: &storage.Search{Text: text, Language: "english"}, Relevance: rel, Limit: 20, MaxTime: 20 * time.Second})
		took := time.Since(start).Round(time.Millisecond)
		if err != nil {
			t.Logf("%-52s %8s  error: %v", name, took, err)
			return
		}
		t.Logf("%-52s %8s  %d results", name, took, len(page.Items))
	}
	search("search: rare word, relevance", "w1999", true)
	search("search: common word, relevance", "w0001", true)
	search("search: 3 words, relevance", "w0001 w0002 w0003", true)
	search("search: phrase", `"w0001 w0002"`, false)
	search("search: 5 common terms, relevance", strings.Join(words[:5], " "), true)
	search("search: 10 common terms, relevance (the cap)", strings.Join(words[:10], " "), true)
	search("search: 20 common terms (above the cap)", strings.Join(words[:20], " "), true)
	search("search: 256 bytes of excluded words", "w0001 "+strings.Repeat("-w0002 ", 35), true)
	start := time.Now()
	if _, err := repo.Aggregate(ctx, storage.AggregateQuery{Search: &storage.Search{Text: "w0001", Language: "english"}, GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: count, MaxTime: 30 * time.Second}); err != nil {
		t.Logf("aggregate with a common word: %v", err)
	} else {
		t.Logf("%-52s %8s", "aggregate: common word, by category", time.Since(start).Round(time.Millisecond))
	}

	// Eight at once, as eight callers would (the endpoint limits each to one at a time).
	var wg sync.WaitGroup
	start = time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = repo.Aggregate(ctx, storage.AggregateQuery{GroupBy: []storage.GroupField{{Field: "status"}}, Metrics: all, MaxTime: 60 * time.Second})
		}()
	}
	wg.Wait()
	t.Logf("%-52s %8s", "8 concurrent whole-collection aggregations", time.Since(start).Round(time.Millisecond))
}
