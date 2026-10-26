package mongodb

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/changefeed"
)

// TestMeasurementGate is the measurement gate of design/triggers-and-realtime.md §3 (issue
// 27.1): what pre-change images cost writes and storage, and what a stream costs to read
// while unrelated writes pass. It runs only with BACKD_MEASURE=1 and prints a table; the
// numbers in the issue come from it. The machine and MongoDB make them indicative, not
// absolute: the ratios are the point.
//
//	BACKD_MEASURE=1 MONGO_TEST_URI=... go test ./internal/mongodb -run MeasurementGate -v -timeout 30m
func TestMeasurementGate(t *testing.T) {
	if os.Getenv("BACKD_MEASURE") != "1" {
		t.Skip("BACKD_MEASURE=1 runs the measurement gate")
	}
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	var out strings.Builder
	defer func() { t.Log("\n" + out.String()) }()
	row := func(format string, a ...any) { fmt.Fprintf(&out, format+"\n", a...) }

	row("== A. write latency of ReplaceOne (one writer, then 8 writers), by document size and pre-images")
	row("%-10s %-10s %-8s %10s %10s %10s %12s", "doc", "images", "writers", "p50", "p99", "mean", "ops/s")
	for _, size := range []int{1 << 10, 16 << 10, 256 << 10} {
		for _, images := range []bool{false, true} {
			for _, writers := range []int{1, 8} {
				coll := measureCollection(t, ctx, client, realm, fmt.Sprintf("w%d_%v_%d", size, images, writers), images)
				res := writeLoad(ctx, coll, size, writers, 1500/writers*writers)
				row("%-10s %-10v %-8d %10s %10s %10s %12.0f", human(size), images, writers, res.p50.Round(time.Microsecond), res.p99.Round(time.Microsecond), res.mean.Round(time.Microsecond), res.opsPerSec)
			}
		}
	}

	row("\n== B. storage after 3000 replaces of 3000 documents of 16 KiB")
	for _, images := range []bool{false, true} {
		coll := measureCollection(t, ctx, client, realm, fmt.Sprintf("s_%v", images), images)
		writeLoad(ctx, coll, 16<<10, 4, 3000)
		var st bson.M
		db := coll.Database()
		_ = db.RunCommand(ctx, bson.D{{Key: "collStats", Value: coll.Name()}}).Decode(&st)
		pre := int64(0)
		var pst bson.M
		if err := client.Database("config").RunCommand(ctx, bson.D{{Key: "collStats", Value: "system.preimages"}}).Decode(&pst); err == nil {
			pre = asInt(pst["size"])
		}
		row("images=%-5v data=%s  config.system.preimages (whole server)=%s", images, human(int(asInt(st["size"]))), human(int(pre)))
	}

	row("\n== C. reading cost: time to drain 5000 watched changes with N unrelated writes per watched one")
	row("%-12s %12s %14s", "unrelated/ev", "drain", "changes/s")
	for _, ratio := range []int{0, 10, 100} {
		d, n := drainWithNoise(t, ctx, client, realm, 5000, ratio)
		row("%-12d %12s %14.0f", ratio, d.Round(time.Millisecond), float64(n)/d.Seconds())
	}

	row("\n== D. several consumers reading the same backlog of 5000 changes")
	row("%-10s %12s %16s", "consumers", "drain (all)", "changes/s total")
	for _, consumers := range []int{1, 2, 4, 8} {
		d := drainMany(t, ctx, client, realm, 5000, consumers)
		row("%-10d %12s %16.0f", consumers, d.Round(time.Millisecond), float64(5000*consumers)/d.Seconds())
	}
}

type loadResult struct {
	p50, p99, mean time.Duration
	opsPerSec      float64
}

func measureCollection(t *testing.T, ctx context.Context, client *mongo.Client, realm, name string, images bool) *mongo.Collection {
	t.Helper()
	db := client.Database(realm + "__m")
	if err := db.CreateCollection(ctx, name); err != nil {
		t.Fatal(err)
	}
	if images {
		if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: name}, {Key: "changeStreamPreAndPostImages", Value: bson.D{{Key: "enabled", Value: true}}}}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	return db.Collection(name)
}

// writeLoad seeds up to 3000 documents and replaces them ops times across writers.
func writeLoad(ctx context.Context, coll *mongo.Collection, size, writers, ops int) loadResult {
	docs := min(ops, 3000)
	body := strings.Repeat("x", size)
	var seed []any
	for i := range docs {
		seed = append(seed, bson.D{{Key: "_id", Value: i}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}, {Key: "body", Value: body}})
	}
	for lo := 0; lo < len(seed); lo += 500 {
		_, _ = coll.InsertMany(ctx, seed[lo:min(lo+500, len(seed))])
	}
	per := ops / writers
	var mu sync.Mutex
	var all []time.Duration
	var wg sync.WaitGroup
	start := time.Now()
	for w := range writers {
		wg.Go(func() {
			lat := make([]time.Duration, 0, per)
			for i := range per {
				id := (w*per + i) % docs
				d := bson.D{{Key: "_id", Value: id}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(2 + i)}}}, {Key: "body", Value: body}}
				t0 := time.Now()
				_, _ = coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, d)
				lat = append(lat, time.Since(t0))
			}
			mu.Lock()
			all = append(all, lat...)
			mu.Unlock()
		})
	}
	wg.Wait()
	total := time.Since(start)
	slices.Sort(all)
	var sum time.Duration
	for _, d := range all {
		sum += d
	}
	return loadResult{p50: all[len(all)/2], p99: all[len(all)*99/100], mean: sum / time.Duration(len(all)), opsPerSec: float64(len(all)) / total.Seconds()}
}

func human(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

// drainWithNoise writes n changes to a watched collection with ratio unrelated writes between
// each, then times a stream reading them all.
func drainWithNoise(t *testing.T, ctx context.Context, client *mongo.Client, realm string, n, ratio int) (time.Duration, int) {
	t.Helper()
	watched := measureCollection(t, ctx, client, realm, fmt.Sprintf("n%d", ratio), true)
	noise := client.Database(realm + "__noise").Collection(fmt.Sprintf("n%d", ratio))
	set := changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: realm, DB: watched.Database().Name(), Coll: watched.Name()}}}
	src := &ChangeSource{Client: client, MaxAwait: 100 * time.Millisecond}
	st, err := src.Open(ctx, set, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := st.Position()
	st.Close()

	var batch []any
	flush := func() {
		if len(batch) > 0 {
			_, _ = noise.InsertMany(ctx, batch, options.InsertMany().SetOrdered(false))
			batch = batch[:0]
		}
	}
	for i := range n {
		_, _ = watched.InsertOne(ctx, bson.D{{Key: "_id", Value: i}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}, {Key: "body", Value: "x"}})
		for j := range ratio {
			batch = append(batch, bson.D{{Key: "n", Value: i*ratio + j}})
		}
		if len(batch) >= 1000 {
			flush()
		}
	}
	flush()
	return timeDrain(t, ctx, src, set, start, n), n
}

func timeDrain(t *testing.T, ctx context.Context, src *ChangeSource, set changefeed.WatchSet, start changefeed.Position, n int) time.Duration {
	t.Helper()
	st, err := src.Open(ctx, set, start)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	t0 := time.Now()
	got := 0
	for got < n {
		ev, err := st.Next(ctx)
		if err != nil {
			t.Fatalf("drain: %v after %d of %d", err, got, n)
		}
		if ev.Change != nil {
			got++
		}
	}
	return time.Since(t0)
}

// drainMany reads the same backlog with several consumers at once.
func drainMany(t *testing.T, ctx context.Context, client *mongo.Client, realm string, n, consumers int) time.Duration {
	t.Helper()
	watched := measureCollection(t, ctx, client, realm, fmt.Sprintf("m%d", consumers), true)
	set := changefeed.WatchSet{Collections: []changefeed.Namespace{{Realm: realm, DB: watched.Database().Name(), Coll: watched.Name()}}}
	src := &ChangeSource{Client: client, MaxAwait: 100 * time.Millisecond}
	st, err := src.Open(ctx, set, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := st.Position()
	st.Close()
	var docs []any
	for i := range n {
		docs = append(docs, bson.D{{Key: "_id", Value: i}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}, {Key: "body", Value: "x"}})
	}
	for lo := 0; lo < n; lo += 500 {
		_, _ = watched.InsertMany(ctx, docs[lo:min(lo+500, n)])
	}
	var wg sync.WaitGroup
	t0 := time.Now()
	for range consumers {
		wg.Go(func() {
			timeDrain(t, ctx, src, set, start, n)
		})
	}
	wg.Wait()
	return time.Since(t0)
}
