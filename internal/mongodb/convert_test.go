package mongodb

import (
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestToBSONOrdersKeys(t *testing.T) {
	now := time.Date(2026, 9, 25, 21, 30, 0, 0, time.UTC)
	doc := map[string]any{
		"zeta": int64(1),
		"id":   "abc",
		"alpha": map[string]any{
			"y": "b", "x": []any{map[string]any{"q": 1.5, "p": true}},
		},
		"_meta": map[string]any{"updated_at": now, "created_at": now},
	}
	want := bson.D{
		{Key: "_id", Value: "abc"},
		{Key: "_meta", Value: bson.D{
			{Key: "created_at", Value: bson.NewDateTimeFromTime(now)},
			{Key: "updated_at", Value: bson.NewDateTimeFromTime(now)},
		}},
		{Key: "alpha", Value: bson.D{
			{Key: "x", Value: bson.A{bson.D{{Key: "p", Value: true}, {Key: "q", Value: 1.5}}}},
			{Key: "y", Value: "b"},
		}},
		{Key: "zeta", Value: int64(1)},
	}
	if got := toBSON(doc); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestFromBSON(t *testing.T) {
	now := time.Date(2026, 9, 25, 21, 30, 0, 0, time.UTC)
	oid := bson.NewObjectID()
	d := bson.D{
		{Key: "_id", Value: "abc"},
		{Key: "_meta", Value: bson.D{{Key: "created_at", Value: bson.NewDateTimeFromTime(now)}}},
		{Key: "n32", Value: int32(7)},
		{Key: "list", Value: bson.A{"a", bson.D{{Key: "k", Value: nil}}}},
		{Key: "oid", Value: oid},
	}
	want := map[string]any{
		"id":    "abc",
		"_meta": map[string]any{"created_at": now},
		"n32":   int64(7),
		"list":  []any{"a", map[string]any{"k": nil}},
		"oid":   oid.Hex(),
	}
	if got := fromBSON(d); !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}
