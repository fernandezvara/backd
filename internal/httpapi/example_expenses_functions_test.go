package httpapi

import "testing"

// The "expenses with functions" example (examples/config/expenses-with-
// functions): what the rules alone enforce, without an executor. Groups
// are identical to the "without functions" example (no holes there); the
// interesting new behavior — add_expense, list_expenses,
// request_settlement, confirm_settlement and balances actually closing
// holes 1-4 — needs a real executor and is proven instead by
// clients/js/examples/expenses-with-functions/hack.js against the local
// stack (`make hack-expenses-functions`). This test covers what a fake
// store can: a direct read only ever returns your own entries (never
// anyone else's — read: false would have refused update and delete too,
// since fetching the current document to check them against goes through
// the read rule; see expenses/rules.yaml's own comment), defense-in-depth
// create checks hold, and only the writer may update or delete their own
// entry.

const (
	efGroupsPath   = "/v1/expenses-with-functions/main/groups"
	efExpensesPath = "/v1/expenses-with-functions/main/expenses"
)

func TestExpensesWithFunctionsExample(t *testing.T) {
	f := newExampleFixture(t, "expenses-with-functions")
	ada, bob := f.signup(t, "ada@example.com"), f.signup(t, "bob@example.com")
	const pair = `["ada@example.com", "bob@example.com"]`

	g := f.want(t, "ada creates a group", 201, ada, "POST", efGroupsPath, `{"name": "Lisbon", "currency": "EUR", "members": `+pair+`}`)
	gid := g["id"].(string)
	group := efGroupsPath + "/" + gid

	t.Run("groups: identical rules to expenses-without-functions", func(t *testing.T) {
		f.want(t, "bob reads the group", 200, bob, "GET", group, "")
		f.want(t, "ada renames the group", 200, ada, "PATCH", group, `{"name": "Lisbon 2026"}`)
		f.want(t, "anonymous lists groups", 401, "", "GET", efGroupsPath, "")
		f.want(t, "removing yourself from the group", 403, bob, "PATCH", group, `{"members": ["ada@example.com"]}`)
		f.want(t, "changing the group's currency", 403, bob, "PATCH", group, `{"currency": "USD"}`)
		f.want(t, "deleting a group you didn't create", 403, bob, "DELETE", group, "")
	})

	// A document created directly (bypassing add_expense/request_settlement)
	// so update/delete/read can be tested without an executor. Real callers
	// never do this — the create rule below is defense in depth, not the
	// only check (add_expense/request_settlement validate group_id and
	// split_between against the real group first; a rule can't).
	dinner := f.want(t, "ada creates an expense directly (what add_expense itself would do)", 201, ada, "POST", efExpensesPath,
		`{"kind": "expense", "group_id": "`+gid+`", "description": "Dinner", "amount": 6000, "paid_by": "ada@example.com", "split_between": `+pair+`}`)
	dinnerPath := efExpensesPath + "/" + dinner["id"].(string)

	t.Run("expenses: a direct read only ever returns your own entries", func(t *testing.T) {
		f.want(t, "ada reads her own dinner directly by id", 200, ada, "GET", dinnerPath, "")
		f.want(t, "an outsider reads ada's dinner directly", 404, bob, "GET", dinnerPath, "")
		code, out := f.as(t, bob, "GET", efExpensesPath, "")
		if code != 200 {
			t.Errorf("bob lists expenses directly: %d, want 200: %v", code, out)
		} else if items, _ := out["items"].([]any); len(items) != 0 {
			t.Errorf("bob's direct list returned someone else's entry: %v", items)
		}
	})

	t.Run("expenses: create is defense in depth", func(t *testing.T) {
		f.want(t, "recording what someone else paid", 403, bob, "POST", efExpensesPath,
			`{"kind": "expense", "group_id": "`+gid+`", "amount": 100, "paid_by": "ada@example.com", "split_between": ["bob@example.com"]}`)
		f.want(t, "a settlement to two people", 403, ada, "POST", efExpensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 100, "paid_by": "ada@example.com", "split_between": ["bob@example.com", "ada@example.com"]}`)
		f.want(t, "a settlement to yourself", 403, ada, "POST", efExpensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 100, "paid_by": "ada@example.com", "split_between": ["ada@example.com"]}`)
		f.want(t, "a pending settlement claiming to be already confirmed", 403, ada, "POST", efExpensesPath,
			`{"kind": "settlement", "group_id": "`+gid+`", "amount": 100, "paid_by": "ada@example.com", "split_between": ["bob@example.com"], "status": "confirmed"}`)
		f.want(t, "a negative amount", 400, ada, "POST", efExpensesPath,
			`{"kind": "expense", "group_id": "`+gid+`", "amount": -5, "paid_by": "ada@example.com", "split_between": ["ada@example.com"]}`)
	})

	t.Run("expenses: only the writer updates or deletes their own, and never its status", func(t *testing.T) {
		f.want(t, "bob edits ada's dinner", 404, bob, "PATCH", dinnerPath, `{"amount": 1}`)
		f.want(t, "bob deletes ada's dinner", 404, bob, "DELETE", dinnerPath, "")
		f.want(t, "ada moves her dinner to another group", 403, ada, "PATCH", dinnerPath, `{"group_id": "other"}`)
		// The rule closes hole 3's other half: not even the writer of a
		// settlement may set its own status — only confirm_settlement may
		// (via ctx.admin.db, as the receiver, never the writer).
		f.want(t, "ada sets her own entry's status directly", 403, ada, "PATCH", dinnerPath, `{"status": "confirmed"}`)
		f.want(t, "ada fixes her dinner's amount", 200, ada, "PATCH", dinnerPath, `{"amount": 6300}`)
		f.want(t, "ada deletes her own dinner", 204, ada, "DELETE", dinnerPath, "")
	})
}
