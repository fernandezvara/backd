package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/email"
	"github.com/fernandezvara/backd/internal/registry"
)

const postsSchema = `{
  "type": "object",
  "properties": {
    "title":     {"type": "string"},
    "published": {"type": "boolean"},
    "status":    {"type": "string"},
    "members":   {"type": "array", "items": {"type": "string"}}
  },
  "required": ["title"]
}`

const postsRules = `
read: >
  document.published == true
  || (user != nil && (document._meta.owner == user.id || user.id in document.members))
create: user != nil && user.email_verified
update: >
  user != nil && document._meta.owner == user.id
  && !('status' in changed())
delete: hasRole(user, 'admin')
`

type rulesFixture struct {
	*fixture
	reg          *registry.Registry
	svc          *auth.Users
	log          *bytes.Buffer
	key          string // API key
	ada          string // session of a verified user
	bob          string // session of another verified user
	carl         string // session of an unverified user
	adaID, bobID string
	runner       *fakeRunner // the functions executor
	callbackKey  []byte
	internal     http.Handler // the internal listener, for functions calling back
}

func newRulesFixture(t *testing.T, opts ...func(*Config)) *rulesFixture {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"acme/realm.yaml":              "signup: open\nsessions:\n  cookie:\n    enabled: true\ncors:\n  origins: [https://app.acme.example]\nroles:\n  admin: {}\n  staff:\n    admin: true\n  support:\n    admin: [users, invitations]\n  keeper:\n    admin: [apikeys, secrets]\n  auditor:\n    admin: [audit]\n  runner:\n    admin: [functions]\n  viewer:\n    admin: read\n  lookout:\n    admin: [read, secrets]\nemail:\n  function: app/deliver\n  from: \"Acme <no-reply@acme.example>\"\n  public_url: https://api.acme.example\n  locales: [en, es]\n  allowed_redirects: [https://app.acme.example]\n  redirects:\n    verify_email: https://app.acme.example/verified\n  links:\n    change_email: https://app.acme.example/confirm?token={token}\n",
		"acme/app/posts/schema.json":   postsSchema,
		"acme/app/posts/rules.yaml":    postsRules,
		"acme/app/notes/schema.json":   postsSchema,
		"acme/app/notes/rules.yaml":    "read: user != nil\nwrite: user != nil\n",
		"acme/app/private/schema.json": postsSchema,
		// A collection that soft-deletes (see soft_delete_test.go).
		// …and one that soft-deletes without a restore rule (nobody sees its trash).
		"acme/app/archive/schema.json":     postsSchema,
		"acme/app/archive/collection.yaml": "soft_delete: true\n",
		"acme/app/archive/rules.yaml":      "read: user != nil\ncreate: user != nil\ndelete: user != nil\n",
		"acme/app/bin/schema.json":         postsSchema,
		"acme/app/bin/collection.yaml":     "soft_delete:\n  retention: 7d\n",
		"acme/app/bin/rules.yaml":          "read: user != nil\ncreate: user != nil\nupdate: user != nil && document._meta.owner == user.id\ndelete: user != nil && document._meta.owner == user.id\nrestore: user != nil && (document._meta.owner == user.id || hasRole(user, 'staff'))\npurge: hasRole(user, 'staff')\n",
		// Functions (see functions_test.go).
		fnDir + "echo/function.yaml":  "invoke: \"user != nil && user.email_verified\"\n",
		fnDir + "echo/index.js":       "",
		fnDir + "typed/function.yaml": "invoke: \"true\"\n",
		// A collection with an erase policy.
		"acme/app/orders/schema.json":     `{"type": "object", "properties": {"buyer": {"type": "string"}, "phone": {"type": "string"}, "members": {"type": "array", "items": {"type": "string"}}, "paid_by": {"type": "string"}}, "additionalProperties": false}`,
		"acme/app/orders/collection.yaml": "on_owner_delete:\n  action: anonymize\n  remove: [phone]\n  replace: {buyer: \"Erased customer\"}\n  pull: {members: email}\n  unset: {paid_by: id}\n",
		fnDir + "notify/function.yaml":    "email: true\ninvoke: \"true\"\n",
		fnDir + "notify/index.js":         "",
		// A custom kind of email, for functions with `email: true`.
		"acme/email/order-shipped/en.subject.txt": "Order {{.Data.order_no}} shipped",
		"acme/email/order-shipped/en.txt":         "Hello {{.User.Email}}, order {{.Data.order_no}} is on its way.",
		"acme/email/order-shipped/en.html":        "<p>Hello {{.User.Email}}, order {{.Data.order_no}} is on its way.</p>",
		"acme/email/order-shipped/es.subject.txt": "Pedido {{.Data.order_no}} enviado",
		"acme/email/order-shipped/es.txt":         "Hola {{.User.Email}}, el pedido {{.Data.order_no}} va de camino.",
		"acme/email/order-shipped/es.html":        "<p>Hola {{.User.Email}}, el pedido {{.Data.order_no}} va de camino.</p>",
		fnDir + "typed/index.js":                  "",
		fnDir + "typed/input.schema.json":         `{"type": "object", "required": ["n"], "properties": {"n": {"type": "integer"}}}`,
		fnDir + "typed/output.schema.json":        `{"type": "object", "required": ["n"]}`,
		fnDir + "job/function.yaml":               "mode: async\ninvoke: \"true\"\n",
		fnDir + "job/index.js":                    "",
		fnDir + "nightly/function.yaml":           "mode: async\nschedule: \"0 3 * * *\"\n",
		fnDir + "nightly/index.js":                "",
		fnDir + "hook/function.yaml":              "mode: webhook\ninvoke: \"true\"\n",
		fnDir + "hook/index.js":                   "",
		fnDir + "billed/function.yaml":            "invoke: \"true\"\nidempotency: required\n",
		fnDir + "billed/index.js":                 "",
		fnDir + "keyed/function.yaml":             "secrets: [KEY]\ninvoke: \"true\"\n",
		fnDir + "keyed/index.js":                  "",
		fnDir + "admin/function.yaml":             "admin: true\ninvoke: \"user != nil\"\n",
		fnDir + "admin/index.js":                  "",
		fnDir + "limited/function.yaml":           "concurrency: 1\ninvoke: \"true\"\n",
		fnDir + "limited/index.js":                "",
		fnDir + "capped/function.yaml":            "invoke: \"true\"\nrate_limit:\n  per: user\n  limit: 2\n  window: 1m\n",
		fnDir + "capped/index.js":                 "",
		fnDir + "cappedip/function.yaml":          "invoke: \"true\"\nrate_limit:\n  per: ip\n  limit: 2\n  window: 1m\n",
		fnDir + "cappedip/index.js":               "",
		fnDir + "deliver/function.yaml":           "internal: true\nmode: async\nretry: {attempts: 3, backoff: 1m}\n",
		fnDir + "deliver/index.js":                "",
		fnDir + "cleanup/function.yaml":           "internal: true\nmode: async\n",
		fnDir + "cleanup/index.js":                "",
		fnDir + "checkout/function.yaml":          "invoke: \"true\"\ncalls: [reserve, tally, echo, capped, admin]\n",
		fnDir + "checkout/index.js":               "",
		fnDir + "reserve/function.yaml":           "internal: true\ncalls: [leaf]\n",
		fnDir + "reserve/index.js":                "",
		fnDir + "leaf/function.yaml":              "internal: true\ncalls: [leaf2]\n",
		fnDir + "leaf/index.js":                   "",
		fnDir + "leaf2/function.yaml":             "internal: true\n",
		fnDir + "leaf2/index.js":                  "",
		fnDir + "leaf3/function.yaml":             "internal: true\n",
		fnDir + "leaf3/index.js":                  "",
		fnDir + "flaky/function.yaml":             "mode: async\ninvoke: \"true\"\nretry: {attempts: 3, backoff: 1m}\n",
		fnDir + "flaky/index.js":                  "",
		fnDir + "tally/function.yaml":             "internal: true\nmode: async\n",
		fnDir + "tally/index.js":                  "",
		// Collections with file fields (see files_test.go): library is private to its owner,
		// gallery can be read by anyone.
		"acme/app/library/schema.json":     filesSchema,
		"acme/app/library/collection.yaml": filesConfig,
		"acme/app/library/rules.yaml":      filesRules,
		"acme/app/forms/schema.json":       formsSchema,
		"acme/app/forms/collection.yaml":   formsConfig,
		"acme/app/forms/rules.yaml":        formsRules,
		"acme/app/videos/schema.json":      videosSchema,
		"acme/app/videos/collection.yaml":  videosConfig,
		"acme/app/videos/rules.yaml":       videosRules,
		"acme/app/gallery/schema.json":     filesSchema,
		"acme/app/gallery/collection.yaml": filesConfig,
		"acme/app/gallery/rules.yaml":      strings.Replace(filesRules, "read: user != nil && document._meta.owner == user.id", "read: true", 1),
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The realm keeps its files in a storage the tests point at their fake S3 server
	// (see files_test.go): the address here is never connected to.
	realmPath := filepath.Join(root, "acme", "realm.yaml")
	realmYAML, _ := os.ReadFile(realmPath)
	if err := os.WriteFile(realmPath, append(realmYAML, []byte(filesStorage)...), 0o644); err != nil {
		t.Fatal(err)
	}
	// The default hosted pages, in English and (the same text) Spanish.
	for _, kind := range email.PageKinds {
		page, _ := email.DefaultPage(kind)
		for _, loc := range []string{"en", "es"} {
			p := filepath.Join(root, "acme", "pages", kind, loc+".html")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, page, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The default email templates, in English and (the same text) Spanish.
	for _, kind := range email.SystemKinds {
		defaults, _ := email.DefaultFiles(kind)
		for name, data := range defaults {
			for _, loc := range []string{"en", "es"} {
				p := filepath.Join(root, "acme", "email", kind, loc+strings.TrimPrefix(name, "en"))
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	writeManifest(t, filepath.Join(root, fnDir))
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms["acme"].Settings,
	}
	var buf bytes.Buffer
	runner := &fakeRunner{}
	callbackKey := []byte("test-callback-key-0123456789abcdef")
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Functions = runner
		c.CallbackKey = callbackKey
		c.CallbackURL = "http://backd-internal:8081"
		c.ExecutorToken = executorToken
		c.Users = func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		}
		c.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		for _, o := range opts {
			o(c)
		}
	})
	rf := &rulesFixture{fixture: f, reg: reg, svc: svc, log: &buf, runner: runner, callbackKey: callbackKey}
	rf.internal = NewInternalHandler(Config{
		Log: slog.New(slog.NewJSONHandler(&buf, nil)), Registry: reg, Store: f.store,
		Now:           func() time.Time { return *f.clock },
		CallbackKey:   callbackKey,
		CallbackURL:   "http://backd-internal:8081",
		Functions:     runner,
		ExecutorToken: executorToken,
		Users: func(realm string) *auth.Users {
			if realm == "acme" {
				return svc
			}
			return nil
		},
	})
	ctx := context.Background()
	_, rf.key, _ = svc.CreateAPIKey(ctx, "svc", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	signup := func(email string, verified bool) (string, string) {
		p, tok, err := svc.Signup(ctx, email, "dev-p4ssw0rd!", "")
		if err != nil {
			t.Fatal(err)
		}
		if verified {
			_ = svc.SetEmailVerified(ctx, email, true)
		}
		return tok, p.User.ID
	}
	rf.ada, rf.adaID = signup("ada@example.com", true)
	rf.bob, rf.bobID = signup("bob@example.com", true)
	rf.carl, _ = signup("carl@example.com", false)
	return rf
}

// as sends a request with the given credential ("" for anonymous).
func (f *rulesFixture) as(t *testing.T, cred, method, path, body string) (int, map[string]any) {
	t.Helper()
	hdr := map[string]string{}
	if cred != "" {
		hdr = bearer(cred)
	}
	rec, out := f.doH(t, method, path, body, hdr)
	return rec.Code, out
}

func ids(out map[string]any) []string {
	var got []string
	items, _ := out["items"].([]any)
	for _, it := range items {
		got = append(got, it.(map[string]any)["title"].(string))
	}
	slices.Sort(got) // the test store ignores order_by
	return got
}

const posts = "/v1/acme/app/posts"

// seed creates posts as Ada: a published one, a draft, and a draft shared with Bob.
func (f *rulesFixture) seed(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for title, body := range map[string]string{
		"public": `{"title": "public", "published": true}`,
		"draft":  `{"title": "draft", "published": false}`,
		"shared": `{"title": "shared", "published": false, "members": ["` + f.bobID + `"]}`,
	} {
		code, doc := f.as(t, f.ada, "POST", posts, body)
		if code != http.StatusCreated {
			t.Fatalf("seed %s: %d %v", title, code, doc)
		}
		out[title] = doc["id"].(string)
	}
	return out
}

func TestRulesList(t *testing.T) {
	f := newRulesFixture(t)
	f.seed(t)
	for _, tt := range []struct {
		name, cred string
		want       []string
	}{
		{"anonymous", "", []string{"public"}},
		{"owner", f.ada, []string{"draft", "public", "shared"}},
		{"member", f.bob, []string{"public", "shared"}},
		{"other user", f.carl, []string{"public"}},
		{"API key", f.key, []string{"draft", "public", "shared"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, out := f.as(t, tt.cred, "GET", posts+"?order_by=title&count=true", "")
			if code != http.StatusOK || strings.Join(ids(out), ",") != strings.Join(tt.want, ",") || out["total"] != float64(len(tt.want)) {
				t.Errorf("%d %v", code, out)
			}
		})
	}

	// A client's $or can't widen what the read rule allows: its branches
	// together match every post, yet each caller still sees only theirs.
	widen := url.QueryEscape(`{"$or": [{"published": true}, {"published": false}, {"members": "` + f.bobID + `"}]}`)
	for _, tt := range []struct {
		name, cred string
		want       []string
	}{
		{"anonymous", "", []string{"public"}},
		{"member", f.bob, []string{"public", "shared"}},
		{"other user", f.carl, []string{"public"}},
		{"owner", f.ada, []string{"draft", "public", "shared"}},
	} {
		code, out := f.as(t, tt.cred, "GET", posts+"?count=true&where="+widen, "")
		if code != http.StatusOK || strings.Join(ids(out), ",") != strings.Join(tt.want, ",") || out["total"] != float64(len(tt.want)) {
			t.Errorf("$or as %s: %d %v", tt.name, code, out)
		}
	}
	// And it narrows like any where.
	narrow := url.QueryEscape(`{"$or": [{"title": "draft"}, {"title": "shared"}]}`)
	if code, out := f.as(t, f.ada, "GET", posts+"?where="+narrow, ""); code != http.StatusOK || strings.Join(ids(out), ",") != "draft,shared" {
		t.Errorf("$or narrowing: %d %v", code, out)
	}

	// Pagination only sees readable documents.
	code, page1 := f.as(t, f.bob, "GET", posts+"?limit=1", "")
	if code != http.StatusOK || len(ids(page1)) != 1 || page1["has_more"] != true {
		t.Errorf("page 1: %d %v", code, page1)
	}
	code, page2 := f.as(t, f.bob, "GET", posts+"?limit=1&skip=1", "")
	if code != http.StatusOK || len(ids(page2)) != 1 || page2["has_more"] != false {
		t.Errorf("page 2: %d %v", code, page2)
	}
	both := append(ids(page1), ids(page2)...)
	slices.Sort(both)
	if strings.Join(both, ",") != "public,shared" {
		t.Errorf("pages = %v", both)
	}
}

func TestRulesGet(t *testing.T) {
	f := newRulesFixture(t)
	p := f.seed(t)
	for _, tt := range []struct {
		name, cred, id string
		want           int
	}{
		{"anonymous, public", "", p["public"], http.StatusOK},
		{"anonymous, draft: hidden", "", p["draft"], http.StatusNotFound},
		{"member, shared", f.bob, p["shared"], http.StatusOK},
		{"other user, draft: hidden", f.bob, p["draft"], http.StatusNotFound},
		{"owner, draft", f.ada, p["draft"], http.StatusOK},
		{"key, draft", f.key, p["draft"], http.StatusOK},
		{"missing", f.ada, "nope", http.StatusNotFound},
	} {
		if code, out := f.as(t, tt.cred, "GET", posts+"/"+tt.id, ""); code != tt.want {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
	}
}

func TestRulesCreate(t *testing.T) {
	f := newRulesFixture(t)
	for _, tt := range []struct {
		name, cred string
		want       int
		wantErr    string
	}{
		{"anonymous", "", http.StatusUnauthorized, "unauthenticated"},
		{"unverified user", f.carl, http.StatusForbidden, "forbidden"},
		{"verified user", f.ada, http.StatusCreated, ""},
		{"API key", f.key, http.StatusCreated, ""},
	} {
		code, out := f.as(t, tt.cred, "POST", posts, `{"title": "x"}`)
		if code != tt.want || errCode(out) != tt.wantErr {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
		if code == http.StatusCreated && tt.cred == f.ada {
			if meta := out["_meta"].(map[string]any); meta["owner"] != f.adaID || meta["created_by"] != "user:"+f.adaID {
				t.Errorf("owner not set: %v", meta)
			}
		}
	}
	// Schema validation still comes first.
	if code, out := f.as(t, f.ada, "POST", posts, `{"published": true}`); code != http.StatusBadRequest {
		t.Errorf("invalid body: %d %v", code, out)
	}
}

func TestRulesUpdate(t *testing.T) {
	f := newRulesFixture(t)
	p := f.seed(t)
	for _, tt := range []struct {
		name, cred, method, id, body string
		want                         int
	}{
		{"owner edits title", f.ada, "PATCH", p["draft"], `{"title": "draft 2"}`, http.StatusOK},
		{"owner replaces", f.ada, "PUT", p["draft"], `{"title": "draft 3", "published": false}`, http.StatusOK},
		{"owner can't change status", f.ada, "PATCH", p["draft"], `{"status": "done"}`, http.StatusForbidden},
		{"member can read but not edit", f.bob, "PATCH", p["shared"], `{"title": "mine"}`, http.StatusForbidden},
		{"hidden from other user", f.bob, "PATCH", p["draft"], `{"title": "mine"}`, http.StatusNotFound},
		{"anonymous on public", "", "PATCH", p["public"], `{"title": "x"}`, http.StatusUnauthorized},
		{"anonymous on draft", "", "PUT", p["draft"], `{"title": "x"}`, http.StatusNotFound},
		{"API key", f.key, "PATCH", p["public"], `{"status": "done"}`, http.StatusOK},
	} {
		if code, out := f.as(t, tt.cred, tt.method, posts+"/"+tt.id, tt.body); code != tt.want {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
	}
	if code, out := f.as(t, f.key, "GET", posts+"/"+p["public"], ""); code != http.StatusOK || out["_meta"].(map[string]any)["updated_by"] != "key:svc" {
		t.Errorf("after key update: %d %v", code, out)
	}
}

func TestRulesDelete(t *testing.T) {
	f := newRulesFixture(t)
	p := f.seed(t)
	for _, tt := range []struct {
		name, cred, id string
		want           int
	}{
		{"owner is not admin", f.ada, p["draft"], http.StatusForbidden},
		{"anonymous, public", "", p["public"], http.StatusUnauthorized},
		{"anonymous, draft", "", p["draft"], http.StatusNotFound},
		{"API key", f.key, p["draft"], http.StatusNoContent},
		{"already gone", f.key, p["draft"], http.StatusNotFound},
	} {
		if code, out := f.as(t, tt.cred, "DELETE", posts+"/"+tt.id, ""); code != tt.want {
			t.Errorf("%s: %d %v", tt.name, code, out)
		}
	}
	// With the admin role, which takes effect on the next request.
	if err := f.svc.AddRole(context.Background(), "ada@example.com", "admin"); err != nil {
		t.Fatal(err)
	}
	if code, out := f.as(t, f.ada, "DELETE", posts+"/"+p["shared"], ""); code != http.StatusNoContent {
		t.Errorf("admin deletes: %d %v", code, out)
	}
}

func TestRulesCollectionWide(t *testing.T) {
	f := newRulesFixture(t)
	// notes: signed-in users only.
	if code, _ := f.as(t, "", "GET", "/v1/acme/app/notes", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous list notes: %d", code)
	}
	if code, _ := f.as(t, f.carl, "POST", "/v1/acme/app/notes", `{"title": "n"}`); code != http.StatusCreated {
		t.Errorf("user creates note: %d", code)
	}
	if code, _ := f.as(t, f.carl, "GET", "/v1/acme/app/notes", ""); code != http.StatusOK {
		t.Errorf("user lists notes: %d", code)
	}
	// private: no rules.yaml, API keys only.
	for _, tt := range []struct {
		cred string
		want int
	}{{"", http.StatusUnauthorized}, {f.ada, http.StatusForbidden}, {f.key, http.StatusOK}} {
		if code, _ := f.as(t, tt.cred, "GET", "/v1/acme/app/private", ""); code != tt.want {
			t.Errorf("private with %q: %d, want %d", tt.cred, code, tt.want)
		}
	}
}

func TestRulesDenialLogged(t *testing.T) {
	f := newRulesFixture(t)
	f.log.Reset()
	f.as(t, f.carl, "POST", posts, `{"title": "secret title"}`)
	log := f.log.String()
	if !strings.Contains(log, `"msg":"access denied"`) || !strings.Contains(log, `"operation":"create"`) || !strings.Contains(log, `"collection":"acme/app/posts"`) {
		t.Errorf("denial not logged at debug: %s", log)
	}
	if strings.Contains(log, "secret title") {
		t.Error("document contents logged")
	}
}

func TestOnBehalfOf(t *testing.T) {
	f := newRulesFixture(t)
	p := f.seed(t)
	as := func(cred, user string) map[string]string {
		h := map[string]string{}
		if cred != "" {
			h["Authorization"] = "Bearer " + cred
		}
		if user != "" {
			h["X-Backd-On-Behalf-Of"] = user
		}
		return h
	}

	// Bob's rules apply: he sees public and shared, not Ada's draft.
	rec, out := f.doH(t, "GET", posts, "", as(f.key, f.bobID))
	if rec.Code != http.StatusOK || strings.Join(ids(out), ",") != "public,shared" {
		t.Errorf("list as bob: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "GET", posts+"/"+p["draft"], "", as(f.key, f.bobID)); rec.Code != http.StatusNotFound {
		t.Errorf("draft as bob: %d", rec.Code)
	}
	// Denials answer 403, as for a signed-in user.
	if rec, _ := f.doH(t, "DELETE", posts+"/"+p["public"], "", as(f.key, f.bobID)); rec.Code != http.StatusForbidden {
		t.Errorf("delete as bob: %d", rec.Code)
	}

	// Ownership is Bob's; the log shows both identities.
	f.log.Reset()
	rec, out = f.doH(t, "POST", posts, `{"title": "by bob"}`, as(f.key, f.bobID))
	meta, _ := out["_meta"].(map[string]any)
	if rec.Code != http.StatusCreated || meta["owner"] != f.bobID || meta["created_by"] != "user:"+f.bobID {
		t.Errorf("create as bob: %d %v", rec.Code, out)
	}
	if !strings.Contains(f.log.String(), `"actor":"key:svc as user:`+f.bobID+`"`) {
		t.Errorf("actor not logged: %s", f.log)
	}

	// Without the header, the key keeps full access.
	if rec, out := f.doH(t, "GET", posts, "", as(f.key, "")); rec.Code != http.StatusOK || len(ids(out)) != 4 {
		t.Errorf("key alone: %d %v", rec.Code, out)
	}

	_ = f.svc.SetDisabled(context.Background(), "carl@example.com", true)
	carl, _ := f.svc.Store.UserByEmail(context.Background(), "carl@example.com")
	for _, tt := range []struct {
		name     string
		hdr      map[string]string
		want     int
		wantCode string
	}{
		{"session", as(f.ada, f.bobID), http.StatusBadRequest, "invalid_header"},
		{"anonymous", as("", f.bobID), http.StatusBadRequest, "invalid_header"},
		{"unknown user", as(f.key, "nope"), http.StatusBadRequest, "invalid_header"},
		{"disabled user", as(f.key, carl.ID), http.StatusForbidden, "forbidden"},
	} {
		rec, out := f.doH(t, "GET", posts, "", tt.hdr)
		if rec.Code != tt.want || errCode(out) != tt.wantCode {
			t.Errorf("%s: %d %v", tt.name, rec.Code, out)
		}
	}
}

// An expired session is refused, never downgraded to anonymous access,
// even where anonymous callers could read.
func TestExpiredSessionNotAnonymous(t *testing.T) {
	f := newRulesFixture(t)
	f.seed(t)
	f.svc.Now = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }
	rec, out := f.doH(t, "GET", posts, "", bearer(f.ada))
	if rec.Code != http.StatusUnauthorized || errCode(out) != "unauthenticated" {
		t.Errorf("expired session: %d %v", rec.Code, out)
	}
	if code, out := f.as(t, "", "GET", posts, ""); code != http.StatusOK || len(ids(out)) != 1 {
		t.Errorf("anonymous still reads published posts: %d %v", code, out)
	}
}

// A PATCH that changes a nested field must show up in changed(): the merge
// must not modify the stored document the rule compares against.
func TestChangedSeesNestedPatch(t *testing.T) {
	root := t.TempDir()
	for p, content := range map[string]string{
		"acme/realm.yaml": "signup: open\n",
		"acme/app/posts/schema.json": `{"type": "object", "properties": {"title": {"type": "string"},
			"author": {"type": "object", "properties": {"email": {"type": "string"}, "name": {"type": "string"}}}}}`,
		"acme/app/posts/rules.yaml": "read: \"true\"\ncreate: \"true\"\nupdate: \"!('author' in changed())\"\n",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{Store: authtest.NewMemStore(), Hasher: auth.NewHasher(1, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}), Settings: reg.Realms["acme"].Settings}
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(string) *auth.Users { return svc }
	})
	_, tok, _ := svc.Signup(context.Background(), "ada@example.com", "dev-p4ssw0rd!", "")
	rec, doc := f.doH(t, "POST", "/v1/acme/app/posts", `{"title": "t", "author": {"email": "ada@example.com"}}`, bearer(tok))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec.Code, doc)
	}
	id := "/v1/acme/app/posts/" + doc["id"].(string)
	for _, patch := range []string{`{"author": {"email": "eve@example.com"}}`, `{"author": {"name": "Eve"}}`, `{"author": null}`} {
		if rec, out := f.doH(t, "PATCH", id, patch, bearer(tok)); rec.Code != http.StatusForbidden {
			t.Errorf("PATCH %s: %d %v, want 403", patch, rec.Code, out)
		}
	}
	if rec, out := f.doH(t, "PATCH", id, `{"title": "t2"}`, bearer(tok)); rec.Code != http.StatusOK {
		t.Errorf("PATCH title: %d %v", rec.Code, out)
	}
	if _, out := f.doH(t, "GET", id, "", bearer(tok)); out["author"].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("author changed: %v", out["author"])
	}
}
