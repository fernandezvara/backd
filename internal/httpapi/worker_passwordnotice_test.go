package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
)

// passwordNoticeJobs counts the password-expired emails in the queue, by user.
func passwordNoticeJobs(t *testing.T, f *rulesFixture) map[string]int {
	t.Helper()
	jobs, _, err := f.svc.Jobs(context.Background(), auth.JobFilter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, j := range jobs {
		if j.Email != nil && j.Email.Kind == "password-expired" {
			out[j.Email.UserID]++
		}
	}
	return out
}

// runJob lets the worker run jobs until the given one has been attempted: the emails it queues
// are claimed by the same worker, and may come first.
func runJob(t *testing.T, f *rulesFixture, w *Worker, id string) auth.Job {
	t.Helper()
	ctx := context.Background()
	for range 100 {
		if j, _, _ := f.svc.GetJob(ctx, id); j.Attempts > 0 {
			return j
		}
		if !w.RunOnce(ctx) {
			break
		}
	}
	j, _, _ := f.svc.GetJob(ctx, id)
	return j
}

// fiveAccounts adds users around the fixture's: an ordinary one, a disabled one, one whose
// address is not verified, and one with no password. It returns the ordinary one's id.
func fiveAccounts(t *testing.T, f *rulesFixture) (ok string) {
	t.Helper()
	ctx := context.Background()
	pw := "dev-p4ssw0rd!"
	for _, e := range []string{"ok@example.com", "off@example.com", "unverified@example.com"} {
		if _, err := f.svc.Create(ctx, e, &pw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Create(ctx, "nopass@example.com", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetDisabled(ctx, "off@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetEmailVerified(ctx, "unverified@example.com", false); err != nil {
		t.Fatal(err)
	}
	u, _ := f.svc.Store.UserByEmail(ctx, "ok@example.com")
	return u.ID
}

func TestWorkerTellsTheUsersOfAnExpiredPassword(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	w := newTestWorker(t, f)
	okID := fiveAccounts(t, f)
	at := time.Now().Add(time.Hour)
	f.svc.Now = func() time.Time { return at }

	// Nothing to tell before an expiry, and the job refuses to start.
	if _, err := f.svc.StartPasswordNotice(ctx, "key:ops", "r"); err != auth.ErrNoPasswordEpoch {
		t.Fatalf("before an epoch: %v", err)
	}
	if _, err := f.svc.ExpireAllPasswords(ctx, "leaked backup", true); err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.StartPasswordNotice(ctx, "key:ops", "req-notify")
	if err != nil {
		t.Fatal(err)
	}
	// One run at a time.
	var running *auth.ErrPasswordNoticeRunning
	if _, err := f.svc.StartPasswordNotice(ctx, "key:ops", "r"); !errors.As(err, &running) || running.JobID != job.ID {
		t.Errorf("a second run: %v", err)
	}
	done := runJob(t, f, w, job.ID)
	if done.Status != auth.JobDone || done.Result == nil || done.Result.Status != "ok" {
		t.Fatalf("the job: %+v", done)
	}
	told := passwordNoticeJobs(t, f)
	if told[okID] != 1 {
		t.Errorf("the ordinary user: %v", told)
	}
	// Ada and Bob (the fixture's users) are told too; the disabled, the unverified and the
	// user with no password are not.
	for _, e := range []string{"off@example.com", "unverified@example.com", "nopass@example.com"} {
		u, _ := f.svc.Store.UserByEmail(ctx, e)
		if told[u.ID] != 0 {
			t.Errorf("%s was told", e)
		}
	}
	for id, n := range told {
		if n != 1 {
			t.Errorf("user %s was told %d times", id, n)
		}
	}
	// The text carries the operator's reason, and the job keeps no address.
	jobs, _, _ := f.svc.Jobs(ctx, auth.JobFilter{Limit: 1000})
	for _, j := range jobs {
		if j.Email != nil && j.Email.Kind == "password-expired" && j.Email.Event != "leaked backup" {
			t.Errorf("the reason: %q", j.Email.Event)
		}
	}
	// Steps and totals.
	if len(done.Steps) != 1 || done.Steps[0].Status != auth.StepDone || !strings.Contains(done.Steps[0].Message, "told") {
		t.Errorf("steps: %+v", done.Steps)
	}
	trail, _, _ := f.svc.AuditTrail(ctx, auth.AuditFilter{Action: auth.AuditPasswordNotify, Limit: 5})
	if len(trail) != 1 || trail[0].Details["queued"] != int64(len(told)) {
		t.Errorf("audit: %+v", trail)
	}

	// A run repeated tells nobody twice.
	again, err := f.svc.StartPasswordNotice(ctx, "key:ops", "r")
	if err != nil {
		t.Fatal(err)
	}
	if d := runJob(t, f, w, again.ID); d.Status != auth.JobDone {
		t.Fatalf("the repeat: %+v", d)
	}
	for id, n := range passwordNoticeJobs(t, f) {
		if n != 1 {
			t.Errorf("after a repeat user %s was told %d times", id, n)
		}
	}
}

func TestPasswordNoticeWaitsForTheEmailLimitsAndGoesOn(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	w := newTestWorker(t, f)
	okID := fiveAccounts(t, f)
	limits := &f.svc.Settings.Email.Limits
	was := limits.PerKindPerHour
	limits.PerKindPerHour = 1
	t.Cleanup(func() { limits.PerKindPerHour = was })
	at := time.Now().Add(time.Hour)
	f.svc.Now = func() time.Time { return at }
	*f.clock = at

	// The ordinary user was sent the message a moment ago without being marked as told.
	if _, err := f.svc.QueueEmail(ctx, auth.EmailRequest{Kind: "password-expired", UserID: okID, Address: "ok@example.com"}); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Minute)
	if _, err := f.svc.ExpireAllPasswords(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.StartPasswordNotice(ctx, "key:ops", "r")
	if err != nil {
		t.Fatal(err)
	}
	held := runJob(t, f, w, job.ID)
	if held.Status != auth.JobQueued || held.Failures != 1 || held.PasswordNotice == nil || held.PasswordNotice.Queued == 0 {
		t.Fatalf("held back, the job waits: %+v", held)
	}
	if got := passwordNoticeJobs(t, f)[okID]; got != 1 {
		t.Errorf("the held user has %d messages", got)
	}

	// An hour later the limit has passed: the same job goes on, the others are not told again.
	at = at.Add(2 * time.Hour)
	*f.clock = at
	for range 100 {
		if j, _, _ := f.svc.GetJob(ctx, job.ID); j.Status == auth.JobDone {
			break
		}
		w.RunOnce(ctx)
	}
	end, _, _ := f.svc.GetJob(ctx, job.ID)
	if end.Status != auth.JobDone || end.Result == nil || end.Result.Status != "ok" {
		t.Fatalf("after the wait: %+v", end)
	}
	for id, n := range passwordNoticeJobs(t, f) {
		want := 1
		if id == okID {
			want = 2 // the first one was queued by hand
		}
		if n != want {
			t.Errorf("user %s has %d messages, want %d", id, n, want)
		}
	}
}

func TestACancelledPasswordNoticeCanStartAgain(t *testing.T) {
	f := newRulesFixture(t)
	f.svc.Realm = "acme"
	ctx := context.Background()
	w := newTestWorker(t, f)
	fiveAccounts(t, f)
	at := time.Now().Add(time.Hour)
	f.svc.Now = func() time.Time { return at }
	if _, err := f.svc.ExpireAllPasswords(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	job, _ := f.svc.StartPasswordNotice(ctx, "key:ops", "r")
	if _, err := f.svc.CancelJob(ctx, job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(passwordNoticeJobs(t, f)) != 0 {
		t.Error("a cancelled run told users")
	}
	again, err := f.svc.StartPasswordNotice(ctx, "key:ops", "r")
	if err != nil {
		t.Fatalf("start again: %v", err)
	}
	if d := runJob(t, f, w, again.ID); d.Status != auth.JobDone || len(passwordNoticeJobs(t, f)) == 0 {
		t.Errorf("the new run: %+v", d)
	}
}
