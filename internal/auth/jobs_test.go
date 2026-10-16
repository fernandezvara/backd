package auth_test

import (
	"context"
	"encoding/json"
	"testing"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

// Jobs that carry their own ids are queued once however many times the same work is queued.
func TestEnqueueJobsSkipsIds(t *testing.T) {
	st := authtest.NewMemStore()
	svc := &Users{Store: st}
	ctx := context.Background()
	jobs := []Job{
		{ID: "a", Database: "main", Function: "f", Input: json.RawMessage(`{}`), CallerActor: "system"},
		{ID: "b", Database: "main", Function: "f", Input: json.RawMessage(`{}`), CallerActor: "system"},
	}
	n, err := svc.EnqueueJobs(ctx, jobs)
	if err != nil || n != 2 {
		t.Fatalf("first: %d %v", n, err)
	}
	again := []Job{{ID: "b", Database: "main", Function: "f"}, {ID: "c", Database: "main", Function: "f"}}
	n, err = svc.EnqueueJobs(ctx, again)
	if err != nil || n != 1 {
		t.Fatalf("with one replay: %d %v", n, err)
	}
	got, found, err := st.GetJob(ctx, "a")
	if err != nil || !found || got.Status != JobQueued || got.ExpiresAt.IsZero() || got.CreatedAt.IsZero() {
		t.Errorf("job = %+v %v %v", got, found, err)
	}
}
