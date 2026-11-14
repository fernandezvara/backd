package mongodb

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/storage"
)

// CountBelow and ScanBelow find the documents written under a version below the current one:
// those with no stamp (version 1), soft-deleted ones included, in id order, through the index.
func TestSchemaMigratorScansBelowTheCurrentVersion(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	reg := loadRegistryWith(t, realm, "", map[string]string{
		"app/people/schema.v1.json": softSchema,
		"app/people/schema.v2.json": softSchema,
		"app/people/schema.v3.json": softSchema,
		"app/people/schema.v2.ts":   "export default (d: unknown) => d;",
		"app/people/schema.v3.ts":   "export default (d: unknown) => d;",
	})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "people")
	repo := (&Store{Client: client}).Repository(c)
	mig := repo.(storage.SchemaMigrator)

	now := time.Now().UTC().Truncate(time.Millisecond)
	put := func(id string, stamp any, deleted bool) {
		meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
		if stamp != nil {
			meta["schema_version"] = stamp
		}
		if deleted {
			meta["deleted_at"] = now
		}
		if err := repo.Create(ctx, storage.Document{"id": id, "_meta": meta, "email": id + "@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	put("a1", nil, false)      // no stamp: version 1
	put("a2", int64(1), false) // version 1
	put("a3", int64(2), false) // version 2
	put("a4", int64(3), false) // current
	put("a5", nil, true)       // in the trash
	put("a6", int64(2), true)

	n, err := mig.CountBelow(ctx, 3)
	if err != nil || n != 5 {
		t.Fatalf("below 3 = %d, %v, want 5", n, err)
	}
	if n, _ := mig.CountBelow(ctx, 2); n != 3 {
		t.Errorf("below 2 = %d, want 3 (no stamp, 1 and the trashed one)", n)
	}
	if n, _ := mig.CountBelow(ctx, 1); n != 0 {
		t.Errorf("below 1 = %d, want 0", n)
	}
	var seen []string
	after := ""
	for {
		docs, err := mig.ScanBelow(ctx, 3, after, 2) // batches of two
		if err != nil {
			t.Fatal(err)
		}
		if len(docs) == 0 {
			break
		}
		for _, d := range docs {
			seen = append(seen, d["id"].(string))
			after = d["id"].(string)
		}
	}
	if fmt.Sprint(seen) != "[a1 a2 a3 a5 a6]" {
		t.Errorf("scanned %v, want [a1 a2 a3 a5 a6] in id order", seen)
	}

	// Written back as the job does: the same document at version 3, nothing left below.
	d, err := repo.Get(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	meta := d["_meta"].(map[string]any)
	meta["version"], meta["schema_version"] = int64(2), int64(3)
	if err := repo.Replace(ctx, d, 1); err != nil {
		t.Fatal(err)
	}
	if n, _ := mig.CountBelow(ctx, 3); n != 4 {
		t.Errorf("below 3 after a write = %d, want 4", n)
	}
	// The count is answered by the index, not a scan.
	var plan bson.M
	cmd := bson.D{{Key: "explain", Value: bson.D{{Key: "count", Value: "people"}, {Key: "query", Value: belowVersion(3)}}}, {Key: "verbosity", Value: "queryPlanner"}}
	if err := client.Database(c.MongoDatabase).RunCommand(ctx, cmd).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if text := fmt.Sprint(plan); !strings.Contains(text, "IXSCAN") || strings.Contains(text, "COLLSCAN") {
		t.Errorf("the count should use the index on _meta.schema_version: %s", text)
	}
}

// The job reads documents with their encrypted fields opened and writes the migrated document
// back with them sealed again (and the blind index of a searchable one rebuilt).
func TestSchemaMigrationRoundTripsEncryptedFields(t *testing.T) {
	_, repo := encryptedFixture(t)
	ctx := context.Background()
	var mig storage.SchemaMigrator = repo
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := insert(t, repo, map[string]any{"code": "ada", "secret": "s3cret", "plain": "p"}, now, "u1")

	docs, err := mig.ScanBelow(ctx, 2, "", 10)
	if err != nil || len(docs) != 1 {
		t.Fatalf("scan: %v %v", docs, err)
	}
	d := docs[0]
	if d["code"] != "ada" || d["secret"] != "s3cret" {
		t.Fatalf("the job must see the plain values: %v", d)
	}
	// The migration changes a searchable encrypted value and the plain one.
	d["code"], d["plain"] = "grace", "migrated"
	meta := d["_meta"].(map[string]any)
	meta["version"], meta["schema_version"] = int64(2), int64(2)
	if err := repo.Replace(ctx, d, 1); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil || got["code"] != "grace" || got["secret"] != "s3cret" || got["plain"] != "migrated" {
		t.Fatalf("read back: %v %v", got, err)
	}
	raw, err := repo.Get(storage.WithRawEncrypted(ctx), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, plain := raw["code"].(string); plain {
		t.Errorf("code is stored in plain: %v", raw["code"])
	}
	if _, plain := raw["secret"].(string); plain {
		t.Errorf("secret is stored in plain: %v", raw["secret"])
	}
	if raw["plain"] != "migrated" {
		t.Errorf("plain = %v", raw["plain"])
	}
	// Its blind index follows the new value: an equality search finds it by "grace", not "ada".
	found := func(value string) int {
		page, err := repo.List(ctx, storage.Query{Filter: storage.Condition{Field: "code", Op: storage.OpEq, Value: value}, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	if found("grace") != 1 || found("ada") != 0 {
		t.Errorf("blind index: grace=%d ada=%d, want 1 and 0", found("grace"), found("ada"))
	}
	if n, _ := mig.CountBelow(ctx, 2); n != 0 {
		t.Errorf("below 2 = %d after the write", n)
	}
}

// A filter on _meta.schema_version reads a document without the stamp as version 1, so "below 2"
// finds what nobody has stamped yet.
func TestFilterOnTheSchemaStampReadsAMissingStampAsOne(t *testing.T) {
	client := testClient(t)
	realm := testRealm(t, client)
	ctx := context.Background()
	log, _ := testLogger()
	reg := loadRegistryWith(t, realm, "", map[string]string{
		"app/people/schema.v1.json": softSchema,
		"app/people/schema.v2.json": softSchema,
		"app/people/schema.v2.ts":   "export default (d: unknown) => d;",
	})
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Collection(realm, "app", "people")
	repo := (&Store{Client: client}).Repository(c)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for id, stamp := range map[string]any{"a": nil, "b": int64(1), "c": int64(2)} {
		meta := map[string]any{"created_at": now, "updated_at": now, "version": int64(1)}
		if stamp != nil {
			meta["schema_version"] = stamp
		}
		if err := repo.Create(ctx, storage.Document{"id": id, "_meta": meta, "email": id + "@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(f storage.Filter) string {
		page, err := repo.List(ctx, storage.Query{Filter: f, Limit: 10, Sort: []storage.SortField{{Field: "id"}}})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range page.Items {
			out = append(out, d["id"].(string))
		}
		return fmt.Sprint(out)
	}
	cond := func(op storage.Operator, v any) storage.Filter {
		return storage.Condition{Field: "_meta.schema_version", Op: op, Value: v}
	}
	for name, tc := range map[string]struct {
		f    storage.Filter
		want string
	}{
		"= 1":          {cond(storage.OpEq, int64(1)), "[a b]"},
		"= 2":          {cond(storage.OpEq, int64(2)), "[c]"},
		"< 2":          {cond(storage.OpLt, int64(2)), "[a b]"},
		">= 2":         {cond(storage.OpGte, int64(2)), "[c]"},
		"!= 1":         {cond(storage.OpNe, int64(1)), "[c]"},
		"in [1, 2]":    {cond(storage.OpIn, []any{int64(1), int64(2)}), "[a b c]"},
		"not in [1]":   {cond(storage.OpNin, []any{int64(1)}), "[c]"},
		"between 1..1": {cond(storage.OpBetween, []any{int64(1), int64(1)}), "[a b]"},
		"is null":      {cond(storage.OpIsNull, true), "[]"},
		"is not null":  {cond(storage.OpIsNull, false), "[a b c]"},
	} {
		if got := ids(tc.f); got != tc.want {
			t.Errorf("%s = %s, want %s", name, got, tc.want)
		}
	}
}
