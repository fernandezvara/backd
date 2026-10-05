package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	. "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
)

func TestCancelJob(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	svc := newUsers(authtest.NewMemStore(), &clock)

	queued, err := svc.EnqueueJob(ctx, Job{Database: "main", Function: "report", TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.CancelJob(ctx, queued.ID)
	if err != nil || got.Status != JobDone || got.Result == nil || got.Result.Status != ResultCancelled || got.CompletedAt.IsZero() {
		t.Fatalf("cancel a queued job: %+v %v", got, err)
	}
	// Cancelled is final: it can't be claimed, completed over or cancelled again.
	if _, found, _ := svc.ClaimJob(ctx, "w1"); found {
		t.Error("a cancelled job was claimed")
	}
	if _, err := svc.CancelJob(ctx, queued.ID); !errors.Is(err, ErrJobFinished) {
		t.Errorf("cancel twice: %v", err)
	}
	_ = svc.CompleteJob(ctx, queued.ID, JobResult{Status: "ok"})
	if after, _, _ := svc.Store.GetJob(ctx, queued.ID); after.Result.Status != ResultCancelled {
		t.Errorf("a late worker overwrote the cancellation: %+v", after.Result)
	}

	// A running job is cancelled at once too; the worker finds it done.
	running, _ := svc.EnqueueJob(ctx, Job{Database: "main", Function: "slow", TimeoutMS: 1000})
	if claimed, found, _ := svc.ClaimJob(ctx, "w1"); !found || claimed.ID != running.ID {
		t.Fatalf("claim: %+v %v", claimed, found)
	}
	if got, err := svc.CancelJob(ctx, running.ID); err != nil || got.Result.Status != ResultCancelled {
		t.Fatalf("cancel a running job: %+v %v", got, err)
	}

	// Finished jobs, unknown jobs and backd's own jobs are refused.
	done, _ := svc.EnqueueJob(ctx, Job{Database: "main", Function: "report", TimeoutMS: 1000})
	_ = svc.CompleteJob(ctx, done.ID, JobResult{Status: "ok"})
	if _, err := svc.CancelJob(ctx, done.ID); !errors.Is(err, ErrJobFinished) {
		t.Errorf("cancel a finished job: %v", err)
	}
	if _, err := svc.CancelJob(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel an unknown job: %v", err)
	}
	mail, _ := svc.EnqueueJob(ctx, Job{Database: "", Function: "", Email: &EmailJob{Kind: "welcome"}})
	if _, err := svc.CancelJob(ctx, mail.ID); !errors.Is(err, ErrJobNotFunction) {
		t.Errorf("cancel an email job: %v", err)
	}
}

func TestRerunJob(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	svc := newUsers(authtest.NewMemStore(), &clock)

	orig, _ := svc.EnqueueJob(ctx, Job{
		Database: "main", Function: "report", Input: json.RawMessage(`{"day":"2026-10-01"}`), TimeoutMS: 1000,
		CallerActor: "user:u1", CallerUserID: "u1", Origin: "http",
	})
	if _, err := svc.RerunJob(ctx, orig.ID, 5000); !errors.Is(err, ErrJobNotFinished) {
		t.Errorf("re-run a queued job: %v", err)
	}
	_ = svc.CompleteJob(ctx, orig.ID, JobResult{Status: "error", Message: "boom"})

	again, err := svc.RerunJob(ctx, orig.ID, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == orig.ID || again.Status != JobQueued || again.RerunOf != orig.ID || again.Origin != "admin" {
		t.Errorf("the new job: %+v", again)
	}
	if again.Database != "main" || again.Function != "report" || string(again.Input) != `{"day":"2026-10-01"}` || again.TimeoutMS != 5000 {
		t.Errorf("same function and input, the function's timeout now: %+v", again)
	}
	if again.CallerUserID != "u1" || again.CallerActor != "user:u1" {
		t.Errorf("the same caller: %+v", again)
	}
	// The original keeps its own result.
	if kept, _, _ := svc.Store.GetJob(ctx, orig.ID); kept.Result == nil || kept.Result.Status != "error" {
		t.Errorf("the original changed: %+v", kept)
	}

	// A cancelled job can be re-run; a scheduled run's re-run acts as the function, like the run did.
	cron, _ := svc.EnqueueJob(ctx, Job{Database: "main", Function: "nightly", Scheduled: true, TimeoutMS: 1000})
	_, _ = svc.CancelJob(ctx, cron.ID)
	if again, err := svc.RerunJob(ctx, cron.ID, 1000); err != nil || !again.ActsAsFunction || again.Scheduled {
		t.Errorf("re-run a scheduled run: %+v %v", again, err)
	}

	erase, _ := svc.EnqueueJob(ctx, Job{Erase: &EraseJob{UserID: "u1"}})
	_ = svc.CompleteJob(ctx, erase.ID, JobResult{Status: "ok"})
	if _, err := svc.RerunJob(ctx, erase.ID, 1000); !errors.Is(err, ErrJobNotFunction) {
		t.Errorf("re-run an erase: %v", err)
	}
	if _, err := svc.RerunJob(ctx, "nope", 1000); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-run an unknown job: %v", err)
	}
}
