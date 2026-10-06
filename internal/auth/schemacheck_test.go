package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

func checkService() *auth.Users {
	return &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: registry.RealmSettings{},
	}
}

// One schema check per realm at a time: another start is refused with the
// running one's id until it is done, failed or cancelled.
func TestOnlyOneSchemaCheckAtATime(t *testing.T) {
	ctx := context.Background()
	s := checkService()
	scope := auth.CheckJob{Collections: []string{"app/notes"}, Database: "app", Collection: "notes"}

	first, err := s.StartSchemaCheck(ctx, scope, "key:ops", "req1")
	if err != nil || first.Check == nil || first.Check.Limit != auth.DefaultCheckLimit || first.Origin != auth.CheckOrigin || first.CallerActor != "key:ops" {
		t.Fatalf("first: %+v %v", first, err)
	}
	var running *auth.ErrCheckRunning
	if _, err := s.StartSchemaCheck(ctx, scope, "key:ops", "req2"); !errors.As(err, &running) || running.JobID != first.ID {
		t.Fatalf("a second start while queued: %v", err)
	}
	if claimed, found, _ := s.ClaimJob(ctx, "w1"); !found || claimed.ID != first.ID {
		t.Fatalf("claim: %+v", claimed)
	}
	if _, err := s.StartSchemaCheck(ctx, scope, "key:ops", "req3"); !errors.As(err, &running) || running.JobID != first.ID {
		t.Fatalf("a second start while running: %v", err)
	}

	// Done frees the name.
	if err := s.CompleteJob(ctx, first.ID, auth.JobResult{Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartSchemaCheck(ctx, scope, "key:ops", "req4")
	if err != nil {
		t.Fatalf("after it finished: %v", err)
	}
	// So does a cancel.
	if _, err := s.CancelJob(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	third, err := s.StartSchemaCheck(ctx, auth.CheckJob{Collections: []string{"app/notes"}, Limit: 5}, "key:ops", "req5")
	if err != nil || third.Check.Limit != 5 {
		t.Fatalf("after a cancel: %+v %v", third, err)
	}
	// A check can't be re-run (starting another is how), finished or not.
	if _, err := s.RerunJob(ctx, third.ID, 1000); !errors.Is(err, auth.ErrJobNotFunction) {
		t.Errorf("re-run of a check: %v", err)
	}
}

func TestCheckReportsKeepTheLatestPerCollection(t *testing.T) {
	ctx := context.Background()
	s := checkService()
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	report := func(coll string, invalid int64, at time.Time) auth.CheckReport {
		return auth.CheckReport{Database: "app", Collection: coll, JobID: "j", StartedAt: at, FinishedAt: at, Scanned: 10, Invalid: invalid, Complete: true, Limit: 100,
			Documents: []auth.CheckedDocument{{ID: "d1", Problems: []auth.CheckProblem{{Path: "n", Reason: "must be a number"}}}}}
	}
	for _, r := range []auth.CheckReport{report("notes", 1, t0), report("labels", 0, t0), report("notes", 2, t0.Add(time.Hour))} {
		if err := s.SaveCheckReport(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.CheckReports(ctx, false)
	if err != nil || len(all) != 2 || all[0].Collection != "labels" || all[1].Collection != "notes" || all[1].Invalid != 2 || all[1].Documents != nil {
		t.Fatalf("summaries: %+v %v", all, err)
	}
	full, err := s.CheckReport(ctx, "app", "notes")
	if err != nil || len(full.Documents) != 1 || full.Documents[0].Problems[0].Reason != "must be a number" {
		t.Fatalf("full: %+v %v", full, err)
	}
	if _, err := s.CheckReport(ctx, "app", "ghost"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("never checked: %v", err)
	}
}
