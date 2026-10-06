package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/storage"
)

func TestCheckPercentReportsEveryFive(t *testing.T) {
	var got []int
	reported := 0
	for scanned := int64(1); scanned <= 1000; scanned++ {
		if pct, due := checkPercent(scanned, 1000, reported); due {
			reported = pct
			got = append(got, pct)
		}
	}
	want := []int{5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 65, 70, 75, 80, 85, 90, 95}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("every 5 up to 95: %v", got)
	}
	// A big jump (a batch of many documents) reports the percentage it reached once.
	if pct, due := checkPercent(500, 1000, 0); !due || pct != 50 {
		t.Errorf("a jump: %d %v", pct, due)
	}
	// More documents than estimated never goes past 95; an empty estimate reports nothing.
	if pct, _ := checkPercent(5000, 1000, 90); pct != 95 {
		t.Errorf("past the estimate: %d", pct)
	}
	if _, due := checkPercent(10, 0, 0); due {
		t.Error("a progress report with no estimate")
	}
}

func seedNotes(f *rulesFixture, n int, invalid map[int]string, trashed map[int]bool) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if f.store.docs == nil {
		f.store.docs = map[string]map[string]storage.Document{}
	}
	key := "acme__app.notes"
	f.store.docs[key] = map[string]storage.Document{}
	at := *f.clock
	for i := 1; i <= n; i++ {
		meta := map[string]any{"version": int64(1), "created_at": at, "updated_at": at}
		if trashed[i] {
			meta["deleted_at"] = at
		}
		doc := storage.Document{"id": fmt.Sprintf("n%05d", i), "title": "a title", "published": true, "_meta": meta}
		switch invalid[i] {
		case "type":
			doc["title"] = int64(42) // the schema says string
		case "missing":
			delete(doc, "title") // required
		case "many":
			doc["title"], doc["published"], doc["members"] = int64(1), "yes", []any{int64(1), int64(2), int64(3), int64(4)}
		}
		f.store.docs[key][doc["id"].(string)] = doc
	}
}

// A check reads every document, soft-deleted ones included, validates it as
// the API validates a write, lists the invalid ones with their paths and rules
// (never values), and keeps the report as the collection's latest.
func TestWorkerChecksACollection(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	ctx := context.Background()
	seedNotes(f, 1200, map[int]string{7: "type", 300: "missing", 301: "type", 900: "many"}, map[int]bool{300: true})

	job, err := f.svc.StartSchemaCheck(ctx, auth.CheckJob{Collections: []string{"app/notes"}, Database: "app", Collection: "notes"}, "key:ops", "req-check")
	if err != nil {
		t.Fatal(err)
	}
	if !w.RunOnce(ctx) {
		t.Fatal("the worker didn't claim the check")
	}
	done, _, _ := f.svc.GetJob(ctx, job.ID)
	if done.Status != auth.JobDone || done.Result == nil || done.Result.Status != "ok" {
		t.Fatalf("the job: %+v", done)
	}
	rep, err := f.svc.CheckReport(ctx, "app", "notes")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 1200 || rep.Invalid != 4 || !rep.Complete || rep.StoppedBy != "" || rep.JobID != job.ID || len(rep.SchemaHash) != 12 || rep.Limit != auth.DefaultCheckLimit {
		t.Fatalf("report: %+v", rep)
	}
	byID := map[string]auth.CheckedDocument{}
	for _, d := range rep.Documents {
		byID[d.ID] = d
	}
	if d := byID["n00007"]; len(d.Problems) != 1 || d.Problems[0].Path != "title" || d.Deleted || strings.Contains(d.Problems[0].Reason, "42") {
		t.Errorf("a wrong type: %+v", d)
	}
	if d := byID["n00300"]; !d.Deleted || len(d.Problems) != 1 || d.Problems[0].Path != "title" || d.Problems[0].Reason != "is required" {
		t.Errorf("a soft-deleted document: %+v", d)
	}
	// Six problems: the first five are listed, and the report says one more.
	if d := byID["n00900"]; len(d.Problems) != auth.MaxCheckProblems || d.MoreProblems != 1 {
		t.Errorf("several problems: %+v", d)
	}

	// The steps: one for the collection, total 100, closed with the whole scan at 100.
	if len(done.Steps) != 1 || done.Steps[0].Name != "app/notes" || done.Steps[0].Total == nil || *done.Steps[0].Total != 100 || done.Steps[0].Current != 100 || done.Steps[0].Status != auth.StepDone {
		t.Errorf("steps: %+v", done.Steps)
	}
	if !strings.Contains(done.Steps[0].Message, "1200 documents, 4 invalid") {
		t.Errorf("message: %q", done.Steps[0].Message)
	}
	// The lock is free again.
	if _, err := f.svc.StartSchemaCheck(ctx, auth.CheckJob{Collections: []string{"app/notes"}}, "key:ops", "r"); err != nil {
		t.Errorf("a new check after it finished: %v", err)
	}
}

func TestCheckStopsAtTheLimitAndSaysSo(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	ctx := context.Background()
	invalid := map[int]string{}
	for i := 1; i <= 50; i++ {
		invalid[i*10] = "type"
	}
	seedNotes(f, 700, invalid, nil)
	if _, err := f.svc.StartSchemaCheck(ctx, auth.CheckJob{Collections: []string{"app/notes"}, Limit: 5}, "key:ops", "r"); err != nil {
		t.Fatal(err)
	}
	w.RunOnce(ctx)
	rep, err := f.svc.CheckReport(ctx, "app", "notes")
	if err != nil || rep.Complete || rep.StoppedBy != "limit" || rep.Invalid != 5 || len(rep.Documents) != 5 || rep.Scanned >= 700 || rep.Limit != 5 {
		t.Errorf("report: %+v %v", rep, err)
	}
}

func TestCheckStopsAtTheTimeLimit(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	f.svc.Now = func() time.Time { return *f.clock }
	w := newTestWorker(t, f)
	ctx := context.Background()
	seedNotes(f, 600, map[int]string{2: "type"}, nil)
	if _, err := f.svc.StartSchemaCheck(ctx, auth.CheckJob{Collections: []string{"app/notes"}}, "key:ops", "r"); err != nil {
		t.Fatal(err)
	}
	// A clock that jumps past the limit as soon as the scan begins.
	reads := 0
	f.svc.Now = func() time.Time {
		reads++
		if reads > 6 {
			return f.clock.Add(auth.CheckMaxDuration + time.Hour)
		}
		return *f.clock
	}
	w.RunOnce(ctx)
	rep, err := f.svc.CheckReport(ctx, "app", "notes")
	if err != nil || rep.Complete || rep.StoppedBy != "time" {
		t.Errorf("report: %+v %v", rep, err)
	}
}

// Cancelling a running check stops it at the next batch and leaves the
// previous report as it was.
func TestCancelledCheckKeepsThePreviousReport(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	w := newTestWorker(t, f)
	ctx := context.Background()
	seedNotes(f, 1500, map[int]string{3: "type"}, nil)
	prev := auth.CheckReport{Database: "app", Collection: "notes", JobID: "old", StartedAt: *f.clock, FinishedAt: *f.clock, Scanned: 9, Invalid: 0, Complete: true, Limit: 100}
	if err := f.svc.SaveCheckReport(ctx, prev); err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.StartSchemaCheck(ctx, auth.CheckJob{Collections: []string{"app/notes"}}, "key:ops", "r")
	if err != nil {
		t.Fatal(err)
	}
	// The administrator cancels while the worker scans: after the first batch.
	cancelled := false
	f.store.beforeWrite = nil
	f.svc.Store = &cancelAfterScan{Store: f.svc.Store, onRenew: func() {
		if !cancelled {
			cancelled = true
			_, _ = f.svc.CancelJob(ctx, job.ID)
		}
	}}
	w.RunOnce(ctx)
	rep, _ := f.svc.CheckReport(ctx, "app", "notes")
	if rep.JobID != "old" || rep.Scanned != 9 {
		t.Errorf("a cancelled check replaced the report: %+v", rep)
	}
	got, _, _ := f.svc.GetJob(ctx, job.ID)
	if got.Result == nil || got.Result.Status != auth.ResultCancelled {
		t.Errorf("the job: %+v", got)
	}
}

// cancelAfterScan wraps a store to run a hook when the worker renews its lease.
type cancelAfterScan struct {
	auth.Store
	onRenew func()
}

func (c *cancelAfterScan) RenewJobLease(ctx context.Context, id string, attempt int, until time.Time) (bool, error) {
	c.onRenew()
	return c.Store.RenewJobLease(ctx, id, attempt, until)
}
