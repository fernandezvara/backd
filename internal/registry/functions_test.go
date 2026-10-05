package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/rules"
)

const fnPrefix = "shop/app/" + FunctionsDir + "/"

// functionTree is a config with an auth realm "shop" and database "app"
// holding the given _functions files.
func functionTree(t *testing.T, files map[string]string) string {
	t.Helper()
	all := map[string]string{
		"shop/realm.yaml":            "roles:\n  staff: {}\n",
		"shop/app/notes/schema.json": `{}`,
		fnPrefix + "deno.json":       `{}`,
		fnPrefix + "lib/money.ts":    "export const x = 1;\n",
	}
	for p, c := range files {
		all[p] = c
	}
	return writeTree(t, all)
}

func TestFunctionDefaults(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "hello/function.yaml": "# all defaults\n",
		fnPrefix + "hello/index.ts":      "export default () => 1;\n",
		fnPrefix + "job/function.yaml":   "mode: async\n",
		fnPrefix + "job/index.js":        "export default () => 1;\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	fns := reg.Realms["shop"].Databases["app"].Functions
	if fns == nil || len(fns.Functions) != 2 {
		t.Fatalf("functions: %+v (lib must not be a function)", fns)
	}
	h := fns.Functions["hello"]
	want := Function{Realm: "shop", Database: "app", Name: "hello", Dir: h.Dir, Entry: "hello/index.ts", Mode: ModeSync,
		Timeout: 10 * time.Second, Memory: 128 << 20, MaxOutput: 1 << 20, Concurrency: 10, Idempotency: "optional"}
	if h.Invoke != nil || h.Admin || h.RateLimit != nil || len(h.Secrets) != 0 || len(h.Network) != 0 || h.InputSchema != nil {
		t.Errorf("hello has non-default settings: %+v", h)
	}
	h.Invoke, h.InputSchema, h.OutputSchema = nil, nil, nil
	if !reflect.DeepEqual(*h, want) {
		t.Errorf("hello = %+v\nwant   %+v", *h, want)
	}
	if j := fns.Functions["job"]; j.Mode != ModeAsync || j.Timeout != 15*time.Minute || j.Entry != "job/index.js" {
		t.Errorf("job = %+v", j)
	}
	if reg.Realms["shop"].Databases["app"].Collections["notes"] == nil {
		t.Error("collections next to _functions must still load")
	}
}

func TestFunctionFull(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "checkout/function.yaml": `mode: sync
timeout: 30s
memory: 256MiB
max_output: 2MB
concurrency: 3
rate_limit: {per: ip, limit: 5, window: 1m}
idempotency: required
invoke: "user != nil && hasRole(user, 'staff')"
admin: true
secrets: [STRIPE_KEY, realm.SHARED_TOKEN]
network: [api.stripe.com, "hooks.example.com:8443", "203.0.113.7"]
`,
		fnPrefix + "checkout/index.js":           "export default () => 1;\n",
		fnPrefix + "checkout/input.schema.json":  `{"type": "object", "required": ["cart"]}`,
		fnPrefix + "checkout/output.schema.json": `{"type": "object"}`,
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	f := reg.Realms["shop"].Databases["app"].Functions.Functions["checkout"]
	if f.Mode != ModeSync || f.Timeout != 30*time.Second || f.Memory != 256<<20 || f.MaxOutput != 2_000_000 ||
		f.Concurrency != 3 || f.Idempotency != "required" || !f.Admin {
		t.Errorf("checkout = %+v", f)
	}
	if f.RateLimit == nil || *f.RateLimit != (RateLimit{Per: "ip", Limit: 5, Window: time.Minute}) {
		t.Errorf("rate limit = %+v", f.RateLimit)
	}
	if len(f.Secrets) != 2 || f.Secrets[0] != (SecretRef{Name: "STRIPE_KEY"}) || f.Secrets[1].String() != "realm.SHARED_TOKEN" {
		t.Errorf("secrets = %+v", f.Secrets)
	}
	if strings.Join(f.Network, ",") != "api.stripe.com,hooks.example.com:8443,203.0.113.7" {
		t.Errorf("network = %v", f.Network)
	}
	if f.InputSchema == nil || f.OutputSchema == nil || f.InputSchema.Validate(map[string]any{}) == nil {
		t.Error("input schema not compiled or not enforced")
	}
	staff := &rules.User{ID: "u1", Roles: []string{"staff"}}
	for _, tt := range []struct {
		user *rules.User
		want bool
	}{{nil, false}, {&rules.User{ID: "u2"}, false}, {staff, true}} {
		if ok, err := f.Invoke.Allow(rules.Values{User: tt.user}); ok != tt.want || err != nil {
			t.Errorf("invoke(%+v) = %v, %v", tt.user, ok, err)
		}
	}
}

// TestWebhookAllowsAnonymousInvoke proves a webhook function with an
// invoke rule that allows anonymous callers loads fine (roadmap F13):
// webhook senders never have a session or API key.
func TestWebhookAllowsAnonymousInvoke(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "hook/function.yaml": "mode: webhook\ninvoke: \"true\"\n",
		fnPrefix + "hook/index.js":      "export default () => 1;\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	f := reg.Realms["shop"].Databases["app"].Functions.Functions["hook"]
	if f.Mode != ModeWebhook || f.Invoke == nil {
		t.Fatalf("hook = %+v", f)
	}
	if ok, err := f.Invoke.Allow(rules.Values{}); !ok || err != nil {
		t.Errorf("invoke(anonymous) = %v, %v", ok, err)
	}
}

func TestFunctionErrors(t *testing.T) {
	fn := func(yaml string) map[string]string {
		return map[string]string{fnPrefix + "f/function.yaml": yaml, fnPrefix + "f/index.js": "export default () => 1;\n"}
	}
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unknown key", fn("timeout: 1s\nretries: 3\n"), "field retries not found"},
		{"retry on sync", fn("retry: {attempts: 3}\n"), "retry: only async functions can be retried"},
		{"retry attempts low", fn("mode: async\nretry: {attempts: 0}\n"), "retry.attempts: must be between 1 and 20"},
		{"retry attempts high", fn("mode: async\nretry: {attempts: 21}\n"), "retry.attempts: must be between 1 and 20"},
		{"retry backoff", fn("mode: async\nretry: {attempts: 3, backoff: soon}\n"), "retry.backoff: invalid duration"},
		{"retry backoff bounds", fn("mode: async\nretry: {attempts: 3, backoff: 500ms}\n"), "retry.backoff: must be between 1s and 24h0m0s"},
		{"retry max below backoff", fn("mode: async\nretry: {attempts: 3, backoff: 10m, max_backoff: 1m}\n"), "retry.max_backoff: must not be shorter than backoff"},
		{"retry too long", fn("mode: async\nretry: {attempts: 20, backoff: 1h, max_backoff: 6h}\n"), "more than the 24h0m0s a job may keep retrying"},
		{"retry unknown key", fn("mode: async\nretry: {attempts: 3, delay: 1m}\n"), "field delay not found"},
		{"internal with invoke", fn("internal: true\ninvoke: \"true\"\n"), "internal: a function with no HTTP route has no use for an invoke rule"},
		{"internal webhook", fn("internal: true\nmode: webhook\ninvoke: \"true\"\n"), "a webhook function is called over HTTP by its sender"},
		{"calls itself", fn("calls: [f]\n"), `calls[0]: "f" calls itself`},
		{"calls twice", fn("calls: [g, g]\n"), `calls[1]: "g" is listed twice`},
		{"calls bad name", fn("calls: [Not_A_Name]\n"), "is not a valid function name"},
		{"calls unknown", fn("calls: [ghost]\n"), `calls: "ghost" is not a function of this database`},
		{"mode", fn("mode: batch\n"), `mode: must be sync, async or webhook, got "batch"`},
		{"sync timeout cap", fn("timeout: 2m\n"), "timeout: must be between 1s and 1m0s for sync functions"},
		{"webhook timeout cap", fn("mode: webhook\ntimeout: 90s\n"), "for webhook functions"},
		{"webhook no invoke", fn("mode: webhook\n"), "mode: webhook requires an invoke rule that allows anonymous callers"},
		{"webhook invoke refuses anonymous", fn("mode: webhook\ninvoke: \"user != nil\"\n"), "mode: webhook requires an invoke rule that allows anonymous callers"},
		{"schedule needs async", fn("schedule: \"* * * * *\"\n"), "schedule: needs mode: async"},
		{"schedule syntax", fn("mode: async\nschedule: \"61 * * * *\"\n"), "schedule: minute"},
		{"schedule fields", fn("mode: async\nschedule: \"* * * *\"\n"), "schedule: want 5 fields"},
		{"overlap value", fn("mode: async\nschedule: \"* * * * *\"\noverlap: queue\n"), `overlap: must be allow or skip, got "queue"`},
		{"overlap needs schedule", fn("mode: async\noverlap: skip\n"), "overlap: only applies to a scheduled function"},
		{"async timeout cap", fn("mode: async\ntimeout: 25h\n"), "between 1s and 24h0m0s for async functions"},
		{"timeout too short", fn("timeout: 500ms\n"), "timeout: must be between 1s"},
		{"bad duration", fn("timeout: soon\n"), "timeout: invalid duration"},
		{"memory unit", fn("memory: 128\n"), `memory: invalid size "128"`},
		{"memory bounds", fn("memory: 8GiB\n"), "memory: must be between 32MiB and 4GiB"},
		{"async output cap", fn("mode: async\nmax_output: 16MiB\n"), "max_output: must be between 1KiB and 15MiB for async functions"},
		{"concurrency", fn("concurrency: 0\n"), "concurrency: must be between 1 and 1000"},
		{"rate limit", fn("rate_limit: {per: session, limit: 0, window: 2d}\n"), "rate_limit.per: must be user or ip"},
		{"rate limit limit", fn("rate_limit: {per: user, limit: 0, window: 1m}\n"), "rate_limit.limit: must be at least 1"},
		{"rate limit window", fn("rate_limit: {per: user, limit: 1, window: 2d}\n"), "rate_limit.window: must be between 1s and 24h0m0s"},
		{"idempotency", fn("idempotency: always\n"), `idempotency: must be optional or required, got "always"`},
		{"invoke document", fn("invoke: \"document.owner == user.id\"\n"), "`document` is not available in invoke rules"},
		{"invoke role", fn("invoke: \"hasRole(user, 'root')\"\n"), `role "root" is not declared in realm.yaml`},
		{"invoke guard", fn("invoke: \"user.email_verified\"\n"), "needs a guard for anonymous callers"},
		{"invoke syntax", fn("invoke: \"user !=\"\n"), "invoke:"},
		{"secret name", fn("secrets: [stripe_key]\n"), `secrets[0]: "stripe_key" must be NAME or realm.NAME`},
		{"secret twice", fn("secrets: [A, A]\n"), `secrets[1]: "A" is listed twice`},
		{"network url", fn("network: [\"https://api.stripe.com\"]\n"), `network[0]: "https://api.stripe.com"`},
		{"network wildcard", fn("network: [\"*.stripe.com\"]\n"), `network[0]: "*.stripe.com"`},
		{"network port", fn("network: [\"api.stripe.com:99999\"]\n"), "invalid port"},
		{"network twice", fn("network: [a.example, a.example]\n"), "is listed twice"},
		{"bad schema", map[string]string{fnPrefix + "f/function.yaml": "", fnPrefix + "f/index.js": "", fnPrefix + "f/input.schema.json": "{"}, "input.schema.json: invalid JSON"},
		{"no function.yaml", map[string]string{fnPrefix + "f/index.js": ""}, "function folder has no function.yaml"},
		{"no entry", map[string]string{fnPrefix + "f/function.yaml": ""}, "function folder has no index.js or index.ts"},
		{"two entries", map[string]string{fnPrefix + "f/function.yaml": "", fnPrefix + "f/index.js": "", fnPrefix + "f/index.ts": ""}, "both index.js and index.ts"},
		{"function name", map[string]string{fnPrefix + "Checkout/function.yaml": "", fnPrefix + "Checkout/index.js": ""}, `invalid function name "Checkout"`},
		{"no functions", map[string]string{}, "no functions (each function is a folder with function.yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(functionTree(t, tt.files))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want %q", err, tt.want)
			}
		})
	}
	// Realms without auth have no users: an invoke rule would do nothing.
	root := writeTree(t, map[string]string{
		"open/realm.yaml": "auth: disabled\n",
		"open/app/" + FunctionsDir + "/f/function.yaml": "invoke: \"user != nil\"\n",
		"open/app/" + FunctionsDir + "/f/index.js":      "",
	})
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "invoke: only applies when auth is enabled") {
		t.Errorf("invoke without auth: %v", err)
	}
	// Realms without auth have no system database to store secrets in.
	root = writeTree(t, map[string]string{
		"open/realm.yaml": "auth: disabled\n",
		"open/app/" + FunctionsDir + "/f/function.yaml": "secrets: [KEY]\n",
		"open/app/" + FunctionsDir + "/f/index.js":      "",
	})
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "secrets: only applies when auth is enabled") {
		t.Errorf("secrets without auth: %v", err)
	}
	// Realms without auth have no system database to store rate limit
	// counters in either.
	root = writeTree(t, map[string]string{
		"open/realm.yaml": "auth: disabled\n",
		"open/app/" + FunctionsDir + "/f/function.yaml": "rate_limit:\n  per: ip\n  limit: 10\n  window: 1m\n",
		"open/app/" + FunctionsDir + "/f/index.js":      "",
	})
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "rate_limit: only applies when auth is enabled") {
		t.Errorf("rate_limit without auth: %v", err)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1B": 1, "2KB": 2000, "128MB": 128_000_000, "1GB": 1e9, "4KiB": 4096, "1MiB": 1 << 20, "2GiB": 2 << 30} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "12", "1.5MB", "1 MB", "1mb", "-1MB", "99999999999GiB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) accepted", in)
		}
	}
}

func TestSourceHashAndBundles(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "hello/function.yaml": "",
		fnPrefix + "hello/index.ts":      "export default () => 1;\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	fns := reg.Realms["shop"].Databases["app"].Functions
	h1, err := SourceHash(fns.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if h2, _ := SourceHash(fns.Dir); h2 != h1 {
		t.Fatal("source hash not stable")
	}

	if err := reg.CheckBundles(); err == nil || !strings.Contains(err.Error(), "functions are not built: run `backd functions build` (for shop/app)") {
		t.Errorf("not built: %v", err)
	}

	// A build, as `backd functions build` writes it.
	bundle := []byte("export default () => 1;\n")
	sum := sha256.Sum256(bundle)
	writeBuild := func(m Manifest) {
		t.Helper()
		dir := filepath.Join(fns.Dir, BuildDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hello-x.js"), bundle, 0o644); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	good := Manifest{Deno: DenoVersion, Source: h1, Functions: map[string]ManifestBundle{"hello": {Bundle: "hello-x.js", SHA256: hex.EncodeToString(sum[:])}}}
	writeBuild(good)
	if err := reg.CheckBundles(); err != nil {
		t.Fatalf("fresh build refused: %v", err)
	}
	// The build directory doesn't count as a source.
	if h, _ := SourceHash(fns.Dir); h != h1 {
		t.Error(".build changed the source hash")
	}

	for _, tt := range []struct {
		name   string
		change func()
		want   string
	}{
		{"source changed", func() {
			os.WriteFile(filepath.Join(fns.Dir, "lib", "money.ts"), []byte("export const x = 2;\n"), 0o644)
		}, "bundles are stale (sources changed since the last build)"},
		{"other deno", func() { m := good; m.Deno = "2.1.4"; writeBuild(m) }, "bundles were built with Deno 2.1.4, this backd uses " + DenoVersion},
		{"bundle missing from manifest", func() { m := good; m.Functions = map[string]ManifestBundle{}; writeBuild(m) }, `function "hello" has no bundle`},
		{"bundle modified", func() {
			writeBuild(good)
			os.WriteFile(filepath.Join(fns.Dir, BuildDir, "hello-x.js"), []byte("//x"), 0o644)
		}, `bundle of "hello" was modified after the build`},
		{"bundle deleted", func() {
			writeBuild(good)
			os.Remove(filepath.Join(fns.Dir, BuildDir, "hello-x.js"))
		}, `bundle of "hello"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.change()
			if err := reg.CheckBundles(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want %q", err, tt.want)
			}
			// Restore the good state for the next case.
			os.WriteFile(filepath.Join(fns.Dir, "lib", "money.ts"), []byte("export const x = 1;\n"), 0o644)
			writeBuild(good)
		})
	}
	if got := reg.FunctionRealms(true); len(got) != 1 || got[0] != "shop" {
		t.Errorf("FunctionRealms(true) = %v", got)
	}
	if got := reg.FunctionRealms(false); len(got) != 0 {
		t.Errorf("FunctionRealms(false) = %v", got)
	}
}

func TestScheduledFunction(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "nightly/function.yaml": "mode: async\nschedule: \"0 3 * * *\"\n",
		fnPrefix + "nightly/index.js":      "export default () => 1;\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	f := reg.Realms["shop"].Databases["app"].Functions.Functions["nightly"]
	if f.Schedule == nil || f.ScheduleExpr != "0 3 * * *" || f.Overlap != OverlapAllow {
		t.Fatalf("nightly = %+v", f)
	}
}

func TestScheduledFunctionOverlap(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "sync/function.yaml": "mode: async\nschedule: \"* * * * *\"\noverlap: skip\n",
		fnPrefix + "sync/index.js":      "export default () => 1;\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Realms["shop"].Databases["app"].Functions.Functions["sync"].Overlap; got != OverlapSkip {
		t.Fatalf("overlap = %q", got)
	}
}

func TestCallGraph(t *testing.T) {
	tree := func(calls map[string]string) map[string]string {
		files := map[string]string{}
		for name, c := range calls {
			files[fnPrefix+name+"/function.yaml"] = "calls: [" + c + "]\n"
			if name == "hook" {
				files[fnPrefix+name+"/function.yaml"] = "mode: webhook\ninvoke: \"true\"\n"
			}
			files[fnPrefix+name+"/index.js"] = "export default () => 1;\n"
		}
		return files
	}
	for _, tt := range []struct {
		name  string
		calls map[string]string
		want  string // "" means valid
	}{
		{"chain of four", map[string]string{"a": "b", "b": "c", "c": "d", "d": ""}, ""},
		{"diamond", map[string]string{"a": "b, c", "b": "d", "c": "d", "d": ""}, ""},
		{"chain of five", map[string]string{"a": "b", "b": "c", "c": "d", "d": "e", "e": ""}, "a chain of 5 functions starts here, the longest allowed is 4"},
		{"calls a webhook", map[string]string{"a": "hook", "hook": ""}, `"hook" is a webhook function`},
		{"cycle of two", map[string]string{"a": "b", "b": "a"}, "cycle a -> b -> a"},
		{"cycle behind an entry", map[string]string{"a": "b", "b": "c", "c": "b"}, "cycle b -> c -> b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(functionTree(t, tree(tt.calls)))
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("error %v, want %q", err, tt.want)
			}
		})
	}
	// The error names the file.
	_, err := Load(functionTree(t, tree(map[string]string{"a": "b", "b": "a"})))
	if err == nil || !strings.Contains(err.Error(), "function.yaml: calls: cycle") {
		t.Errorf("cycle error should name the file: %v", err)
	}
}

func TestInternalFunction(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "send/function.yaml":     "internal: true\nmode: async\n",
		fnPrefix + "send/index.js":          "export default () => 1;\n",
		fnPrefix + "checkout/function.yaml": "invoke: \"user != nil\"\ncalls: [send]\n",
		fnPrefix + "checkout/index.js":      "export default () => 1;\n",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	fns := reg.Realms["shop"].Databases["app"].Functions.Functions
	if !fns["send"].Internal || fns["checkout"].Internal {
		t.Errorf("Internal: send=%v checkout=%v", fns["send"].Internal, fns["checkout"].Internal)
	}
	if !reflect.DeepEqual(fns["checkout"].Calls, []string{"send"}) {
		t.Errorf("Calls = %v", fns["checkout"].Calls)
	}
}

func TestRetryPolicy(t *testing.T) {
	root := functionTree(t, map[string]string{
		fnPrefix + "none/function.yaml":  "mode: async\n",
		fnPrefix + "none/index.js":       "",
		fnPrefix + "once/function.yaml":  "mode: async\nretry: {attempts: 1}\n",
		fnPrefix + "once/index.js":       "",
		fnPrefix + "five/function.yaml":  "mode: async\nretry: {attempts: 5}\n",
		fnPrefix + "five/index.js":       "",
		fnPrefix + "tuned/function.yaml": "mode: async\nretry: {attempts: 4, backoff: 10s, max_backoff: 25s}\n",
		fnPrefix + "tuned/index.js":      "",
	})
	reg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	fns := reg.Realms["shop"].Databases["app"].Functions.Functions
	if fns["none"].Retry != nil || fns["once"].Retry != nil {
		t.Errorf("one attempt means no retry: %+v %+v", fns["none"].Retry, fns["once"].Retry)
	}
	if r := fns["five"].Retry; r == nil || r.Attempts != 5 || r.Backoff != time.Minute || r.MaxBackoff != time.Hour {
		t.Errorf("defaults: %+v", r)
	}
	r := fns["tuned"].Retry
	var waits []time.Duration
	for i := 1; i <= 4; i++ {
		waits = append(waits, r.Wait(i))
	}
	if !reflect.DeepEqual(waits, []time.Duration{10 * time.Second, 20 * time.Second, 25 * time.Second, 25 * time.Second}) {
		t.Errorf("waits = %v", waits)
	}
}
