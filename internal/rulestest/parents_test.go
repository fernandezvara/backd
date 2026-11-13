package rulestest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

// itemsCollection loads the lists and items of the rules page ("Rules that follow a reference").
func itemsCollection(t *testing.T) *registry.Collection {
	t.Helper()
	root := t.TempDir()
	for p, content := range map[string]string{
		"acme/realm.yaml":                "auth:\n  strategies:\n    password: [password]\n",
		"acme/app/lists/schema.json":     `{"type":"object","properties":{"name":{"type":"string"},"members":{"type":"array","items":{"type":"string"}}},"required":["name"]}`,
		"acme/app/lists/collection.yaml": "realtime: true\nrules:\n  read: user != nil && user.id in document.members\n",
		"acme/app/items/schema.json":     `{"type":"object","properties":{"title":{"type":"string"},"list_id":{"type":"string"}},"required":["title","list_id"]}`,
		"acme/app/items/collection.yaml": `references:
  list: {field: list_id, collection: lists}
rules:
  read: user != nil && user.id in parent(document.list_id).members
  create: user != nil && user.id in parent(data.list_id).members
  update: >
    user != nil && user.id in parent(document.list_id).members
    && user.id in parent(data.list_id).members
  delete: user != nil && user.id in parent(document.list_id).members
`,
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
	c, _ := reg.Collection("acme", "app", "items")
	return c
}

// The fixture of the rules page: it keeps the documentation honest.
const parentFixture = `users:
  ada: { id: u1 }
  bob: { id: u2 }
parents:
  list:
    groceries: { name: Groceries, members: [u1] }
    chores: { name: Chores, members: [u2] }
    old: { name: Old, members: [u1], _meta: { deleted_at: "2026-01-01T00:00:00Z" } }
documents:
  milk: { title: Milk, list_id: groceries }
  sweep: { title: Sweep, list_id: chores }
  stray: { title: Stray, list_id: old }
  lost: { title: Lost, list_id: nowhere }
tests:
  - as: ada
    read: { allow: [milk], deny: [sweep, stray, lost] }
    create:
      - { data: { title: Eggs, list_id: groceries }, expect: allow }
      - { data: { title: Mop, list_id: chores }, expect: deny }
      - { data: { title: Hmm, list_id: old }, expect: deny }
    update:
      - { document: milk, patch: { title: Oat milk }, expect: allow }
      - { document: milk, patch: { list_id: chores }, expect: deny }
    delete: { allow: [milk], deny: [sweep] }
  - as: bob
    read: { allow: [sweep], deny: [milk] }
  - as: anonymous
    read: { allow: [], deny: [milk, sweep] }
`

func TestParentRulesFixture(t *testing.T) {
	checks, problems := Run(itemsCollection(t), nil, []byte(parentFixture))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for _, c := range checks {
		if !c.Pass {
			t.Errorf("%s: %s", c.What, c.Detail)
		}
	}
	if len(checks) < 15 {
		t.Errorf("only %d checks ran", len(checks))
	}
}

func TestParentFixtureProblems(t *testing.T) {
	_, problems := Run(itemsCollection(t), nil, []byte("parents:\n  nope: {}\ntests:\n  - as: anonymous\n    read: { allow: [], deny: [] }\n"))
	if len(problems) == 0 || !strings.Contains(problems[0].Error(), "no reference by that name") {
		t.Fatalf("problems = %v", problems)
	}
}
