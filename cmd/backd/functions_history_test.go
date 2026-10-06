package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/registry"
)

func TestFunctionsHistoryAndLogs(t *testing.T) {
	fnDir := "acme/app/" + registry.FunctionsDir
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":              "roles:\n  ops:\n    admin: true\n",
		fnDir + "/hello/function.yaml": "invoke: \"false\"\n", // API keys bypass this
		fnDir + "/hello/index.js":      "",
	})
	writeFuncManifest(t, filepath.Join(root, fnDir), "hello")
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}

	runner := &fakeFuncRunner{handle: func(req executor.InvokeRequest) (executor.Result, error) {
		return executor.Result{
			Status:     executor.StatusOK,
			Output:     req.Envelope.Input,
			Logs:       []executor.LogLine{{Level: "log", Line: "hello from the function"}},
			DurationMS: 7,
		}, nil
	}}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		Functions:     runner,
		CallbackURL:   "http://backd-internal:8081",
		ExecutorToken: "test-executor-token-0123456789ab",
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()

	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}

	// Call the function twice so there's real history to read.
	c.expect(0, "null", "", "functions", "invoke", "--function", "acme/app/hello", "--url", srv.URL)
	c.expect(0, "null", "", "functions", "invoke", "--function", "acme/app/hello", "--url", srv.URL)

	out := c.expect(0, "STATUS", "", "functions", "history", "--function", "acme/app/hello", "--url", srv.URL)
	if strings.Count(out, "\n") != 3 { // header + 2 records
		t.Errorf("history: want 2 records, got:\n%s", out)
	}
	if !strings.Contains(out, "ok") || !strings.Contains(out, "7ms") {
		t.Errorf("history missing status/duration:\n%s", out)
	}

	out = c.expect(0, "hello from the function", "", "functions", "logs", "--function", "acme/app/hello", "--url", srv.URL)
	if strings.Count(out, "hello from the function") != 2 {
		t.Errorf("logs: want one line per call, got:\n%s", out)
	}

	// --json prints one record per line.
	out = c.expect(0, `"status":"ok"`, "", "functions", "history", "--function", "acme/app/hello", "--json", "--url", srv.URL)
	if strings.Count(out, "\n") != 2 {
		t.Errorf("history --json: want 2 lines, got:\n%s", out)
	}

	// A function with no calls has empty, not erroring, history.
	code, out, _ := c.run("", "functions", "history", "--function", "acme/app/hello2", "--url", srv.URL)
	if code != 0 || !strings.Contains(out, "STATUS") {
		t.Errorf("empty history: %d %q", code, out)
	}
}

func TestFunctionsJobs(t *testing.T) {
	root := writeConfig(t, map[string]string{"acme/realm.yaml": "roles:\n  ops:\n    admin: true\n", "acme/app/notes/schema.json": `{}`})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 3, 0, 5, 0, time.UTC)
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
		Now:      func() time.Time { return now },
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnqueueJob(ctx, auth.Job{Database: "app", Function: "export", CallerActor: "user:u1", TimeoutMS: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := svc.EnqueueScheduledJob(ctx, auth.Job{Database: "app", Function: "nightly", TimeoutMS: 1000}, now.Truncate(time.Minute), false); err != nil || !created {
		t.Fatalf("scheduled job: %v %v", created, err)
	}
	if err := svc.CompleteJob(ctx, auth.ScheduledJobID("app", "nightly", now.Truncate(time.Minute)), auth.JobResult{Status: "ok", DurationMS: 42}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		ExecutorToken: "test-executor-token-0123456789ab",
		Now:           func() time.Time { return now },
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}

	out := c.expect(0, "SCHEDULED", "", "functions", "jobs", "--realm", "acme", "--url", srv.URL)
	if strings.Count(out, "\n") != 3 || !strings.Contains(out, "cron_app_nightly_202609290300") || !strings.Contains(out, "42ms") {
		t.Errorf("all jobs:\n%s", out)
	}
	out = c.expect(0, "export", "", "functions", "jobs", "--function", "acme/app/export", "--url", srv.URL)
	if strings.Count(out, "\n") != 2 || strings.Contains(out, "nightly") || !strings.Contains(out, "queued") {
		t.Errorf("one function:\n%s", out)
	}
	out = c.expect(0, "nightly", "", "functions", "jobs", "--realm", "acme", "--scheduled", "--status", "done", "--url", srv.URL)
	if strings.Count(out, "\n") != 2 {
		t.Errorf("scheduled and done:\n%s", out)
	}
	out = c.expect(0, `"scheduled":true`, "", "functions", "jobs", "--realm", "acme", "--scheduled", "--json", "--url", srv.URL)
	if strings.Count(out, "\n") != 1 {
		t.Errorf("--json: %q", out)
	}
	c.expect(2, "not both", "", "functions", "jobs", "--realm", "acme", "--function", "acme/app/export", "--url", srv.URL)
	c.expect(2, "give --function", "", "functions", "jobs", "--url", srv.URL)
	c.expect(2, "--status", "", "functions", "jobs", "--realm", "acme", "--status", "paused", "--url", srv.URL)
}

func TestFunctionsCancelAndRerun(t *testing.T) {
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":                          "roles:\n  ops:\n    admin: true\n",
		"acme/app/notes/schema.json":               `{}`,
		"acme/app/_functions/export/function.yaml": "mode: async\n",
		"acme/app/_functions/export/index.ts":      "export default () => ({});\n",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
		Now:      func() time.Time { return now },
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.EnqueueJob(ctx, auth.Job{Database: "app", Function: "export", CallerActor: "user:u1", TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		ExecutorToken: "test-executor-token-0123456789ab",
		Now:           func() time.Time { return now },
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}
	base := []string{"--realm", "acme", "--url", srv.URL}

	c.expect(2, "--job", "", append([]string{"functions", "cancel"}, base...)...)
	c.expect(0, "cancelled "+job.ID+" (app/export)", "", append([]string{"functions", "cancel", "--job", job.ID}, base...)...)
	c.expect(1, "already finished", "", append([]string{"functions", "cancel", "--job", job.ID}, base...)...)
	c.expect(1, "not found", "", append([]string{"functions", "cancel", "--job", "nope"}, base...)...)

	out := c.expect(0, "queued as ", "", append([]string{"functions", "rerun", "--job", job.ID}, base...)...)
	if newID := strings.Fields(out)[2]; newID == job.ID || len(newID) < 10 {
		t.Errorf("the new job's id: %q", out)
	}
	out = c.expect(0, `"rerun_of":"`+job.ID+`"`, "", append([]string{"functions", "rerun", "--job", job.ID, "--json"}, base...)...)
	if !strings.Contains(out, `"status":"queued"`) {
		t.Errorf("--json: %q", out)
	}
	// A job that hasn't finished can't be re-run.
	waiting, _ := svc.EnqueueJob(ctx, auth.Job{Database: "app", Function: "export", TimeoutMS: 1000})
	c.expect(1, "hasn't finished", "", append([]string{"functions", "rerun", "--job", waiting.ID}, base...)...)
}

func TestFunctionsPauseAndResume(t *testing.T) {
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":                           "roles:\n  ops:\n    admin: true\n",
		"acme/app/notes/schema.json":                `{}`,
		"acme/app/_functions/nightly/function.yaml": "mode: async\nschedule: \"0 3 * * *\"\ntimezone: Europe/Madrid\n",
		"acme/app/_functions/nightly/index.ts":      "export default () => ({});\n",
		"acme/app/_functions/plain/function.yaml":   "mode: async\n",
		"acme/app/_functions/plain/index.ts":        "export default () => ({});\n",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	_, apiKey, err := svc.CreateAPIKey(context.Background(), "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		ExecutorToken: "test-executor-token-0123456789ab",
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}
	fn := []string{"--function", "acme/app/nightly", "--url", srv.URL}

	c.expect(0, "running", "", "functions", "schedules", "--realm", "acme", "--url", srv.URL)
	c.expect(0, `app/nightly: schedule "0 3 * * *" is paused`, "", append([]string{"functions", "pause"}, fn...)...)
	out := c.expect(0, "paused", "", "functions", "schedules", "--realm", "acme", "--url", srv.URL)
	if !strings.Contains(out, "Europe/Madrid") || strings.Contains(out, "plain") {
		t.Errorf("schedules: %q", out)
	}
	c.expect(0, `"paused":true`, "", append([]string{"functions", "pause", "--json"}, fn...)...)
	c.expect(0, "is running", "", append([]string{"functions", "resume"}, fn...)...)
	c.expect(1, "no schedule", "", "functions", "pause", "--function", "acme/app/plain", "--url", srv.URL)
	c.expect(1, "not found", "", "functions", "pause", "--function", "acme/app/ghost", "--url", srv.URL)
}

func TestFunctionsShowSteps(t *testing.T) {
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":                          "roles:\n  ops:\n    admin: true\n",
		"acme/app/notes/schema.json":               `{}`,
		"acme/app/_functions/export/function.yaml": "mode: async\n",
		"acme/app/_functions/export/index.ts":      "export default () => ({});\n",
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.EnqueueJob(ctx, auth.Job{Database: "app", Function: "export", CallerActor: "user:u1", TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	claimed, _, _ := svc.ClaimJob(ctx, "w1")
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	total := 10.0
	steps := []auth.Step{
		{N: 1, Name: "load", Status: auth.StepDone, StartedAt: now, EndedAt: now.Add(2 * time.Second), DurationMS: 2000, Current: 10, Total: &total, UpdatedAt: now},
		{N: 2, Name: "save", Status: auth.StepRunning, StartedAt: now, Current: 4, Total: &total, Message: "rows", UpdatedAt: now},
	}
	if ok, err := svc.SetJobSteps(ctx, claimed.ID, claimed.Attempts, steps, 7); err != nil || !ok {
		t.Fatalf("steps: %v %v", ok, err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		ExecutorToken: "test-executor-token-0123456789ab",
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}

	c.expect(0, "save 4/10", "", "functions", "jobs", "--realm", "acme", "--url", srv.URL)
	out := c.expect(0, job.ID, "", "functions", "jobs", "--realm", "acme", "--job", job.ID, "--url", srv.URL)
	for _, want := range []string{"load", "done", "2000ms", "10/10", "save", "running", "4/10", "rows", "7 steps left out"} {
		if !strings.Contains(out, want) {
			t.Errorf("job detail lacks %q:\n%s", want, out)
		}
	}
	c.expect(0, `"steps":[`, "", "functions", "jobs", "--realm", "acme", "--job", job.ID, "--json", "--url", srv.URL)
	c.expect(2, "--realm", "", "functions", "jobs", "--job", job.ID, "--url", srv.URL)
	c.expect(1, "not found", "", "functions", "jobs", "--realm", "acme", "--job", "nope", "--url", srv.URL)
}

// fakeCheckWorker plays the worker of a schema check for the CLI tests: it
// claims the job, reports a step and a report per collection, and completes it.
func fakeCheckWorker(t *testing.T, svc *auth.Users, reports map[string]auth.CheckReport) (stop func()) {
	t.Helper()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ctx := context.Background()
		for {
			select {
			case <-done:
				return
			default:
			}
			job, found, _ := svc.ClaimJob(ctx, "w1")
			if !found || job.Check == nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			total := 100.0
			now := time.Now().UTC()
			_, _ = svc.SetJobSteps(ctx, job.ID, job.Attempts, []auth.Step{{N: 1, Name: job.Check.Collections[0], Status: auth.StepRunning, StartedAt: now, Current: 45, Total: &total, Message: "scanned 450 of about 1000 documents", UpdatedAt: now}}, 0)
			time.Sleep(60 * time.Millisecond)
			for _, key := range job.Check.Collections {
				if r, ok := reports[key]; ok {
					r.JobID = job.ID
					_ = svc.SaveCheckReport(ctx, r)
				}
			}
			_ = svc.CompleteJob(ctx, job.ID, auth.JobResult{Status: "ok"})
		}
	}()
	return func() { close(done); <-finished }
}

func TestDataCheck(t *testing.T) {
	root := writeConfig(t, map[string]string{
		"acme/realm.yaml":             "roles:\n  ops:\n    admin: true\n",
		"acme/app/notes/schema.json":  `{"type": "object"}`,
		"acme/app/labels/schema.json": `{"type": "object"}`,
	})
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	_, apiKey, err := svc.CreateAPIKey(ctx, "cli", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Config{
		Log:           slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Registry:      reg,
		Ready:         func(context.Context) error { return nil },
		ExecutorToken: "test-executor-token-0123456789ab",
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	}))
	defer srv.Close()
	c := &cliEnv{t: t, env: map[string]string{
		"BACKD_CREDENTIALS": filepath.Join(t.TempDir(), "backd", "credentials"),
		"BACKD_API_KEY":     apiKey,
	}}
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	notes := auth.CheckReport{Database: "app", Collection: "notes", StartedAt: at, FinishedAt: at.Add(time.Minute), Scanned: 1000, Invalid: 2, Complete: true, Limit: 100, SchemaHash: "abc123def456",
		Documents: []auth.CheckedDocument{
			{ID: "n1", Problems: []auth.CheckProblem{{Path: "title", Reason: "is required"}}},
			{ID: "n7", Deleted: true, MoreProblems: 3, Problems: []auth.CheckProblem{{Path: "items.2.price", Reason: "got string, want number"}}},
		}}
	labels := auth.CheckReport{Database: "app", Collection: "labels", StartedAt: at, FinishedAt: at.Add(time.Minute), Scanned: 10, Complete: true, Limit: 100, SchemaHash: "000000000000"}
	plain := []string{"--realm", "acme", "--url", srv.URL}
	base := append(append([]string{}, plain...), "--interval", "10ms")

	// Drift in one collection: the reports are printed and the exit status is 1.
	stop := fakeCheckWorker(t, svc, map[string]auth.CheckReport{"app/notes": notes, "app/labels": labels})
	out := c.expect(1, "drift found: 2 invalid documents in 1 collections", "", append([]string{"data", "check"}, base...)...)
	for _, want := range []string{"app/notes: scanned 1000, invalid 2 (complete)", "n1", "title: is required", "n7", "yes", "items.2.price: got string, want number; and 3 more", "app/labels: scanned 10, invalid 0 (complete)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	stop()

	// A collection with nothing wrong exits 0.
	stop = fakeCheckWorker(t, svc, map[string]auth.CheckReport{"app/labels": labels})
	c.expect(0, "app/labels: scanned 10, invalid 0 (complete)", "", append([]string{"data", "check", "--database", "app", "--collection", "labels"}, base...)...)
	stop()

	// --json: one object per collection.
	stop = fakeCheckWorker(t, svc, map[string]auth.CheckReport{"app/notes": notes})
	c.expect(1, `"invalid":2`, "", append([]string{"data", "check", "--database", "app", "--collection", "notes", "--json"}, base...)...)
	stop()

	// --no-wait starts it and prints the job id; another start is refused with it.
	id := strings.TrimSpace(c.expect(0, "", "", append([]string{"data", "check", "--no-wait"}, base...)...))
	if len(id) < 10 {
		t.Fatalf("the job id: %q", id)
	}
	c.expect(1, "already running in this realm (job "+id+")", "", append([]string{"data", "check"}, base...)...)
	c.expect(0, "a schema check is queued (job "+id+")", "", append([]string{"data", "checks"}, plain...)...) // stderr is mixed in by the helper
	if _, err := svc.CancelJob(ctx, id); err != nil {
		t.Fatal(err)
	}

	// The latest reports.
	table := c.expect(0, "app/notes", "", append([]string{"data", "checks"}, plain...)...)
	if !strings.Contains(table, "never checked") && !strings.Contains(table, "1000") {
		t.Errorf("checks:\n%s", table)
	}
	c.expect(0, "n7", "", append([]string{"data", "checks", "--database", "app", "--collection", "notes"}, plain...)...)
	c.expect(1, "no schema check report yet", "", append([]string{"data", "checks", "--database", "app", "--collection", "ghost"}, plain...)...)
	c.expect(2, "--collection needs --database", "", append([]string{"data", "check", "--collection", "notes"}, base...)...)
}
