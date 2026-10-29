package client

import (
	"reflect"
	"testing"
	"time"
)

func group(name, key string, count float64, extra ...any) AggregateGroup {
	g := AggregateGroup{Key: map[string]any{name: key}, Values: map[string]any{"count": count}}
	for i := 0; i+1 < len(extra); i += 2 {
		g.Values[extra[i].(string)] = extra[i+1]
	}
	return g
}

func keysOf(gs []AggregateGroup, name string) []any {
	var out []any
	for _, g := range gs {
		out = append(out, g.Key[name])
	}
	return out
}

func TestBucketKeys(t *testing.T) {
	d := time.Date(2026, 1, 1, 23, 30, 0, 0, time.FixedZone("x", -5*3600)) // 2026-01-02 04:30 UTC
	cases := map[BucketUnit]string{BucketDay: "2026-01-02", BucketWeek: "2026-W01", BucketMonth: "2026-01", BucketYear: "2026"}
	for u, want := range cases {
		if got, err := BucketKey(d, u); err != nil || got != want {
			t.Errorf("%s = %q %v; want %q", u, got, err, want)
		}
	}
	// ISO weeks belong to the year of their Thursday
	if k, _ := BucketKey(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), BucketWeek); k != "2026-W53" {
		t.Errorf("2027-01-01 = %s", k)
	}
	if k, _ := BucketKey(time.Date(2024, 12, 30, 0, 0, 0, 0, time.UTC), BucketWeek); k != "2025-W01" {
		t.Errorf("2024-12-30 = %s", k)
	}
	if _, err := BucketKey(d, "hour"); err == nil {
		t.Error("an unknown unit")
	}
}

func TestFillBucketsMonths(t *testing.T) {
	in := []AggregateGroup{group("paid_at:month", "2026-01", 3, "sum_total", "10"), group("paid_at:month", "2026-03", 1)}
	got, err := FillBuckets(in, FillBucketsOptions{Field: "paid_at", Unit: BucketMonth, From: "2025-12", To: "2026-04", Empty: map[string]any{"sum_total": "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keysOf(got, "paid_at:month"), []any{"2025-12", "2026-01", "2026-02", "2026-03", "2026-04"}) {
		t.Errorf("keys = %v", keysOf(got, "paid_at:month"))
	}
	if c, _ := got[0].Count(); c != 0 || got[0].Values["sum_total"] != "0" {
		t.Errorf("an empty bucket: %+v", got[0])
	}
	if c, _ := got[1].Count(); c != 3 || got[1].Values["sum_total"] != "10" {
		t.Errorf("a kept bucket: %+v", got[1])
	}
}

func TestFillBucketsWeeksDaysYearsAndDates(t *testing.T) {
	got, err := FillBuckets(nil, FillBucketsOptions{Field: "d", Unit: BucketWeek, From: "2026-W52", To: "2027-W02"})
	if err != nil || !reflect.DeepEqual(keysOf(got, "d:week"), []any{"2026-W52", "2026-W53", "2027-W01", "2027-W02"}) {
		t.Errorf("weeks: %v %v", keysOf(got, "d:week"), err)
	}
	got, _ = FillBuckets(nil, FillBucketsOptions{Field: "d", Unit: BucketDay, From: time.Date(2026, 2, 27, 18, 0, 0, 0, time.UTC), To: "2026-03-02"})
	if !reflect.DeepEqual(keysOf(got, "d:day"), []any{"2026-02-27", "2026-02-28", "2026-03-01", "2026-03-02"}) {
		t.Errorf("days: %v", keysOf(got, "d:day"))
	}
	got, _ = FillBuckets(nil, FillBucketsOptions{Field: "d", Unit: BucketYear, From: "2024", To: "2026"})
	if !reflect.DeepEqual(keysOf(got, "d:year"), []any{"2024", "2025", "2026"}) {
		t.Errorf("years: %v", keysOf(got, "d:year"))
	}
}

func TestFillBucketsKeepsTheNullGroupFirstAndChecksInput(t *testing.T) {
	null := AggregateGroup{Key: map[string]any{"d:month": nil}, Values: map[string]any{"count": float64(2)}}
	got, err := FillBuckets([]AggregateGroup{group("d:month", "2026-01", 1), null}, FillBucketsOptions{Field: "d", Unit: BucketMonth, From: "2026-01", To: "2026-01"})
	if err != nil || len(got) != 2 || got[0].Key["d:month"] != nil {
		t.Errorf("%v %+v", err, got)
	}
	bad := []FillBucketsOptions{
		{Field: "d", Unit: "hour", From: "2026", To: "2026"},
		{Field: "d", Unit: BucketMonth, From: "2026-02", To: "2026-01"},
		{Field: "d", Unit: BucketMonth, From: "soon", To: "2026-01"},
		{Field: "d", Unit: BucketDay, From: "2000-01-01", To: "2026-01-01"}, // too many buckets
	}
	for _, o := range bad {
		if _, err := FillBuckets(nil, o); err == nil {
			t.Errorf("%+v was accepted", o)
		}
	}
	other := AggregateGroup{Key: map[string]any{"status": "x"}}
	if _, err := FillBuckets([]AggregateGroup{other}, FillBucketsOptions{Field: "d", Unit: BucketMonth, From: "2026-01", To: "2026-01"}); err == nil {
		t.Error("groups grouped by something else")
	}
}
