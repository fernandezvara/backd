package storage

import (
	"reflect"
	"testing"
	"time"
)

func sale(id, cat string, total any, paid *time.Time) Document {
	d := Document{"id": id, "category": cat, "total": total, "_meta": map[string]any{"owner": "u1"}}
	if paid != nil {
		d["paid_at"] = *paid
	}
	return d
}

func TestAggregateDocs(t *testing.T) {
	ts := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	docs := []Document{
		sale("1", "shoes", 100.1, ts("2026-09-30T23:30:00Z")),
		sale("2", "shoes", 200.4, ts("2026-10-01T00:30:00Z")),
		sale("3", "hats", int64(10), nil),
		sale("4", "shoes", nil, ts("2026-10-04T10:00:00Z")),
		{"id": "5", "total": 5.5},
	}
	res, err := AggregateDocs(docs, AggregateQuery{
		GroupBy: []GroupField{{Field: "category"}},
		Metrics: []Metric{{Op: MetricCount}, {Op: MetricSum, Field: "total"}, {Op: MetricAvg, Field: "total"}, {Op: MetricMax, Field: "paid_at"}, {Op: MetricCountDistinct, Field: "_meta.owner"}},
		Sort:    []AggregateSort{{Name: "sum_total", Desc: true}},
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalGroups != 3 || len(res.Groups) != 2 {
		t.Fatalf("groups = %+v", res)
	}
	shoes := res.Groups[0]
	if shoes.Key[0] != "shoes" || shoes.Values["count"] != int64(3) || shoes.Values["sum_total"] != Decimal("300.5") || shoes.Values["avg_total"] != Decimal("150.25") || shoes.Values["count_distinct__meta.owner"] != int64(1) {
		t.Errorf("shoes = %+v", shoes)
	}
	if !shoes.Values["max_paid_at"].(time.Time).Equal(*ts("2026-10-04T10:00:00Z")) {
		t.Errorf("max = %v", shoes.Values["max_paid_at"])
	}
	// The null group (a document with no category) is the lowest sum: 5.5 < 10.
	all, _ := AggregateDocs(docs, AggregateQuery{GroupBy: []GroupField{{Field: "category"}}, Metrics: []Metric{{Op: MetricCount}}})
	if len(all.Groups) != 3 || all.Groups[0].Key[0] != nil || all.Groups[1].Key[0] != "hats" {
		t.Errorf("null sorts first: %+v", all.Groups)
	}

	// Buckets in a time zone: 23:30Z on the 30th is already October 1st in Madrid (UTC+2).
	byDay, _ := AggregateDocs(docs, AggregateQuery{GroupBy: []GroupField{{Field: "paid_at", Bucket: BucketMonth, TZ: "Europe/Madrid"}}, Metrics: []Metric{{Op: MetricCount}}})
	got := map[any]any{}
	for _, g := range byDay.Groups {
		got[g.Key[0]] = g.Values["count"]
	}
	if !reflect.DeepEqual(got, map[any]any{nil: int64(2), "2026-10": int64(3)}) {
		t.Errorf("months in Madrid = %v", got)
	}
	utc, _ := AggregateDocs(docs, AggregateQuery{GroupBy: []GroupField{{Field: "paid_at", Bucket: BucketMonth}}, Metrics: []Metric{{Op: MetricCount}}})
	if len(utc.Groups) != 3 {
		t.Errorf("months in UTC = %+v", utc.Groups)
	}

	// No group_by: one group, empty key, and a filter that matches nothing still has one total of 0.
	one, _ := AggregateDocs(docs, AggregateQuery{Metrics: []Metric{{Op: MetricCount}, {Op: MetricAvg, Field: "total"}}})
	if len(one.Groups) != 1 || len(one.Groups[0].Key) != 0 || one.Groups[0].Values["count"] != int64(5) {
		t.Errorf("total = %+v", one)
	}
	none, _ := AggregateDocs(docs, AggregateQuery{Filter: Const(false), Metrics: []Metric{{Op: MetricCount}}})
	if none.TotalGroups != 1 || none.Groups[0].Values["count"] != int64(0) {
		t.Errorf("a total over nothing is a count of 0: %+v", none)
	}
	grouped, _ := AggregateDocs(docs, AggregateQuery{Filter: Const(false), GroupBy: []GroupField{{Field: "category"}}, Metrics: []Metric{{Op: MetricCount}}})
	if grouped.TotalGroups != 0 || len(grouped.Groups) != 0 {
		t.Errorf("no documents, no groups: %+v", grouped)
	}

	if _, err := AggregateDocs(docs, AggregateQuery{Metrics: []Metric{{Op: MetricCountDistinct, Field: "id"}}, MaxDistinct: 2}); err != ErrAggregateTooExpensive {
		t.Errorf("distinct cap: %v", err)
	}
}

func TestFormatQuotient(t *testing.T) {
	for _, c := range []struct {
		sum   string
		n     int64
		scale int
		want  string
	}{
		{"18234.5", 412, 1, "44.25849514563106796116504854368932"},
		{"6", 2, 0, "3"},
		{"300.5", 2, 1, "150.25"},
		{"1", 3, 0, "0.3333333333333333333333333333333333"},
		{"2", 3, 0, "0.6666666666666666666666666666666667"},
	} {
		r, _, _ := decimalOf(0.0)
		r.SetString(c.sum)
		if got := formatQuotient(r, c.n, c.scale); got != c.want {
			t.Errorf("%s/%d = %s, want %s", c.sum, c.n, got, c.want)
		}
	}
}
