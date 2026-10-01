package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
)

func TestJobsOnMongoDB(t *testing.T) {
	s, _ := authFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

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
	t0 := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
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
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
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
