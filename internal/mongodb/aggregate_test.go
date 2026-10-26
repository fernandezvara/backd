package mongodb

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

const aggSchema = `{
  "type": "object",
  "properties": {
    "category": {"type": ["string", "null"]},
    "paid":     {"type": "boolean"},
    "total":    {"type": ["number", "null"]},
    "qty":      {"type": "integer"},
    "paid_at":  {"type": ["string", "null"], "format": "date-time", "x-backd-store": "date"}
  }
}`

// The database's aggregation gives what the in-memory reference gives, for random documents:
// groups, null groups, buckets across time zones and daylight saving changes, exact sums and
// averages, minimum and maximum, distinct counts, ordering and limits.
func TestAggregateMatchesTheReference(t *testing.T) {
	_, repo := provisionOne(t, aggSchema)
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(7, 11))
	cats := []any{"shoes", "hats", "coats", nil}
	// Around the 2026 daylight saving changes of Europe/Madrid (March 29 and October 25), and
	// the ISO week boundary of the end of the year.
	instants := []time.Time{
		time.Date(2026, 3, 28, 22, 30, 0, 0, time.UTC), time.Date(2026, 3, 28, 23, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC), time.Date(2026, 3, 29, 1, 1, 0, 0, time.UTC),
		time.Date(2026, 10, 24, 22, 30, 0, 0, time.UTC), time.Date(2026, 10, 24, 23, 30, 0, 0, time.UTC),
		time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC), time.Date(2027, 1, 3, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	}
	var docs []storage.Document
	for range 120 {
		fields := map[string]any{"category": cats[rng.IntN(len(cats))], "paid": rng.IntN(2) == 0, "qty": int64(rng.IntN(5))}
		switch rng.IntN(4) {
		case 0:
			fields["total"] = nil
		case 1:
			fields["total"] = int64(rng.IntN(1000))
		default:
			fields["total"] = float64(rng.IntN(100000)) / 100
		}
		if rng.IntN(5) > 0 {
			fields["paid_at"] = instants[rng.IntN(len(instants))]
		} else {
			fields["paid_at"] = nil
		}
		d := newDoc(fields)
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, d)
	}
	// What a read of the stored documents holds (dates as times, ints as int64).
	page, err := repo.List(ctx, storage.Query{Limit: 500, Sort: []storage.SortField{{Field: "id"}}})
	if err != nil {
		t.Fatal(err)
	}
	stored := page.Items

	all := []storage.Metric{
		{Op: storage.MetricCount}, {Op: storage.MetricSum, Field: "total"}, {Op: storage.MetricAvg, Field: "total"},
		{Op: storage.MetricMin, Field: "total"}, {Op: storage.MetricMax, Field: "paid_at"}, {Op: storage.MetricCountDistinct, Field: "qty"},
	}
	for name, q := range map[string]storage.AggregateQuery{
		"total":        {Metrics: all},
		"by category":  {GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: all, Sort: []storage.AggregateSort{{Name: "category"}}},
		"two keys":     {GroupBy: []storage.GroupField{{Field: "category"}, {Field: "paid"}}, Metrics: all},
		"by sum desc":  {GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: all, Sort: []storage.AggregateSort{{Name: "sum_total", Desc: true}}, Limit: 3},
		"day in UTC":   {GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: storage.BucketDay}}, Metrics: all},
		"day Madrid":   {GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: storage.BucketDay, TZ: "Europe/Madrid"}}, Metrics: all},
		"week Madrid":  {GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: storage.BucketWeek, TZ: "Europe/Madrid"}}, Metrics: all},
		"month NYC":    {GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: storage.BucketMonth, TZ: "America/New_York"}}, Metrics: all},
		"year":         {GroupBy: []storage.GroupField{{Field: "paid_at", Bucket: storage.BucketYear}}, Metrics: all},
		"bucket+field": {GroupBy: []storage.GroupField{{Field: "category"}, {Field: "paid_at", Bucket: storage.BucketMonth, TZ: "Europe/Madrid"}}, Metrics: all, Sort: []storage.AggregateSort{{Name: "count", Desc: true}}},
		"filtered": {
			Filter:  storage.Condition{Field: "paid", Op: storage.OpEq, Value: true},
			GroupBy: []storage.GroupField{{Field: "category"}}, Metrics: all,
		},
		"with access": {
			Access:  storage.Condition{Field: "qty", Op: storage.OpGte, Value: int64(2)},
			GroupBy: []storage.GroupField{{Field: "paid"}}, Metrics: all,
		},
		"nothing": {Filter: storage.Const(false), Metrics: all},
	} {
		t.Run(name, func(t *testing.T) {
			want, err := storage.AggregateDocs(stored, q)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.Aggregate(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if got.TotalGroups != want.TotalGroups || len(got.Groups) != len(want.Groups) {
				t.Fatalf("groups: got %d of %d, want %d of %d", len(got.Groups), got.TotalGroups, len(want.Groups), want.TotalGroups)
			}
			for i := range want.Groups {
				w, g := want.Groups[i], got.Groups[i]
				if !equalKeys(w.Key, g.Key) {
					t.Fatalf("group %d key: got %v, want %v", i, g.Key, w.Key)
				}
				for _, m := range q.Metrics {
					if !equalValue(w.Values[m.Name()], g.Values[m.Name()]) {
						t.Errorf("group %d (%v) %s: got %v (%T), want %v (%T)", i, w.Key, m.Name(), g.Values[m.Name()], g.Values[m.Name()], w.Values[m.Name()], w.Values[m.Name()])
					}
				}
			}
		})
	}

	t.Run("too many distinct values", func(t *testing.T) {
		_, err := repo.Aggregate(ctx, storage.AggregateQuery{Metrics: []storage.Metric{{Op: storage.MetricCountDistinct, Field: "_id"}}, MaxDistinct: 10})
		if err != storage.ErrAggregateTooExpensive {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a time limit", func(t *testing.T) {
		_, err := repo.Aggregate(ctx, storage.AggregateQuery{Metrics: []storage.Metric{{Op: storage.MetricCount}}, MaxTime: time.Nanosecond})
		if err != storage.ErrAggregateTooExpensive {
			t.Errorf("err = %v", err)
		}
	})
	_ = docs
	_ = fmt.Sprint
}

func equalKeys(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalValue(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalValue(a, b any) bool {
	if ta, ok := a.(time.Time); ok {
		tb, ok := b.(time.Time)
		return ok && ta.Equal(tb)
	}
	// Numbers: the same value, whether the database kept an integer or a double.
	fa, oka := number(a)
	fb, okb := number(b)
	if oka && okb {
		return fa == fb
	}
	return reflect.DeepEqual(a, b)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
