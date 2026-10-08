package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rs/xid"

	backdauth "github.com/fernandezvara/backd/internal/auth"
)

// An erase against a real MongoDB: the policy's indexes, the batches, the
// per-document updates and the tombstone, end to end through HTTP and a worker.
func TestEraseAgainstMongoDB(t *testing.T) {
	realm := "t" + xid.New().String()
	f := mongoFixture(t, map[string]string{
		realm + "/realm.yaml":                 "signup: open\n",
		realm + "/app/orders/schema.json":     `{"type": "object", "properties": {"buyer": {"type": "string"}, "phone": {"type": "string"}, "members": {"type": "array", "items": {"type": "string"}}, "paid_by": {"type": "string"}}, "additionalProperties": false}`,
		realm + "/app/orders/collection.yaml": rulesSection("read: user != nil\ncreate: user != nil\nupdate: user != nil\ndelete: user != nil\n") + "on_owner_delete:\n  action: anonymize\n  remove: [phone]\n  replace: {buyer: \"Erased customer\"}\n  pull: {members: email}\n  unset: {paid_by: id}\n",
		realm + "/app/notes/schema.json":      `{}`,
	})
	ctx := context.Background()
	svc := f.users[realm]
	base := "/v1/" + realm
	signup := func(email string) (string, string) {
		_, out := f.do(t, "POST", base+"/_auth/signup", `{"email": "`+email+`", "password": "dev-p4ssw0rd!"}`)
		return out["token"].(string), out["user"].(map[string]any)["id"].(string)
	}
	ada, adaID := signup("ada@example.com")
	bob, bobID := signup("bob@example.com")
	_, key, err := svc.CreateAPIKey(ctx, "ops", backdauth.KeyOptions{Role: backdauth.KeyRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	as := func(tok, method, path, body string) (int, map[string]any) {
		rec, out := f.doH(t, method, path, body, bearer(tok))
		return rec.Code, out
	}
	create := func(tok, body string) string {
		code, out := as(tok, "POST", base+"/app/orders", body)
		if code != http.StatusCreated {
			t.Fatalf("create: %d %v", code, out)
		}
		return out["id"].(string)
	}
	// Ada owns 450 orders (more than two batches); Bob has one that mentions her.
	for range 450 {
		create(ada, `{"buyer": "Ada Lovelace", "phone": "555"}`)
	}
	shared := create(bob, `{"buyer": "Bob", "members": ["ada@example.com", "bob@example.com"], "paid_by": "`+adaID+`"}`)
	mine := create(bob, `{"buyer": "Bob", "members": ["bob@example.com"], "paid_by": "`+bobID+`"}`)

	code, owned := as(key, "GET", base+"/_admin/users/"+adaID+"/owned", "")
	col := owned["collections"].([]any)[0].(map[string]any)
	if code != 200 || col["owned"] != float64(450) || col["pull"].(map[string]any)["members"] != float64(1) || col["unset"].(map[string]any)["paid_by"] != float64(1) {
		t.Fatalf("owned: %d %v", code, owned)
	}

	code, out := as(key, "DELETE", base+"/_admin/users/"+adaID, "")
	if code != http.StatusAccepted {
		t.Fatalf("erase: %d %v", code, out)
	}
	jobID := out["id"].(string)
	w := NewWorker(Config{Registry: f.reg, Store: f.backing, Users: func(r string) *backdauth.Users { return f.users[r] }}, "t")
	if !w.RunOnce(ctx) {
		t.Fatal("no erase job")
	}

	job, _, _ := svc.GetJob(ctx, jobID)
	if job.Result == nil || job.Result.Status != "ok" || job.Erase.Email != "" ||
		job.Erase.Counts["app/orders/anonymized"] != 450 || job.Erase.Counts["app/orders/pulled"] != 1 || job.Erase.Counts["app/orders/cleared"] != 1 {
		t.Fatalf("job: %+v / %+v", job, job.Erase)
	}
	// None of her orders is left owned, and they hold no phone.
	_, page := as(key, "GET", base+"/app/orders?limit=100&where="+url.QueryEscape(`{"phone": {"$ne": null}}`), "")
	if items, _ := page["items"].([]any); len(items) != 0 {
		t.Errorf("a phone is left: %v", items)
	}
	_, page = as(key, "GET", base+"/app/orders?limit=1&count=true&where="+url.QueryEscape(`{"buyer": "Erased customer"}`), "")
	if page["total"] != float64(450) {
		t.Errorf("anonymized orders: %v", page["total"])
	}
	// Bob's documents: she is out of the shared one, the other is untouched.
	_, doc := as(bob, "GET", base+"/app/orders/"+shared, "")
	if m := doc["members"].([]any); len(m) != 1 || m[0] != "bob@example.com" || doc["paid_by"] != nil || doc["_meta"].(map[string]any)["updated_by"] != "backd:erase" {
		t.Errorf("the shared order: %v", doc)
	}
	_, doc = as(bob, "GET", base+"/app/orders/"+mine, "")
	if doc["paid_by"] != bobID || doc["_meta"].(map[string]any)["version"] != float64(1) {
		t.Errorf("Bob's own order: %v", doc)
	}
	// She can't sign in; her address is free for a new person.
	if code, _ := as(ada, "GET", base+"/_auth/me", ""); code != http.StatusUnauthorized {
		t.Errorf("her session: %d", code)
	}
	rec, _ := f.do(t, "POST", base+"/_auth/signup", `{"email": "ada@example.com", "password": "dev-p4ssw0rd!2"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("signing up again: %d %s", rec.Code, rec.Body)
	}
	if recs, _, _ := svc.AuditTrail(ctx, backdauth.AuditFilter{Action: backdauth.AuditUserErased}); len(recs) != 1 || strings.Contains(jsonString(recs[0].Details), "ada@example.com") {
		t.Errorf("audit: %+v", recs)
	}
}
