//go:build linux

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// migrateRequest is a batch of docs through code, the bundle of a collection's steps.
func (f *fixture) migrateRequest(code string, current int, docs ...Doc) MigrateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256([]byte(code))
	hash := hex.EncodeToString(sum[:])
	f.bundles[hash] = []byte(code)
	return MigrateRequest{
		Collection: "shop/app/items",
		Bundle:     Bundle{SHA256: hash, URL: f.backd.URL + "/_internal/functions/" + hash},
		Current:    current,
		Docs:       docs,
		TimeoutMS:  5000, MemoryBytes: 64 << 20, MaxOutput: 1 << 20,
	}
}

func doc(id string, schemaVersion int, fields string) Doc {
	return Doc{ID: id, Version: 3, SchemaVersion: schemaVersion, Meta: json.RawMessage(`{"created_at":"2026-01-01T00:00:00.000Z","owner":"u1"}`), Fields: json.RawMessage(fields)}
}

const twoSteps = `
export const steps = [
  { to: 2, run: (doc, meta) => ({ ...doc, label: doc.name + ":" + meta.owner }) },
  { to: 3, run: (doc) => ({ ...doc, slug: doc.label.toLowerCase(), id: "ignored", _meta: { x: 1 } }) },
];`

func TestMigrateChain(t *testing.T) {
	f := newFixture(t)
	res := f.ex.Migrate(context.Background(), f.migrateRequest(twoSteps, 3,
		doc("a", 0, `{"name":"A"}`),              // no stamp: version 1, both steps
		doc("b", 2, `{"name":"B","label":"b!"}`), // only the second
		doc("c", 3, `{"name":"C"}`),              // already current
		doc("d", 1, `{"name":"D"}`),
	))
	if res.Status != StatusOK || len(res.Outcomes) != 4 {
		t.Fatalf("migrate: %+v", res)
	}
	want := map[string]string{
		"a": `{"name":"A","label":"A:u1","slug":"a:u1"}`,
		"b": `{"name":"B","label":"b!","slug":"b!"}`,
		"d": `{"name":"D","label":"D:u1","slug":"d:u1"}`,
	}
	for _, o := range res.Outcomes {
		switch o.ID {
		case "c":
			if !o.Skipped || o.Fields != nil {
				t.Errorf("c = %+v, want skipped", o)
			}
		default:
			if o.Error != nil || string(o.Fields) != want[o.ID] {
				t.Errorf("%s = %+v %s, want %s (id and _meta of the result are dropped)", o.ID, o.Error, o.Fields, want[o.ID])
			}
		}
	}
}

// Each guard makes the document fail, and only that one.
func TestMigrateGuards(t *testing.T) {
	f := newFixture(t)
	for name, tc := range map[string]struct {
		step string
		kind string
		msg  string
	}{
		"Date.now":        {`(d) => ({ ...d, t: Date.now() })`, "threw", "deterministic"},
		"new Date()":      {`(d) => ({ ...d, t: new Date() })`, "threw", "deterministic"},
		"Date()":          {`(d) => ({ ...d, t: Date() })`, "threw", "deterministic"},
		"performance.now": {`(d) => ({ ...d, t: performance.now() })`, "threw", "deterministic"},
		"Math.random":     {`(d) => ({ ...d, t: Math.random() })`, "threw", "deterministic"},
		"getRandomValues": {`(d) => ({ ...d, t: crypto.getRandomValues(new Uint8Array(2)) })`, "threw", "deterministic"},
		"randomUUID":      {`(d) => ({ ...d, t: crypto.randomUUID() })`, "threw", "deterministic"},
		"a promise":       {`async (d) => d`, "async", "synchronous"},
		"NaN":             {`(d) => ({ ...d, n: NaN })`, "not_json", "n is NaN"},
		"a function":      {`(d) => ({ ...d, f: () => 1 })`, "not_json", "f is a function"},
		"undefined":       {`(d) => undefined`, "not_json", "must return an object"},
		"an array":        {`(d) => [d]`, "not_json", "must return an object"},
		"a throw":         {`(d) => { throw new TypeError("nope") }`, "threw", "TypeError: nope"},
		"reading a file":  {`(d) => ({ ...d, t: Deno.readTextFileSync("/etc/hostname") })`, "threw", "read access"},
		"the environment": {`(d) => ({ ...d, t: Deno.env.get("HOME") })`, "threw", "env access"},
		"the network":     {`(d) => { Deno.listen({ port: 0 }); return d }`, "threw", "net access"},
	} {
		t.Run(name, func(t *testing.T) {
			code := "export const steps = [{ to: 2, run: " + tc.step + " }];"
			res := f.ex.Migrate(context.Background(), f.migrateRequest(code, 2, doc("x", 1, `{"name":"X"}`)))
			if res.Status != StatusOK || len(res.Outcomes) != 1 {
				t.Fatalf("batch: %+v", res)
			}
			e := res.Outcomes[0].Error
			if e == nil || e.Kind != tc.kind || e.Step != "1 → 2" || !strings.Contains(e.Message, tc.msg) {
				t.Errorf("outcome = %+v (fields %s), want %s mentioning %q at step 1 → 2", e, res.Outcomes[0].Fields, tc.kind, tc.msg)
			}
		})
	}

	// Converting a stored date still works, and gives the same answer every time.
	code := `export const steps = [{ to: 2, run: (d) => ({ ...d, year: new Date(d.at).getUTCFullYear(), ms: Date.parse(d.at), iso: new Date(d.at).toISOString() }) }];`
	res := f.ex.Migrate(context.Background(), f.migrateRequest(code, 2, doc("x", 1, `{"at":"2026-03-04T05:06:07.000Z"}`)))
	if res.Status != StatusOK || res.Outcomes[0].Error != nil || string(res.Outcomes[0].Fields) != `{"at":"2026-03-04T05:06:07.000Z","year":2026,"ms":1772600767000,"iso":"2026-03-04T05:06:07.000Z"}` {
		t.Errorf("date conversion: %+v", res)
	}
}

// A document that fails doesn't stop the batch, and a step that is missing is its own failure.
func TestMigrateFailureInTheMiddle(t *testing.T) {
	f := newFixture(t)
	code := `export const steps = [
  { to: 2, run: (d) => { if (d.bad) throw new Error("bad document"); return { ...d, ok: true }; } },
  { to: 3, run: (d) => ({ ...d, three: true }) },
];`
	res := f.ex.Migrate(context.Background(), f.migrateRequest(code, 3,
		doc("a", 1, `{"n":1}`), doc("b", 1, `{"bad":true}`), doc("c", 1, `{"n":3}`)))
	if res.Status != StatusOK || len(res.Outcomes) != 3 {
		t.Fatalf("batch: %+v", res)
	}
	if res.Outcomes[0].Error != nil || string(res.Outcomes[0].Fields) != `{"n":1,"ok":true,"three":true}` || res.Outcomes[2].Error != nil || string(res.Outcomes[2].Fields) != `{"n":3,"ok":true,"three":true}` {
		t.Errorf("the others migrated: %+v %+v", res.Outcomes[0], res.Outcomes[2])
	}
	if e := res.Outcomes[1].Error; e == nil || e.Kind != "threw" || e.Step != "1 → 2" || !strings.Contains(e.Message, "bad document") {
		t.Errorf("failed one: %+v", res.Outcomes[1])
	}

	// A bundle that stops at 2 can't take a document to 3; a document below its first step either.
	res = f.ex.Migrate(context.Background(), f.migrateRequest(`export const steps = [{ to: 3, run: (d) => d }];`, 3, doc("a", 1, `{}`), doc("b", 2, `{}`)))
	if res.Outcomes[0].Error == nil || res.Outcomes[0].Error.Kind != "no_step" || res.Outcomes[1].Error != nil {
		t.Errorf("missing step: %+v", res.Outcomes)
	}

	res = f.ex.Migrate(context.Background(), f.migrateRequest(`export const steps = "no";`, 2, doc("a", 1, `{}`)))
	if res.Status != StatusError || !strings.Contains(res.Message, "steps") {
		t.Errorf("bad bundle: %+v", res)
	}
}

func TestMigrateTimeoutAndLogs(t *testing.T) {
	f := newFixture(t)
	req := f.migrateRequest(`export const steps = [{ to: 2, run: (d) => { while (true) {} } }];`, 2, doc("a", 1, `{}`))
	req.TimeoutMS = 400
	if res := f.ex.Migrate(context.Background(), req); res.Status != StatusTimeout {
		t.Errorf("a loop: %+v", res)
	}

	req = f.migrateRequest(`export const steps = [{ to: 2, run: (d) => { console.log("owner is", d.secret); return d; } }];`, 2, doc("a", 1, `{"secret":"sk_live_x"}`))
	req.Mask = []string{"sk_live_x"}
	res := f.ex.Migrate(context.Background(), req)
	if res.Status != StatusOK || len(res.Logs) != 1 || res.Logs[0] != (LogLine{Level: "log", Line: "owner is ***"}) {
		t.Errorf("logs: %+v", res)
	}
}

// Migration batches have their own limits: apart from the functions' processes, a few at once,
// and two for a realm.
func TestMigrateLimits(t *testing.T) {
	f := newFixture(t)
	slow := `export const steps = [{ to: 2, run: (d) => { Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 1500); return d; } }];`
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := []string{}
	run := func(collection string) {
		defer wg.Done()
		req := f.migrateRequest(slow, 2, doc("a", 1, `{}`))
		req.Collection = collection
		res := f.ex.Migrate(context.Background(), req)
		mu.Lock()
		statuses = append(statuses, collection+" "+res.Status)
		mu.Unlock()
	}
	wg.Add(2)
	go run("shop/app/a")
	go run("shop/app/b")
	time.Sleep(300 * time.Millisecond) // both are running
	req := f.migrateRequest(slow, 2, doc("a", 1, `{}`))
	req.Collection = "shop/app/c"
	if res := f.ex.Migrate(context.Background(), req); res.Status != StatusBusy || !strings.Contains(res.Message, "realm") {
		t.Errorf("a third batch of the realm: %+v", res)
	}
	// Another realm is not held back by it, and neither are functions.
	req.Collection = "other/app/c"
	if res := f.ex.Migrate(context.Background(), req); res.Status != StatusOK {
		t.Errorf("another realm: %+v", res)
	}
	if res := f.invoke(f.request(`export default () => 1;`)); res.Status != StatusOK {
		t.Errorf("a function during a migration: %+v", res)
	}
	wg.Wait()
	for _, s := range statuses {
		if !strings.HasSuffix(s, " ok") {
			t.Errorf("batches ended %v", statuses)
		}
	}
}

func TestMigrateHandler(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.ex.Handler())
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: token}
	res, err := c.Migrate(context.Background(), f.migrateRequest(twoSteps, 3, doc("a", 1, `{"name":"A"}`)))
	if err != nil || res.Status != StatusOK || len(res.Outcomes) != 1 || string(res.Outcomes[0].Fields) != `{"name":"A","label":"A:u1","slug":"a:u1"}` {
		t.Errorf("client: %+v %v", res, err)
	}
	bad := &Client{URL: srv.URL, Token: "wrong"}
	if _, err := bad.Migrate(context.Background(), f.migrateRequest(twoSteps, 3)); err == nil || !strings.Contains(err.Error(), http.StatusText(http.StatusUnauthorized)) {
		t.Errorf("wrong token: %v", err)
	}
}
