package main

import (
	"bytes"
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
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// End to end, on MongoDB with a real executor: functions reach the realm's
// data as their caller (ctx.db) and, when declared, as themselves
// (ctx.admin.db), through backd's internal listener.
func TestFunctionsEndToEnd(t *testing.T) {
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
		realm + "/app/notes/rules.yaml":                 "read: user != nil && document._meta.owner == user.id\nwrite: user != nil\n",
		realm + "/app/_functions/addnote/function.yaml": "invoke: \"user != nil\"\n",
		realm + "/app/_functions/addnote/index.ts": `import { trimmed } from "../lib/text.ts";
type Ctx = { input: { title: string }; db: (name: string) => any };
export default async (ctx: Ctx) => {
  const notes = ctx.db("app").collection("notes");
  await notes.create({ title: trimmed(ctx.input.title) });
  const page = await notes.list();
  return { mine: page.items.map((n: { title: string }) => n.title) };
};
`,
		realm + "/app/_functions/lib/text.ts":         "export const trimmed = (s: string): string => s.trim();\n",
		realm + "/app/_functions/stats/function.yaml": "admin: true\ninvoke: \"user != nil\"\n",
		realm + "/app/_functions/stats/index.js": `export default async (ctx) => {
  const page = await ctx.admin.db("app").collection("notes").list({ count: true });
  if (page.total === 0) throw ctx.error(404, "no_notes", "nothing yet");
  return { total: page.total, by: [...new Set(page.items.map((n) => n._meta.created_by))].sort() };
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

	// Build, provision, and wire serve, the internal listener and an executor.
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

	post := func(path, token, body string) (int, map[string]any) {
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
		return res.StatusCode, out
	}
	signup := func(email string) string {
		code, out := post("/v1/"+realm+"/_auth/signup", "", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"}`)
		if code != 201 {
			t.Fatalf("signup: %d %v", code, out)
		}
		return out["token"].(string)
	}
	ada, bob := signup("ada@example.com"), signup("bob@example.com")
	fn := "/v1/" + realm + "/app/_func/"

	if code, out := post(fn+"stats", ada, ``); code != 404 || out["error"].(map[string]any)["code"] != "no_notes" {
		t.Errorf("function error: %d %v", code, out)
	}
	// ctx.db acts as the caller: each sees only their own notes.
	if code, out := post(fn+"addnote", ada, `{"title": "  ada's note "}`); code != 200 || !equalJSON(out["mine"], []any{"ada's note"}) {
		t.Fatalf("addnote as ada: %d %v", code, out)
	}
	if code, out := post(fn+"addnote", bob, `{"title": "bob's"}`); code != 200 || !equalJSON(out["mine"], []any{"bob's"}) {
		t.Fatalf("addnote as bob: %d %v", code, out)
	}
	if code, _ := post(fn+"addnote", "", `{"title": "x"}`); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	// ctx.admin.db sees everything; the notes were created by the users.
	code, out := post(fn+"stats", bob, ``)
	if code != 200 || out["total"] != float64(2) || len(out["by"].([]any)) != 2 || !strings.HasPrefix(out["by"].([]any)[0].(string), "user:") {
		t.Errorf("stats: %d %v", code, out)
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
