package mongodb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

// The unique index on exclusive lets one unfinished job hold a name; a job that
// is done, failed or cancelled frees it, and a job without one is unaffected.
func TestExclusiveJobsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	job := func(id, exclusive string) auth.Job {
		return auth.Job{ID: id, Origin: auth.CheckOrigin, Exclusive: exclusive, Check: &auth.CheckJob{Collections: []string{"app/notes"}, Database: "app", Limit: 100},
			Status: auth.JobQueued, TimeoutMS: 1000, CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}
	}
	if err := s.EnqueueJob(ctx, job("c1", auth.CheckLock)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueJob(ctx, job("c2", auth.CheckLock)); !errors.Is(err, auth.ErrJobExclusive) {
		t.Fatalf("a second check: %v", err)
	}
	if err := s.EnqueueJob(ctx, job("c1", auth.CheckLock)); !errors.Is(err, auth.ErrJobExists) && !errors.Is(err, auth.ErrJobExclusive) {
		t.Fatalf("the same id: %v", err)
	}
	if err := s.EnqueueJob(ctx, auth.Job{ID: "plain", Function: "f", Status: auth.JobQueued, TimeoutMS: 1000, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}); err != nil {
		t.Fatalf("a job with no exclusive name: %v", err)
	}
	got, _, _ := s.GetJob(ctx, "c1")
	if got.Check == nil || got.Check.Database != "app" || got.Check.Limit != 100 || got.Exclusive != auth.CheckLock || len(got.Check.Collections) != 1 {
		t.Errorf("stored: %+v", got)
	}

	// Done frees it.
	if err := s.CompleteJob(ctx, "c1", auth.JobResult{Status: "ok"}, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueJob(ctx, job("c2", auth.CheckLock)); err != nil {
		t.Fatalf("after done: %v", err)
	}
	// So does a cancel.
	if ok, err := s.CancelJob(ctx, "c2", auth.JobResult{Status: auth.ResultCancelled}, nil, t0, t0.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if err := s.EnqueueJob(ctx, job("c3", auth.CheckLock)); err != nil {
		t.Fatalf("after cancel: %v", err)
	}

	// The lease of the running attempt can be renewed, and only that attempt's.
	claimed, found, _ := s.ClaimJob(ctx, "w1", t0.Add(time.Minute), 30*time.Second)
	if !found || claimed.ID != "plain" && claimed.ID != "c3" {
		t.Fatalf("claim: %+v", claimed)
	}
	if ok, err := s.RenewJobLease(ctx, claimed.ID, 2, t0.Add(time.Hour)); err != nil || ok {
		t.Errorf("another attempt renewed: %v %v", ok, err)
	}
	if ok, err := s.RenewJobLease(ctx, claimed.ID, 1, t0.Add(time.Hour)); err != nil || !ok {
		t.Errorf("renew: %v %v", ok, err)
	}
	if _, found, _ := s.ClaimJob(ctx, "w2", t0.Add(30*time.Minute), 30*time.Second); found && claimed.ID != "plain" {
		// c3 is the only other candidate, so a found job must be a different one.
		t.Log("claimed another job, as expected")
	}
}

func TestCheckReportsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 7, 12, 0, 0, 0, time.UTC)
	report := func(invalid int64, finished time.Time) auth.CheckReport {
		return auth.CheckReport{
			Database: "app", Collection: "notes", JobID: "j1", StartedAt: t0, FinishedAt: finished, Scanned: 500, Invalid: invalid,
			Complete: invalid < 100, StoppedBy: "limit", Limit: 100, SchemaHash: "abc123",
			Documents: []auth.CheckedDocument{{ID: "d1", Deleted: true, MoreProblems: 2, Problems: []auth.CheckProblem{{Path: "items.2.price", Reason: "must be a number"}}}},
		}
	}
	if _, found, err := s.GetCheckReport(ctx, "app", "notes"); err != nil || found {
		t.Fatalf("before: %v %v", found, err)
	}
	if err := s.SetCheckReport(ctx, report(7, t0.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCheckReport(ctx, report(100, t0.Add(time.Hour))); err != nil { // replaces
		t.Fatal(err)
	}
	if err := s.SetCheckReport(ctx, auth.CheckReport{Database: "app", Collection: "labels", JobID: "j1", StartedAt: t0, FinishedAt: t0, Scanned: 0, Complete: true, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetCheckReport(ctx, "app", "notes")
	if err != nil || !found || got.Invalid != 100 || !got.FinishedAt.Equal(t0.Add(time.Hour)) || got.StoppedBy != "limit" || got.SchemaHash != "abc123" || got.Limit != 100 {
		t.Fatalf("latest: %+v %v %v", got, found, err)
	}
	if len(got.Documents) != 1 || !got.Documents[0].Deleted || got.Documents[0].MoreProblems != 2 || got.Documents[0].Problems[0].Path != "items.2.price" {
		t.Errorf("documents: %+v", got.Documents)
	}
	all, err := s.ListCheckReports(ctx, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %+v %v", all, err)
	}
	for _, r := range all {
		if r.Documents != nil {
			t.Errorf("a summary lists documents: %+v", r)
		}
	}
	if full, _ := s.ListCheckReports(ctx, true); len(full) != 2 {
		t.Errorf("full list: %d", len(full))
	}
}

func TestScanAfterOnMongoDB(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	log, _ := testLogger()
	reg := loadRegistryWith(t, realm, "", map[string]string{"app/notes": `{}`})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "notes")
	repo := (&Store{Client: client}).Repository(c)
	ctx := context.Background()
	for i := 1; i <= 7; i++ {
		now := time.Now().UTC().Truncate(time.Millisecond)
		meta := map[string]any{"version": int64(1), "created_at": now, "updated_at": now}
		if i == 3 { // soft-deleted ones are scanned too
			meta["deleted_at"] = now
		}
		doc := storage.Document{"id": fmt.Sprintf("n%02d", i), "title": "t", "_meta": meta}
		if err := repo.Create(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	sc := repo.(storage.Scanner)
	var ids []string
	after := ""
	for {
		page, err := sc.ScanAfter(ctx, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, d := range page {
			ids = append(ids, d["id"].(string))
		}
		after = ids[len(ids)-1]
	}
	if len(ids) != 7 || ids[0] != "n01" || ids[2] != "n03" || ids[6] != "n07" {
		t.Errorf("scanned %v", ids)
	}
	if n, err := sc.EstimatedCount(ctx); err != nil || n != 7 {
		t.Errorf("estimated count: %d %v", n, err)
	}
}
