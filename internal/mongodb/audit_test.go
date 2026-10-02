package mongodb

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestAuditOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 27, 12, 0, 0, 0, time.UTC)
	for i, r := range []auth.AuditRecord{
		{Action: auth.AuditUserCreate, Actor: "key:ops", Target: "user:u1", Details: map[string]any{"password": true, "roles": []string{"ops"}}, RequestID: "r1", ClientIP: "192.0.2.1"},
		{Action: auth.AuditRoleAdd, Actor: "user:u0", Target: "user:u1", Details: map[string]any{"role": "editor"}},
		{Action: auth.AuditAdminRefused, Actor: auth.ActorAnonymous, Details: map[string]any{"reason": "outside"}},
	} {
		r.ID, r.At = "a"+string(rune('0'+i)), t0.Add(time.Duration(i)*time.Minute)
		r.ExpiresAt = r.At.Add(24 * time.Hour)
		if err := s.AppendAudit(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	recs, more, err := s.ListAudit(ctx, auth.AuditFilter{Limit: 2})
	if err != nil || !more || len(recs) != 2 || recs[0].ID != "a2" || recs[1].ID != "a1" || recs[0].Target != "" {
		t.Fatalf("newest first: %+v %v %v", recs, more, err)
	}
	recs, more, _ = s.ListAudit(ctx, auth.AuditFilter{Limit: 2, Skip: 2})
	if more || len(recs) != 1 || recs[0].ID != "a0" {
		t.Errorf("second page: %+v %v", recs, more)
	}
	first := recs[0]
	want := map[string]any{"password": true, "roles": []any{"ops"}}
	if !reflect.DeepEqual(first.Details, want) || first.RequestID != "r1" || first.ClientIP != "192.0.2.1" || !first.At.Equal(t0) || !first.ExpiresAt.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("record: %+v", first)
	}
	for _, tt := range []struct {
		f    auth.AuditFilter
		want []string
	}{
		{auth.AuditFilter{Target: "user:u1"}, []string{"a1", "a0"}},
		{auth.AuditFilter{Action: auth.AuditRoleAdd}, []string{"a1"}},
		{auth.AuditFilter{Actor: "key:ops"}, []string{"a0"}},
		{auth.AuditFilter{Since: t0.Add(time.Minute)}, []string{"a2", "a1"}},
		{auth.AuditFilter{Until: t0.Add(time.Minute)}, []string{"a0"}},
	} {
		recs, _, err := s.ListAudit(ctx, tt.f)
		var ids []string
		for _, r := range recs {
			ids = append(ids, r.ID)
		}
		if err != nil || !reflect.DeepEqual(ids, tt.want) {
			t.Errorf("%+v: %v %v", tt.f, ids, err)
		}
	}

	// The collection's validator refuses records without an actor.
	if _, err := s.audit().InsertOne(ctx, bson.D{{Key: "_id", Value: "x"}, {Key: "at", Value: t0}, {Key: "expires_at", Value: t0}, {Key: "action", Value: "a"}}); err == nil {
		t.Error("validator accepted a record without an actor")
	}
}
