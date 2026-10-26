package triggers_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/xid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/triggers"
)

// Load scenarios for the dispatcher (design/triggers-and-realtime.md §6, issue 27.19). They run
// at a reduced scale in CI: set BACKD_LOAD_SCALE=10 (or more) for the numbers that go in the
// operations guide, and BACKD_LOAD_REPORT=<file> to append the results as lines of text.
//
// What they check is correctness under load (nothing silently lost, no job twice, every drop
// and gap accounted for); what they report is how long it takes and what it holds.

const loadSchema = `{"type":"object","properties":{"status":{"type":"string"},"n":{"type":"integer"},"a":{"type":"integer"},"b":{"type":"integer"},"c":{"type":"integer"}}}`

func loadScale() int {
	if n, err := strconv.Atoi(os.Getenv("BACKD_LOAD_SCALE")); err == nil && n > 0 {
		return n
	}
	return 1
}

type loadEnv struct {
	t      *testing.T
	client *mongo.Client
	realm  string
	reg    *registry.Registry
	state  *mongodb.TriggerState
	log    *slog.Logger
}

// report prints and, when asked, appends one result line.
func report(t *testing.T, scenario string, kv ...any) {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario=%s", scenario)
	for i := 0; i+1 < len(kv); i += 2 {
		switch v := kv[i+1].(type) {
		case float64:
			fmt.Fprintf(&b, " %v=%.1f", kv[i], v)
		case time.Duration:
			fmt.Fprintf(&b, " %v=%dms", kv[i], v.Milliseconds())
		default:
			fmt.Fprintf(&b, " %v=%v", kv[i], v)
		}
	}
	t.Log("RESULT " + b.String())
	if path := os.Getenv("BACKD_LOAD_REPORT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, b.String())
			f.Close()
		}
	}
}

// newLoadEnv provisions a realm whose `items` collection has the triggers in the YAML.
func newLoadEnv(t *testing.T, collectionYAML string, functions ...string) *loadEnv {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Disconnect(ctx) }) //nolint:errcheck
	realm := "l" + xid.New().String()
	t.Cleanup(func() {
		names, _ := client.ListDatabaseNames(ctx, bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: "^" + realm + "__"}}}})
		for _, n := range names {
			_ = client.Database(n).Drop(ctx)
		}
		db := client.Database(mongodb.DeploymentDatabase)
		_, _ = db.Collection(mongodb.TriggerStateCollection).DeleteMany(ctx, bson.D{{Key: "_id", Value: "dispatcher:" + realm}})
		_, _ = db.Collection(mongodb.TriggerGapsCollection).DeleteMany(ctx, bson.D{{Key: "deployment", Value: realm}})
	})
	root := t.TempDir()
	files := map[string]string{
		realm + "/realm.yaml":                 "auth: enabled\nsignup: open\n",
		realm + "/main/items/schema.json":     loadSchema,
		realm + "/main/items/collection.yaml": collectionYAML,
		realm + "/main/_functions/deno.json":  "{}",
	}
	if len(functions) == 0 {
		functions = []string{"f1"}
	}
	for _, f := range functions {
		files[realm+"/main/_functions/"+f+"/function.yaml"] = "internal: true\nmode: async\ntimeout: 20s\n"
		files[realm+"/main/_functions/"+f+"/index.ts"] = "export default () => 1;\n"
	}
	for p, c := range files {
		full := root + "/" + p
		if err := os.MkdirAll(full[:len(full)-len(baseName(full))-1], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := (&mongodb.Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return &loadEnv{t: t, client: client, realm: realm, reg: reg, log: log,
		state: &mongodb.TriggerState{Client: client, Deployment: realm}}
}

func (e *loadEnv) items() *mongo.Collection {
	return e.client.Database(e.realm + "__main").Collection("items")
}

// dispatcher starts a dispatcher; stop waits for it.
func (e *loadEnv) dispatcher(holder string, limits *registry.TriggerLimits) (stop func()) {
	d := &triggers.Dispatcher{
		Source: mongodb.NewChangeSource(e.client), State: e.state, Queue: realQueue{e.client}, Holder: holder, Log: e.log,
		LeaseTTL: 2 * time.Second, FlushEvery: 20 * time.Millisecond, WatermarkEvery: 100 * time.Millisecond,
	}
	if limits != nil {
		d.Limits = func(*registry.Registry, string) registry.TriggerLimits { return *limits }
	}
	d.SetRegistry(e.reg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				e.t.Error("dispatcher did not stop")
			}
		})
	}
}

func (e *loadEnv) waitPosition() {
	waitFor(e.t, "the first position", func() bool {
		pos, _, found, _ := e.state.Watermark(context.Background())
		return found && len(pos) > 0
	})
}

func (e *loadEnv) jobs(filter bson.D) int {
	n, err := e.client.Database(mongodb.SystemDatabaseName(e.realm)).Collection(mongodb.JobsCollection).CountDocuments(context.Background(), filter)
	if err != nil {
		e.t.Fatal(err)
	}
	return int(n)
}

func (e *loadEnv) jobsOf(trigger string) int {
	return e.jobs(bson.D{{Key: "origin", Value: "trigger:main/items/" + trigger}})
}

// gaps is how many changes were recorded as gaps, by reason.
func (e *loadEnv) gaps(reason string) int {
	cur, err := e.client.Database(mongodb.DeploymentDatabase).Collection(mongodb.TriggerGapsCollection).
		Find(context.Background(), bson.D{{Key: "deployment", Value: e.realm}, {Key: "reason", Value: reason}})
	if err != nil {
		e.t.Fatal(err)
	}
	var docs []bson.M
	if err := cur.All(context.Background(), &docs); err != nil {
		e.t.Fatal(err)
	}
	total := 0
	for _, d := range docs {
		switch c := d["count"].(type) {
		case int32:
			total += int(c)
		case int64:
			total += int(c)
		}
	}
	return total
}

func metaDoc(v int64) bson.D {
	now := time.Now().UTC()
	return bson.D{{Key: "created_at", Value: now}, {Key: "updated_at", Value: now}, {Key: "version", Value: v}}
}

// insertMany inserts n documents, in batches, with status and the given extra fields.
func (e *loadEnv) insertMany(prefix string, n int, status string, chain *bson.D) {
	e.t.Helper()
	const batch = 500
	for from := 0; from < n; from += batch {
		var docs []any
		for i := from; i < min(from+batch, n); i++ {
			m := metaDoc(1)
			if chain != nil {
				m = append(m, bson.E{Key: "chain", Value: *chain})
			}
			docs = append(docs, bson.D{{Key: "_id", Value: fmt.Sprintf("%s-%06d", prefix, i)}, {Key: "_meta", Value: m}, {Key: "status", Value: status}, {Key: "n", Value: int64(i)}})
		}
		if _, err := e.items().InsertMany(context.Background(), docs); err != nil {
			e.t.Fatal(err)
		}
	}
}

func heapMB() float64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// An import of many documents: every one is a first-party event, none is dropped, and the
// backlog grows in the jobs collection while the dispatcher keeps up with the stream.
func TestLoadImport(t *testing.T) {
	n := 2000 * loadScale()
	e := newLoadEnv(t, "triggers:\n  on_new:\n    events: [created]\n    function: main/f1\n")
	stop := e.dispatcher("a", nil)
	defer stop()
	e.waitPosition()
	heap0 := heapMB()
	t0 := time.Now()
	e.insertMany("imp", n, "new", nil)
	written := time.Since(t0)
	waitFor(t, "every job", func() bool { return e.jobsOf("on_new") >= n })
	total := time.Since(t0)
	time.Sleep(300 * time.Millisecond)
	if got := e.jobsOf("on_new"); got != n {
		t.Fatalf("%d jobs for %d documents (none may be dropped or doubled)", got, n)
	}
	if g := e.gaps("backlog") + e.gaps("window") + e.gaps("preimage_missing"); g != 0 {
		t.Fatalf("%d changes recorded as gaps", g)
	}
	report(t, "import", "documents", n, "write", written, "until_queued", total, "jobs_per_s", float64(n)/total.Seconds(), "heap_delta_mb", heapMB()-heap0)
}

// A hot collection with several triggers, one of them with an expensive condition: every
// matching change makes exactly its jobs.
func TestLoadHotCollectionSeveralTriggers(t *testing.T) {
	n := 1000 * loadScale()
	var clauses []string
	for i := range 40 {
		clauses = append(clauses, fmt.Sprintf("(document.n >= %d || document.n < %d)", -i, i+1))
	}
	e := newLoadEnv(t, `triggers:
  all_new:
    events: [created]
    function: main/f1
  paid:
    events: [updated]
    when: "document.status == 'paid' && before.status != 'paid'"
    function: main/f2
  heavy:
    events: [created, updated]
    when: "`+strings.Join(clauses, " && ")+`"
    function: main/f3
`, "f1", "f2", "f3")
	stop := e.dispatcher("a", nil)
	defer stop()
	e.waitPosition()
	t0 := time.Now()
	e.insertMany("hot", n, "new", nil)
	for i := 0; i < n; i += 2 { // half of them are paid: a transition
		if _, err := e.items().UpdateOne(context.Background(), bson.D{{Key: "_id", Value: fmt.Sprintf("hot-%06d", i)}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "paid"}, {Key: "_meta.version", Value: int64(2)}}}}); err != nil {
			t.Fatal(err)
		}
	}
	wantPaid := (n + 1) / 2
	wantHeavy := n + wantPaid
	waitFor(t, "every job", func() bool {
		return e.jobsOf("all_new") >= n && e.jobsOf("paid") >= wantPaid && e.jobsOf("heavy") >= wantHeavy
	})
	total := time.Since(t0)
	time.Sleep(300 * time.Millisecond)
	for name, want := range map[string]int{"all_new": n, "paid": wantPaid, "heavy": wantHeavy} {
		if got := e.jobsOf(name); got != want {
			t.Errorf("trigger %s: %d jobs, want %d", name, got, want)
		}
	}
	report(t, "hot_collection", "changes", n+wantPaid, "jobs", n+wantPaid+wantHeavy, "until_queued", total, "jobs_per_s", float64(n+wantPaid+wantHeavy)/total.Seconds())
}

// A chained burst is bounded per trigger per minute, a first-party import in the same run is
// not, and a chain stops at its depth: every drop is counted.
func TestLoadChainedStormAndDepth(t *testing.T) {
	n := 500 * loadScale()
	limits := registry.TriggerLimits{MaxChain: 4, MaxChainedPerMinute: 20, MaxPerCollection: 20, MaxBacklog: 1_000_000}
	e := newLoadEnv(t, "triggers:\n  on_new:\n    events: [created]\n    function: main/f1\n")
	stop := e.dispatcher("a", &limits)
	defer stop()
	e.waitPosition()
	chained := bson.D{{Key: "root", Value: "r-1"}, {Key: "depth", Value: int64(1)}}
	e.insertMany("storm", n, "new", &chained) // n chained events at depth 1
	e.insertMany("first", n, "new", nil)      // n first-party events
	deep := bson.D{{Key: "root", Value: "r-2"}, {Key: "depth", Value: int64(limits.MaxChain)}}
	e.insertMany("deep", 10, "new", &deep) // at the cap: each one is one level too deep
	want := n + limits.MaxChainedPerMinute
	waitFor(t, "the first-party events and the allowed chained ones", func() bool { return e.jobsOf("on_new") >= want })
	time.Sleep(500 * time.Millisecond)
	if got := e.jobsOf("on_new"); got != want {
		t.Fatalf("%d jobs, want %d: all %d first-party plus %d chained", got, want, n, limits.MaxChainedPerMinute)
	}
	report(t, "chained_storm", "chained_events", n, "first_party", n, "queued", want, "dropped", n-limits.MaxChainedPerMinute+10)
}

// Past the realm's backlog ceiling events are recorded as gaps, not queued, and what was
// queued plus what was recorded accounts for every change.
func TestLoadBacklogCeiling(t *testing.T) {
	n := 1000 * loadScale()
	limits := registry.TriggerLimits{MaxChain: 16, MaxChainedPerMinute: 60, MaxPerCollection: 20, MaxBacklog: 200}
	e := newLoadEnv(t, "triggers:\n  on_new:\n    events: [created]\n    function: main/f1\n")
	stop := e.dispatcher("a", &limits)
	defer stop()
	e.waitPosition()
	e.insertMany("cap", n, "new", nil)
	waitFor(t, "every change accounted for", func() bool { return e.jobsOf("on_new")+e.gaps("backlog") >= n })
	time.Sleep(500 * time.Millisecond)
	queued, gaps := e.jobsOf("on_new"), e.gaps("backlog")
	if queued+gaps != n {
		t.Fatalf("%d queued + %d gaps = %d, want %d: a change was lost silently", queued, gaps, queued+gaps, n)
	}
	if gaps == 0 || queued < limits.MaxBacklog {
		t.Fatalf("queued %d, gaps %d: the ceiling of %d did not apply as expected", queued, gaps, limits.MaxBacklog)
	}
	report(t, "backlog_ceiling", "changes", n, "ceiling", limits.MaxBacklog, "queued", queued, "gap_changes", gaps)
}

// The lease changes hands while changes keep coming: every change makes exactly one job.
func TestLoadLeaseFailoverDuringTraffic(t *testing.T) {
	n := 3000 * loadScale()
	e := newLoadEnv(t, "triggers:\n  on_new:\n    events: [created]\n    function: main/f1\n")
	stopA := e.dispatcher("a", nil)
	e.waitPosition()
	var wg sync.WaitGroup
	wg.Go(func() {
		e.insertMany("fo", n, "new", nil)
	})
	waitFor(t, "traffic", func() bool { return e.jobsOf("on_new") >= n/4 })
	stopA()
	stopB := e.dispatcher("b", nil)
	defer stopB()
	wg.Wait()
	t0 := time.Now()
	waitFor(t, "every job", func() bool { return e.jobsOf("on_new") >= n })
	handoff := time.Since(t0)
	time.Sleep(500 * time.Millisecond)
	if got := e.jobsOf("on_new"); got != n {
		t.Fatalf("%d jobs for %d documents across a failover", got, n)
	}
	report(t, "failover", "documents", n, "drained_after_handoff", handoff)
}

// The dispatcher is away while documents change (an outage past what the history holds, here
// simulated by starting it from now); a rescan brings back what a gap lost.
func TestLoadOutageThenRescan(t *testing.T) {
	n := 1000 * loadScale()
	e := newLoadEnv(t, "triggers:\n  on_paid:\n    events: [updated]\n    when: \"document.status == 'paid'\"\n    function: main/f1\n")
	since := time.Now().UTC().Add(-time.Second)
	e.insertMany("out", n, "paid", nil)
	stop := e.dispatcher("a", nil) // starts from now: what happened before is not in its stream
	defer stop()
	e.waitPosition()
	time.Sleep(500 * time.Millisecond)
	if got := e.jobsOf("on_paid"); got != 0 {
		t.Fatalf("%d jobs from changes made before the dispatcher's position", got)
	}
	t0 := time.Now()
	res, err := triggers.Rescan(context.Background(), e.reg, triggers.RescanRequest{
		Realm: e.realm, Database: "main", Collection: "items", Since: since, Run: "load", Confirm: true,
	}, &mongodb.DocScanner{Client: e.client}, realQueue{e.client}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != n || e.jobsOf("on_paid") != n {
		t.Fatalf("rescan queued %d (%d in the jobs collection), want %d: %+v", res.Queued, e.jobsOf("on_paid"), n, res)
	}
	report(t, "outage_rescan", "documents", n, "rescan", time.Since(t0), "docs_per_s", float64(n)/time.Since(t0).Seconds())
}

// A soak: a steady stream of changes for BACKD_SOAK seconds (default a few, for CI) must not
// make the heap or the lag grow.
func TestLoadSoak(t *testing.T) {
	secs := 4
	if v, err := strconv.Atoi(os.Getenv("BACKD_SOAK")); err == nil && v > 0 {
		secs = v
	}
	e := newLoadEnv(t, "triggers:\n  on_new:\n    events: [created]\n    function: main/f1\n")
	// No worker drains the queue here, so lift the realm's ceiling: the soak is about the dispatcher.
	limits := registry.TriggerLimits{MaxChain: 16, MaxChainedPerMinute: 60, MaxPerCollection: 20, MaxBacklog: 100_000_000}
	stop := e.dispatcher("a", &limits)
	defer stop()
	e.waitPosition()
	end := time.Now().Add(time.Duration(secs) * time.Second)
	var heapFirst, heapLast float64
	sent := 0
	for round := 0; time.Now().Before(end); round++ {
		e.insertMany(fmt.Sprintf("soak%d", round), 100, "new", nil)
		sent += 100
		time.Sleep(50 * time.Millisecond)
		if round == 5 {
			heapFirst = heapMB()
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for e.jobsOf("on_new") < sent && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := e.jobsOf("on_new"); got < sent {
		t.Fatalf("%d jobs for %d documents after the soak (%d gap changes recorded)", got, sent, e.gaps("backlog")+e.gaps("window")+e.gaps("preimage_missing"))
	}
	heapLast = heapMB()
	if got := e.jobsOf("on_new"); got != sent {
		t.Fatalf("%d jobs for %d documents", got, sent)
	}
	// Generous: the point is a leak (growth with the number of events), not a few MiB of noise.
	if heapFirst > 0 && heapLast > heapFirst+64 {
		t.Errorf("heap grew from %.1f to %.1f MiB over %d events", heapFirst, heapLast, sent)
	}
	report(t, "soak", "seconds", secs, "events", sent, "heap_first_mb", heapFirst, "heap_last_mb", heapLast)
}
