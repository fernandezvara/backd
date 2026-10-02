---
title: "Deleting and erasing users"
description: "Deactivate an account and keep its data, or erase a user: a tombstone, and the collections' policies applied. What is kept, and for how long."
icon: "delete_forever"
weight: 565
toc: true
---

`backd` separates two things people call "deleting an account":

| What | Who | Effect | Reversible |
|---|---|---|---|
| **Deactivate** | the user (`DELETE /_auth/me`, with their password), or an administrator (`PATCH /_admin/users/{id}` with `disabled`, `backd user disable`) | the user is disabled and every session ends; **all data is kept** | yes: an administrator reactivates (`backd user enable`) |
| **Erase** | an administrator (`DELETE /_admin/users/{id}`, `backd user delete --yes`) | the user becomes a tombstone, and the policies of your collections are applied (below) | **no** |

{{< hint note >}}
**Deactivating is not erasing.** A user who deletes their own account is only deactivated: their email stays registered (signing up with it answers as for any registered address) and their data stays. When someone asks for their data to be erased, an administrator, or your own backend with an admin API key, erases them. Erasure is an administrator's action because it is irreversible, and only you know whether it is allowed now: open orders, a dispute, records the law makes you keep.
{{< /hint >}}

## What an erase does

The call answers `202` with an **erase job**, after doing the certain part at once:

- the user becomes a **tombstone**: the id stays, the email becomes `erased-<id>@erased.invalid` (a reserved, undeliverable domain, unique per user), and the roles, language, networks and pending or previous addresses are cleared. The real address is **free again** for a new registration. The tombstone is disabled and can't be enabled; it can still be read (`erased_at` is set);
- their **sign-in methods, sessions, email tokens and the emails queued for them are deleted**.

A worker then applies the [`collection.yaml`](../../configuration/config-dir/#collectionyaml) policy of every collection that declares one, in batches: deleting or anonymizing the documents the user owns, and removing them from other people's documents (a group's member list, a `paid_by` field). **A collection without a policy is left alone**: its documents keep everything, which is what you want for records you must keep, such as purchases.

- **The ids stay.** `_meta.owner`, `created_by` and `updated_by` in documents keep the user's id (the tombstone has no identity, so an id is only a pseudonym), except that `anonymize` clears `_meta.owner` on the documents it applies to. History stays consistent, and nothing breaks for code that looks the id up.
- **Anything personal in a document is for its collection's policy.** Say, per collection and per field, whether it is kept, anonymized or deleted. The choice is in your versioned config, not made per user when erasing.
- **It is safe to repeat and to interrupt.** Each batch only touches documents that still match, so a worker that stops, a retry or a second worker converge to the same result. Updates are atomic per document, bump `_meta.version` and record `backd:erase` in `updated_by`.
- **The result** is one audit record, `user.erased`, with the counts per collection (deleted, anonymized, pulled, cleared), never content. The job is in the jobs list with `origin: backd:account.erase`.
- **If it fails** (a database unreachable, a document that doesn't fit), the job is retried; when its attempts run out it ends **failed**, visible in the jobs list, and the audit record says `needs_attention`. Nothing is skipped silently. Repeating the `DELETE` for that user queues the same job again: it holds the email the policies match by until it succeeds, so resume it before the job expires (`functions.job_retention`).
- `backd user delete --yes` waits for the job (`--wait`, 2 minutes by default) and prints the counts. A worker (`backd worker`, or `serve --with-worker`) must be running.

Preview it first: `backd user owned --email …` (or [`GET /_admin/users/{id}/owned`](../admin/#previewing-an-erase)) counts, per collection with a policy, what the erase would touch.

## Before you erase: external clean-up

{{< hint warning >}}
**Clean up outside systems before calling erase.** If you must delete the customer at Stripe, or remove the address from a mailing list, do it first, in your own backend or runbook, **while the data still exists**: those calls often need ids stored in documents that a policy may delete or anonymize. `backd` has no clean-up hook yet (it is planned, together with erasing automatically after a deactivation period).
{{< /hint >}}

A "delete my data" button belongs in your backend: it checks your own rules (no open orders, a cooling-off period), does the clean-up, then calls `DELETE /v1/{realm}/_admin/users/{id}` with an admin API key. `backd` stays out of your business rules.

## What `backd` keeps about a user, and for how long

| Store | Holds | On erase |
|---|---|---|
| User record | email, roles, language, networks | tombstone (above) |
| Sign-in methods, sessions | password hash, hashed session tokens | deleted |
| Email tokens, the user's queued emails | the user id; addresses of an email change | deleted |
| Documents | whatever you store | the collections' policies |
| Login and email counters | hashes of the address, expiring within a day | expire on their own |
| [Audit trail](../audit/) | `user:<id>` as actor or target, never addresses | kept for `audit.retention` (365 days by default); the id is a pseudonym after the tombstone |
| [Function history](../../functions/logs/), [async jobs](../../functions/jobs/), idempotency records | actor `user:<id>`; a job's input and output; a function's console output | left to their retention: `functions.log_retention` (7 days), `functions.job_retention` (24 hours), 24 hours |
| Backups | everything at that moment | out of scope: they age out with your backup retention |

Two things the table can't know:

- **Function inputs, outputs and logs can hold personal data**, because your code decides what it writes. Shorten `functions.log_retention` and `functions.job_retention` if that matters, and don't log what you don't need.
- **`realm.yaml` can list a person's email** in a role's `users` (seed assignments). That is your file: remove the entry when you erase them.
