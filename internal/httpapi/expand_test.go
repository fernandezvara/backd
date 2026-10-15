package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

const (
	app = "/v1/acme/app/"
	crm = "/v1/acme/crm/"
)

// expandFixture is realm acme with posts that reference categories (and a category's parent),
// tags (many), the author's profile (through _meta.owner), private notes, another database's
// customers and a collection nobody may read.
type expandFixture struct {
	*fixture
	svc *auth.Users
	key string // an administrator's API key, which bypasses rules
	ada string // session tokens
	bob string
	log *bytes.Buffer
	ids map[string]string
}

func newExpandFixture(t *testing.T, tune func(*Config)) *expandFixture {
	t.Helper()
	read := "read: user != nil\ncreate: user != nil\n"
	files := map[string]string{
		"acme/realm.yaml": "signup: open\n",
		"acme/app/posts/schema.json": `{"type":"object","properties":{
			"title":{"type":"string"},"category":{"type":["string","null"]},"tags":{"type":"array","items":{"type":"string"}},
			"customer_id":{"type":"string"},"note":{"type":"string"},"secret_id":{"type":"string"}},"additionalProperties":false}`,
		"acme/app/posts/collection.yaml": `references:
  category: {collection: categories}
  tags: {collection: tags, many: true}
  author: {field: _meta.owner, collection: profiles, key: _meta.owner}
  customer: {field: customer_id, collection: customers, database: crm}
  note: {collection: notes}
  secret: {field: secret_id, collection: vault}
` + rulesSection(read),
		"acme/app/categories/schema.json":     `{"type":"object","properties":{"name":{"type":"string"},"parent":{"type":"string"},"slug":{"type":"string"}},"additionalProperties":false}`,
		"acme/app/categories/collection.yaml": "references:\n  parent: {collection: categories}\n" + rulesSection(read),
		"acme/app/tags/schema.json":           `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
		"acme/app/tags/collection.yaml":       rulesSection(read),
		"acme/app/profiles/schema.json":       `{"type":"object","properties":{"name":{"type":"string"},"avatar":{"type":"string"},"email":{"type":"string"}},"additionalProperties":false}`,
		"acme/app/profiles/indexes.json":      `[{"fields": ["_meta.owner"], "unique": true}]`,
		"acme/app/profiles/collection.yaml":   rulesSection(read),
		"acme/app/notes/schema.json":          `{"type":"object","properties":{"text":{"type":"string"}},"additionalProperties":false}`,
		"acme/app/notes/collection.yaml":      rulesSection("read: user != nil && document._meta.owner == user.id\ncreate: user != nil\n"),
		"acme/app/vault/schema.json":          `{"type":"object","properties":{"text":{"type":"string"}},"additionalProperties":false}`,
		"acme/app/vault/collection.yaml":      rulesSection("create: user != nil\n"),
		"acme/crm/customers/schema.json":      `{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`,
		"acme/crm/customers/collection.yaml":  rulesSection(read),
	}
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{Store: authtest.NewMemStore(), Hasher: auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}), Settings: reg.Realms["acme"].Settings}
	var buf bytes.Buffer
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(string) *auth.Users { return svc }
		c.Log = slog.New(slog.NewJSONHandler(&buf, nil))
		if tune != nil {
			tune(c)
		}
	})
	ctx := context.Background()
	_, key, err := svc.CreateAPIKey(ctx, "ops", auth.KeyOptions{Role: auth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	x := &expandFixture{fixture: f, svc: svc, key: key, log: &buf, ids: map[string]string{}}
	for _, who := range []string{"ada", "bob"} {
		_, tok, err := svc.Signup(ctx, who+"@example.com", "dev-p4ssw0rd!", "")
		if err != nil {
			t.Fatal(err)
		}
		if who == "ada" {
			x.ada = tok
		} else {
			x.bob = tok
		}
	}
	return x
}

// create posts body to a collection as token and remembers the id under name.
func (x *expandFixture) create(t *testing.T, token, name, path, body string) string {
	t.Helper()
	rec, out := x.doH(t, "POST", path, body, bearer(token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", name, rec.Code, out)
	}
	id := out["id"].(string)
	x.ids[name] = id
	return id
}

// seed makes the documents most tests read.
func (x *expandFixture) seed(t *testing.T) {
	t.Helper()
	x.create(t, x.ada, "root", app+"categories", `{"name": "Everything", "slug": "all"}`)
	x.create(t, x.ada, "go", app+"categories", fmt.Sprintf(`{"name": "Go", "slug": "go", "parent": %q}`, x.ids["root"]))
	x.create(t, x.ada, "t1", app+"tags", `{"name": "fast"}`)
	x.create(t, x.ada, "t2", app+"tags", `{"name": "small"}`)
	x.create(t, x.ada, "t3", app+"tags", `{"name": "typed"}`)
	x.create(t, x.ada, "profile", app+"profiles", `{"name": "Ada", "avatar": "ada.png", "email": "ada@example.com"}`)
	x.create(t, x.ada, "customer", crm+"customers", `{"name": "Acme"}`)
	x.create(t, x.bob, "bobsnote", app+"notes", `{"text": "private"}`)
	x.create(t, x.ada, "vault", app+"vault", `{"text": "hidden"}`)
}

func (x *expandFixture) post(t *testing.T, token string, fields map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(fields)
	return x.create(t, token, fmt.Sprint("post-", len(x.ids)), app+"posts", string(b))
}

func TestExpandGetShape(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	id := x.post(t, x.ada, map[string]any{
		"title": "Hello", "category": x.ids["go"], "tags": []string{x.ids["t1"], "gone", x.ids["t3"]},
		"customer_id": x.ids["customer"],
	})

	rec, out := x.doH(t, "GET", app+"posts/"+id+"?expand=category(name),tags(name),author(name,avatar),customer", "", bearer(x.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %v", rec.Code, out)
	}
	exp := out["_expanded"].(map[string]any)
	if got := exp["category"]; fmt.Sprint(got) != fmt.Sprint(map[string]any{"id": x.ids["go"], "name": "Go"}) {
		t.Errorf("category = %v", got)
	}
	tags := exp["tags"].([]any)
	if len(tags) != 3 || tags[1] != nil || tags[0].(map[string]any)["name"] != "fast" || tags[2].(map[string]any)["id"] != x.ids["t3"] {
		t.Errorf("tags = %v (positions are kept, a missing one is null)", tags)
	}
	if author := exp["author"].(map[string]any); author["name"] != "Ada" || author["avatar"] != "ada.png" || author["email"] != nil || author["id"] != x.ids["profile"] {
		t.Errorf("author = %v: only the selected fields and the id", author)
	}
	if cust := exp["customer"].(map[string]any); cust["name"] != "Acme" || cust["_meta"] == nil {
		t.Errorf("customer = %v: without a selection the whole document", cust)
	}
	// The document itself is unchanged.
	if out["category"] != x.ids["go"] || out["title"] != "Hello" {
		t.Errorf("document = %v", out)
	}
	if rec.Header().Get("ETag") != `"1"` || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("headers = %v", rec.Header())
	}
	if rec.Header().Get("X-Backd-Expand-Queries") != "4" || rec.Header().Get("X-Backd-Expand-Docs") != "5" {
		t.Errorf("cost headers = %q %q", rec.Header().Get("X-Backd-Expand-Queries"), rec.Header().Get("X-Backd-Expand-Docs"))
	}

	// Never a 304, whatever the validator.
	rec, _ = x.doH(t, "GET", app+"posts/"+id+"?expand=category", "", map[string]string{"Authorization": "Bearer " + x.ada, "If-None-Match": `"1"`})
	if rec.Code != http.StatusOK {
		t.Errorf("If-None-Match with expand = %d", rec.Code)
	}
	// Without expand nothing changes: no _expanded, no cost headers.
	rec, out = x.doH(t, "GET", app+"posts/"+id, "", bearer(x.ada))
	if _, has := out["_expanded"]; has || rec.Header().Get("X-Backd-Expand-Queries") != "" {
		t.Errorf("nothing was asked: %v %v", out, rec.Header())
	}
}

func TestExpandListBatchesPerPath(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	for i := 0; i < 5; i++ {
		x.post(t, x.ada, map[string]any{"title": fmt.Sprint("p", i), "category": x.ids["go"], "tags": []string{x.ids["t1"], x.ids["t2"]}})
	}
	x.post(t, x.ada, map[string]any{"title": "none"}) // no references at all

	rec, out := x.doH(t, "GET", app+"posts?expand=category(name),tags(name),author(name)&limit=100", "", bearer(x.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %v", rec.Code, out)
	}
	items := out["items"].([]any)
	if len(items) != 6 {
		t.Fatalf("items = %d", len(items))
	}
	if rec.Header().Get("X-Backd-Expand-Queries") != "3" {
		t.Errorf("a list costs one query per path, got %q", rec.Header().Get("X-Backd-Expand-Queries"))
	}
	// 5 posts × (category + 2 tags + author), and the author of the sixth
	if rec.Header().Get("X-Backd-Expand-Docs") != "21" {
		t.Errorf("docs = %q", rec.Header().Get("X-Backd-Expand-Docs"))
	}
	if rec.Header().Get("ETag") != "" || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("headers = %v", rec.Header())
	}
	for _, it := range items {
		m := it.(map[string]any)
		exp := m["_expanded"].(map[string]any)
		if m["title"] == "none" {
			if exp["category"] != nil || len(exp["tags"].([]any)) != 0 || exp["author"].(map[string]any)["name"] != "Ada" {
				t.Errorf("empty sources = %v", exp)
			}
			continue
		}
		if exp["category"].(map[string]any)["name"] != "Go" {
			t.Errorf("category = %v", exp["category"])
		}
	}
	// The request logged what the expansion cost, and never a document.
	if !strings.Contains(x.log.String(), `"expand_queries":3`) || !strings.Contains(x.log.String(), `"expand_docs":21`) {
		t.Errorf("access log = %s", x.log)
	}
}

func TestExpandSecondLevelAndSelection(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	id := x.post(t, x.ada, map[string]any{"title": "Deep", "category": x.ids["go"]})

	rec, out := x.doH(t, "GET", app+"posts/"+id+"?expand=category(name).parent", "", bearer(x.ada))
	// A selection only closes an entry: category(name).parent isn't syntax.
	if rec.Code != http.StatusBadRequest || errCode(out) != "invalid_expand" {
		t.Fatalf("%d %v", rec.Code, out)
	}
	rec, out = x.doH(t, "GET", app+"posts/"+id+"?expand=category.parent(name)", "", bearer(x.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %v", rec.Code, out)
	}
	cat := out["_expanded"].(map[string]any)["category"].(map[string]any)
	if cat["name"] != "Go" || cat["slug"] != "go" {
		t.Errorf("the way to a second level is returned whole: %v", cat)
	}
	parent := cat["_expanded"].(map[string]any)["parent"].(map[string]any)
	if parent["name"] != "Everything" || parent["slug"] != nil {
		t.Errorf("parent = %v", parent)
	}
	if rec.Header().Get("X-Backd-Expand-Queries") != "2" || rec.Header().Get("X-Backd-Expand-Docs") != "2" {
		t.Errorf("cost = %q %q", rec.Header().Get("X-Backd-Expand-Queries"), rec.Header().Get("X-Backd-Expand-Docs"))
	}

	// Selecting the way and its fields: the source of the second level needn't be selected.
	rec, out = x.doH(t, "GET", app+"posts/"+id+"?expand=category(name),category.parent(name)", "", bearer(x.ada))
	cat = out["_expanded"].(map[string]any)["category"].(map[string]any)
	if rec.Code != http.StatusOK || cat["slug"] != nil || cat["parent"] != nil || cat["_expanded"].(map[string]any)["parent"].(map[string]any)["name"] != "Everything" {
		t.Errorf("category = %v", cat)
	}
}

func TestExpandInvalid(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	id := x.post(t, x.ada, map[string]any{"title": "t"})
	for expand, want := range map[string]string{
		"nope":                   `"nope" is not a reference of posts; declared: author, category, customer, note, secret, tags`,
		"category.parent.parent": "goes 3 levels deep, at most 2 are followed",
		"category,tags,author,customer,note,secret": "6 entries, at most 5 per request",
		"category(nope)":      `"nope" is not a field of categories`,
		"category(name":       "unbalanced",
		"category,category":   `"category" is listed twice`,
		"category.nope":       `"nope" is not a reference of categories; declared: parent`,
		"tags.x":              `"x" is not a reference of tags; tags declares no references`,
		"category(_expanded)": `"_expanded" is not a field of categories`,
	} {
		for _, path := range []string{app + "posts/" + id, app + "posts"} {
			rec, out := x.doH(t, "GET", path+"?expand="+strings.ReplaceAll(expand, " ", ""), "", bearer(x.ada))
			if rec.Code != http.StatusBadRequest || errCode(out) != "invalid_expand" || !strings.Contains(fmt.Sprint(out), want) {
				t.Errorf("%s ?expand=%s: %d %v, want %q", path, expand, rec.Code, out, want)
			}
		}
	}
}

func TestExpandMissingUnreadableAndForeignAreIdentical(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	// ada reads three posts that point at: a note nobody can read for her (bob's), one that
	// doesn't exist, and a document of a collection with no read rule at all.
	for _, f := range []map[string]any{
		{"title": "unreadable", "note": x.ids["bobsnote"]},
		{"title": "missing", "note": "nosuchnote0000000000"},
		{"title": "no rule", "secret_id": x.ids["vault"]},
	} {
		x.post(t, x.ada, f)
	}
	rec, out := x.doH(t, "GET", app+"posts?expand=note,secret&limit=100", "", bearer(x.ada))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %v", rec.Code, out)
	}
	for _, it := range out["items"].([]any) {
		exp := it.(map[string]any)["_expanded"].(map[string]any)
		if exp["note"] != nil || exp["secret"] != nil {
			t.Errorf("%v: %v must be null", it.(map[string]any)["title"], exp)
		}
	}
	// The same document is visible to its owner: only the caller's rules decide.
	id := x.post(t, x.bob, map[string]any{"title": "bob", "note": x.ids["bobsnote"]})
	_, out = x.doH(t, "GET", app+"posts/"+id+"?expand=note", "", bearer(x.bob))
	if out["_expanded"].(map[string]any)["note"].(map[string]any)["text"] != "private" {
		t.Errorf("owner = %v", out)
	}
	// An administrator's key bypasses rules, as on a direct read.
	_, out = x.doH(t, "GET", app+"posts/"+id+"?expand=secret,note", "", bearer(x.key))
	if out["_expanded"].(map[string]any)["note"] == nil {
		t.Errorf("key = %v", out)
	}
}

func TestExpandKeyScopesNarrowTargets(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	id := x.post(t, x.ada, map[string]any{"title": "t", "category": x.ids["go"]})
	scopes, err := auth.ParseScopes([]string{"read:app/posts"})
	if err != nil {
		t.Fatal(err)
	}
	_, narrow, err := x.svc.CreateAPIKey(context.Background(), "narrow", auth.KeyOptions{Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	_, out := x.doH(t, "GET", app+"posts/"+id+"?expand=category", "", bearer(narrow))
	if exp := out["_expanded"].(map[string]any); exp["category"] != nil {
		t.Errorf("a key without read on categories got %v", exp)
	}
	_, out = x.doH(t, "GET", app+"posts/"+id+"?expand=category", "", bearer(x.key))
	if out["_expanded"].(map[string]any)["category"] == nil {
		t.Errorf("the unrestricted key = %v", out)
	}
}

func TestExpandLimits(t *testing.T) {
	x := newExpandFixture(t, func(c *Config) {
		c.Expand = registry.ExpandLimits{MaxArray: 2, MaxDocs: 5}
	})
	x.seed(t)
	id := x.post(t, x.ada, map[string]any{"title": "many", "tags": []string{x.ids["t1"], x.ids["t2"], x.ids["t3"]}})
	_, out := x.doH(t, "GET", app+"posts/"+id+"?expand=tags(name)", "", bearer(x.ada))
	tags := out["_expanded"].(map[string]any)["tags"].(map[string]any)
	if tags["truncated"] != true || len(tags["items"].([]any)) != 2 {
		t.Errorf("tags = %v", tags)
	}

	// Six posts with one category each: six documents, five allowed. Nothing partial.
	for i := 0; i < 6; i++ {
		x.post(t, x.ada, map[string]any{"title": "c", "category": x.ids["go"]})
	}
	rec, out := x.doH(t, "GET", app+"posts?expand=category&limit=100", "", bearer(x.ada))
	if rec.Code != http.StatusBadRequest || errCode(out) != "expand_too_large" || out["items"] != nil {
		t.Errorf("%d %v", rec.Code, out)
	}
	if rec, _ = x.doH(t, "GET", app+"posts?expand=category&limit=5&where="+`{"title":"c"}`, "", bearer(x.ada)); rec.Code != http.StatusOK {
		t.Errorf("five fit: %d", rec.Code)
	}
}

func TestExpandCantBeFilteredOrWritten(t *testing.T) {
	x := newExpandFixture(t, nil)
	x.seed(t)
	id := x.post(t, x.ada, map[string]any{"title": "w", "category": x.ids["go"]})
	for _, q := range []string{`where={"category.name":"Go"}`, `where={"_expanded.category.name":"Go"}`, `order_by=author.name`} {
		rec, out := x.doH(t, "GET", app+"posts?"+q, "", bearer(x.ada))
		if rec.Code != http.StatusBadRequest || errCode(out) != "invalid_query" || !strings.Contains(fmt.Sprint(out), "can't be filtered or sorted") {
			t.Errorf("%s: %d %v", q, rec.Code, out)
		}
	}
	// A client that sends an expanded read back changes nothing, and gets no expansion.
	_, got := x.doH(t, "GET", app+"posts/"+id+"?expand=category", "", bearer(x.key))
	body, _ := json.Marshal(got)
	rec, out := x.doH(t, "PUT", app+"posts/"+id+"?expand=category", string(body), bearer(x.key))
	if rec.Code != http.StatusOK || out["_expanded"] != nil || rec.Header().Get("X-Backd-Expand-Queries") != "" {
		t.Fatalf("PUT = %d %v", rec.Code, out)
	}
	_, stored := x.doH(t, "GET", app+"posts/"+id, "", bearer(x.key))
	if stored["_expanded"] != nil || stored["title"] != "w" {
		t.Errorf("stored = %v", stored)
	}
}
