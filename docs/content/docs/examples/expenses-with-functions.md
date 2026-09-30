---
title: "Expenses with functions"
description: "The same shared-expenses app as \"expenses without functions\", with its four fixable holes closed by server-side functions — compared side by side."
icon: "receipt_long"
weight: 730
toc: true
---

The same group-expenses tracker as [expenses without functions](../expenses/), with the same layout, the same data model's spirit, and the same rules where nothing needed to change — but holes 1 through 4 are closed, by five small [functions](../../functions/) instead of client-trusted copies. Hole 5 (email verification) is still open: nothing here changes it, since email verification itself doesn't exist yet.

Read [expenses without functions](../expenses/) first if you haven't: this page assumes you know the five holes it documents, and spends its time on what's different here and why.

The configuration is the `expenses-with-functions` realm in `examples/config/expenses-with-functions`. The application is in `clients/js/examples/expenses-with-functions`.

## Try it

```sh
make example
```

Open <https://localhost:8443/example/expenses-with-functions/>. It looks and works exactly like [expenses without functions](../expenses/#try-it) — sign up, create a group, invite a second email, add expenses, watch balances — with one visible difference: "I paid this" now says the other person needs to confirm it, and a pending settlement shows a "Confirm received" button to its receiver.

```sh
make hack-expenses-functions
```

Runs the same kind of attacks as [`make hack-expenses`](../expenses/#try-the-attacks) does against the other example, against this one — and where that script reports holes 1-4 as open, this one reports them **closed**, with real calls, not just by reading the functions' source.

## What changed

| | Expenses without functions | Expenses with functions |
|---|---|---|
| **Layout** | Groups sidebar · group panel · balances · new expense · history | Identical |
| **`groups` collection** | Rules enforce everything needed | **Unchanged** — no holes there, so nothing to close |
| **`expenses` collection** | `members`: a client-written copy of the group, checked by rules | **No `members` field.** A direct read only ever returns your own entries (`read: user != nil && document._meta.owner == user.id`); everyone else's go through `list_expenses` |
| **Creating an expense** | `expenses.create({...})`, a direct write | `db.fn('add_expense', {...})`, which validates `group_id` and `split_between` against the real, current group first |
| **Reading a group's expenses** | `expenses.iterate({ where: { group_id } })`, a direct read using the stale `members` copy | `db.fn('list_expenses', { group_id })`, which checks the caller's *current* group membership every time |
| **Settling up** | `expenses.create({ kind: 'settlement', ... })` — final the moment it's written | `db.fn('request_settlement', {...})` records it as `status: 'pending'`; only `db.fn('confirm_settlement', { id })`, called by the **receiver**, makes it count |
| **Balances** | Computed in the browser, from whatever entries the client happens to have (`ledger.js`) | `db.fn('balances', { group_id })`, computed once on the server from data the holes above no longer let anyone fake |
| **`ledger.js`** | ~75 lines: balances, the settlement plan, `toCents` | ~10 lines: only `toCents` — parsing what someone typed before sending it to the server, the one piece of money math still in the browser |

## The functions

Five functions in `examples/config/expenses-with-functions/main/_functions/`, one per hole (roughly) plus a shared helper:

| Function | `admin: true`? | Closes | What it does |
|---|---|---|---|
| `add_expense` | no | 1 | Reads the group as the caller (`ctx.db`, so a non-member's attempt fails not found before anything is written), checks `split_between` against its real, current `members`, then creates the expense as the caller — same as a direct write, just validated first |
| `list_expenses` | yes | 1, 2 | Same group-membership check, then `ctx.admin.db` lists every expense for that group — bypassing the direct read rule's owner-only limit, but only after membership passed |
| `request_settlement` | no | 3 (half) | Same group check on the receiver (`to`), then creates a `settlement` with `status: 'pending'` |
| `confirm_settlement` | yes | 3 (half) | `ctx.admin.db` reads the entry (the receiver isn't its owner, so a direct read wouldn't see it), checks the caller is really `split_between[0]`, and flips `status` to `'confirmed'` — the one write a direct PATCH could never make, since `expenses/rules.yaml`'s update rule refuses anyone from changing `status`, even the entry's own writer |
| `balances` | yes | 4 | Same group check, then `ctx.admin.db` lists the group's expenses, counts only confirmed settlements (a pending one doesn't move anyone's balance yet), and returns the same balances/settlement-plan shape `ledger.js` used to compute alone |

`add_expense` and `request_settlement` need only `ctx.db`, acting as the caller — everything they check is already something the rules would let that caller do themselves; the function just does the group lookup in the same call instead of trusting a copy. `list_expenses`, `confirm_settlement` and `balances` need `admin: true` because their whole job is showing or changing something that belongs to *someone else* (another member's expenses, another person's pending settlement) — the function itself, via its own membership or receiver check, is what makes that safe, not the rules.

Every function that reads a group re-throws `ctx.db`'s own error (see `lib/relay.ts`) instead of letting it become a generic `500 function_failed`: only what a function throws via `ctx.error(...)` reaches its caller with a real status, so an uncaught error from `ctx.db` needs to be turned into one on purpose. A non-member calling any of these five sees the same `404 not_found` reading the group directly would give them.

## Why the read rule looks the way it does

A first draft of `expenses/rules.yaml` set `read: false`, meaning to force every read through `list_expenses`. It broke updating and deleting your own entries: backd fetches the *current* document through the collection's read rule before checking `update`/`delete` against it (the same way `changed()` needs the current value to compare against) — so a read rule that refuses everyone refuses those too, not just direct `GET`/`list`. `read: user != nil && document._meta.owner == user.id` is the fix: it's exactly narrow enough to let update and delete keep working normally for your own entries (which never goes stale — ownership doesn't change), while still refusing a direct read of anyone else's. Seeing the group collectively — including entries you don't own — is `list_expenses`'s job alone.

One visible consequence: editing or deleting *someone else's* entry now answers `404`, not `403` as it does in [expenses without functions](../expenses/#what-the-rules-enforce) — there, the reader's copy of `members` let them see the entry (read succeeds, then the write is refused); here, a direct read of someone else's entry was never allowed in the first place, so the document doesn't exist as far as they're concerned, same as a stranger's private draft in the [blog example](../blog/).

## Known holes

Hole 5 is the only one left, unchanged from [expenses without functions](../expenses/#known-holes): email verification doesn't exist yet, so whoever signs up with an invited email first gets the group. Nothing about functions can fix this — it needs email verification itself, still planned.

## Best practices

What this version adds to [expenses without functions](../expenses/#best-practices)' list:

- When a check needs data from another document, a function reading it as the caller (`ctx.db`) both performs the check and proves the caller may see what it read — no separate permission check needed on top.
- Reserve `ctx.admin.db` for exactly the part of a function's job the caller themselves couldn't do directly — here, seeing or changing someone *else's* document — and keep every other check (ownership, "you only write what's yours") in the rules, where it still applies as a backstop.
- A field only a function should ever set (`status`, here) needs the rules to say so explicitly (`!('status' in changed())`) — `admin: true` bypasses rules entirely, but the direct document API is still there for anyone to try.
- Computing something once on the server, from data the rules and functions together keep honest, beats computing it on every client from data some of them might not be able to trust.
