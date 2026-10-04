package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
)

// scoped makes a data key with the grants.
func (f *rulesFixture) scoped(t *testing.T, name string, grants ...string) string {
	t.Helper()
	scopes, err := auth.ParseScopes(grants)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := f.svc.CreateAPIKey(context.Background(), name, auth.KeyOptions{Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAPIKeyScopes(t *testing.T) {
	f := newRulesFixture(t)
	const notes, orders = "/v1/acme/app/notes", "/v1/acme/app/orders"
	call := func(key, method, path, body string) int {
		t.Helper()
		code, _ := f.as(t, key, method, path, body)
		return code
	}
	full := f.key // no scopes: everything, as keys always did

	// Read-only on one collection.
	reader := f.scoped(t, "reader", "read:app/posts")
	for name, tt := range map[string]struct {
		method, path, body string
		want               int
	}{
		"list posts":       {"GET", posts, "", http.StatusOK},
		"list notes":       {"GET", notes, "", http.StatusForbidden},
		"list private":     {"GET", "/v1/acme/app/private", "", http.StatusForbidden},
		"create a post":    {"POST", posts, `{"title": "x"}`, http.StatusForbidden},
		"delete a post":    {"DELETE", posts + "/nope", "", http.StatusForbidden},
		"another database": {"GET", "/v1/acme/other/posts", "", http.StatusNotFound}, // no such database: the route answers before the scope
	} {
		if got := call(reader, tt.method, tt.path, tt.body); got != tt.want {
			t.Errorf("read:app/posts, %s: %d, want %d", name, got, tt.want)
		}
	}
	// The refusal says what was refused.
	if _, out := f.as(t, reader, "POST", posts, `{"title": "x"}`); !strings.Contains(out["error"].(map[string]any)["message"].(string), "write app/posts") {
		t.Errorf("message: %v", out)
	}

	// Write without read; a database grant covers its collections.
	writer := f.scoped(t, "writer", "write:app/posts")
	if got := call(writer, "POST", posts, `{"title": "x"}`); got != http.StatusCreated {
		t.Errorf("write:app/posts creating: %d", got)
	}
	if got := call(writer, "GET", posts, ""); got != http.StatusForbidden {
		t.Errorf("write doesn't give read: %d", got)
	}
	if got := call(writer, "POST", notes, `{"title": "x"}`); got != http.StatusForbidden {
		t.Errorf("write on another collection: %d", got)
	}
	database := f.scoped(t, "database", "read:app")
	for _, path := range []string{posts, notes, "/v1/acme/app/private"} {
		if got := call(database, "GET", path, ""); got != http.StatusOK {
			t.Errorf("read:app on %s: %d", path, got)
		}
	}

	// Batch writes need write on every collection they touch.
	batch := "/v1/acme/app/_batch"
	both := `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "a"}}, {"op": "create", "collection": "notes", "document": {"title": "b"}}]}`
	onlyPosts := `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "a"}}]}`
	batcher := f.scoped(t, "batcher", "write:app/posts")
	if got := call(batcher, "POST", batch, onlyPosts); got != http.StatusOK {
		t.Errorf("batch within the scope: %d", got)
	}
	if got := call(batcher, "POST", batch, both); got != http.StatusForbidden {
		t.Errorf("batch reaching a second collection: %d", got)
	}
	if got := call(reader, "POST", batch, onlyPosts); got != http.StatusForbidden {
		t.Errorf("batch with a read-only key: %d", got)
	}

	// Functions need a call grant, even for a read-only key.
	if got := call(reader, "POST", "/v1/acme/app/_func/echo", `{"n": 1}`); got != http.StatusForbidden {
		t.Errorf("a read-only key calling a function: %d", got)
	}
	caller := f.scoped(t, "caller", "call:app/echo")
	if got := call(caller, "POST", "/v1/acme/app/_func/echo", `{"n": 1}`); got != http.StatusOK {
		t.Errorf("call:app/echo calling echo: %d", got)
	}
	if got := call(caller, "POST", "/v1/acme/app/_func/typed", `{}`); got != http.StatusForbidden {
		t.Errorf("call:app/echo calling typed: %d", got)
	}
	if got := call(caller, "GET", posts, ""); got != http.StatusForbidden {
		t.Errorf("a call grant isn't a read grant: %d", got)
	}

	// Jobs: readable by keys that may call their function, hidden from the rest.
	code, out := f.as(t, full, "POST", "/v1/acme/app/_func/job", `{"n": 1}`)
	if code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %v", code, out)
	}
	jobPath := "/v1/acme/app/_jobs/" + out["id"].(string)
	if got := call(full, "GET", jobPath, ""); got != http.StatusOK {
		t.Errorf("an unscoped key reading a job: %d", got)
	}
	jobs := f.scoped(t, "jobs", "call:app/job")
	if got := call(jobs, "GET", jobPath, ""); got != http.StatusOK {
		t.Errorf("a key that may call the function reading its job: %d", got)
	}
	if got := call(caller, "GET", jobPath, ""); got != http.StatusNotFound {
		t.Errorf("a key that may not call the function reading its job: %d", got)
	}

	// Acting on behalf of a user doesn't widen the scope.
	hdr := map[string]string{"Authorization": "Bearer " + reader, "X-Backd-On-Behalf-Of": f.adaID, "Content-Type": "application/json"}
	if rec, _ := f.doH(t, "GET", posts, "", hdr); rec.Code != http.StatusOK {
		t.Errorf("on behalf, within the scope: %d", rec.Code)
	}
	if rec, _ := f.doH(t, "POST", posts, `{"title": "x"}`, hdr); rec.Code != http.StatusForbidden {
		t.Errorf("on behalf, outside the scope: %d", rec.Code)
	}

	// Without scopes nothing changed.
	for _, path := range []string{posts, notes, orders} {
		if got := call(full, "GET", path, ""); got != http.StatusOK {
			t.Errorf("an unscoped key on %s: %d", path, got)
		}
	}
	// Users aren't affected: scopes are for keys.
	if got := call(f.ada, "GET", posts, ""); got != http.StatusOK {
		t.Errorf("a user: %d", got)
	}
}

func TestAdminCreatesScopedKeys(t *testing.T) {
	f := newRulesFixture(t)
	create := func(body string) (int, map[string]any) {
		t.Helper()
		rec, out := f.doH(t, "POST", admin+"/apikeys", body, map[string]string{"Authorization": "Bearer " + f.key, "Content-Type": "application/json"})
		return rec.Code, out
	}
	code, out := create(`{"name": "site", "scopes": ["read:app/posts", "call:app/typed", "read:app/posts"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}
	if got := out["scopes"].([]any); len(got) != 2 || got[0] != "read:app/posts" || got[1] != "call:app/typed" {
		t.Errorf("scopes = %v (duplicates collapse)", got)
	}
	// And the list shows them, and the key works within them.
	rec, _ := f.doH(t, "GET", admin+"/apikeys", "", map[string]string{"Authorization": "Bearer " + f.key})
	if !strings.Contains(strings.ReplaceAll(rec.Body.String(), " ", ""), `"scopes":["read:app/posts","call:app/typed"]`) {
		t.Errorf("list: %s", rec.Body)
	}
	if got, _ := f.as(t, out["key"].(string), "GET", posts, ""); got != http.StatusOK {
		t.Errorf("the new key reading posts: %d", got)
	}
	// An unscoped key lists as having none.
	if code, out = create(`{"name": "plain"}`); code != http.StatusCreated || len(out["scopes"].([]any)) != 0 {
		t.Errorf("unscoped: %d %v", code, out)
	}

	for name, body := range map[string]string{
		"unknown grant":       `{"name": "a", "scopes": ["admin"]}`,
		"bad target":          `{"name": "a", "scopes": ["read:Blog"]}`,
		"unknown database":    `{"name": "a", "scopes": ["read:nope"]}`,
		"unknown collection":  `{"name": "a", "scopes": ["write:app/nope"]}`,
		"unknown function":    `{"name": "a", "scopes": ["call:app/nope"]}`,
		"database sans funcs": `{"name": "a", "scopes": ["call:nope"]}`,
		"on an admin key":     `{"name": "a", "role": "admin", "scopes": ["read"]}`,
		"not a list":          `{"name": "a", "scopes": "read"}`,
	} {
		if code, out := create(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}
