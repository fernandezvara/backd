package mongodb

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/fernandezvara/backd/internal/storage"
)

const eraseSchema = `{
  "type": "object",
  "properties": {
    "buyer_name": {"type": "string"},
    "phone":      {"type": "string"},
    "total":      {"type": "integer"},
    "members":    {"type": "array", "items": {"type": "string"}},
    "paid_by":    {"type": "string"}
  },
  "additionalProperties": false
}`

const erasePolicy = `
on_owner_delete:
  action: anonymize
  remove: [phone]
  replace: {buyer_name: "Erased customer", total: 0}
  pull: {members: email}
  unset: {paid_by: id}
`

func eraseFixture(t *testing.T) (storage.Eraser, storage.Repository, context.Context) {
	t.Helper()
	client := testClient(t)
	realm := testRealm(t, client)
	log, _ := testLogger()
	reg := loadRegistryWith(t, realm, "signup: open\n", map[string]string{"app/orders": eraseSchema, "app/orders/collection.yaml": erasePolicy})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c, _ := reg.Collection(realm, "app", "orders")
	repo := (&Store{Client: client}).Repository(c)

	// The indexes an erase searches by are created by provisioning.
	existing, err := listIndexes(context.Background(), client.Database(c.MongoDatabase).Collection(c.Name))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range existing {
		keys = append(keys, e.keys)
	}
	for _, want := range []string{"_meta.owner:1", "members:1", "paid_by:1"} {
		if !slices.Contains(keys, want) {
			t.Errorf("index %s missing; have %v", want, keys)
		}
	}
	return repo.(storage.Eraser), repo, context.Background()
}

func ownedDoc(owner string, fields map[string]any) storage.Document {
	d := newDoc(fields)
	d["_meta"].(map[string]any)["owner"] = owner
	d["_meta"].(map[string]any)["created_by"] = "user:" + owner
	d["_meta"].(map[string]any)["updated_by"] = "user:" + owner
	return d
}

func TestEraserOnMongoDB(t *testing.T) {
	eraser, repo, ctx := eraseFixture(t)
	now := time.Date(2126, 10, 1, 12, 0, 0, 0, time.UTC)
	mine := []storage.Document{
		ownedDoc("u1", map[string]any{"buyer_name": "Ada Lovelace", "phone": "555", "total": int64(1250)}),
		ownedDoc("u1", map[string]any{"buyer_name": "Ada Lovelace", "total": int64(300)}),
		ownedDoc("u1", map[string]any{"buyer_name": "Ada Lovelace", "total": int64(99)}),
	}
	others := []storage.Document{
		ownedDoc("u2", map[string]any{"buyer_name": "Bob", "members": []any{"ada@example.com", "bob@example.com"}, "paid_by": "u1"}),
		ownedDoc("u2", map[string]any{"buyer_name": "Bob", "members": []any{"bob@example.com"}, "paid_by": "u2"}),
		ownedDoc("u3", map[string]any{"buyer_name": "Cy", "members": []any{"ada@example.com"}, "paid_by": "u1"}),
	}
	for _, d := range append(slices.Clone(mine), others...) {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := eraser.CountOwned(ctx, "u1"); err != nil || n != 3 {
		t.Errorf("CountOwned: %d %v", n, err)
	}
	if n, _ := eraser.CountReferences(ctx, "members", "ada@example.com", true); n != 2 {
		t.Errorf("CountReferences members: %d", n)
	}
	if n, _ := eraser.CountReferences(ctx, "paid_by", "u1", false); n != 2 {
		t.Errorf("CountReferences paid_by: %d", n)
	}

	// Anonymizing, in batches: each call touches at most `limit` documents, and the
	// ones it finished no longer match, so repeating converges.
	var total int64
	for range 5 {
		n, err := eraser.AnonymizeOwned(ctx, "u1", []string{"phone"}, map[string]any{"buyer_name": "Erased customer", "total": int64(0)}, 2, now)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n == 0 {
			break
		}
	}
	if total != 3 {
		t.Errorf("anonymized %d documents, want 3", total)
	}
	got, _ := repo.Get(ctx, mine[0]["id"].(string))
	meta := got["_meta"].(map[string]any)
	if got["buyer_name"] != "Erased customer" || got["total"] != int64(0) || got["phone"] != nil || meta["owner"] != nil ||
		meta["updated_by"] != storage.ErasedBy || meta["version"] != int64(2) || !meta["updated_at"].(time.Time).Equal(now) || meta["created_by"] != "user:u1" {
		t.Errorf("an anonymized document: %#v", got)
	}
	if _, has := got["phone"]; has {
		t.Errorf("phone should be removed: %#v", got)
	}
	if n, _ := eraser.CountOwned(ctx, "u1"); n != 0 {
		t.Errorf("owned after anonymizing: %d", n)
	}
	// Someone else's document is untouched.
	if other, _ := repo.Get(ctx, others[0]["id"].(string)); other["buyer_name"] != "Bob" || other["_meta"].(map[string]any)["version"] != int64(1) {
		t.Errorf("another user's document: %#v", other)
	}

	// References in documents of other users.
	var pulled, cleared int64
	for range 3 {
		n, err := eraser.PullReference(ctx, "members", "ada@example.com", 1, now)
		if err != nil {
			t.Fatal(err)
		}
		pulled += n
		if n == 0 {
			break
		}
	}
	for range 3 {
		n, err := eraser.ClearReference(ctx, "paid_by", "u1", 5, now)
		if err != nil {
			t.Fatal(err)
		}
		cleared += n
		if n == 0 {
			break
		}
	}
	if pulled != 2 || cleared != 2 {
		t.Errorf("pulled %d, cleared %d, want 2 and 2", pulled, cleared)
	}
	first, _ := repo.Get(ctx, others[0]["id"].(string))
	if m, _ := first["members"].([]any); len(m) != 1 || m[0] != "bob@example.com" {
		t.Errorf("members after pulling: %v", first["members"])
	}
	if _, has := first["paid_by"]; has || first["_meta"].(map[string]any)["updated_by"] != storage.ErasedBy {
		t.Errorf("paid_by after clearing: %#v", first)
	}
	if untouched, _ := repo.Get(ctx, others[1]["id"].(string)); untouched["paid_by"] != "u2" || untouched["_meta"].(map[string]any)["version"] != int64(1) {
		t.Errorf("a document that held nobody erased: %#v", untouched)
	}
	if n, _ := eraser.CountReferences(ctx, "members", "ada@example.com", true); n != 0 {
		t.Errorf("references left: %d", n)
	}

	// Deleting.
	for _, d := range []storage.Document{ownedDoc("u4", nil), ownedDoc("u4", nil), ownedDoc("u4", nil)} {
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	var deleted int64
	for range 5 {
		n, err := eraser.DeleteOwned(ctx, "u4", 2)
		if err != nil {
			t.Fatal(err)
		}
		deleted += n
		if n == 0 {
			break
		}
	}
	if deleted != 3 {
		t.Errorf("deleted %d, want 3", deleted)
	}
	if n, _ := eraser.CountOwned(ctx, "u2"); n != 2 {
		t.Errorf("another owner's documents were deleted: %d", n)
	}
}
