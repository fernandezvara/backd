package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

// batchFixture is shop/store: two collections, auth disabled, for
// cross-collection batch and atomicity tests.
func batchFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"shop/realm.yaml": "auth: disabled\n",
		"shop/store/orders/schema.json": `{
			"type": "object",
			"properties": {"item": {"type": "string"}, "qty": {"type": "integer", "minimum": 1}},
			"required": ["item", "qty"], "additionalProperties": false
		}`,
		"shop/store/stock/schema.json": `{
			"type": "object",
			"properties": {"item": {"type": "string"}, "qty": {"type": "integer", "minimum": 0}},
			"required": ["item", "qty"], "additionalProperties": false
		}`,
	} {
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
	return newFixtureWith(t, reg, &memStore{})
}

func TestBatchCreateAndPatch(t *testing.T) {
	f := batchFixture(t)
	rec, seed := f.do(t, "POST", "/v1/shop/store/stock", `{"item": "widget", "qty": 10}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed stock: %d %s", rec.Code, rec.Body)
	}
	stockID := seed["id"].(string)

	body := `{"operations": [
		{"op": "create", "collection": "orders", "document": {"item": "widget", "qty": 2}},
		{"op": "patch", "collection": "stock", "id": "` + stockID + `", "patch": {"qty": 8}}
	]}`
	rec, out := f.do(t, "POST", "/v1/shop/store/_batch", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	results, _ := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %v", out)
	}
	order := results[0].(map[string]any)
	if order["item"] != "widget" || order["qty"].(float64) != 2 {
		t.Errorf("order = %v", order)
	}
	stock := results[1].(map[string]any)
	if stock["qty"].(float64) != 8 {
		t.Errorf("stock = %v", stock)
	}
	if meta, ok := stock["_meta"].(map[string]any); !ok || meta["version"].(float64) != 2 {
		t.Errorf("stock not versioned: %v", stock)
	}

	// Both writes are visible afterward.
	rec, _ = f.do(t, "GET", "/v1/shop/store/stock/"+stockID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get stock: %d", rec.Code)
	}
}

func TestBatchDelete(t *testing.T) {
	f := batchFixture(t)
	rec, body := f.do(t, "POST", "/v1/shop/store/orders", `{"item": "widget", "qty": 1}`)
	id := body["id"].(string)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d", rec.Code)
	}
	rec, out := f.do(t, "POST", "/v1/shop/store/_batch", `{"operations": [{"op": "delete", "collection": "orders", "id": "`+id+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch delete: %d %s", rec.Code, rec.Body)
	}
	results, _ := out["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["id"] != id {
		t.Errorf("results = %v", results)
	}
	rec, _ = f.do(t, "GET", "/v1/shop/store/orders/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("not deleted: %d", rec.Code)
	}
}

// TestBatchAtomic proves the "all or nothing" guarantee: a batch whose
// second operation fails leaves the first operation's write rolled back.
func TestBatchAtomic(t *testing.T) {
	f := batchFixture(t)
	body := `{"operations": [
		{"op": "create", "collection": "orders", "document": {"item": "widget", "qty": 1}},
		{"op": "delete", "collection": "orders", "id": "does-not-exist"}
	]}`
	rec, out := f.do(t, "POST", "/v1/shop/store/_batch", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	code, details := errorOf(out)
	if code != codeNotFound {
		t.Errorf("code = %q", code)
	}
	if len(details) != 1 || details[0].(map[string]any)["path"] != "1" {
		t.Errorf("details = %v", details)
	}
	// The first operation's create never happened.
	rec, out = f.do(t, "GET", "/v1/shop/store/orders", "")
	items, _ := out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 0 {
		t.Errorf("orders not rolled back: %d %v", rec.Code, items)
	}
}

func TestBatchValidation(t *testing.T) {
	f := batchFixture(t)
	for _, tt := range []struct {
		name string
		body string
		want string // substring of the error message
	}{
		{"not an array", `{"operations": {}}`, `"operations" must be an array`},
		{"empty", `{"operations": []}`, "must not be empty"},
		{"too many", `{"operations": ` + repeatOp(101) + `}`, "at most 100 operations"},
		{"bad op", `{"operations": [{"op": "upsert", "collection": "orders"}]}`, `"op" must be one of`},
		{"unknown collection", `{"operations": [{"op": "create", "collection": "ghost", "document": {}}]}`, "no such collection"},
		{"missing id", `{"operations": [{"op": "delete", "collection": "orders"}]}`, `"id" is required`},
		{"missing document", `{"operations": [{"op": "create", "collection": "orders"}]}`, `"document" is required`},
		{"missing patch", `{"operations": [{"op": "patch", "collection": "orders", "id": "x"}]}`, `"patch" is required`},
		{"bad if_match", `{"operations": [{"op": "delete", "collection": "orders", "id": "x", "if_match": "nope"}]}`, "If-Match must be"},
		{"schema violation", `{"operations": [{"op": "create", "collection": "orders", "document": {"item": "widget"}}]}`, "failed schema validation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec, out := f.do(t, "POST", "/v1/shop/store/_batch", tt.body)
			if rec.Code < 400 {
				t.Fatalf("code = %d, want an error: %s", rec.Code, rec.Body)
			}
			msg, _ := out["error"].(map[string]any)["message"].(string)
			if !strings.Contains(msg, tt.want) {
				t.Errorf("message = %q, want it to contain %q", msg, tt.want)
			}
		})
	}
	// Nothing was ever written: every case above failed before the
	// transaction opened.
	rec, out := f.do(t, "GET", "/v1/shop/store/orders", "")
	items, _ := out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 0 {
		t.Errorf("orders not empty: %v", items)
	}
}

func TestBatchVersionMismatch(t *testing.T) {
	f := batchFixture(t)
	_, body := f.do(t, "POST", "/v1/shop/store/stock", `{"item": "widget", "qty": 10}`)
	id := body["id"].(string)

	rec, out := f.do(t, "POST", "/v1/shop/store/_batch", `{"operations": [{"op": "patch", "collection": "stock", "id": "`+id+`", "patch": {"qty": 5}, "if_match": "\"9\""}]}`)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	code, _ := errorOf(out)
	if code != codeVersionMismatch {
		t.Errorf("code = %q", code)
	}
}

func TestBatchDatabaseNotFound(t *testing.T) {
	f := batchFixture(t)
	rec, _ := f.do(t, "POST", "/v1/shop/ghost/_batch", `{"operations": [{"op": "create", "collection": "orders", "document": {}}]}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d", rec.Code)
	}
}

func repeatOp(n int) string {
	op := `{"op": "create", "collection": "orders", "document": {"item": "x", "qty": 1}}`
	ops := make([]string, n)
	for i := range ops {
		ops[i] = op
	}
	return "[" + strings.Join(ops, ",") + "]"
}

// TestBatchRules checks that batch operations go through the caller's
// access rules exactly like a single write (roadmap F7: "checked against
// the caller's rules for ctx.db"), and that an API key (the analog of
// ctx.admin.db) bypasses them.
func TestBatchRules(t *testing.T) {
	f := newRulesFixture(t)
	// posts' create rule: user != nil && user.email_verified.
	create := `{"operations": [{"op": "create", "collection": "posts", "document": {"title": "hi"}}]}`

	// Anonymous: the create rule needs a signed-in user.
	if code, out := f.as(t, "", "POST", "/v1/acme/app/_batch", create); code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d %v", code, out)
	}
	// Signed in but unverified: the rule requires a verified email.
	if code, out := f.as(t, f.carl, "POST", "/v1/acme/app/_batch", create); code != http.StatusForbidden {
		t.Errorf("unverified: %d %v", code, out)
	}
	// Signed in and verified: allowed.
	if code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_batch", create); code != http.StatusOK {
		t.Errorf("verified: %d %v", code, out)
	}
	// An API key bypasses rules entirely, as it does for single writes.
	if code, out := f.as(t, f.key, "POST", "/v1/acme/app/_batch", create); code != http.StatusOK {
		t.Errorf("api key: %d %v", code, out)
	}

	// A read the caller's rules don't allow answers not-found, not
	// forbidden (existence isn't leaked) — here bob patching ada's
	// unpublished post: its read rule only lets its owner see it.
	code, out := f.as(t, f.ada, "POST", "/v1/acme/app/_batch", create)
	if code != http.StatusOK {
		t.Fatalf("seed: %d %v", code, out)
	}
	results, _ := out["results"].([]any)
	postID := results[0].(map[string]any)["id"].(string)
	patch := `{"operations": [{"op": "patch", "collection": "posts", "id": "` + postID + `", "patch": {"title": "hijacked"}}]}`
	if code, out := f.as(t, f.bob, "POST", "/v1/acme/app/_batch", patch); code != http.StatusNotFound {
		t.Errorf("no read access: %d %v", code, out)
	}
}

// TestBatchAtomicAgainstMongoDB proves the "one MongoDB transaction"
// promise for real, not just against the in-memory test fake: a batch
// whose second operation fails leaves the first operation's write rolled
// back in the actual database.
func TestBatchAtomicAgainstMongoDB(t *testing.T) {
	f := mongoFixture(t, map[string]string{
		"shop/store/orders/schema.json": `{"type": "object", "properties": {"item": {"type": "string"}}, "required": ["item"]}`,
		"shop/store/stock/schema.json":  `{"type": "object", "properties": {"item": {"type": "string"}, "qty": {"type": "integer"}}, "required": ["item", "qty"]}`,
	})
	rec, seed := f.do(t, "POST", "/v1/shop/store/stock", `{"item": "widget", "qty": 10}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	stockID := seed["id"].(string)

	body := `{"operations": [
		{"op": "create", "collection": "orders", "document": {"item": "widget"}},
		{"op": "patch", "collection": "stock", "id": "` + stockID + `", "patch": {"qty": 2}, "if_match": "\"99\""}
	]}`
	rec, out := f.do(t, "POST", "/v1/shop/store/_batch", body)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	if code, _ := errorOf(out); code != codeVersionMismatch {
		t.Errorf("code = %q", code)
	}

	// The order from operation 0 was rolled back with operation 1's failure.
	rec, out = f.do(t, "GET", "/v1/shop/store/orders", "")
	items, _ := out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 0 {
		t.Fatalf("orders not rolled back: %d %v", rec.Code, items)
	}
	// Stock is untouched too.
	rec, out = f.do(t, "GET", "/v1/shop/store/stock/"+stockID, "")
	if rec.Code != http.StatusOK || out["qty"].(float64) != 10 {
		t.Errorf("stock changed: %d %v", rec.Code, out)
	}

	// A whole successful batch really does commit both writes.
	body = `{"operations": [
		{"op": "create", "collection": "orders", "document": {"item": "widget"}},
		{"op": "patch", "collection": "stock", "id": "` + stockID + `", "patch": {"qty": 9}}
	]}`
	rec, _ = f.do(t, "POST", "/v1/shop/store/_batch", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	rec, out = f.do(t, "GET", "/v1/shop/store/orders", "")
	items, _ = out["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("order not committed: %d %v", rec.Code, items)
	}
	rec, out = f.do(t, "GET", "/v1/shop/store/stock/"+stockID, "")
	if rec.Code != http.StatusOK || out["qty"].(float64) != 9 {
		t.Errorf("stock not committed: %d %v", rec.Code, out)
	}
}
