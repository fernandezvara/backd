package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestQueryPerformance times the API's reads and writes against a real MongoDB, grouped by how
// complex they are, to keep a baseline of how fast they are on this machine. It is for local use
// only: it is skipped unless BACKD_QUERY_PERF is set, so CI never runs it (see Development →
// Performance tests, and `make perf-queries`).
//
// Requests go through the whole HTTP handler (routing, rules, query parsing, storage, encoding)
// in-process, so the numbers are the server's own cost without a network. What counts is the
// comparison between two runs on the same idle machine, not the absolute figures.
//
// Knobs (all optional):
//
//	BACKD_QUERY_PERF=1            run it
//	BACKD_PERF_DOCS=100000        documents in the collection
//	BACKD_PERF_RUNS=30            timed requests per scenario (after 3 warm-ups)
//	BACKD_PERF_REPORT=<file>      write the results as JSON (the next run's baseline)
//	BACKD_PERF_BASELINE=<file>    compare with a report written earlier; a scenario that is
//	                              BACKD_PERF_TOLERANCE times slower (1.5 by default) is flagged
func TestQueryPerformance(t *testing.T) {
	if os.Getenv("BACKD_QUERY_PERF") == "" {
		t.Skip("set BACKD_QUERY_PERF=1 to run the query speed test (local only)")
	}
	docs := perfEnvInt("BACKD_PERF_DOCS", 100_000)
	runs := perfEnvInt("BACKD_PERF_RUNS", 30)

	realm := "t" + xid.New().String()
	const schema = `{"type":"object","properties":{
	  "category":{"type":"string"},"status":{"type":"string"},"total":{"type":"number"},"qty":{"type":"integer"},
	  "paid_at":{"type":"string","format":"date-time","x-backd-store":"date"},
	  "title":{"type":"string"},"body":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},
	  "related":{"type":"array","items":{"type":"string"}}}}`
	f := mongoFixture(t, map[string]string{
		realm + "/app/categories/schema.json": `{"type":"object","properties":{"name":{"type":"string"}}}`,
		realm + "/app/orders/schema.json":     schema,
		// category, paid_at and the text index are indexed; status, total, qty and tags are not,
		// so the scenarios show what an index is worth.
		realm + "/app/orders/indexes.json": `[{"fields": ["category"]}, {"fields": ["paid_at"]},
		  {"text": {"fields": {"title": 10, "body": 1}, "default_language": "english"}}]`,
		realm + "/app/orders/collection.yaml": `references:
  category:
    collection: categories
  related:
    collection: categories
    many: true
aggregate:
  group_by: [status, category, paid_at]
  metrics: [total, qty]
`,
	})

	ctx := context.Background()
	seed := func(collection string, n int, doc func(i int) bson.D) {
		c, ok := f.reg.Collection(realm, "app", collection)
		if !ok {
			t.Fatalf("no collection %s", collection)
		}
		coll := f.mongo.Database(c.MongoDatabase).Collection(c.Name)
		for done := 0; done < n; {
			batch := make([]any, 0, 5000)
			for ; len(batch) < 5000 && done < n; done++ {
				batch = append(batch, doc(done))
			}
			if _, err := coll.InsertMany(ctx, batch); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A fixed seed: every run measures the same data.
	rng := rand.New(rand.NewPCG(1, 2))
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	meta := func(at time.Time) bson.D {
		d := bson.NewDateTimeFromTime(at)
		return bson.D{{Key: "created_at", Value: d}, {Key: "updated_at", Value: d}, {Key: "version", Value: int64(1)}}
	}
	seed("categories", 50, func(i int) bson.D {
		return bson.D{{Key: "_id", Value: fmt.Sprintf("c%02d", i)}, {Key: "_meta", Value: meta(base)}, {Key: "name", Value: fmt.Sprintf("Category %d", i)}}
	})
	words := make([]string, 2000)
	for i := range words {
		words[i] = fmt.Sprintf("w%04d", i)
	}
	// A few words are common and most are rare, as in prose.
	word := func() string { return words[int(rng.Float64()*rng.Float64()*float64(len(words)))] }
	phrase := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = word()
		}
		return strings.Join(w, " ")
	}
	started := time.Now()
	seed("orders", docs, func(i int) bson.D {
		at := base.Add(time.Duration(rng.IntN(3*365*24)) * time.Hour)
		return bson.D{
			{Key: "_id", Value: fmt.Sprintf("o%08d", i)},
			{Key: "_meta", Value: meta(at)},
			{Key: "category", Value: fmt.Sprintf("c%02d", rng.IntN(50))},
			{Key: "status", Value: []string{"new", "paid", "shipped", "returned", "lost"}[rng.IntN(5)]},
			{Key: "total", Value: float64(rng.IntN(100000)) / 100},
			{Key: "qty", Value: int32(1 + rng.IntN(20))},
			{Key: "paid_at", Value: bson.NewDateTimeFromTime(at)},
			{Key: "title", Value: phrase(4)},
			{Key: "body", Value: phrase(40)},
			{Key: "tags", Value: bson.A{fmt.Sprintf("t%d", rng.IntN(100)), fmt.Sprintf("t%d", rng.IntN(100))}},
			{Key: "related", Value: bson.A{fmt.Sprintf("c%02d", rng.IntN(50)), fmt.Sprintf("c%02d", rng.IntN(50)), fmt.Sprintf("c%02d", rng.IntN(50))}},
		}
	})
	t.Logf("seeded %d documents in %s", docs, time.Since(started).Round(time.Millisecond))

	base1 := "/v1/" + realm + "/app/orders"
	// get builds a URL with an unencoded-looking query, encoded as a client would.
	get := func(path string, params ...string) string {
		q := url.Values{}
		for i := 0; i+1 < len(params); i += 2 {
			q.Set(params[i], params[i+1])
		}
		if len(q) == 0 {
			return base1 + path
		}
		return base1 + path + "?" + q.Encode()
	}
	// The cursor of a first page, for the "next page" scenario.
	var cursor string
	{
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, httptest.NewRequest("GET", get("", "order_by", "_meta.created_at,id", "limit", "20"), nil))
		var page struct {
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.NextCursor == "" {
			t.Fatalf("no cursor from the first page: %d %s", rec.Code, rec.Body)
		}
		cursor = page.NextCursor
	}
	lastYear := base.AddDate(2, 0, 0).Format(time.RFC3339)
	lastMonth := base.AddDate(2, 11, 0).Format(time.RFC3339)
	mid := docs / 2

	type scenario struct {
		tier, name, method, path, body string
	}
	var nextWrite int
	const (
		trivial  = "1 trivial"
		simple   = "2 simple"
		moderate = "3 moderate"
		complexT = "4 complex"
		writes   = "5 writes"
	)
	scenarios := []scenario{
		{tier: trivial, name: "get one document by id", path: base1 + fmt.Sprintf("/o%08d", mid)},
		{tier: trivial, name: "list, first page (20)", path: get("")},
		{tier: trivial, name: "list, limit=100", path: get("", "limit", "100")},
		{tier: trivial, name: "list, limit=100, fields=title,status", path: get("", "limit", "100", "fields", "title,status")},

		{tier: simple, name: "where eq on indexed field", path: get("", "where", `{"category":"c07"}`)},
		{tier: simple, name: "where eq on unindexed field", path: get("", "where", `{"status":"paid"}`)},
		{tier: simple, name: "where range on indexed date", path: get("", "where", `{"paid_at":{"$gte":"`+lastYear+`"}}`)},
		{tier: simple, name: "order_by indexed date, descending", path: get("", "order_by", "-paid_at")},
		{tier: simple, name: "order_by unindexed field", path: get("", "order_by", "-total")},
		{tier: simple, name: "count=true, whole collection", path: get("", "count", "true")},
		{tier: simple, name: "next page by cursor", path: get("", "after", cursor)},

		{tier: moderate, name: "count=true with where on indexed field", path: get("", "where", `{"category":"c07"}`, "count", "true")},
		{tier: moderate, name: "where $in + $between, unindexed", path: get("", "where", `{"status":{"$in":["paid","shipped"]},"total":{"$between":[100,200]}}`)},
		{tier: moderate, name: "where on indexed + unindexed, ordered", path: get("", "where", `{"category":"c07","status":"paid","qty":{"$gte":10}}`, "order_by", "-paid_at")},
		{tier: moderate, name: "$or across two fields", path: get("", "where", `{"$or":[{"category":"c07"},{"status":"lost"}]}`)},
		{tier: moderate, name: "$icontains (scans the collection)", path: get("", "where", `{"title":{"$icontains":"w1500"}}`)},
		{tier: moderate, name: "array field match", path: get("", "where", `{"tags":"t42"}`)},
		{tier: moderate, name: "deep skip (skip=10000)", path: get("", "skip", "10000", "order_by", "_meta.created_at,id")},
		{tier: moderate, name: "where + order + count, last year", path: get("", "where", `{"paid_at":{"$gte":"`+lastYear+`"},"status":"paid"}`, "order_by", "-paid_at", "count", "true")},

		{tier: complexT, name: "search: rare word, relevance", path: get("", "where", `{"$search":"w1999"}`, "order_by", "_relevance")},
		{tier: complexT, name: "search: common word, relevance", path: get("", "where", `{"$search":"w0001"}`, "order_by", "_relevance")},
		{tier: complexT, name: "search: 3 words + where", path: get("", "where", `{"$search":"w0001 w0002 w0003","status":"paid"}`, "order_by", "_relevance")},
		{tier: complexT, name: "search: phrase", path: get("", "where", `{"$search":"\"w0001 w0002\""}`)},
		{tier: complexT, name: "expand one reference", path: get("", "expand", "category(name)")},
		{tier: complexT, name: "expand one + many references, limit=100", path: get("", "expand", "category(name),related(name)", "limit", "100")},
		{tier: complexT, name: "expand + where + order", path: get("", "where", `{"status":"paid"}`, "order_by", "-paid_at", "expand", "category(name)")},
		{tier: complexT, name: "aggregate: count, whole collection", path: get("/_aggregate", "metrics", "count")},
		{tier: complexT, name: "aggregate: by category (indexed)", path: get("/_aggregate", "group_by", "category", "metrics", "count,sum:total")},
		{tier: complexT, name: "aggregate: by status (unindexed)", path: get("/_aggregate", "group_by", "status", "metrics", "count,sum:total,avg:total")},
		{tier: complexT, name: "aggregate: by month, tz Europe/Madrid", path: get("/_aggregate", "group_by", "paid_at:month", "tz", "Europe/Madrid", "metrics", "count,sum:total")},
		{tier: complexT, name: "aggregate: category × status × month", path: get("/_aggregate", "group_by", "category,status,paid_at:month", "metrics", "count,sum:total", "limit", "1000")},
		{tier: complexT, name: "aggregate: by category, last month", path: get("/_aggregate", "group_by", "category", "metrics", "count,sum:total", "where", `{"paid_at":{"$gte":"`+lastMonth+`"}}`)},
		{tier: complexT, name: "aggregate: by category with a search", path: get("/_aggregate", "group_by", "category", "metrics", "count", "where", `{"$search":"w0001"}`)},
		{tier: complexT, name: "aggregate: count_distinct of a field", path: get("/_aggregate", "metrics", "count_distinct:category")},

		{tier: writes, name: "create one document", method: "POST", path: base1, body: "create"},
		{tier: writes, name: "patch one document", method: "PATCH", path: base1 + fmt.Sprintf("/o%08d", mid), body: "patch"},
		{tier: writes, name: "replace one document (PUT)", method: "PUT", path: base1 + fmt.Sprintf("/o%08d", mid+1), body: "put"},
	}

	do := func(s scenario) (time.Duration, int) {
		method := s.method
		if method == "" {
			method = "GET"
		}
		var body *strings.Reader
		switch s.body {
		case "create":
			nextWrite++
			body = strings.NewReader(`{"category":"c01","status":"new","total":9.5,"qty":2,"title":"perf ` + strconv.Itoa(nextWrite) + `"}`)
		case "patch":
			nextWrite++
			body = strings.NewReader(`{"qty":` + strconv.Itoa(1+nextWrite%20) + `}`)
		case "put":
			nextWrite++
			body = strings.NewReader(`{"category":"c02","status":"paid","total":12.5,"qty":3,"title":"replaced ` + strconv.Itoa(nextWrite) + `"}`)
		default:
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(method, s.path, body)
		setContentType(req)
		rec := httptest.NewRecorder()
		start := time.Now()
		f.h.ServeHTTP(rec, req)
		took := time.Since(start)
		if rec.Code >= 300 {
			t.Fatalf("%s: %s %s answered %d: %s", s.name, method, s.path, rec.Code, rec.Body)
		}
		return took, rec.Code
	}

	var results []perfResult
	for _, s := range scenarios {
		for range 3 { // warm-up: plans, caches
			do(s)
		}
		times := make([]time.Duration, runs)
		for i := range times {
			times[i], _ = do(s)
		}
		slices.Sort(times)
		results = append(results, perfResult{
			Tier: s.tier, Scenario: s.name,
			MinMs: ms(times[0]), MedianMs: ms(times[len(times)/2]), P95Ms: ms(times[min(len(times)-1, len(times)*95/100)]), MaxMs: ms(times[len(times)-1]),
		})
	}

	var baseline map[string]perfResult
	if path := os.Getenv("BACKD_PERF_BASELINE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("baseline: %v", err)
		}
		var rep perfReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Fatalf("baseline %s: %v", path, err)
		}
		baseline = map[string]perfResult{}
		for _, r := range rep.Results {
			baseline[r.Scenario] = r
		}
		if rep.Docs != docs {
			t.Logf("WARNING: the baseline was measured with %d documents, this run has %d: the ratios mean little", rep.Docs, docs)
		}
	}
	tolerance := 1.5
	if v, err := strconv.ParseFloat(os.Getenv("BACKD_PERF_TOLERANCE"), 64); err == nil && v > 1 {
		tolerance = v
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nquery speed: %d documents, %d runs per scenario (times in ms)\n", docs, runs)
	fmt.Fprintf(&b, "%-11s %-46s %9s %9s %9s %9s", "tier", "scenario", "min", "median", "p95", "max")
	if baseline != nil {
		fmt.Fprintf(&b, " %10s", "vs base")
	}
	b.WriteString("\n")
	tier := ""
	slower := 0
	for _, r := range results {
		label := r.Tier
		if r.Tier == tier {
			label = ""
		}
		tier = r.Tier
		fmt.Fprintf(&b, "%-11s %-46s %9.2f %9.2f %9.2f %9.2f", label, r.Scenario, r.MinMs, r.MedianMs, r.P95Ms, r.MaxMs)
		if old, ok := baseline[r.Scenario]; ok && old.MedianMs > 0 {
			ratio := r.MedianMs / old.MedianMs
			flag := ""
			// Below a millisecond, noise is bigger than any change worth flagging.
			if ratio >= tolerance && r.MedianMs >= 1 {
				flag = "  SLOWER"
				slower++
			}
			fmt.Fprintf(&b, " %9.2fx%s", ratio, flag)
		} else if baseline != nil {
			fmt.Fprintf(&b, " %10s", "new")
		}
		b.WriteString("\n")
	}
	if baseline != nil {
		fmt.Fprintf(&b, "\n%d scenario(s) at least %.1fx slower than the baseline (median)\n", slower, tolerance)
	}
	t.Log(b.String())

	if path := os.Getenv("BACKD_PERF_REPORT"); path != "" {
		out, _ := json.MarshalIndent(perfReport{Docs: docs, Runs: runs, At: time.Now().UTC().Format(time.RFC3339), Results: results}, "", "  ")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("report: %v", err)
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatalf("report: %v", err)
		}
		t.Logf("report written to %s", path)
	}
}

type perfResult struct {
	Tier     string  `json:"tier"`
	Scenario string  `json:"scenario"`
	MinMs    float64 `json:"min_ms"`
	MedianMs float64 `json:"median_ms"`
	P95Ms    float64 `json:"p95_ms"`
	MaxMs    float64 `json:"max_ms"`
}

type perfReport struct {
	Docs    int          `json:"docs"`
	Runs    int          `json:"runs"`
	At      string       `json:"at"`
	Results []perfResult `json:"results"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func perfEnvInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}
