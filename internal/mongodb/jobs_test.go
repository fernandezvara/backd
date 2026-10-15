package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestJobsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)

	j1 := auth.Job{ID: "j0", Database: "app", Function: "checkout", Input: json.RawMessage(`{"cart":["a","b"],"total":19.5}`),
		CallerActor: "user:u1", CallerUserID: "u1", TimeoutMS: 15 * 60 * 1000, RequestID: "r1",
		Status: auth.JobQueued, CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}
	j2 := j1
	j2.ID, j2.Input, j2.CreatedAt = "j1", json.RawMessage(`[1,2,3]`), t0.Add(time.Minute)
	for _, j := range []auth.Job{j1, j2} {
		if err := s.EnqueueJob(ctx, j); err != nil {
			t.Fatalf("EnqueueJob(%s): %v", j.ID, err)
		}
	}

	got, found, err := s.GetJob(ctx, "j0")
	if err != nil || !found || got.Status != auth.JobQueued || got.CallerUserID != "u1" {
		t.Fatalf("GetJob = %+v, %v, %v", got, found, err)
	}
	if string(got.Input) != `{"cart":["a","b"],"total":19.5}` {
		t.Errorf("input round-trip = %s", got.Input)
	}

	// ClaimJob picks the oldest job first, and leases it strictly beyond
	// its own timeout.
	claimAt := t0.Add(2 * time.Minute)
	claimed, found, err := s.ClaimJob(ctx, "worker-a", claimAt, 30*time.Second)
	if err != nil || !found || claimed.ID != "j0" || claimed.Status != auth.JobRunning || claimed.Attempts != 1 {
		t.Fatalf("first claim = %+v, %v, %v", claimed, found, err)
	}
	if string(claimed.Input) != `{"cart":["a","b"],"total":19.5}` {
		t.Errorf("claimed input = %s", claimed.Input)
	}

	// The next claim, at the same instant, skips j0 (its lease is live)
	// and picks j1.
	claimed2, found, err := s.ClaimJob(ctx, "worker-b", claimAt, 30*time.Second)
	if err != nil || !found || claimed2.ID != "j1" {
		t.Fatalf("second claim = %+v, %v, %v", claimed2, found, err)
	}
	if string(claimed2.Input) != `[1,2,3]` {
		t.Errorf("second claimed input = %s", claimed2.Input)
	}

	// Nothing left to claim: both jobs are running with live leases.
	if _, found, err := s.ClaimJob(ctx, "worker-c", claimAt, 30*time.Second); err != nil || found {
		t.Errorf("claim with nothing available: found=%v err=%v", found, err)
	}

	// A dead worker's job is picked up again once its lease has expired:
	// j0's lease was claimAt + 15m + 30s; well past that, it's claimable.
	past := claimAt.Add(20 * time.Minute)
	reclaimed, found, err := s.ClaimJob(ctx, "worker-d", past, 30*time.Second)
	if err != nil || !found || reclaimed.ID != "j0" || reclaimed.Attempts != 2 {
		t.Fatalf("reclaim after lease expiry = %+v, %v, %v", reclaimed, found, err)
	}

	// Completing a job removes it from further claims and stores its result.
	result := auth.JobResult{Status: "ok", Output: json.RawMessage(`{"total":19.5}`), DurationMS: 42}
	completedAt := past.Add(time.Second)
	expiresAt := completedAt.Add(24 * time.Hour)
	if err := s.CompleteJob(ctx, "j0", result, completedAt, expiresAt); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	done, found, err := s.GetJob(ctx, "j0")
	if err != nil || !found || done.Status != auth.JobDone || done.Result == nil {
		t.Fatalf("GetJob after complete = %+v, %v, %v", done, found, err)
	}
	if done.Result.Status != "ok" || string(done.Result.Output) != `{"total":19.5}` || done.Result.DurationMS != 42 {
		t.Errorf("result = %+v", done.Result)
	}
	if !done.CompletedAt.Equal(completedAt) || !done.ExpiresAt.Equal(expiresAt) {
		t.Errorf("timestamps: completed_at=%v expires_at=%v", done.CompletedAt, done.ExpiresAt)
	}

	// j1 (still running) is independently reclaimable once its own lease
	// expires; complete it too so the next claim has nothing left at
	// all, proving specifically that a done job (j0) is never among
	// what's claimable again, not just that the queue happens to be busy.
	if err := s.CompleteJob(ctx, "j1", result, completedAt, expiresAt); err != nil {
		t.Fatalf("CompleteJob(j1): %v", err)
	}
	if _, found, err := s.ClaimJob(ctx, "worker-e", past.Add(time.Hour), 30*time.Second); err != nil || found {
		t.Errorf("a done job must never be claimed again: found=%v err=%v", found, err)
	}

	// The collection's validator refuses jobs without a status.
	if _, err := s.jobs().InsertOne(ctx, bson.D{
		{Key: "_id", Value: "x"}, {Key: "database", Value: "app"}, {Key: "function", Value: "checkout"},
		{Key: "input", Value: nil}, {Key: "caller_actor", Value: "user:u1"}, {Key: "attempts", Value: int32(0)},
		{Key: "timeout_ms", Value: int64(1000)}, {Key: "created_at", Value: t0}, {Key: "expires_at", Value: t0},
	}); err == nil {
		t.Error("validator accepted a job without a status")
	}
}

// ListJobs (roadmap F19): newest first, filtered by function, status and
// origin, paged, and (unlike GetJob) safe to show to an administrator.
func TestListJobsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 3, 0, 0, 0, time.UTC)
	mk := func(id, function string, at time.Time, scheduled bool) auth.Job {
		return auth.Job{ID: id, Database: "app", Function: function, Scheduled: scheduled, CallerActor: "user:u1", CallerUserID: "u1",
			TimeoutMS: 1000, Status: auth.JobQueued, CreatedAt: at, ExpiresAt: at.Add(48 * time.Hour)}
	}
	for _, j := range []auth.Job{
		mk("a", "export", t0, false),
		mk("b", "nightly", t0.Add(time.Hour), true),
		mk("c", "nightly", t0.Add(2*time.Hour), true),
		mk("d", "export", t0.Add(3*time.Hour), false),
	} {
		if err := s.EnqueueJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueJob(ctx, mk("a", "export", t0, false)); !errors.Is(err, auth.ErrJobExists) {
		t.Errorf("duplicate id: %v, want ErrJobExists", err)
	}
	if _, _, err := s.ClaimJob(ctx, "w", t0.Add(4*time.Hour), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, "a", auth.JobResult{Status: "ok", DurationMS: 5}, t0.Add(5*time.Hour), t0.Add(29*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ids := func(f auth.JobFilter) (out []string, more bool) {
		t.Helper()
		jobs, more, err := s.ListJobs(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			out = append(out, j.ID)
		}
		return out, more
	}
	yes, no := true, false
	for name, tc := range map[string]struct {
		f    auth.JobFilter
		want string
		more bool
	}{
		"all, newest first": {auth.JobFilter{}, "d c b a", false},
		"function":          {auth.JobFilter{Database: "app", Function: "nightly"}, "c b", false},
		"scheduled":         {auth.JobFilter{Scheduled: &yes}, "c b", false},
		"not scheduled":     {auth.JobFilter{Scheduled: &no}, "d a", false},
		"done":              {auth.JobFilter{Status: auth.JobDone}, "a", false},
		"since and until":   {auth.JobFilter{Since: t0.Add(time.Hour), Until: t0.Add(3 * time.Hour)}, "c b", false},
		"limit":             {auth.JobFilter{Limit: 2}, "d c", true},
		"skip":              {auth.JobFilter{Limit: 2, Skip: 2}, "b a", false},
		"another database":  {auth.JobFilter{Database: "other"}, "", false},
	} {
		got, more := ids(tc.f)
		if strings.Join(got, " ") != tc.want || more != tc.more {
			t.Errorf("%s: got %v more=%v, want %q more=%v", name, got, more, tc.want, tc.more)
		}
	}
	jobs, _, _ := s.ListJobs(ctx, auth.JobFilter{Status: auth.JobDone})
	if len(jobs) != 1 || jobs[0].Result == nil || jobs[0].Result.Status != "ok" || jobs[0].CompletedAt.IsZero() {
		t.Errorf("done job: %+v", jobs)
	}
}

func TestRetryJobOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := s.EnqueueJob(ctx, auth.Job{ID: "r0", Database: "app", Function: "flaky", Input: json.RawMessage(`{}`),
		TimeoutMS: 60000, Status: auth.JobQueued, CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.ClaimJob(ctx, "w", t0, 30*time.Second)
	if err != nil || !found || claimed.Attempts != 1 || claimed.Failures != 0 {
		t.Fatalf("claim: %+v %v %v", claimed, found, err)
	}

	// A failed attempt queues it again, not claimable before its wait is over.
	again := t0.Add(time.Minute)
	if err := s.RetryJob(ctx, "r0", again); err != nil {
		t.Fatal(err)
	}
	j, _, _ := s.GetJob(ctx, "r0")
	if j.Status != auth.JobQueued || j.Failures != 1 || j.Attempts != 1 || !j.NextAttemptAt.Equal(again) {
		t.Fatalf("after RetryJob: %+v", j)
	}
	if _, found, _ := s.ClaimJob(ctx, "w", again.Add(-time.Second), 30*time.Second); found {
		t.Error("a retry was claimed before its wait was over")
	}
	claimed, found, err = s.ClaimJob(ctx, "w", again, 30*time.Second)
	if err != nil || !found || claimed.Attempts != 2 || claimed.Failures != 1 || !claimed.NextAttemptAt.IsZero() {
		t.Fatalf("claim of the retry: %+v %v %v", claimed, found, err)
	}

	// A job that's done stays done.
	if err := s.CompleteJob(ctx, "r0", auth.JobResult{Status: "ok"}, again, again.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryJob(ctx, "r0", again.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := s.GetJob(ctx, "r0"); j.Status != auth.JobDone || j.Failures != 1 {
		t.Errorf("a done job was retried: %+v", j)
	}
}

func TestJobStatsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)
	mk := func(id string, at time.Time, edit func(*auth.Job)) {
		j := auth.Job{ID: id, Database: "app", Function: "f", CallerActor: "anonymous", TimeoutMS: 60000, Status: auth.JobQueued, CreatedAt: at, ExpiresAt: t0.Add(48 * time.Hour)}
		if edit != nil {
			edit(&j)
		}
		if err := s.EnqueueJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	mk("f1", t0, nil)
	mk("f2", t0.Add(time.Minute), nil)
	mk("s1", t0, func(j *auth.Job) { j.Scheduled = true })
	mk("e1", t0, func(j *auth.Job) { j.Email = &auth.EmailJob{Kind: "verify-email", UserID: "u"} })
	mk("x1", t0, func(j *auth.Job) { j.Erase = &auth.EraseJob{UserID: "u"} })
	mk("x2", t0, func(j *auth.Job) { j.Erase = &auth.EraseJob{UserID: "v"} })

	// f1 is claimed (running); e1 failed once and waits for its retry.
	if c, found, err := s.ClaimJob(ctx, "w", t0.Add(2*time.Minute), 30*time.Second); err != nil || !found {
		t.Fatalf("claim: %v %v", found, err)
	} else if c.ID != "f1" && c.ID != "s1" && c.ID != "e1" && c.ID != "x1" && c.ID != "x2" {
		t.Fatalf("claimed %s", c.ID)
	}
	now := t0.Add(3 * time.Minute)
	stats, err := s.JobStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	total := map[string]int64{}
	for _, st := range stats {
		total["queued"] += st.Queued
		total["running"] += st.Running
		total["waiting"] += st.Waiting
		if st.Queued > 0 && st.OldestQueued.IsZero() {
			t.Errorf("%s: queued without an oldest", st.Kind)
		}
	}
	if total["queued"] != 5 || total["running"] != 1 || total["waiting"] != 0 {
		t.Errorf("totals = %v (%+v)", total, stats)
	}
	var kinds []string
	for _, st := range stats {
		kinds = append(kinds, st.Kind)
	}
	for _, want := range []string{"function", "schedule", "email", "erase"} {
		if !strings.Contains(strings.Join(kinds, ","), want) {
			t.Errorf("no %s in %v", want, kinds)
		}
	}

	// A failed erase needs attention; a finished one does not.
	if err := s.CompleteJob(ctx, "x1", auth.JobResult{Status: "error", Message: "boom"}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteJob(ctx, "x2", auth.JobResult{Status: "ok"}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.EraseNeedsAttention(ctx); err != nil || n != 1 {
		t.Errorf("needs attention = %d, %v", n, err)
	}
}

func TestEnqueueFinishedJobOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 5, 12, 0, 0, 0, time.UTC)
	err := s.EnqueueJob(ctx, auth.Job{
		ID: "skipped1", Database: "app", Function: "report", Scheduled: true, Status: auth.JobDone, TimeoutMS: 1000,
		CreatedAt: t0, CompletedAt: t0, ExpiresAt: t0.Add(24 * time.Hour),
		Result: &auth.JobResult{Status: auth.ResultSkipped, Message: "skipped: the previous run hasn't finished"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, found, _ := s.GetJob(ctx, "skipped1")
	if !found || got.Status != auth.JobDone || got.Result == nil || got.Result.Status != auth.ResultSkipped || !got.CompletedAt.Equal(t0) {
		t.Errorf("stored: %+v %v", got, found)
	}
	if _, found, _ := s.ClaimJob(ctx, "w1", t0.Add(time.Minute), 30*time.Second); found {
		t.Error("a job enqueued done was claimed")
	}
}

func TestCancelJobOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"c0", "c1", "c2"} {
		if err := s.EnqueueJob(ctx, auth.Job{ID: id, Database: "app", Function: "report", Status: auth.JobQueued, TimeoutMS: 1000, CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour), RerunOf: "orig"}); err != nil {
			t.Fatal(err)
		}
	}
	cancelled := auth.JobResult{Status: auth.ResultCancelled, Message: "cancelled by an administrator"}

	// A queued job: done, with the result, never claimable again.
	if ok, err := s.CancelJob(ctx, "c0", cancelled, nil, t0.Add(time.Minute), t0.Add(25*time.Hour)); err != nil || !ok {
		t.Fatalf("cancel queued: %v %v", ok, err)
	}
	got, _, _ := s.GetJob(ctx, "c0")
	if got.Status != auth.JobDone || got.Result == nil || got.Result.Status != auth.ResultCancelled || got.CompletedAt.IsZero() || got.RerunOf != "orig" {
		t.Errorf("after cancel: %+v", got)
	}
	// A running job loses its lease, and the worker's late result can't overwrite it.
	claimed, found, _ := s.ClaimJob(ctx, "w1", t0.Add(2*time.Minute), 30*time.Second)
	if !found || claimed.ID != "c1" {
		t.Fatalf("claim: %+v %v", claimed, found)
	}
	if ok, _ := s.CancelJob(ctx, "c1", cancelled, nil, t0.Add(3*time.Minute), t0.Add(25*time.Hour)); !ok {
		t.Fatal("cancel running")
	}
	if err := s.CompleteJob(ctx, "c1", auth.JobResult{Status: "ok"}, t0.Add(4*time.Minute), t0.Add(26*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetJob(ctx, "c1"); got.Result.Status != auth.ResultCancelled {
		t.Errorf("a late completion overwrote the cancellation: %+v", got.Result)
	}
	// Once done (or unknown), there is nothing to cancel, and nothing changes.
	if ok, err := s.CancelJob(ctx, "c1", cancelled, nil, t0, t0); err != nil || ok {
		t.Errorf("cancel a done job: %v %v", ok, err)
	}
	if ok, err := s.CancelJob(ctx, "nope", cancelled, nil, t0, t0); err != nil || ok {
		t.Errorf("cancel an unknown job: %v %v", ok, err)
	}
	// The one left is still claimable.
	if next, found, _ := s.ClaimJob(ctx, "w1", t0.Add(5*time.Minute), 30*time.Second); !found || next.ID != "c2" {
		t.Errorf("the next claim: %+v %v", next, found)
	}
}

func TestScheduleStatesOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 5, 12, 0, 0, 0, time.UTC)
	if got, err := s.ListScheduleStates(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, st := range []auth.ScheduleState{
		{Database: "app", Function: "nightly", Paused: true, ChangedAt: t0, ChangedBy: "key:ops"},
		{Database: "app", Function: "nightly", Paused: false, ChangedAt: t0.Add(time.Hour), ChangedBy: "user:u1"}, // replaces
		{Database: "app", Function: "digest", Paused: true, ChangedAt: t0},
	} {
		if err := s.SetScheduleState(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListScheduleStates(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("list: %v %v", got, err)
	}
	for _, st := range got {
		switch st.Function {
		case "nightly":
			if st.Paused || !st.ChangedAt.Equal(t0.Add(time.Hour)) || st.ChangedBy != "user:u1" || st.Database != "app" {
				t.Errorf("nightly: %+v", st)
			}
		case "digest":
			if !st.Paused {
				t.Errorf("digest: %+v", st)
			}
		}
	}
}

func TestJobStepsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 5, 12, 0, 0, 0, time.UTC)
	if err := s.EnqueueJob(ctx, auth.Job{ID: "s1", Database: "app", Function: "report", Status: auth.JobQueued, TimeoutMS: 1000, CreatedAt: t0, ExpiresAt: t0.Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	total := 10.0
	steps := []auth.Step{
		{N: 1, Name: "load", Status: auth.StepDone, StartedAt: t0, EndedAt: t0.Add(time.Second), DurationMS: 1000, Current: 10, Total: &total, Message: "ten", UpdatedAt: t0.Add(time.Second)},
		{N: 2, Name: "save", Status: auth.StepRunning, StartedAt: t0.Add(time.Second), UpdatedAt: t0.Add(2 * time.Second)},
	}
	// Not running yet: nothing is stored.
	if ok, err := s.SetJobSteps(ctx, "s1", 1, steps, 0); err != nil || ok {
		t.Fatalf("queued job: %v %v", ok, err)
	}
	if _, found, _ := s.ClaimJob(ctx, "w1", t0.Add(time.Minute), 30*time.Second); !found {
		t.Fatal("claim")
	}
	// Only the running attempt writes.
	if ok, _ := s.SetJobSteps(ctx, "s1", 2, steps, 0); ok {
		t.Error("another attempt wrote")
	}
	if ok, err := s.SetJobSteps(ctx, "s1", 1, steps, 3); err != nil || !ok {
		t.Fatalf("running attempt: %v %v", ok, err)
	}
	got, _, _ := s.GetJob(ctx, "s1")
	if len(got.Steps) != 2 || got.StepsOmitted != 3 || got.Steps[0].Total == nil || *got.Steps[0].Total != 10 || got.Steps[0].Message != "ten" || !got.Steps[0].EndedAt.Equal(t0.Add(time.Second)) || !got.Steps[1].EndedAt.IsZero() || got.Steps[1].Status != auth.StepRunning {
		t.Errorf("stored: %+v omitted %d", got.Steps, got.StepsOmitted)
	}
	// A retry is claimed again: its attempt starts with no steps.
	if err := s.RetryJob(ctx, "s1", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.ClaimJob(ctx, "w1", t0.Add(3*time.Minute), 30*time.Second); !found {
		t.Fatal("claim the retry")
	}
	if got, _, _ := s.GetJob(ctx, "s1"); len(got.Steps) != 0 || got.StepsOmitted != 0 || got.Attempts != 2 {
		t.Errorf("after the claim: %+v", got)
	}
	// A cancel stores the steps it was given.
	if ok, err := s.CancelJob(ctx, "s1", auth.JobResult{Status: auth.ResultCancelled}, steps, t0.Add(4*time.Minute), t0.Add(25*time.Hour)); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if got, _, _ := s.GetJob(ctx, "s1"); len(got.Steps) != 2 {
		t.Errorf("after the cancel: %+v", got.Steps)
	}
	// And an invocation record keeps its steps.
	rec := auth.InvocationRecord{ID: "i1", At: t0, ExpiresAt: t0.Add(time.Hour), Function: "app/report", Actor: "key:x", Mode: "async", Status: "ok", DurationMS: 5, Steps: steps, StepsOmitted: 1}
	if err := s.RecordInvocation(ctx, rec); err != nil {
		t.Fatal(err)
	}
	recs, _, err := s.ListInvocations(ctx, auth.InvocationFilter{Function: "app/report"})
	if err != nil || len(recs) != 1 || len(recs[0].Steps) != 2 || recs[0].StepsOmitted != 1 || recs[0].Steps[1].Name != "save" {
		t.Errorf("invocation: %+v %v", recs, err)
	}
}

func TestImageJobsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 10, 5, 12, 0, 0, 0, time.UTC)
	image := &auth.ImageJob{Collection: "library", Field: "picture", DocumentID: "d1", FileID: "fl_x", Version: "thumb", Params: `{"max_width":5}`}
	job := auth.Job{ID: "img1", Database: "app", Function: "library", Origin: auth.ImageOrigin, Exclusive: "image:fl_x", Image: image, Status: auth.JobQueued, TimeoutMS: 1000, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	if err := s.EnqueueJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	// One job per file at a time.
	job.ID = "img2"
	if err := s.EnqueueJob(ctx, job); !errors.Is(err, auth.ErrJobExclusive) {
		t.Errorf("a second job for the file: %v", err)
	}
	got, found, err := s.ClaimJob(ctx, "w1", t0.Add(time.Second), time.Second)
	if err != nil || !found || got.ID != "img1" || got.Image == nil || *got.Image != *image || got.Origin != auth.ImageOrigin {
		t.Fatalf("claim: %+v %v %v", got, found, err)
	}
}

func TestImageDeclarationsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	if got, err := s.ImageDeclarations(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	if err := s.SetImageDeclaration(ctx, "app/library.picture/thumb", "aa"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImageDeclaration(ctx, "app/library.picture/thumb", "bb"); err != nil { // replaced
		t.Fatal(err)
	}
	_ = s.SetImageDeclaration(ctx, "app/library.picture/big", "cc")
	got, err := s.ImageDeclarations(ctx)
	if err != nil || len(got) != 2 || got["app/library.picture/thumb"] != "bb" || got["app/library.picture/big"] != "cc" {
		t.Errorf("recorded: %v %v", got, err)
	}
}

// Jobs with ids of their own are inserted once however often they're offered, and the
// dispatcher's backlog counts only the unfinished trigger ones.
func TestEnqueueJobsDedupsAndTriggerBacklogCounts(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	now := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)
	mk := func(id, origin, status string) auth.Job {
		return auth.Job{ID: id, Database: "app", Function: "f", Input: json.RawMessage(`{}`), CallerActor: "system",
			Origin: origin, EventID: "evt-" + id, ChainRoot: "root", ChainDepth: 2, TimeoutMS: 1000,
			Status: status, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	first := []auth.Job{mk("t1", "trigger:app/orders/on_paid", auth.JobQueued), mk("t2", "trigger:app/orders/on_paid", auth.JobQueued), mk("h1", "http", auth.JobQueued)}
	n, err := s.EnqueueJobs(ctx, first)
	if err != nil || n != 3 {
		t.Fatalf("first: %d %v", n, err)
	}
	// A replay with one new job: the two known are skipped, the batch is not an error.
	n, err = s.EnqueueJobs(ctx, []auth.Job{mk("t1", "trigger:app/orders/on_paid", auth.JobQueued), mk("t3", "trigger:app/orders/on_paid", auth.JobQueued), mk("t2", "trigger:app/orders/on_paid", auth.JobQueued)})
	if err != nil || n != 1 {
		t.Fatalf("replay: %d %v", n, err)
	}
	if n, err := s.EnqueueJobs(ctx, nil); err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
	got, found, err := s.GetJob(ctx, "t1")
	if err != nil || !found || got.EventID != "evt-t1" || got.ChainRoot != "root" || got.ChainDepth != 2 || got.Origin != "trigger:app/orders/on_paid" {
		t.Fatalf("job = %+v %v %v", got, found, err)
	}
	if n, err := s.CountTriggerJobs(ctx); err != nil || n != 3 {
		t.Fatalf("backlog = %d %v, want the 3 trigger jobs (not the http one)", n, err)
	}
	// A finished one doesn't count.
	if err := s.CompleteJob(ctx, "t1", auth.JobResult{Status: "ok"}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountTriggerJobs(ctx); n != 2 {
		t.Errorf("backlog after one finished = %d, want 2", n)
	}
}

// Ordered jobs: the oldest unfinished job of a serial key is the only one a worker may take,
// whether it is queued, waiting out a retry or running; other keys and plain jobs go on.
func TestOrderedJobsRunOneAtATimeInOrder(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)
	mk := func(id, key string, order int64) auth.Job {
		return auth.Job{ID: id, Database: "app", Function: "f", Input: json.RawMessage(`{}`), CallerActor: "system", TimeoutMS: 1000,
			SerialKey: key, SerialOrder: order, Status: auth.JobQueued, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour)}
	}
	// Inserted newest first: only the order field says who is the head.
	jobs := []auth.Job{mk("a3", "doc-a", 30), mk("a2", "doc-a", 20), mk("a1", "doc-a", 10), mk("b1", "doc-b", 5), mk("p1", "", 0)}
	if n, err := s.EnqueueJobs(ctx, jobs); err != nil || n != 5 {
		t.Fatalf("enqueue: %d %v", n, err)
	}
	claim := func() string {
		t.Helper()
		j, ok, err := s.ClaimJob(ctx, "w1", t0, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return j.ID
	}
	got := map[string]bool{}
	for id := claim(); id != ""; id = claim() {
		got[id] = true
	}
	if len(got) != 3 || !got["a1"] || !got["b1"] || !got["p1"] {
		t.Fatalf("claimed %v, want the head of each key and the plain job", got)
	}
	if n, err := s.SerialWaiting(ctx); err != nil || n != 2 {
		t.Fatalf("waiting = %d %v, want a2 and a3", n, err)
	}

	// The others finish (their leases would otherwise run out during the retry wait below).
	for _, id := range []string{"b1", "p1"} {
		if err := s.CompleteJob(ctx, id, auth.JobResult{Status: "ok"}, t0, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// While a1 runs, a2 isn't available; once it is done, it is, and a3 still isn't.
	if err := s.CompleteJob(ctx, "a1", auth.JobResult{Status: "ok"}, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if id := claim(); id != "a2" {
		t.Fatalf("after a1 finished the next is %q, want a2", id)
	}
	if id := claim(); id != "" {
		t.Fatalf("a3 was claimed (%q) while a2 is unfinished", id)
	}

	// A retry waiting out its delay keeps its place: a3 stays behind it.
	if err := s.RetryJob(ctx, "a2", t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if id := claim(); id != "" {
		t.Fatalf("a3 was claimed (%q) while a2 waits to retry", id)
	}
	later := t0.Add(11 * time.Minute)
	j, ok, err := s.ClaimJob(ctx, "w1", later, time.Minute)
	if err != nil || !ok || j.ID != "a2" {
		t.Fatalf("after the delay: %+v %v %v, want a2 again", j, ok, err)
	}
	if err := s.CompleteJob(ctx, "a2", auth.JobResult{Status: "ok"}, later, later.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	j, ok, err = s.ClaimJob(ctx, "w1", later, time.Minute)
	if err != nil || !ok || j.ID != "a3" || j.SerialKey != "doc-a" || j.SerialOrder != 30 {
		t.Fatalf("finally: %+v %v %v, want a3", j, ok, err)
	}
	if n, _ := s.SerialWaiting(ctx); n != 0 {
		t.Errorf("waiting = %d, want 0", n)
	}
}

// Several workers at once never run two jobs of one key together, and each key's jobs finish in
// the order of their serial order.
func TestOrderedJobsAcrossConcurrentWorkers(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2126, 9, 29, 12, 0, 0, 0, time.UTC)
	var jobs []auth.Job
	const keys, per = 4, 12
	for k := 0; k < keys; k++ {
		for i := 0; i < per; i++ {
			jobs = append(jobs, auth.Job{
				ID: fmt.Sprintf("k%d-%02d", k, i), Database: "app", Function: "f", Input: json.RawMessage(`{}`), CallerActor: "system", TimeoutMS: 1000,
				SerialKey: fmt.Sprintf("doc-%d", k), SerialOrder: int64(i + 1), Status: auth.JobQueued, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour),
			})
		}
	}
	if n, err := s.EnqueueJobs(ctx, jobs); err != nil || n != keys*per {
		t.Fatalf("enqueue: %d %v", n, err)
	}
	var mu sync.Mutex
	running := map[string]string{} // key -> job id
	finished := map[string][]string{}
	done := 0
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				if done == keys*per {
					mu.Unlock()
					return
				}
				mu.Unlock()
				j, ok, err := s.ClaimJob(ctx, fmt.Sprintf("w%d", w), time.Now(), time.Minute)
				if err != nil {
					errs <- err
					return
				}
				if !ok {
					time.Sleep(2 * time.Millisecond)
					continue
				}
				mu.Lock()
				if other, busy := running[j.SerialKey]; busy {
					errs <- fmt.Errorf("%s started while %s of the same key was running", j.ID, other)
					mu.Unlock()
					return
				}
				running[j.SerialKey] = j.ID
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				delete(running, j.SerialKey)
				finished[j.SerialKey] = append(finished[j.SerialKey], j.ID)
				done++
				mu.Unlock()
				if err := s.CompleteJob(ctx, j.ID, auth.JobResult{Status: "ok"}, time.Now(), time.Now().Add(time.Hour)); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for k := 0; k < keys; k++ {
		ids := finished[fmt.Sprintf("doc-%d", k)]
		if len(ids) != per {
			t.Fatalf("doc-%d finished %d jobs, want %d", k, len(ids), per)
		}
		if !sort.StringsAreSorted(ids) {
			t.Errorf("doc-%d finished out of order: %v", k, ids)
		}
	}
}
