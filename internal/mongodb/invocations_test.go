package mongodb

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestInvocationsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for i, r := range []auth.InvocationRecord{
		{Function: "app/checkout", Actor: "user:u1", Mode: "sync", Status: "ok", DurationMS: 12, RequestID: "r1", Logs: []auth.LogLine{{Level: "log", Line: "charging card"}}},
		{Function: "app/checkout", Actor: "user:u0", Mode: "sync", Status: "function_error", Code: "out_of_stock", DurationMS: 3, RequestID: "r2"},
		{Function: "app/stats", Actor: auth.ActorAnonymous, Mode: "sync", Status: "timeout", DurationMS: 10000},
	} {
		r.ID, r.At = "a"+string(rune('0'+i)), t0.Add(time.Duration(i)*time.Minute)
		r.ExpiresAt = r.At.Add(7 * 24 * time.Hour)
		if err := s.RecordInvocation(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	recs, more, err := s.ListInvocations(ctx, auth.InvocationFilter{Limit: 2})
	if err != nil || !more || len(recs) != 2 || recs[0].ID != "a2" || recs[1].ID != "a1" {
		t.Fatalf("newest first: %+v %v %v", recs, more, err)
	}
	recs, more, _ = s.ListInvocations(ctx, auth.InvocationFilter{Limit: 2, Skip: 2})
	if more || len(recs) != 1 || recs[0].ID != "a0" {
		t.Errorf("second page: %+v %v", recs, more)
	}
	first := recs[0]
	want := auth.LogLine{Level: "log", Line: "charging card"}
	if len(first.Logs) != 1 || first.Logs[0] != want || first.RequestID != "r1" || !first.At.Equal(t0) || !first.ExpiresAt.Equal(t0.Add(7*24*time.Hour)) {
		t.Errorf("record: %+v", first)
	}

	for _, tt := range []struct {
		f    auth.InvocationFilter
		want []string
	}{
		{auth.InvocationFilter{Function: "app/checkout"}, []string{"a1", "a0"}},
		{auth.InvocationFilter{Function: "app/stats"}, []string{"a2"}},
		{auth.InvocationFilter{RequestID: "r2"}, []string{"a1"}},
		{auth.InvocationFilter{Since: t0.Add(time.Minute)}, []string{"a2", "a1"}},
		{auth.InvocationFilter{Until: t0.Add(time.Minute)}, []string{"a0"}},
	} {
		recs, _, err := s.ListInvocations(ctx, tt.f)
		var ids []string
		for _, r := range recs {
			ids = append(ids, r.ID)
		}
		if err != nil || !reflect.DeepEqual(ids, tt.want) {
			t.Errorf("%+v: %v %v", tt.f, ids, err)
		}
	}

	// The collection's validator refuses records without a status.
	if _, err := s.invocations().InsertOne(ctx, bson.D{
		{Key: "_id", Value: "x"}, {Key: "at", Value: t0}, {Key: "expires_at", Value: t0},
		{Key: "function", Value: "app/x"}, {Key: "actor", Value: "user:u1"}, {Key: "mode", Value: "sync"},
		{Key: "duration_ms", Value: int64(1)},
	}); err == nil {
		t.Error("validator accepted a record without a status")
	}
}
