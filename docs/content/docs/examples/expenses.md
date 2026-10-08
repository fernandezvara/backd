---
title: "Expenses without functions"
description: "A shared-expenses example: what access rules can enforce today, and the holes that need server-side functions."
icon: "receipt_long"
weight: 720
toc: true
---

A small group-expenses tracker in the style of Tricount or Splitwise. People create a group, invite others by email, record what they paid, and see who owes whom.

Unlike the [blog](../blog/), where each post belongs to its writer, here documents are shared between users, and some of the checks it needs involve more than one document. It is also a lesson in the limits of access rules. Its [rules](../../auth/rules/) are as strict as rules can be today, and they still leave holes, because some checks need data from another document or logic on the server. Those holes are documented here on purpose: this is deliberately the "before" half of a pair. **[Expenses with functions](../expenses-with-functions/)** is the "after" — the same app, same layout, with holes 1–4 closed by [server-side functions](../../functions/), compared side by side with what changed and why. Hole 5 is closed by [email verification](../../auth/sessions/#email-verification) in both: access here is by email address, so the address has to be the user's own.

The configuration is the `expenses` realm in `examples/config/expenses`. The application is in `clients/js/examples/expenses-without-functions`.

{{< live-example >}}

## Try it

```sh
make example
```

Open <https://localhost:8443/example/expenses-without-functions/>. Sign up: the realm requires a verified address, so the app answers "We sent a link" instead of signing you in. Open the [Mailbox](../../functions/email/#developing-without-a-provider) (the page offers a link; the local stack sends no real mail), press the button in the verification email, and log in. "Forgot your password?" on the login form sends a reset link the same way; it also gets a locked-out user back in. Create a group and invite a second email. Then log out and repeat for that email in the same browser, or in a private window: once it is verified, the group is there. Add expenses as each person, and watch the balances and the "to settle up" list change. "I paid this" records a settlement.

The app is plain JavaScript with [Alpine.js](https://alpinejs.dev/) and the [JavaScript client](../../clients/js/), with no build step:

| File | Contents |
|---|---|
| `app.js` | Sign-up, groups, invitations, expenses, settlements |
| `ledger.js` | Balances in cents, the settlement plan, amount parsing, with unit tests in `clients/js/test/examples.test.js` |
| `index.html`, `style.css` | The page |
| `hack.js` | The attacks described below, run against the local stack (see [Try the attacks](#try-the-attacks)) |

## Data model

Two collections in the database `main`:

| Collection | Fields |
|---|---|
| `groups` | `name`, `currency` (ISO 4217, such as `EUR`), `members`: a list of emails |
| `expenses` | `kind` (`expense` or `settlement`), `group_id`, `description`, `amount` (integer cents), `paid_by` (email), `split_between` (emails), `members` (a copy of the group's members) |

- **Membership is by email.** Rules compare the signed-in user's email with the lists (`user.email in document.members`), so an invitation is just an email added to a group: the invited person sees the group as soon as they sign up or log in with that email. Browsers can't look up users by email (only API keys can, through the [admin API](../../auth/admin/)), so ids wouldn't work for invitations. Emails are stored lowercase, as backd stores users' emails; the schema rejects uppercase ones.
- **Amounts are integer cents**, so sums and splits are exact: `"amount": 6000` is 60.00 in the group's currency. One currency per group, fixed when the group is created.
- **A settlement** records that `paid_by` paid money back to the one person in `split_between`.
- **Balances are computed by each client** from the group's expenses (`ledger.js`). A split's remainder cents go to the first people in the split, so shares always add up. The "to settle up" list pairs the largest debts with the largest credits.
- **Expenses carry a copy of the group's members**, because a rule can only see the document it's evaluating, never the group. This copy is the root of the first two holes below. When the app sees that a group's members changed, it updates the copies on the user's own expenses; only an expense's writer may.
- **The app marks entries paid by someone outside the group** with "⚠ not from a member": a former member, or anyone who knew the group's id (hole 1).

## What the rules enforce

The `rules:` of `groups/collection.yaml` (every rule of both collections also starts with `user != nil && user.email_verified`; the table shows the rest):

| Rule | Enforces |
|---|---|
| `read: user != nil && user.email_verified && user.email in document.members` | Only members see a group, and only with a verified address: membership is by email, so the email must be the user's own (hole 5) |
| `create: … user.email in data.members` | You can't create a group without being in it |
| `update: … user.email in document.members && user.email in data.members && !('currency' in changed())` | Members edit the group and invite people; nobody removes themselves, and the currency never changes |
| `delete: … document._meta.owner == user.id` | Only the creator deletes it |

And those of `expenses/collection.yaml`:

| Rule | Enforces |
|---|---|
| `read: user != nil && user.email in document.members` | Only people in the expense's members copy see it |
| `create: … data.paid_by == user.email` | You only record what **you** paid |
| `create: … all(data.split_between, # in data.members)` | You only split with people in the members copy |
| `create: … len(data.split_between) == 1 && !(user.email in data.split_between)` for settlements | A settlement goes to exactly one other person |
| `update: … document._meta.owner == user.id && !('group_id' in changed()) && !('kind' in changed())` | Only the writer edits it, and it can't move to another group or change kind |
| `delete: … document._meta.owner == user.id` | Only the writer deletes it |

The schemas add what schemas can check: amounts above zero and whole, at most 50 members, valid lowercase emails, and no unknown fields.

### Rules to avoid

Tempting shortcuts, and what they would allow:

| Instead of | A naive rule | Would let anyone who is signed in… |
|---|---|---|
| `read: user != nil && user.email in document.members` | `read: user != nil` | read every group and expense in the realm |
| the `paid_by` check | `create: user != nil` | record expenses that someone else "paid", creating debts in their name |
| the owner check on update | `update: user != nil && user.email in document.members` | change other members' expenses, for example the amount |
| `!('group_id' in changed())` | no check | move an expense into another group |
| the `members` checks on groups | `update: user != nil` | add themselves to any group whose id they know |

A test (`internal/httpapi/example_expenses_test.go`) runs every one of these attacks against the real configuration and checks that the rules stop them.

## Known holes

These remain with the rules above. The same test checks that each one is still possible today, so that closing one is a deliberate change.

| # | Hole | Why rules can't close it | What closes it |
|---|---|---|---|
| 1 | **Fake expenses in someone else's group.** Anyone who knows a group's id creates an expense with that `group_id` and a `members` copy of their own, and it shows up in the group, changing everyone's balances. | Rules can't read the group, so they can't check the expense's `group_id` and `members` against it. | A [server-side function](../../functions/) that validates an expense against its group — see [expenses with functions](../expenses-with-functions/) |
| 2 | **Stale member copies.** Someone removed from a group still reads the expenses written while they were a member. New members don't see old expenses until their writers' apps update the copies. | The copies live in each expense, and only the expense's writer may change them. | A function reading membership fresh from the group every time — see [expenses with functions](../expenses-with-functions/) |
| 3 | **Unconfirmed settlements.** Mallory records "Mallory paid Ada 20 €" without paying; Ada's balance changes without her agreement. | A rule can check who records a settlement, not that the other person agrees. | A function implementing a request-and-accept flow — see [expenses with functions](../expenses-with-functions/) |
| 4 | **Balances are computed by clients.** Each client computes its own balances, so none can falsify another's, but fake entries (holes 1 and 3) change everyone's numbers, and there's no authoritative balance. | backd has no server-side computation. | A function that computes balances on the server — see [expenses with functions](../expenses-with-functions/) |
| 5 | **Signing up with someone else's email.** Anyone who signs up as an invited email, before its owner does, would get that group. | Rules compare emails, so the email must be the user's own. | **Closed here:** the realm sets `account.require_verified_email` (no session before the address is verified) and every rule checks `user.email_verified`. Someone who signs up as `dan@…` without access to that mailbox never gets a session |

Hole 5 is closed with email verification, in the realm and in the rules. Holes 1–4 didn't need *new* backd features to close — [`sync` functions](../../functions/), reaching data through `ctx.db` with the caller's own rules and ownership, were exactly the tool. **[Expenses with functions](../expenses-with-functions/)** is the same app with them closed: same layout, compared side by side, with what changed and why.

## Try the attacks

`hack.js` plays a malicious user with the same client library the app uses. With the local stack running (`make example`):

```sh
make hack-expenses
```

It signs up fresh users (Ada, Bob, Mallory and others), then:

1. tries every attack in [Rules to avoid](#rules-to-avoid), plus reading a group as an outsider (including with an `$or` that would match everything) and as an anonymous caller, and reports each one as stopped;
2. reproduces holes 1, 2 and 3, and shows their effect. For example, Mallory's fake taxi in a group she left moves Ada's balance, as Ada's app computes it, from 20.00 € to −430.00 €;
3. signs up as an invited email without access to its mailbox, and shows that no session starts (hole 5, closed).

The script exits with status 0 when the results match this page: every attack stopped and holes 1–4 open — this example stays as it is, deliberately, for the comparison. [Expenses with functions](../expenses-with-functions/) has its own `hack.js` (`make hack-expenses-functions`) making the same attacks and reporting holes 1–4 **closed**, with real calls against the real functions, not just by reading their source.

```
Attacks the rules stop:
  ✓ stopped      an outsider reads the group (404 not_found)
  ✓ stopped      Bob records an expense that Ada "paid" (403 forbidden)
  …
{{< hint note >}}
This example is deliberately incomplete: it keeps its known holes open so you can compare it with [the version with functions](../expenses-with-functions/). Don't copy it into a real app as it is.
{{< /hint >}}

Holes this example leaves open (server-side functions would close them):
  ! open         1. Mallory adds a 900 € "Taxi" to the group she left, split between Ada and Bob
                 Ada's balance, as her app computes it: 20.00 € → -430.00 €.
  …
```

To attack another server, set `BACKD_URL` (and `NODE_EXTRA_CA_CERTS` for a private CA). It creates users on that server, so only use it on test servers.

## Best practices

What this example follows, and what applies to any app on backd:

- Put every check that rules *can* express in the rules and the schema, never only in the client.
- Treat any field copied from another document as untrusted: rules can only check it against the caller, not against its source.
- Keep money in integer minor units, and computations others rely on off the client — a [server-side function](../../functions/) reaching data through `ctx.db`, not each client computing its own.
- Test the attacks, not just the happy path, and keep tests for the holes you know about.
