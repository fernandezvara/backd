---
title: "Audit trail"
description: "A permanent, append-only record of who changed users, roles, network restrictions, API keys and invitations, and when."
icon: "history"
weight: 560
toc: true
---

Each realm with `auth: enabled` keeps an **audit trail**: one record per security-sensitive action or event, with who did it, to what, when and from where. It answers questions such as "who gave Bob the admin role?" or "which key created this user?". Records can be read, but not changed or deleted: they expire after the realm's retention period.

## What is recorded

| Action | When | Details |
|---|---|---|
| `user.create` | An administrator (or `backd bootstrap`) creates a user | `password` (whether one was set), `roles` (seeded from `realm.yaml`) |
| `user.signup` | A user signs up | `invited`, `roles` |
| `user.password` | An administrator sets a user's password | |
| `user.password_change` | A user changes their own password | |
| `user.verify_email` | An administrator marks an email as verified, or not; or a user verifies it with the link in their email | `verified` |
| `user.password_reset` | A user sets a new password with a reset link | `verified_address` |
| `user.email_changed` | A user's address changes: confirmed by the new address, or set by an administrator | `by` (`self` or `admin`) |
| `user.email_change_reverted` | The old address undid a change | |
| `user.purged_unverified` | A worker deletes accounts that never verified their address | `count`, `older_than` |
| `function.invoke_manual` | An administrator runs a function by hand ([admin API](../admin/)) | `as` (the user's **id**, never their email, or null) |
| `files.versions.regenerate` | An administrator runs [`backd files regenerate`](../../files/file-fields/#after-you-change-a-declaration) ([admin API](../admin/)) | `version`, `missing_only`, `job_id` |
| `storage.reconcile` | An administrator runs [`backd storage reconcile`](../../files/maintenance/#reconciling) ([admin API](../admin/)) | `delete`, `orphans` (how many were found), `deleted` |
| `storage.check` | An administrator runs a [storage check](../../files/storage/#checking-it) ([admin API](../admin/)); never the keys or anything stored | `ok`, `provider`, `bucket` |
| `data.check` | An administrator starts a [schema check](../../configuration/validation/#finding-documents-that-no-longer-match) ([admin API](../admin/)); the target is `data:<database>/<collection>`, `data:<database>` or `data:*`; never the report | `collections`, `limit`, `job_id` |
| `schedule.pause`, `schedule.resume` | An administrator pauses or resumes a function's schedule ([admin API](../admin/)); only a change is recorded; the target is `schedule:<database>/<function>` | none |
| `job.cancel`, `job.rerun` | An administrator cancels a job, or queues a finished one again ([admin API](../admin/)); the target is `job:<id>` | `function`; for a re-run also `new_job` |
| `user.disable`, `user.enable` | An administrator disables or re-enables a user | |
| `user.delete` | An administrator erases a user (the tombstone is made; the data work is in the job) | `job_id` |
| `user.erased` | The erase job finished, or failed for good | `job_id` and `counts` per `database/collection/operation`; `needs_attention` and `error` when it failed. Never content |
| `user.delete_account` | A user deletes their own account (it is deactivated, not erased) | |
| `user.networks` | A user's [network restrictions](../../configuration/realm/#network-restrictions) change, through the API or from `realm.yaml` | `admin_networks`, `login_networks` |
| `role.add`, `role.remove` | A role is assigned or taken away, through the API or from `realm.yaml` seeds | `role` |
| `apikey.create`, `apikey.revoke` | An API key is created or revoked | `role`, `networks`, `expires_at` |
| `invitation.create`, `invitation.revoke` | An invitation is created or revoked | `bound_to_email` (whether it is), `expires_at` |
| `invitation.sent` | An invitation is emailed to its address | `expires_at` |
| `admin.login` | A user holding an admin role logs in | `session_id` |
| `data.create`, `data.update`, `data.delete`, `data.restore`, `data.purge` | A document is written through the [admin data route](../admin/#data) (a batch audits each operation) | `database`, `collection`, `id`; the target is `doc:<database>/<collection>/<id>`; never content |
| `session.revoke` | An administrator ends a user's session ([admin API](../admin/)) | `session` |
| `admin.refused` | An admin API request is refused: by a network restriction, or because the administrator's roles don't open the [area](../admin/#admin-rights) or hold the rights of the user or role involved | `reason`, `method`, `path` |
| `realm.bootstrap` | `backd bootstrap` creates the realm's first administrator | `role` |

Failed logins aren't recorded here: they are [throttled](../sessions/#brute-force-protection) and counted, and every request is in the access log.

## Records

```json
{
  "id": "d3cb1q0spovnshcrql9g",
  "at": "2026-09-27T12:00:00.000Z",
  "action": "role.add",
  "actor": "user:d3c9ljp8hc2g00b6s1m0",
  "target": "user:d3cb0m8spovnshcrqk90",
  "details": { "role": "editor" },
  "request_id": "d3cb1q0spovnshcrql8g",
  "client_ip": "192.0.2.10"
}
```

- **`actor`** is who acted: `user:<id>` (a session), `key:<name>` (an API key), `anonymous` (sign-ups and refused requests), `config:realm.yaml` (seeds applied at startup) or `cli:bootstrap`.
- **`target`** is what the action applied to: `user:<id>`, `key:<name>`, `invitation:<id>`, or null.
- **`request_id`** matches the request's `X-Request-ID` and its line in the access log. **`client_ip`** follows [`TRUSTED_PROXIES`](../../operations/#client-addresses-behind-a-proxy).

Records name users by id, never by email, and **never** contain passwords or password hashes, session tokens, API keys, invitation tokens, or request bodies. A test checks this for every action.

## Reading the trail

With the CLI, as an [administrator](../cli/):

```sh
backd audit --realm blog --since 7d
# TIME                      ACTION         ACTOR                    TARGET                   CLIENT      DETAILS
# 2026-09-27T12:00:00.000Z  role.add       user:d3c9ljp8hc2g00b6s1m0  user:d3cb0m8spovnshcrqk90  192.0.2.10  role=editor
# …

backd audit --realm blog --user bob@example.com       # records about one user
backd audit --realm blog --action apikey.create --json
```

`--action`, `--actor` and `--target` filter exactly; `--user` looks up an existing user's id (for a deleted user, use `--target user:<id>`); `--since` takes an RFC 3339 time or a duration back from now; `--limit` defaults to 50; `--json` prints one JSON record per line.

Over HTTP, `GET /v1/{realm}/_admin/audit` returns a page of records, newest first, with the query parameters `action`, `actor`, `target`, `since`, `until`, `limit` and `skip` (see the [admin API](../admin/#endpoints)). The JavaScript client has `client.admin.audit.list(…)`.

Each record is also written to the log, as a line with `"msg":"audit"` and the same fields, so the trail reaches your log pipeline too. If a record can't be stored, the action it describes has already happened; `backd` logs the record as an error (`"msg":"audit record not stored"`) instead.

## Retention

Records are kept for one year by default. Set another period per realm in [`realm.yaml`](../../configuration/realm/):

```yaml
audit:
  retention: 180d   # at least 1d
```

Each record's expiry is fixed when it's written, so a new retention applies to records written from then on. MongoDB removes expired records (a TTL index on `expires_at`).

Records hold client IP addresses and user ids, which are personal data in many jurisdictions: choose a retention your privacy policy covers, and include the trail in answers to access requests.

## Append-only

- No endpoint or command changes or deletes records.
- In the [production reference](../../operations/production/#least-privilege-provisioning), `backd`'s own database user may only read and insert into the `audit` collection, so not even a compromised `backd` process can rewrite history.
- Records are stored in the realm's [system database](../../operations/#realm-system-databases) and included in its [backups](../../operations/backup/).
- The trail doesn't cover direct access to MongoDB: people with database credentials can do anything. Keep those credentials to the provisioning step, as the production reference does.
