package mongodb

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestIdempotencyOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)

	rec := auth.IdempotencyRecord{
		ID:       auth.IdempotencyID("app", "checkout", "user:u1", "order-42"),
		Function: "app/checkout", CallerActor: "user:u1", InputHash: "abc123", Mode: "sync",
		Status: auth.IdempotencyRunning, CreatedAt: t0, ExpiresAt: t0.Add(24 * time.Hour),
	}
	claimed, ok, err := s.ClaimIdempotency(ctx, rec)
	if err != nil || !ok || claimed.Status != auth.IdempotencyRunning {
		t.Fatalf("first claim: %+v %v %v", claimed, ok, err)
	}

	// A second claim of the same id, before it's completed, finds the
	// running record instead of claiming it.
	again, ok, err := s.ClaimIdempotency(ctx, rec)
	if err != nil || ok || again.Status != auth.IdempotencyRunning || again.InputHash != "abc123" {
		t.Fatalf("second claim while running: %+v %v %v", again, ok, err)
	}

	// Complete it with a sync result.
	result := &auth.JobResult{Status: "ok", Output: json.RawMessage(`{"total":19.5}`), DurationMS: 42}
	completedAt := t0.Add(time.Second)
	expiresAt := completedAt.Add(24 * time.Hour)
	if err := s.CompleteIdempotency(ctx, rec.ID, result, "", completedAt, expiresAt); err != nil {
		t.Fatalf("CompleteIdempotency: %v", err)
	}
	done, ok, err := s.ClaimIdempotency(ctx, rec)
	if err != nil || ok || done.Status != auth.IdempotencyDone {
		t.Fatalf("claim after complete: %+v %v %v", done, ok, err)
	}
	if done.Result == nil || done.Result.Status != "ok" || string(done.Result.Output) != `{"total":19.5}` || done.Result.DurationMS != 42 {
		t.Errorf("stored result: %+v", done.Result)
	}
	if !done.ExpiresAt.Equal(expiresAt) {
		t.Errorf("expires_at = %v, want %v", done.ExpiresAt, expiresAt)
	}

	// A function_error result round-trips its HTTP status too (needed
	// to replay the original response, roadmap F12).
	rec2 := rec
	rec2.ID = auth.IdempotencyID("app", "checkout", "user:u1", "order-43")
	if _, _, err := s.ClaimIdempotency(ctx, rec2); err != nil {
		t.Fatal(err)
	}
	errResult := &auth.JobResult{Status: "function_error", Code: "out_of_stock", Message: "no stock", HTTPStatus: 409}
	if err := s.CompleteIdempotency(ctx, rec2.ID, errResult, "", completedAt, expiresAt); err != nil {
		t.Fatalf("CompleteIdempotency(function_error): %v", err)
	}
	got, _, err := s.ClaimIdempotency(ctx, rec2)
	if err != nil || got.Result == nil || got.Result.HTTPStatus != 409 || got.Result.Code != "out_of_stock" {
		t.Errorf("function_error result: %+v %v", got.Result, err)
	}

	// An async claim, completed with a job id instead of a result.
	rec3 := rec
	rec3.ID = auth.IdempotencyID("app", "reconcile", "user:u1", "job-1")
	rec3.Mode = "async"
	if _, _, err := s.ClaimIdempotency(ctx, rec3); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteIdempotency(ctx, rec3.ID, nil, "d3c9ljp8hc2g00b6s1m0", completedAt, expiresAt); err != nil {
		t.Fatalf("CompleteIdempotency(async): %v", err)
	}
	gotAsync, _, err := s.ClaimIdempotency(ctx, rec3)
	if err != nil || gotAsync.JobID != "d3c9ljp8hc2g00b6s1m0" || gotAsync.Result != nil {
		t.Errorf("async completion: %+v %v", gotAsync, err)
	}

	// Releasing a claim removes it entirely: a new claim of the same id
	// succeeds again, as if it had never been made.
	if err := s.ReleaseIdempotency(ctx, rec.ID); err != nil {
		t.Fatalf("ReleaseIdempotency: %v", err)
	}
	reclaimed, ok, err := s.ClaimIdempotency(ctx, rec)
	if err != nil || !ok || reclaimed.Status != auth.IdempotencyRunning {
		t.Fatalf("claim after release: %+v %v %v", reclaimed, ok, err)
	}
}
