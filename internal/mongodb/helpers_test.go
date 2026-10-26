package mongodb

import (
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestTrimPage(t *testing.T) {
	for _, tt := range []struct {
		in    []int
		limit int
		want  []int
		more  bool
	}{
		{[]int{1, 2, 3}, 2, []int{1, 2}, true},
		{[]int{1, 2}, 2, []int{1, 2}, false},
		{[]int{1, 2, 3}, 0, []int{1, 2, 3}, false},
		{nil, 5, nil, false},
	} {
		got, more := trimPage(tt.in, tt.limit)
		if !reflect.DeepEqual(got, tt.want) || more != tt.more {
			t.Errorf("trimPage(%v, %d) = %v, %v; want %v, %v", tt.in, tt.limit, got, more, tt.want, tt.more)
		}
	}
}

func TestWithinTime(t *testing.T) {
	since, until := time.Unix(10, 0), time.Unix(20, 0)
	if got := withinTime(bson.D{}, "at", time.Time{}, time.Time{}); len(got) != 0 {
		t.Fatalf("no bounds added %v", got)
	}
	got := withinTime(bson.D{{Key: "x", Value: 1}}, "at", since, until)
	want := bson.D{{Key: "x", Value: 1}, {Key: "at", Value: bson.D{{Key: "$gte", Value: since}, {Key: "$lt", Value: until}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	got = withinTime(bson.D{}, "at", time.Time{}, until)
	if !reflect.DeepEqual(got, bson.D{{Key: "at", Value: bson.D{{Key: "$lt", Value: until}}}}) {
		t.Fatalf("until only: %v", got)
	}
}

func TestMapAllNeverNil(t *testing.T) {
	if got := mapAll([]int(nil), func(i int) string { return "" }); got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", got)
	}
}
