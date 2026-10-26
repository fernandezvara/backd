package mongodb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/registry"
)

// hookRealm is a realm with organizations and the three membership hooks, over MongoDB.
func hookRealm(t *testing.T) (*auth.Users, *AuthStore, string) {
	t.Helper()
	client := testClient(t)
	realm := testRealm(t, client)
	root := t.TempDir()
	files := map[string]string{
		realm + "/realm.yaml": `signup: open
organizations:
  roles:
    owner: {manage: [org, members, roles, teams]}
    member: {}
  creator_roles: [owner]
  default_roles: [member]
  hooks:
    on_member_joined: app/hook
    on_member_left: app/hook
    on_roles_changed: app/hook
account:
  on_signup: app/hook
`,
		realm + "/app/notes/schema.json":             `{}`,
		realm + "/app/_functions/deno.json":          `{}`,
		realm + "/app/_functions/hook/function.yaml": "internal: true\nmode: async\n",
		realm + "/app/_functions/hook/index.ts":      "export default () => 1;\n",
	}
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := testLogger()
	if err := (&Provisioner{Client: client, Registry: reg, Log: log}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := NewAuthStore(client, realm)
	svc := &auth.Users{Store: store, Hasher: auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}), Settings: reg.Realms[realm].Settings, Realm: realm, Log: log}
	return svc, store, realm
}

func hookJobs(t *testing.T, store *AuthStore, origin string) []map[string]any {
	t.Helper()
	cur, err := store.jobs().Find(context.Background(), bson.D{{Key: "origin", Value: origin}})
	if err != nil {
		t.Fatal(err)
	}
	var docs []jobDoc
	if err := cur.All(context.Background(), &docs); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, d := range docs {
		var in map[string]any
		raw, _ := json.Marshal(d.Input)
		_ = json.Unmarshal(raw, &in)
		out = append(out, in)
	}
	return out
}

// An organization, its first member and the on_member_joined event are made together.
func TestOrgCreationAndItsHookAreOneTransaction(t *testing.T) {
	svc, store, _ := hookRealm(t)
	ctx := context.Background()
	u, err := svc.Create(ctx, "ada@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.CreateOrg(ctx, auth.Principal{User: u}, "Acme", "acme")
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	_ = m
	joined := hookJobs(t, store, auth.OrgMemberJoinedOrigin)
	if len(joined) != 1 || joined[0]["user"] != u.ID || joined[0]["org"] != m.Org.ID || joined[0]["seq"] != float64(1) {
		t.Fatalf("joined = %v", joined)
	}
}

// A membership change and its event are one transaction: when the event can't be queued, the
// change is not made — nothing is changed without its event, or announced without the change.
func TestMembershipChangeIsAtomicWithItsHook(t *testing.T) {
	svc, store, _ := hookRealm(t)
	ctx := context.Background()
	owner, _ := svc.Create(ctx, "ada@example.com", nil)
	bob, _ := svc.Create(ctx, "bob@example.com", nil)
	m, err := svc.CreateOrg(ctx, auth.Principal{User: owner}, "Acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	org := m.Org.ID

	// Adding bob works and queues exactly one event.
	if _, err := svc.AdminAddMember(ctx, org, bob.ID, []string{"member"}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if n := len(hookJobs(t, store, auth.OrgMemberJoinedOrigin)); n != 2 {
		t.Fatalf("joined events = %d, want 2 (the creator and bob)", n)
	}

	// A change whose event can't be stored is rolled back. The jobs collection refuses the
	// write (a document the validator rejects stands in for any failure of the queue).
	if err := store.db.RunCommand(ctx, bson.D{{Key: "collMod", Value: JobsCollection}, {Key: "validator", Value: bson.D{{Key: "nonexistent_required", Value: bson.D{{Key: "$exists", Value: true}}}}}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatal(err)
	}
	_, err = svc.AdminSetMemberRoles(ctx, org, bob.ID, []string{"owner"})
	if err == nil {
		t.Fatal("a role change whose event can't be queued succeeded")
	}
	mem, merr := store.OrgMember(ctx, org, bob.ID)
	if merr != nil {
		t.Fatal(merr)
	}
	for _, g := range mem.Roles {
		if g.Role == "owner" {
			t.Fatalf("the role change was kept although its event was not queued: %+v", mem.Roles)
		}
	}
	// Removing a member is refused in the same way and keeps the membership.
	if err := svc.AdminRemoveMember(ctx, org, bob.ID); err == nil {
		t.Fatal("a removal whose event can't be queued succeeded")
	}
	if _, merr := store.OrgMember(ctx, org, bob.ID); merr != nil {
		t.Fatalf("the member is gone although the removal failed: %v", merr)
	}
}

// Queuing an event twice — a retried attempt — is one job.
func TestKeyedJobsCollapse(t *testing.T) {
	svc, store, _ := hookRealm(t)
	ctx := context.Background()
	now := time.Now().UTC()
	job := auth.Job{ID: auth.KeyedJobID("test", "a", "1"), Database: "app", Function: "hook", Input: json.RawMessage(`{}`), CallerActor: "system", TimeoutMS: 1000}
	for i := range 3 {
		isNew, err := svc.EnqueueKeyedJob(ctx, job)
		if err != nil || isNew != (i == 0) {
			t.Fatalf("attempt %d: new=%v err=%v", i, isNew, err)
		}
	}
	n, err := store.jobs().CountDocuments(ctx, bson.D{{Key: "_id", Value: job.ID}})
	if err != nil || n != 1 {
		t.Fatalf("%d jobs, %v", n, err)
	}
	_ = now
	if errors.Is(err, auth.ErrJobExists) {
		t.Fatal("unexpected")
	}
}
