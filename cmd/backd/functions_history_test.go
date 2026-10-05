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
	if created, err := svc.EnqueueScheduledJob(ctx, auth.Job{Database: "app", Function: "nightly", TimeoutMS: 1000}, now.Truncate(time.Minute)); err != nil || !created {
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
