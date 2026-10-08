package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rs/xid"

	backdauth "github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
)

// mongoFixture provisions the given config files (paths relative to
// CONFIG_DIR, e.g. "realm/db/coll/schema.json") in a real MongoDB and returns
// a fixture serving them. It skips the test when MONGO_TEST_URI is unset.
func mongoFixture(t *testing.T, files map[string]string) *fixture {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set; skipping integration test")
	}
	ctx := context.Background()
	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(ctx) })

	root := t.TempDir()
	files = maps.Clone(files)
	for p := range files {
		realm, _, _ := strings.Cut(p, "/")
		if _, ok := files[realm+"/"+registry.RealmFile]; !ok {
			files[realm+"/"+registry.RealmFile] = "auth: disabled\n"
		}
	}
	for p, content := range files {
		file := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, db := range reg.Databases() {
			_ = client.Database(db.MongoName).Drop(ctx)
		}
		for name := range reg.Realms {
			_ = client.Database(mongodb.SystemDatabaseName(name)).Drop(ctx)
		}
	})
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if err := (&mongodb.Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	// Realms whose realm.yaml enables auth get a user service.
	hasher := backdauth.NewHasher(2, backdauth.Argon2Params{Memory: 64, Time: 1, Threads: 1})
	services := map[string]*backdauth.Users{}
	for name, rl := range reg.Realms {
		if rl.Settings.AuthEnabled {
			services[name] = &backdauth.Users{Store: mongodb.NewAuthStore(client, name), Hasher: hasher, Settings: rl.Settings}
		}
	}
	f := newFixtureWith(t, reg, &mongodb.Store{Client: client}, func(c *Config) {
		c.Users = func(realm string) *backdauth.Users { return services[realm] }
	})
	f.users = services
	return f
}

// TestAuthAgainstMongoDB signs up, logs in and manages sessions over HTTP
// against a real MongoDB.
func TestAuthAgainstMongoDB(t *testing.T) {
	realm := "t" + xid.New().String()
	f := mongoFixture(t, map[string]string{
		realm + "/realm.yaml":            "signup: open\n",
		realm + "/app/notes/schema.json": `{}`,
	})
	base := "/v1/" + realm + "/_auth"

	rec, out := f.do(t, "POST", base+"/signup", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: %d %v", rec.Code, out)
	}
	token := out["token"].(string)
	rec, out = f.do(t, "POST", base+"/login", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %v", rec.Code, out)
	}
	token2 := out["token"].(string)

	auth := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }
	if rec, out := f.doH(t, "GET", base+"/me", "", auth(token)); rec.Code != http.StatusOK || out["email"] != "ada@example.com" {
		t.Errorf("me: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "GET", base+"/sessions", "", auth(token))
	if items, _ := out["items"].([]any); rec.Code != http.StatusOK || len(items) != 2 {
		t.Errorf("sessions: %d %v", rec.Code, out)
	}
	if rec, _ := f.doH(t, "POST", base+"/logout", "", auth(token2)); rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", base+"/me", "", auth(token2)); rec.Code != http.StatusUnauthorized {
		t.Errorf("logged-out token: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "GET", base+"/me", "", auth(token)); rec.Code != http.StatusOK {
		t.Errorf("other session affected by logout: %d", rec.Code)
	}

	// Data routes recognize the session (403: no rule allows it yet) and
	// refuse anonymous callers.
	data := "/v1/" + realm + "/app/notes"
	if rec, out := f.doH(t, "GET", data, "", auth(token)); rec.Code != http.StatusForbidden {
		t.Errorf("data with session: %d %v", rec.Code, out)
	}
	if rec, out := f.do(t, "GET", data, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("data without credentials: %d %v", rec.Code, out)
	}

	// Ownership fields pass the MongoDB validator and can be queried.
	_, key, err := f.users[realm].CreateAPIKey(context.Background(), "svc", backdauth.KeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rec, out = f.doH(t, "POST", data, `{"text": "hi"}`, auth(key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with key: %d %v", rec.Code, out)
	}
	id := out["id"].(string)
	rec, out = f.doH(t, "PATCH", data+"/"+id, `{"text": "hello"}`, auth(key))
	if meta, _ := out["_meta"].(map[string]any); rec.Code != http.StatusOK || meta["created_by"] != "key:svc" || meta["updated_by"] != "key:svc" {
		t.Errorf("patch with key: %d %v", rec.Code, out)
	}
	rec, out = f.doH(t, "GET", data+`?where={"_meta.created_by":"key:svc","_meta.owner":null}`, "", auth(key))
	if items, _ := out["items"].([]any); rec.Code != http.StatusOK || len(items) != 1 {
		t.Errorf("query on ownership fields: %d %v", rec.Code, out)
	}
}

// TestCRUDAgainstMongoDB runs the full document lifecycle through the HTTP
// layer against a real MongoDB with provisioned (strict) validators.
func TestCRUDAgainstMongoDB(t *testing.T) {
	// Two realms with several databases and collections.
	realmA, realmB := "t"+xid.New().String(), "t"+xid.New().String()
	files := map[string]string{realmA + "/orders/clients/indexes.json": `[{"fields": ["name"], "unique": true}]`}
	for _, p := range []string{
		realmA + "/orders/items", realmA + "/orders/clients", realmA + "/billing/items", realmB + "/orders/items",
	} {
		files[p+"/schema.json"] = itemSchema
	}
	f := mongoFixture(t, files)

	for _, base := range []string{
		"/v1/" + realmA + "/orders/items", "/v1/" + realmA + "/orders/clients",
		"/v1/" + realmA + "/billing/items", "/v1/" + realmB + "/orders/items",
	} {
		t.Run(base, func(t *testing.T) {
			// additionalProperties: false must hold at API and DB level.
			rec, created := f.do(t, "POST", base, `{"name": "Widget", "age": 3, "price": 2.5, "address": {"zip": "1", "city": "X"}}`)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create = %d %s", rec.Code, rec.Body)
			}
			id := created["id"].(string)

			if rec, got := f.do(t, "GET", base+"/"+id, ""); rec.Code != http.StatusOK || got["name"] != "Widget" || got["age"] != float64(3) {
				t.Errorf("get = %d %v", rec.Code, got)
			}
			if rec, _ := f.do(t, "PUT", base+"/"+id, `{"name": "Gadget", "price": 3}`); rec.Code != http.StatusOK {
				t.Errorf("put = %d %s", rec.Code, rec.Body)
			}
			if rec, got := f.do(t, "PATCH", base+"/"+id, `{"price": null, "age": 7}`); rec.Code != http.StatusOK || got["price"] != nil || got["age"] != float64(7) {
				t.Errorf("patch = %d %v", rec.Code, got)
			}
			if rec, list := f.do(t, "GET", base, ""); rec.Code != http.StatusOK || len(list["items"].([]any)) != 1 {
				t.Errorf("list = %d %v", rec.Code, list)
			}
			if rec, _ := f.do(t, "DELETE", base+"/"+id, ""); rec.Code != http.StatusNoContent {
				t.Errorf("delete = %d", rec.Code)
			}
			if rec, _ := f.do(t, "GET", base+"/"+id, ""); rec.Code != http.StatusNotFound {
				t.Errorf("get after delete = %d", rec.Code)
			}
		})
	}

	t.Run("query", func(t *testing.T) {
		base := "/v1/" + realmB + "/orders/items"
		for _, doc := range []string{`{"name": "a", "age": 1}`, `{"name": "b", "age": 5}`, `{"name": "c", "age": 9}`, `{"name": "d"}`} {
			if rec, _ := f.do(t, "POST", base, doc); rec.Code != http.StatusCreated {
				t.Fatalf("create = %d %s", rec.Code, rec.Body)
			}
		}
		q := url.Values{"where": {`{"age": {"$gte": 5}, "name": {"$in": ["b", "c", "d"]}}`}, "order_by": {"-age"}, "count": {"true"}, "limit": {"1"}}
		rec, body := f.do(t, "GET", base+"?"+q.Encode(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("query = %d %s", rec.Code, rec.Body)
		}
		list := body["items"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["name"] != "c" || body["total"] != float64(2) || body["has_more"] != true {
			t.Errorf("query result = %v", body)
		}
	})

	t.Run("unique conflict", func(t *testing.T) {
		base := "/v1/" + realmA + "/orders/clients"
		if rec, _ := f.do(t, "POST", base, `{"name": "Ann"}`); rec.Code != http.StatusCreated {
			t.Fatalf("create = %d %s", rec.Code, rec.Body)
		}
		rec, out := f.do(t, "POST", base, `{"name": "Ann"}`)
		code, details := errorOf(out)
		if rec.Code != http.StatusConflict || code != codeConflict || len(details) != 1 || details[0].(map[string]any)["path"] != "name" {
			t.Errorf("duplicate create = %d %s", rec.Code, rec.Body)
		}
		_, other := f.do(t, "POST", base, `{"name": "Bob"}`)
		rec, _ = f.do(t, "PATCH", base+"/"+other["id"].(string), `{"name": "Ann"}`)
		if rec.Code != http.StatusConflict {
			t.Errorf("duplicate patch = %d %s", rec.Code, rec.Body)
		}
	})
}

// TestConcurrentPatchesLoseNoUpdates fires parallel PATCHes at one document.
// Every PATCH answered 200 must be reflected in the final document; the
// others must have been told so with 409 write_conflict.
func TestConcurrentPatchesLoseNoUpdates(t *testing.T) {
	realm := "t" + xid.New().String()
	f := mongoFixture(t, map[string]string{realm + "/app/counters/schema.json": `{"type": "object"}`})
	base := "/v1/" + realm + "/app/counters"

	_, created := f.do(t, "POST", base, `{}`)
	doc := base + "/" + created["id"].(string)

	const writers = 16
	codes := make([]int, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("PATCH", doc, strings.NewReader(fmt.Sprintf(`{"f%d": %d}`, i, i)))
			setContentType(req)
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}()
	}
	wg.Wait()

	_, final := f.do(t, "GET", doc, "")
	ok := 0
	for i, code := range codes {
		_, present := final[fmt.Sprintf("f%d", i)]
		switch code {
		case http.StatusOK:
			ok++
			if !present {
				t.Errorf("PATCH %d answered 200 but its update was lost", i)
			}
		case http.StatusConflict:
			if present {
				t.Errorf("PATCH %d answered 409 but its update was applied", i)
			}
		default:
			t.Errorf("PATCH %d answered %d", i, code)
		}
	}
	if ok == 0 {
		t.Fatal("no PATCH succeeded")
	}
	if v := final["_meta"].(map[string]any)["version"]; v != float64(1+ok) {
		t.Errorf("version = %v, want %d (1 + %d successful writes)", v, 1+ok, ok)
	}
	t.Logf("%d/%d concurrent PATCHes applied, %d got write_conflict", ok, writers, writers-ok)
}

// TestRulesAgainstMongoDB checks that read rules become MongoDB filters
// with the expected results, and that writes are enforced end to end.
func TestRulesAgainstMongoDB(t *testing.T) {
	realm := "t" + xid.New().String()
	schema := `{"type": "object", "properties": {"title": {"type": "string"}, "published": {"type": "boolean"},
		"status": {"type": "string"}, "members": {"type": "array", "items": {"type": "string"}}}, "required": ["title"]}`
	f := mongoFixture(t, map[string]string{
		realm + "/realm.yaml":             "signup: open\n",
		realm + "/app/posts/schema.json":  schema,
		realm + "/app/posts/indexes.json": `[{"fields": ["_meta.owner"]}]`,
		realm + "/app/posts/collection.yaml": rulesSection(`
read: >
  document.published == true
  || (user != nil && (document._meta.owner == user.id || user.id in document.members))
write: user != nil
`),
		realm + "/app/recent/schema.json":     schema,
		realm + "/app/recent/collection.yaml": rulesSection("read: document._meta.created_at > now - duration('1h')\nwrite: user != nil\n"),
		realm + "/app/open/schema.json":       schema,
		realm + "/app/open/collection.yaml":   rulesSection("read: \"!(document.status == 'done')\"\nwrite: user != nil\n"),
	})
	ctx := context.Background()
	svc := f.users[realm]
	session := func(email string) (string, string) {
		p, tok, err := svc.Signup(ctx, email, "dev-p4ssw0rd!", "")
		if err != nil {
			t.Fatal(err)
		}
		return tok, p.User.ID
	}
	ada, _ := session("ada@example.com")
	bob, bobID := session("bob@example.com")
	as := func(tok string) map[string]string {
		if tok == "" {
			return nil
		}
		return map[string]string{"Authorization": "Bearer " + tok}
	}
	base := "/v1/" + realm + "/app/"
	for _, body := range []string{
		`{"title": "public", "published": true}`,
		`{"title": "draft", "published": false}`,
		`{"title": "shared", "members": ["` + bobID + `", "someone"]}`,
	} {
		if rec, out := f.doH(t, "POST", base+"posts", body, as(ada)); rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %v", rec.Code, out)
		}
	}
	titles := func(out map[string]any) string {
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["title"].(string))
		}
		return strings.Join(got, ",")
	}
	for _, tt := range []struct {
		name, tok, want string
	}{
		{"anonymous", "", "public"},
		{"owner", ada, "draft,public,shared"},
		{"member", bob, "public,shared"},
	} {
		rec, out := f.doH(t, "GET", base+"posts?order_by=title&count=true", "", as(tt.tok))
		if rec.Code != http.StatusOK || titles(out) != tt.want || out["total"] != float64(strings.Count(tt.want, ",")+1) {
			t.Errorf("%s: %d %v", tt.name, rec.Code, out)
		}
	}
	// The client's where is combined with the rule, never replacing it.
	rec, out := f.doH(t, "GET", base+`posts?where={"title":{"$ne":"public"}}`, "", as(bob))
	if rec.Code != http.StatusOK || titles(out) != "shared" {
		t.Errorf("where + rule: %d %v", rec.Code, out)
	}
	// Bob can read "shared" but gets 404 for Ada's draft.
	_, all := f.doH(t, "GET", base+"posts?order_by=title", "", as(ada))
	draftID := all["items"].([]any)[0].(map[string]any)["id"].(string)
	if rec, _ := f.doH(t, "GET", base+"posts/"+draftID, "", as(bob)); rec.Code != http.StatusNotFound {
		t.Errorf("hidden draft: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", base+"posts/"+draftID, "", as(bob)); rec.Code != http.StatusNotFound {
		t.Errorf("delete hidden draft: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "DELETE", base+"posts/"+draftID, "", as(ada)); rec.Code != http.StatusNoContent {
		t.Errorf("owner deletes draft: %d", rec.Code)
	}

	// now arithmetic against stored dates.
	f.doH(t, "POST", base+"recent", `{"title": "new"}`, as(ada))
	if rec, out := f.doH(t, "GET", base+"recent", "", nil); rec.Code != http.StatusOK || titles(out) != "new" {
		t.Errorf("recent: %d %v", rec.Code, out)
	}

	// Negation matches documents without the field, like expr's nil != "done".
	f.doH(t, "POST", base+"open", `{"title": "no status"}`, as(ada))
	f.doH(t, "POST", base+"open", `{"title": "done", "status": "done"}`, as(ada))
	f.doH(t, "POST", base+"open", `{"title": "todo", "status": "todo"}`, as(ada))
	if rec, out := f.doH(t, "GET", base+"open?order_by=title", "", nil); rec.Code != http.StatusOK || titles(out) != "no status,todo" {
		t.Errorf("negation: %d %v", rec.Code, out)
	}
}
