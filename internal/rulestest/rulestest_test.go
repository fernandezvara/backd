package rulestest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandezvara/backd/internal/registry"
)

const schema = `{"type":"object","properties":{
  "title":{"type":"string","minLength":1},"published":{"type":"boolean"},"status":{"type":"string"},
  "members":{"type":"array","items":{"type":"string"}}},"required":["title"],"additionalProperties":false}`

// collection loads a one-collection config: the schema, the rules, and a realm
// that declares the roles admin and staff.
func collection(t *testing.T, rulesSrc string) *registry.Collection {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "acme", "app", "posts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for p, content := range map[string]string{
		filepath.Join(root, "acme", "realm.yaml"): "roles:\n  admin: {}\n  staff: {}\n",
		filepath.Join(dir, "schema.json"):         schema,
		filepath.Join(dir, "rules.yaml"):          rulesSrc,
	} {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Collection("acme", "app", "posts")
	if !ok {
		t.Fatal("no collection")
	}
	return c
}

var roles = []string{"admin", "staff"}

const goodRules = `
read: document.published == true || (user != nil && document._meta.owner == user.id) || hasRole(user, 'staff')
create: user != nil && user.email_verified
update: user != nil && document._meta.owner == user.id && !('status' in changed())
delete: hasRole(user, 'admin') || (user != nil && document._meta.owner == user.id && document._meta.created_at > now - duration('1h'))
`

func TestRunDecisions(t *testing.T) {
	c := collection(t, goodRules)
	checks, problems := Run(c, roles, []byte(`
now: 2026-10-04T12:00:00Z
users:
  ada:   { id: u1, email: ada@example.com }
  bob:   { id: u2, email: bob@example.com, roles: [staff] }
  newbie: { id: u3, email: n@example.com, email_verified: false }
  boss:  { id: u4, email: boss@example.com, roles: [admin] }
documents:
  mine:      { title: Mine, published: false, _meta: { owner: u1 } }
  public:    { title: Public, published: true, _meta: { owner: u2 } }
  old:       { title: Old, published: true, _meta: { owner: u1, created_at: "2026-10-01T00:00:00Z" } }
tests:
  - as: anonymous
    read:   { allow: [public], deny: [mine] }
    create: [ { data: { title: x }, expect: deny } ]
  - as: ada
    read:   { allow: [mine, public], deny: [] }
    create: [ { data: { title: x }, expect: allow } ]
    update:
      - { document: mine, patch: { title: new }, expect: allow }
      - { document: mine, patch: { status: done }, expect: deny }
      - { document: mine, data: { title: whole, status: done }, expect: deny }
      - { document: public, patch: { title: stolen }, expect: deny }
    delete: { allow: [mine], deny: [old, public] }
  - as: newbie
    create: [ { data: { title: x }, expect: deny } ]
  - as: bob
    read: { allow: [mine, public] }
    delete: { deny: [mine, old] }
  - as: boss
    read: { deny: [mine] }
    delete: { deny: [mine] }   # an admin who can't read it can't delete it
`))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for _, ch := range checks {
		if !ch.Pass {
			t.Errorf("%s: %s", ch.What, ch.Detail)
		}
	}
	if len(checks) < 20 {
		t.Errorf("only %d assertions ran", len(checks))
	}
}

// A wrong expectation is reported with what the rules decided and why.
func TestRunReportsFailures(t *testing.T) {
	c := collection(t, goodRules)
	checks, problems := Run(c, roles, []byte(`
users: { ada: { id: u1, email: ada@example.com } }
documents: { mine: { title: Mine, _meta: { owner: u1 } } }
tests:
  - name: wrong on purpose
    as: ada
    read: { deny: [mine] }
    update: [ { document: mine, patch: { status: x }, expect: allow } ]
`))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if len(checks) != 2 || checks[0].Pass || checks[1].Pass {
		t.Fatalf("checks = %+v", checks)
	}
	if !strings.Contains(checks[0].Detail, "expected deny, the rules allow it") || checks[0].Test != "wrong on purpose" {
		t.Errorf("read: %+v", checks[0])
	}
	if !strings.Contains(checks[1].Detail, "expected allow, the rules deny it (rule update is false)") {
		t.Errorf("update: %+v", checks[1])
	}
}

// With no rule for an operation, or no rules.yaml, everything is denied.
func TestRunWithoutRules(t *testing.T) {
	c := collection(t, "read: \"true\"\n")
	checks, problems := Run(c, roles, []byte(`
users: { ada: { id: u1, email: a@example.com } }
documents: { d: { title: D } }
tests:
  - as: ada
    read: { allow: [d] }
    create: [ { data: { title: x }, expect: deny } ]
    update: [ { document: d, patch: { title: y }, expect: deny } ]
    delete: { deny: [d] }
`))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for _, ch := range checks {
		if !ch.Pass {
			t.Errorf("%s: %s", ch.What, ch.Detail)
		}
	}
	// And the reason says so.
	checks, _ = Run(c, roles, []byte("users: {a: {id: u1}}\ndocuments: {d: {title: D}}\ntests:\n  - as: a\n    delete: { allow: [d] }\n"))
	if len(checks) != 1 || checks[0].Pass || !strings.Contains(checks[0].Detail, "no delete rule") {
		t.Errorf("no rule: %+v", checks)
	}
}

// The read rule is the database filter, so a missing field behaves as in
// MongoDB: `not document.published` lists a document without the field.
func TestRunUsesTheDatabaseSemantics(t *testing.T) {
	c := collection(t, "read: not document.published\n")
	checks, problems := Run(c, roles, []byte(`
documents:
  unset:     { title: Unset }
  published: { title: P, published: true }
  hidden:    { title: H, published: false }
tests:
  - as: anonymous
    read: { allow: [unset, hidden], deny: [published] }
`))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	for _, ch := range checks {
		if !ch.Pass {
			t.Errorf("%s: %s", ch.What, ch.Detail)
		}
	}
}

func TestRunFixtureProblems(t *testing.T) {
	c := collection(t, goodRules)
	for name, tt := range map[string]struct{ fixture, want string }{
		"invalid yaml":     {"tests: [", "invalid YAML"},
		"unknown key":      {"tests: []\nusers: {}\nwho: x\n", "field who not found"},
		"no tests":         {"users: {}\n", "tests: none"},
		"unknown user":     {"tests:\n  - as: nobody\n    read: { allow: [] }\n", `tests[0].as: "nobody" is not one of the fixture's users`},
		"unknown document": {"documents: {d: {title: D}}\ntests:\n  - read: { allow: [x] }\n", `"x" is not one of the fixture's documents`},
		"undeclared role":  {"users: {a: {id: u1, roles: [wizard]}}\ntests:\n  - as: a\n    read: {}\n", `role "wizard" is not declared`},
		"anonymous user":   {"users: {anonymous: {id: u1}}\ntests: [{read: {}}]\n", "users.anonymous"},
		"user without id":  {"users: {a: {email: a@example.com}}\ntests: [{read: {}}]\n", "users.a: id is required"},
		"document breaks":  {"documents: {d: {title: \"\"}}\ntests: [{read: {}}]\n", "documents.d: breaks schema.json"},
		"unknown field":    {"documents: {d: {title: D, extra: 1}}\ntests: [{read: {}}]\n", "documents.d: breaks schema.json"},
		"data breaks":      {"tests:\n  - create: [ { data: { title: 5 }, expect: allow } ]\n", "the server would answer 400"},
		"bad expect":       {"tests:\n  - create: [ { data: { title: x }, expect: maybe } ]\n", "must be allow or deny"},
		"missing expect":   {"tests:\n  - create: [ { data: { title: x } } ]\n", "must be allow or deny"},
		"update both":      {"documents: {d: {title: D}}\ntests:\n  - update: [ { document: d, data: {title: x}, patch: {title: y}, expect: allow } ]\n", "exactly one of data"},
		"update neither":   {"documents: {d: {title: D}}\ntests:\n  - update: [ { document: d, expect: allow } ]\n", "exactly one of data"},
		"empty test":       {"tests:\n  - as: anonymous\n", "no assertions"},
		"bad now":          {"now: yesterday\ntests: [{read: {}}]\n", "RFC 3339"},
		"bad meta key":     {"documents: {d: {title: D, _meta: {owner_id: u1}}}\ntests: [{read: {}}]\n", "_meta.owner_id is not a field"},
		"bad created_at":   {"documents: {d: {title: D, _meta: {created_at: soon}}}\ntests: [{read: {}}]\n", "_meta.created_at must be an RFC 3339"},
		"patch breaks":     {"documents: {d: {title: D}}\ntests:\n  - update: [ { document: d, patch: { title: null }, expect: deny } ]\n", "result"},
	} {
		checks, problems := Run(c, roles, []byte(tt.fixture))
		if len(checks) > 0 {
			t.Errorf("%s: assertions ran despite the mistakes", name)
		}
		var all []string
		for _, p := range problems {
			all = append(all, p.Error())
		}
		if !strings.Contains(strings.Join(all, "\n"), tt.want) {
			t.Errorf("%s: problems %q, want one with %q", name, all, tt.want)
		}
	}
}
