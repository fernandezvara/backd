package httpapi

import (
	"context"
	"slices"
	"testing"
	"time"
)

func seedOrder(t *testing.T, f *rulesFixture, id, owner string, fields map[string]any) {
	t.Helper()
	c, ok := f.reg.Collection("acme", "app", "orders")
	if !ok {
		t.Fatal("no orders collection")
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	doc := map[string]any{"id": id, "_meta": map[string]any{"created_at": now, "updated_at": now, "version": int64(1), "owner": owner}}
	for k, v := range fields {
		doc[k] = v
	}
	if err := f.store.Repository(c).Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
}

// The report says what an erase would do, for the collections with a policy.
func TestOwnedReport(t *testing.T) {
	f := newRulesFixture(t)
	seedOrder(t, f, "o1", f.adaID, map[string]any{"buyer": "Ada", "phone": "555"})
	seedOrder(t, f, "o2", f.adaID, map[string]any{"buyer": "Ada"})
	seedOrder(t, f, "o3", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"ada@example.com", "bob@example.com"}, "paid_by": f.adaID})
	seedOrder(t, f, "o4", f.bobID, map[string]any{"buyer": "Bob", "members": []any{"bob@example.com"}})

	code, out := f.as(t, f.key, "GET", "/v1/acme/_admin/users/"+f.adaID+"/owned", "")
	if code != 200 {
		t.Fatalf("owned: %d %v", code, out)
	}
	user := out["user"].(map[string]any)
	if user["id"] != f.adaID || user["status"] != "active" {
		t.Errorf("user: %v", user)
	}
	cols := out["collections"].([]any)
	if len(cols) != 3 { // orders, and the file collections with a policy (files_account_test.go): profiles, vault
		t.Fatalf("collections: %v", cols)
	}
	c := cols[0].(map[string]any)
	if c["database"] != "app" || c["collection"] != "orders" || c["action"] != "anonymize" || c["owned"] != float64(2) {
		t.Errorf("orders: %v", c)
	}
	if pull := c["pull"].(map[string]any); pull["members"] != float64(1) {
		t.Errorf("pull: %v", pull)
	}
	if unset := c["unset"].(map[string]any); unset["paid_by"] != float64(1) {
		t.Errorf("unset: %v", unset)
	}
	if rm := c["remove"].([]any); len(rm) != 1 || rm[0] != "phone" {
		t.Errorf("remove: %v", rm)
	}
	if rp := c["replace"].([]any); len(rp) != 1 || rp[0] != "buyer" {
		t.Errorf("replace: %v", rp)
	}
	without := out["without_policy"].([]any)
	if !slices.Contains(without, "app.notes") {
		t.Errorf("without_policy: %v", without)
	}
	for _, n := range without {
		if n == "app.orders" {
			t.Errorf("app.orders has a policy: %v", without)
		}
	}
	// Deactivated users are reported too, and nothing about content is returned.
	if err := f.svc.SetDisabled(context.Background(), "ada@example.com", true); err != nil {
		t.Fatal(err)
	}
	_, out = f.as(t, f.key, "GET", "/v1/acme/_admin/users/"+f.adaID+"/owned", "")
	if out["user"].(map[string]any)["status"] != "deactivated" {
		t.Errorf("status: %v", out["user"])
	}
	// Admin only, and an unknown user is a 404.
	if code, _ := f.as(t, f.ada, "GET", "/v1/acme/_admin/users/"+f.adaID+"/owned", ""); code != 403 && code != 401 {
		t.Errorf("a non-admin: %d", code)
	}
	if code, _ := f.as(t, f.key, "GET", "/v1/acme/_admin/users/nope/owned", ""); code != 404 {
		t.Errorf("an unknown user: %d", code)
	}
	if code, _ := f.as(t, "", "GET", "/v1/acme/_admin/users/"+f.adaID+"/owned", ""); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
}
