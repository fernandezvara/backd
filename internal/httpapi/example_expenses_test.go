package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/auth/authtest"
	"github.com/fernandezvara/backd/internal/registry"
)

// The "expenses without functions" example (examples/config/expenses):
// its rules must allow normal use and stop what rules can stop, and the
// holes documented for it must exist, until server-side functions close
// them. When a hole gets closed, flip its test and update the docs page.

const (
	groupsPath   = "/v1/expenses/main/groups"
	expensesPath = "/v1/expenses/main/expenses"
)

// exampleFixture serves examples/config, with users for one of its realms.
type exampleFixture struct {
	*fixture
	svc *auth.Users
}

// expensesFixture is the fixture of the expenses realm.
type expensesFixture = exampleFixture

func newExpensesFixture(t *testing.T) *expensesFixture { return newExampleFixture(t, "expenses") }

func newExampleFixture(t *testing.T, realm string) *exampleFixture {
	t.Helper()
	reg, err := registry.Load("../../examples/config")
	if err != nil {
		t.Fatal(err)
	}
	svc := &auth.Users{
		Store:    authtest.NewMemStore(),
		Hasher:   auth.NewHasher(2, auth.Argon2Params{Memory: 64, Time: 1, Threads: 1}),
		Settings: reg.Realms[realm].Settings,
	}
	f := newFixtureWith(t, reg, &memStore{}, func(c *Config) {
		c.Users = func(r string) *auth.Users {
			if r == realm {
				return svc
			}
			return nil
		}
	})
	return &exampleFixture{fixture: f, svc: svc}
}

// signup returns a session token for a new user.
func (f *exampleFixture) signup(t *testing.T, email string) string {
	t.Helper()
	_, tok, err := f.svc.Signup(context.Background(), email, "dev-p4ssw0rd!", "")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *exampleFixture) as(t *testing.T, tok, method, path, body string) (int, map[string]any) {
	t.Helper()
	hdr := map[string]string{}
	if tok != "" {
		hdr = bearer(tok)
	}
	rec, out := f.doH(t, method, path, body, hdr)
	return rec.Code, out
}

func (f *exampleFixture) want(t *testing.T, what string, want int, tok, method, path, body string) map[string]any {
	t.Helper()
	code, out := f.as(t, tok, method, path, body)
	if code != want {
		t.Errorf("%s: %d, want %d: %v", what, code, want, out)
	}
	return out
}

func TestExpensesExample(t *testing.T) {
	f := newExpensesFixture(t)
	ada, bob, mallory, carl := f.signup(t, "ada@example.com"), f.signup(t, "bob@example.com"), f.signup(t, "mallory@example.com"), f.signup(t, "carl@example.com")
	const trio = `["ada@example.com", "bob@example.com", "mallory@example.com"]`

	g := f.want(t, "ada creates a group", 201, ada, "POST", groupsPath, `{"name": "Lisbon", "currency": "EUR", "members": `+trio+`}`)
	gid := g["id"].(string)
	group := groupsPath + "/" + gid
	dinner := f.want(t, "ada records a dinner", 201, ada, "POST", expensesPath,
		`{"kind": "expense", "group_id": "`+gid+`", "description": "Dinner", "amount": 6000, "paid_by": "ada@example.com", "split_between": `+trio+`, "members": `+trio+`}`)
	dinnerPath := expensesPath + "/" + dinner["id"].(string)

	t.Run("normal use", func(t *testing.T) {
		f.want(t, "bob reads the group", 200, bob, "GET", group, "")
		f.want(t, "bob reads the dinner", 200, bob, "GET", dinnerPath, "")
		f.want(t, "bob settles with ada", 201, bob, "POST", expensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 2000, "paid_by": "bob@example.com", "split_between": ["ada@example.com"], "members": `+trio+`}`)
		f.want(t, "ada renames the group", 200, ada, "PATCH", group, `{"name": "Lisbon 2026"}`)
		f.want(t, "ada fixes her dinner", 200, ada, "PATCH", dinnerPath, `{"amount": 6300}`)
	})

	t.Run("stopped by rules", func(t *testing.T) {
		exp := func(fields string) string {
			return `{"kind": "expense", "group_id": "` + gid + `", "amount": 100, ` + fields + `}`
		}
		f.want(t, "anonymous lists groups", 401, "", "GET", groupsPath, "")
		f.want(t, "a non-member reads the group", 404, carl, "GET", group, "")
		f.want(t, "a non-member reads an expense", 404, carl, "GET", dinnerPath, "")
		f.want(t, "creating a group without yourself", 403, carl, "POST", groupsPath, `{"name": "x", "currency": "EUR", "members": ["ada@example.com"]}`)
		f.want(t, "recording what someone else paid", 403, bob, "POST", expensesPath,
			exp(`"paid_by": "ada@example.com", "split_between": ["bob@example.com"], "members": `+trio))
		f.want(t, "splitting with an outsider", 403, ada, "POST", expensesPath,
			exp(`"paid_by": "ada@example.com", "split_between": ["zoe@example.com"], "members": `+trio))
		f.want(t, "an expense whose members leave you out", 403, ada, "POST", expensesPath,
			exp(`"paid_by": "ada@example.com", "split_between": ["bob@example.com"], "members": ["bob@example.com"]`))
		f.want(t, "a settlement to yourself", 403, ada, "POST", expensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 100, "paid_by": "ada@example.com", "split_between": ["ada@example.com"], "members": `+trio+`}`)
		f.want(t, "a settlement to two people", 403, ada, "POST", expensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 100, "paid_by": "ada@example.com", "split_between": ["bob@example.com", "mallory@example.com"], "members": `+trio+`}`)
		f.want(t, "editing someone else's expense", 403, bob, "PATCH", dinnerPath, `{"amount": 1}`)
		f.want(t, "deleting someone else's expense", 403, bob, "DELETE", dinnerPath, "")
		f.want(t, "moving an expense to another group", 403, ada, "PATCH", dinnerPath, `{"group_id": "other"}`)
		f.want(t, "changing the group's currency", 403, bob, "PATCH", group, `{"currency": "USD"}`)
		f.want(t, "removing yourself from the group", 403, bob, "PATCH", group, `{"members": ["ada@example.com", "mallory@example.com"]}`)
		f.want(t, "deleting a group you didn't create", 403, bob, "DELETE", group, "")
		f.want(t, "a negative amount", 400, ada, "POST", expensesPath,
			`{"kind": "expense", "group_id": "`+gid+`", "amount": -5, "paid_by": "ada@example.com", "split_between": ["ada@example.com"], "members": `+trio+`}`)
		f.want(t, "a fractional amount", 400, ada, "POST", expensesPath,
			`{"kind": "expense", "group_id": "`+gid+`", "amount": 10.5, "paid_by": "ada@example.com", "split_between": ["ada@example.com"], "members": `+trio+`}`)
		f.want(t, "an uppercase member email", 400, ada, "PATCH", group, `{"members": ["ada@example.com", "Bob@example.com"]}`)
	})

	// The holes. Each must fail once functions exist.
	t.Run("holes until functions exist", func(t *testing.T) {
		// Hole 3: a settlement nobody confirms. Mallory says she paid Ada.
		f.want(t, "hole 3: mallory records a settlement ada never received", 201, mallory, "POST", expensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 2000, "paid_by": "mallory@example.com", "split_between": ["ada@example.com"], "members": `+trio+`}`)

		// Ada removes Mallory from the group.
		f.want(t, "ada removes mallory", 200, ada, "PATCH", group, `{"members": ["ada@example.com", "bob@example.com"]}`)
		f.want(t, "mallory can't read the group any more", 404, mallory, "GET", group, "")

		// Hole 2: stale member copies. Mallory still reads the old dinner.
		f.want(t, "hole 2: mallory still reads expenses from before her removal", 200, mallory, "GET", dinnerPath, "")

		// Hole 1: anyone who knows the group id writes into it.
		fake := f.want(t, "hole 1: mallory adds a fake expense to a group she left", 201, mallory, "POST", expensesPath,
			`{"kind": "expense", "group_id": "`+gid+`", "description": "Taxi", "amount": 90000, "paid_by": "mallory@example.com", "split_between": ["ada@example.com", "bob@example.com"], "members": `+trio+`}`)
		where := url.QueryEscape(`{"group_id": "` + gid + `"}`)
		code, out := f.as(t, ada, "GET", expensesPath+"?where="+where, "")
		found := false
		for _, it := range out["items"].([]any) {
			found = found || it.(map[string]any)["id"] == fake["id"]
		}
		if code != http.StatusOK || !found {
			t.Errorf("hole 1: ada should see the fake expense in her group: %d %v", code, out)
		}

		// Hole 5: no email verification. Whoever signs up as an invited
		// email gets the group.
		f.want(t, "ada invites dan", 200, ada, "PATCH", group, `{"members": ["ada@example.com", "bob@example.com", "dan@example.com"]}`)
		squatter := f.signup(t, "dan@example.com")
		f.want(t, "hole 5: anyone signing up as dan@example.com reads the group", 200, squatter, "GET", group, "")
	})
}
