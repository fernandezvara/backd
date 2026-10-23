package mongodb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/storage"
)

// plainDocs stores documents the way they were before the fields were encrypted: plaintext, past
// the validator that now expects binary.
func plainDocs(t *testing.T, repo *Repository, n int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	for i := range n {
		id := fmt.Sprintf("d%04d", i)
		ids = append(ids, id)
		_, err := repo.coll.InsertOne(ctx, bson.D{
			{Key: "_id", Value: id},
			{Key: "_meta", Value: bson.D{{Key: "created_at", Value: time.Now()}, {Key: "updated_at", Value: time.Now()}, {Key: "version", Value: int64(1)}}},
			{Key: "status", Value: "s"}, {Key: "code", Value: fmt.Sprintf("C%d", i%3)}, {Key: "secret", Value: fmt.Sprintf("secret-%d", i)},
		}, options.InsertOne().SetBypassDocumentValidation(true))
		if err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func migrateAll(t *testing.T, repo *Repository, batch int, o storage.MigrateOptions) storage.MigrateCounts {
	t.Helper()
	var total storage.MigrateCounts
	after := ""
	for {
		res, err := repo.MigrateBatch(context.Background(), after, batch, o)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(res.Counts)
		if res.Counts.Scanned < int64(batch) {
			return total
		}
		after = res.Last
	}
}

func statusOf(t *testing.T, repo *Repository, path string) storage.FieldStatus {
	t.Helper()
	all, err := repo.EncryptionStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("no status for %s in %+v", path, all)
	return storage.FieldStatus{}
}

func TestMigrationTurnsPlaintextIntoEncryptedValuesAndBuildsIndexes(t *testing.T) {
	_, repo := encryptedFixture(t)
	ctx := context.Background()
	plainDocs(t, repo, 7)
	if st := statusOf(t, repo, "secret"); st.Plaintext != 7 || st.Encrypted != 0 {
		t.Fatalf("before: %+v", st)
	}
	// Reads accept what is stored, and a search misses the documents that have no index yet.
	if page, err := repo.List(ctx, storage.Query{Limit: 20}); err != nil || len(page.Items) != 7 || page.Items[0]["secret"] != "secret-0" {
		t.Fatalf("read of plaintext: %v %v", page.Items, err)
	}
	search := storage.Condition{Field: "code", Op: storage.OpEq, Value: "C1"}
	if page, _ := repo.List(ctx, storage.Query{Filter: search, Limit: 20}); len(page.Items) != 0 {
		t.Errorf("a search found %d documents that have no blind index", len(page.Items))
	}

	// A dry run says what it would do and does it nowhere.
	dry := migrateAll(t, repo, 3, storage.MigrateOptions{DryRun: true})
	if dry.Scanned != 7 || dry.Encrypted != 14 || dry.Indexed != 7 {
		t.Errorf("dry run = %+v", dry)
	}
	if st := statusOf(t, repo, "secret"); st.Plaintext != 7 {
		t.Errorf("a dry run changed the data: %+v", st)
	}

	// Half way (two batches of three), the documents migrated are found and the others not.
	first, err := repo.MigrateBatch(ctx, "", 3, storage.MigrateOptions{})
	if err != nil || first.Counts.Encrypted != 6 {
		t.Fatalf("first batch: %+v %v", first, err)
	}
	if page, _ := repo.List(ctx, storage.Query{Filter: search, Limit: 20}); len(page.Items) != 1 { // d0001 only
		t.Errorf("half way, a search found %d", len(page.Items))
	}
	if st := statusOf(t, repo, "secret"); st.Plaintext != 4 || st.Encrypted != 3 {
		t.Errorf("half way: %+v", st)
	}

	// Going on from where it reached finishes; the results are the same as having done it at once.
	rest := migrateAll(t, repo, 3, storage.MigrateOptions{})
	_ = rest
	for _, p := range []string{"secret", "code"} {
		if st := statusOf(t, repo, p); st.Plaintext != 0 || st.Encrypted != 7 || st.Current != 7 || st.Old != 0 {
			t.Errorf("%s after: %+v", p, st)
		}
	}
	if st := statusOf(t, repo, "code"); st.Indexed != 7 {
		t.Errorf("index coverage: %+v", st)
	}
	page, _ := repo.List(ctx, storage.Query{Filter: search, Limit: 20})
	if len(page.Items) != 2 { // d0001, d0004
		t.Errorf("after: a search found %d", len(page.Items))
	}
	if got, _ := repo.Get(ctx, "d0005"); got["secret"] != "secret-5" || got["code"] != "C2" || got["status"] != "s" {
		t.Errorf("a migrated document reads the same: %v", got)
	}
	// What is stored is ciphertext, the version did not move, and nothing else changed.
	var raw bson.D
	if err := repo.coll.FindOne(ctx, bson.D{{Key: "_id", Value: "d0005"}}).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	for _, e := range raw {
		if (e.Key == "secret" || e.Key == "code") && e.Value.(bson.Binary).Subtype != 6 {
			t.Errorf("%s = %v", e.Key, e.Value)
		}
		if e.Key == "_meta" && versionOfRaw(raw) != 1 {
			t.Error("the version moved")
		}
	}
	// A second run finds nothing to do.
	if again := migrateAll(t, repo, 3, storage.MigrateOptions{}); again.Encrypted+again.Reencrypted+again.Decrypted+again.Indexed+again.Marked != 0 {
		t.Errorf("a second run changed %+v", again)
	}
}

func TestRekeyingStartsAGenerationAndMigrationMovesTheValuesAndRetiresTheOld(t *testing.T) {
	_, repo := encryptedFixture(t)
	ctx := context.Background()
	for i := range 4 {
		insert(t, repo, map[string]any{"code": fmt.Sprintf("C%d", i), "secret": fmt.Sprintf("s%d", i)}, time.Now(), nil)
	}
	gens, _ := repo.Generations(ctx, "secret")
	if len(gens) != 1 || gens[0].Generation != 1 || gens[0].Values != 4 || gens[0].Status != "active" {
		t.Fatalf("generations: %+v", gens)
	}
	g, err := repo.Rekey(ctx, "secret")
	if err != nil || g.Generation != 2 || g.Status != "active" {
		t.Fatalf("rekey: %+v %v", g, err)
	}
	// New writes use the new key; old values stay readable under the old one.
	insert(t, repo, map[string]any{"code": "C9", "secret": "fresh"}, time.Now(), nil)
	st := statusOf(t, repo, "secret")
	if st.Current != 1 || st.Old != 4 || st.Encrypted != 5 {
		t.Errorf("after the rekey: %+v", st)
	}
	for _, d := range all(t, repo, nil) {
		if d["secret"] == nil {
			t.Fatalf("an old value can't be read: %v", d)
		}
	}
	if gens, _ = repo.Generations(ctx, "secret"); gens[0].Status != "decrypt_only" || gens[1].Status != "active" || gens[0].Values != 4 || gens[1].Values != 1 {
		t.Errorf("generations after the rekey: %+v", gens)
	}
	// Nothing retires while values are left under the old key.
	if ids, _ := repo.Retire(ctx); len(ids) != 0 {
		t.Errorf("retired %v with values left", ids)
	}

	counts := migrateAll(t, repo, 2, storage.MigrateOptions{})
	if counts.Reencrypted != 4 || counts.Encrypted != 0 {
		t.Errorf("counts = %+v", counts)
	}
	st = statusOf(t, repo, "secret")
	if st.Current != 5 || st.Old != 0 {
		t.Errorf("after: %+v", st)
	}
	ids, err := repo.Retire(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("retire: %v %v", ids, err)
	}
	if gens, _ = repo.Generations(ctx, "secret"); gens[0].Status != "retired" || gens[1].Status != "active" {
		t.Errorf("generations after: %+v", gens)
	}
}

func TestAFieldThatIsNoLongerEncryptedIsOpenedOnReadAndByTheMigration(t *testing.T) {
	c, repo := encryptedFixture(t)
	ctx := context.Background()
	for i := range 5 {
		insert(t, repo, map[string]any{"code": fmt.Sprintf("C%d", i), "secret": fmt.Sprintf("s%d", i)}, time.Now(), nil)
	}
	// The configuration drops `secret` (and keeps `code`).
	if c.Encrypted[0].Path != "code" {
		t.Fatalf("fixture order: %+v", c.Encrypted)
	}
	dropped := *c
	dropped.Encrypted = []registry.EncryptedField{c.Encrypted[0]}
	repo2 := &Repository{coll: repo.coll, c: &dropped, ring: repo.ring}
	repo2.ring.Reset()
	// Provisioning the new configuration relaxes the validator: `secret` is plain text again.
	log, _ := testLogger()
	v := (&Provisioner{Log: log}).validator(&dropped)
	if err := repo.coll.Database().RunCommand(ctx, bson.D{{Key: "collMod", Value: repo.coll.Name()}, {Key: "validator", Value: v}, {Key: "validationLevel", Value: "moderate"}}).Err(); err != nil {
		t.Fatal(err)
	}

	// Reads still open it.
	for _, d := range all(t, repo2, nil) {
		if s, _ := d["secret"].(string); len(s) != 2 || s[0] != 's' {
			t.Fatalf("a field that stopped being encrypted can't be read: %v", d)
		}
	}
	st := statusOf(t, repo2, "secret")
	if st.Configured || st.Encrypted != 5 {
		t.Errorf("status: %+v", st)
	}
	// A write goes through the configuration: plaintext, and not marked.
	insert(t, repo2, map[string]any{"code": "C7", "secret": "plain"}, time.Now(), nil)
	if st = statusOf(t, repo2, "secret"); st.Plaintext != 1 || st.Encrypted != 5 {
		t.Errorf("after a write: %+v", st)
	}
	counts := migrateAll(t, repo2, 2, storage.MigrateOptions{Fields: []string{"code"}})
	if counts.Decrypted != 5 {
		t.Errorf("counts = %+v", counts)
	}
	if st = statusOf(t, repo2, "secret"); st.Encrypted != 0 || st.Plaintext != 6 {
		t.Errorf("after: %+v", st)
	}
	var raw bson.D
	if err := repo.coll.FindOne(ctx, bson.D{{Key: "secret", Value: bson.D{{Key: "$exists", Value: true}}}}).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	for _, e := range raw {
		if _, isBin := e.Value.(bson.Binary); isBin && e.Key == "secret" {
			t.Error("the value is still binary")
		}
	}
	// Nothing is left: the old key retires.
	if ids, err := repo2.Retire(ctx); err != nil || len(ids) != 1 {
		t.Errorf("retire: %v %v", ids, err)
	}
}

func TestAMigrationThatDiesGoesOnFromItsPlace(t *testing.T) {
	_, repo := encryptedFixture(t)
	plainDocs(t, repo, 30)
	// One worker reads 12 and dies; another, with nothing in memory, goes on after the last.
	res, err := repo.MigrateBatch(context.Background(), "", 12, storage.MigrateOptions{})
	if err != nil || res.Last != "d0011" {
		t.Fatalf("first: %+v %v", res, err)
	}
	repo.ring.Reset()
	second := &Repository{coll: repo.coll, c: repo.c, ring: repo.ring}
	var total storage.MigrateCounts
	after := res.Last
	for {
		r, err := second.MigrateBatch(context.Background(), after, 7, storage.MigrateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(r.Counts)
		if r.Counts.Scanned < 7 {
			break
		}
		after = r.Last
	}
	if total.Scanned != 18 || total.Encrypted != 36 || total.Indexed != 18 {
		t.Errorf("the rest = %+v", total)
	}
	// A pass over everything again, as a restarted job without its place would make, changes nothing.
	if again := migrateAll(t, second, 50, storage.MigrateOptions{}); again.Encrypted+again.Reencrypted+again.Indexed != 0 {
		t.Errorf("a repeated pass changed %+v", again)
	}
	if st := statusOf(t, second, "secret"); st.Encrypted != 30 || st.Plaintext != 0 {
		t.Errorf("status: %+v", st)
	}
	// A document someone wrote meanwhile is a conflict, left to its writer.
	plainDocs2 := func() {
		_, err := second.coll.InsertOne(context.Background(), bson.D{{Key: "_id", Value: "z1"}, {Key: "_meta", Value: bson.D{{Key: "version", Value: int64(1)}}}, {Key: "secret", Value: "x"}}, options.InsertOne().SetBypassDocumentValidation(true))
		if err != nil {
			t.Fatal(err)
		}
	}
	plainDocs2()
	two := int64(2)
	_ = two
	got, err := second.MigrateBatch(context.Background(), "d0029", 10, storage.MigrateOptions{})
	if err != nil || got.Counts.Encrypted != 1 {
		t.Errorf("late document: %+v %v", got, err)
	}
}
