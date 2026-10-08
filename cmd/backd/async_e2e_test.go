package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/rs/xid"

	"github.com/fernandezvara/backd/internal/executor"
	"github.com/fernandezvara/backd/internal/functions"
	"github.com/fernandezvara/backd/internal/httpapi"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// End to end, on MongoDB with a real executor: an async function
// (roadmap F11) is queued, claimed and run by a real Worker (the same
// type `backd worker` and `backd serve --with-worker` drive), reaching
// data through ctx.db exactly like a sync call, and its result read back
// through GET .../_jobs/{id}.
func TestAsyncEndToEnd(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	if _, err := exec.LookPath("deno"); err != nil {
		t.Skip("deno not found; skipping (the dockerized test image has it)")
	}
	realm := "t" + xid.New().String()
	root := writeConfig(t, map[string]string{
		realm + "/realm.yaml":                           "signup: open\n",
		realm + "/app/notes/schema.json":                `{"type": "object", "properties": {"title": {"type": "string"}}, "required": ["title"]}`,
		realm + "/app/notes/collection.yaml":            rulesSection("read: user != nil && document._meta.owner == user.id\nwrite: user != nil\n"),
		realm + "/app/_functions/addnote/function.yaml": "mode: async\ninvoke: \"user != nil\"\n",
		realm + "/app/_functions/addnote/index.js": `export default async (ctx) => {
  const notes = ctx.db("app").collection("notes");
  await notes.create({ title: ctx.input.title });
  const page = await notes.list();
  return { mine: page.items.map((n) => n.title) };
};
`,
	})
	env := map[string]string{
		"CONFIG_DIR": root, "MONGO_URI": uri, "LOG_LEVEL": "error", "PASSWORD_HASH_CONCURRENCY": "1",
	}
	getenv := func(k string) string { return env[k] }
	t.Cleanup(func() {
		ctx := context.Background()
		if client, err := mongodb.Connect(ctx, uri); err == nil {
			for _, db := range []string{realm + "__app", realm + "___system"} {
				_ = client.Database(db).Drop(ctx)
			}
			_ = client.Disconnect(ctx)
		}
	})
	ctx := context.Background()

	reg0, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := functions.Build(ctx, reg0, functions.Options{}); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"provision"}, getenv, nil, io.Discard, io.Discard); code != 0 {
		t.Fatal("provision failed")
	}
	a, err := setup(ctx, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	const token = "e2e-executor-token-0123456789abcdef"
	ex, err := executor.New(executor.Config{Dir: t.TempDir(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	exSrv := httptest.NewServer(ex.Handler())
	defer exSrv.Close()
	a.cfg.ExecutorURL, a.cfg.ExecutorToken = exSrv.URL, token
	a.cfg.CallbackKey = "e2e-callback-key-0123456789abcdef012"
	internal := httptest.NewServer(a.internalHandler())
	defer internal.Close()
	a.cfg.CallbackURL = internal.URL
	api := httptest.NewServer(a.handler())
	defer api.Close()

	post := func(path, token, body string) (int, map[string]any, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("POST", api.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		var out map[string]any
		json.Unmarshal(data, &out)
		return res.StatusCode, out, res.Header
	}
	get := func(path, token string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("GET", api.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		var out map[string]any
		json.Unmarshal(data, &out)
		return res.StatusCode, out
	}
	signup := func(email string) string {
		code, out, _ := post("/v1/"+realm+"/_auth/signup", "", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"}`)
		if code != 201 {
			t.Fatalf("signup: %d %v", code, out)
		}
		return out["token"].(string)
	}
	ada := signup("ada@example.com")

	code, out, hdr := post("/v1/"+realm+"/app/_func/addnote", ada, `{"title": "hello"}`)
	if code != http.StatusAccepted || out["status"] != "queued" {
		t.Fatalf("enqueue: %d %v", code, out)
	}
	id := out["id"].(string)
	loc := hdr.Get("Location")
	if loc == "" || !strings.HasSuffix(loc, "/_jobs/"+id) {
		t.Fatalf("Location = %q, want it to end with /_jobs/%s", loc, id)
	}

	w := httpapi.NewWorker(a.handlerConfig(), "e2e-worker")
	if !w.RunOnce(ctx) {
		t.Fatal("worker found nothing to claim")
	}

	code, out = get(loc, ada)
	if code != 200 || out["status"] != "done" {
		t.Fatalf("get job: %d %v", code, out)
	}
	result := out["result"].(map[string]any)
	if result["status"] != "ok" {
		t.Fatalf("result: %v", result)
	}
	output := result["output"].(map[string]any)
	if mine, _ := output["mine"].([]any); len(mine) != 1 || mine[0] != "hello" {
		t.Errorf("output: %v", output)
	}

	// A second RunOnce has nothing left to claim.
	if w.RunOnce(ctx) {
		t.Error("worker claimed a second time with nothing queued")
	}
}
