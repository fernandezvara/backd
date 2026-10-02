# Design: account lifecycle

- **Issues:** design 4.1 (#5) → features 4.3 (#11), 4.4 (#12), 4.5 (#13), 4.6 (#14), 4.7 (#15), 4.8, 4.9
- **Status:** decided; §4.6 and §5 revised 2026-10-02 (deactivate by default, tombstone, opt-in policies)
- **Related designs:** [email-delivery.md](./email-delivery.md) (templates, tokens, hosted pages, limits), [internal-functions.md](./internal-functions.md) (the deferred `account.on_delete` hook)

## 1. Decisions at a glance

| Topic | Decision |
|---|---|
| Tokens | created at send time, SHA-256 hash only, single use, atomic redemption, siblings invalidated on success |
| Lifetimes | verification 48 h, reset 1 h (configurable) |
| Verified email to sign in | per realm `account.require_verified_email` (default `false`) |
| Sessions from verification | never; users sign in afterwards |
| Who is born verified | users created by `backd bootstrap`, by the admin API, or by accepting an invitation; a redeemed reset token also verifies the address |
| Unverified accounts | optional `account.purge_unverified_after` (default off) |
| Sign-up emails | `verify-email` only; `welcome` opt-in, sent **after** verification |
| Generic sign-up answer | only with `require_verified_email: true`; otherwise `409 email_taken` stays |
| Email change | per realm `account.allow_email_change` (default `false`); confirmed by the new address, revert link to the old one |
| Extra emails | `password-changed` and `invitation` |
| Deleting an account | `DELETE /_auth/me` (password) **deactivates**: disabled, sessions revoked, **all data kept**. Only an administrator **erases** (`DELETE /_admin/users/{id}`); a deactivated account keeps its email, so the address stays taken until then |
| Erased user | a **tombstone**: the id stays; the email becomes `erased-<id>@erased.invalid` (unique, never deliverable); password, sign-in methods, roles and networks are removed. Ids in `_meta` and in documents stay (pseudonymous) |
| Erasure policy | **optional** `collection.yaml` with `on_owner_delete`: `action: delete \| anonymize` (+ `remove`, `replace`) for the user's documents, and `pull` / `unset` for references in any document, each field naming `email` or `id`. **No file: nothing happens to the collection** |
| Erasure execution | the request deactivates and tombstones at once and queues one **erase job**; a worker applies the policies in batches, resumable; one audit record with counts |
| Other stores | deleted: sign-in methods, sessions, email tokens, the user's email jobs. Left to their retention, documented: audit, function history, async jobs, idempotency records, counters. Backups are out of scope |
| External clean-up | done by the developer **before** calling erase; the `account.on_delete` hook is deferred (§13) |
| Before erasing | an "owned" report, limited to collections that declare a policy |

## 2. Configuration (`realm.yaml`)

Written commented by `backd template realm`.

```yaml
account:
  require_verified_email: false   # true: no session until the address is verified; needs email:
  welcome_email: false            # true: send "welcome" once the address is verified; needs email:
  allow_email_change: false       # true: users may change their own email; needs email:
  tokens:
    verify_email: 48h
    reset_password: 1h
    change_email: 24h
    revert_email_change: 7d
  purge_unverified_after: 30d     # optional, off by default: delete accounts that never verified; needs email:
```

**Startup checks:** any of `require_verified_email`, `welcome_email`, `allow_email_change` set to `true` without an `email:` section fails; durations must be positive; `purge_unverified_after` without an `email:` section fails.

## 3. Tokens

| Purpose | Lifetime | Sent to | Created by |
|---|---|---|---|
| `verify-email` | `tokens.verify_email` (48 h) | the user's address | sign-up, resend |
| `reset-password` | `tokens.reset_password` (1 h) | the user's address | reset request |
| `change-email` | `tokens.change_email` (24 h) | the **new** address | email change request |
| `revert-email-change` | `tokens.revert_email_change` (7 d) | the **old** address | email change confirmed |
| invitation | today's invitation expiry (7 d) | the invited address | admin invitation with `send: true` |

Rules (from [email-delivery.md §8](./email-delivery.md#8-tokens)): created when the email is sent; only the SHA-256 hash stored, with `purpose`, `user_id`, `expires_at`, `used_at`, `redirect_to`; redemption hashes the presented token and marks it used in one atomic update; success invalidates the user's other outstanding tokens of the same purpose; invalid, expired and used tokens look identical to the caller (`400 invalid_token`, or the same hosted error page).

## 4. Flows

### 4.1 Sign-up

| Realm | New address | Address already registered |
|---|---|---|
| no `email:` | `201` + session (today) | `409 email_taken` (today) |
| `email:`, `require_verified_email: false` | `201` + session; `verify-email` sent | `409 email_taken` |
| `email:`, `require_verified_email: true` | `202`, no session; `verify-email` sent | `202`, no session; `account-exists` sent |

- In the `202` case backd still hashes the submitted password for registered addresses, so timing doesn't reveal registration.
- Sign-up accepts `locale` (mapped silently, see [email-delivery.md §7](./email-delivery.md#7-languages)) and `redirect_to` (checked against `allowed_redirects`).
- The docs state plainly: a realm that hands out a session at sign-up can't hide who is registered.

### 4.2 Email verification

- Link → hosted page (GET shows a "Confirm my email" button; POST verifies) or `POST /_auth/verify-email {token}`.
- Success sets `email_verified: true`, sends `welcome` if `welcome_email: true`, and redirects (hosted page). **No session is issued.**
- **Born verified:** users created by `backd bootstrap`, by the admin API (`POST /_admin/users`) and by accepting an invitation start with `email_verified: true`, since an operator or the address's owner vouched for them. Users who sign up themselves start unverified.
- **Enabling the gate on an existing realm** locks out users who are unverified until they use resend or password reset; the docs say so before the switch is flipped.
- **Purging squatters:** with `account.purge_unverified_after`, a worker periodically deletes accounts that never verified after that time (a hard delete of the record, with no tombstone and no erase: an account that never verified is useless information; documents it wrote, if any, are kept as with any deletion that isn't an erase); audited as `user.purged_unverified` with a count, never addresses.
- **Resend:** `POST /_auth/verify-email/resend { email, redirect_to? }` answers `202` identically whether or not the account exists or is already verified; email limits apply.
- **Login gate** (`require_verified_email: true`): a login with the **correct password** for an unverified address answers `403 email_not_verified`; the check happens only after the password is verified, so it reveals nothing to someone without the password. Wrong passwords keep answering `invalid_credentials`.

### 4.3 Password reset

- **Request:** `POST /_auth/reset-password/request { email, redirect_to? }` answers `202` identically for known and unknown addresses, in similar time; throttled per account and per IP (login-style counters) plus email limits.
- Link → hosted page (GET shows a form for the new password twice, token in a hidden field; POST applies the password policy, shows errors on the same form) or `POST /_auth/reset-password { token, password }`.
- Success sets the password, **revokes every session** of the user, **marks the address verified** (redeeming the token proves the owner reads that mailbox, and it ends any squatter's access), sends `password-changed`, and redirects.
- `password-changed` is also sent after `POST /_auth/password` (change while signed in, which already revokes the user's other sessions) and after an admin sets a password (which already revokes all sessions).

### 4.4 Email change (issue 4.8)

Only when `allow_email_change: true` (otherwise the route doesn't exist: `404`).

1. `POST /_auth/email { new_email, password, redirect_to? }` — signed in **and** current password required. Answers `202`. Stores the pending address on the user; sends `change-email` to the **new** address.
2. The new address confirms (hosted page or `POST /_auth/confirm-email-change {token}`): uniqueness is checked again; the email changes; `email_verified` stays `true`; the user's **other** sessions are revoked; audited.
3. The **old** address receives `email-changed`: "your address was changed; if this wasn't you, revert it", with a **revert link valid 7 days**.
4. Revert (hosted page or `POST /_auth/revert-email-change {token}`): restores the old address, **revokes every session**, and requires a password reset (a `reset-password` email is sent to the restored address); audited.

**Admin changes:** `POST /_admin/users/{id}/email { email }` works in any realm with `email:`, regardless of `allow_email_change`; both addresses are notified, the revert link applies, the change is audited.

### 4.5 Invitations by email (issue 4.9)

- `POST /_admin/invitations { email, send: true, redirect_to? }` (and `backd user invite --send`): for invitations bound to an address, backd emails an `invitation` with a link.
- Link → hosted `accept-invitation` page (email shown, not editable; password twice; optional locale) or the app's sign-up page via `links.invitation`, which calls `POST /_auth/accept-invitation { token, password, locale? }`.
- Accepting creates the account and marks the address **verified** (only its owner could have received the link). No session is issued; the user signs in.
- Existing invitations without `send` keep working as today.

### 4.6 Account deletion

| Who | Call | What happens |
|---|---|---|
| The user | `DELETE /_auth/me` (password) | **Deactivation**: the user is disabled and every session ends; nothing else changes. Audited as `user.delete_account` |
| An administrator | `PATCH /_admin/users/{id}` `disabled` (`backd user disable` / `enable`) | Deactivates or reactivates, as today |
| An administrator | `DELETE /_admin/users/{id}` (`backd user delete --yes`) | **Erasure** (§5). Irreversible; an erased user can't be enabled |

- **Why deactivation by default:** the data (purchase history, for example) is needed for historical reasons, and deactivation is reversible. The docs say plainly that **deactivating is not erasing**: a person who asks for their data to be erased needs an administrator (or the developer's backend, with an admin key) to erase them.
- **Why only an administrator erases:** it is irreversible, the operator is the one who knows whether erasing is allowed now (open orders, disputes, legal retention), and it can touch other people's documents. A "delete my data" button belongs in the developer's backend, which checks its own rules and then calls the admin API. A background process that erases after a deactivation period (`account.erase_after`) is a later follow-up (§13).
- **Finding who asked:** self-deletion is in the audit trail (`backd audit --action user.delete_account`); there is no extra field.
- A deactivated account still holds its email, so signing up with it answers as for any registered address until the account is erased.

## 5. Erasure of personal data (issue 4.6)

### 5.1 Policy per collection

New **optional** file `collection.yaml` next to `schema.json`, parsed strictly; the home for collection-level data settings (future soft delete, 7.15, goes here too). **A collection without the file is left alone by an erase**: its documents keep everything, which is also how purchase records are kept. `backd template database` writes a commented example only on request.

```yaml
on_owner_delete:
  action: anonymize          # delete | anonymize
  remove: [phone]            # anonymize: these fields are removed
  replace:                   # anonymize: these fields get a fixed value
    buyer_name: "Erased customer"
  pull:                      # in EVERY document of the collection: remove the user from these arrays
    members: email           # what the array holds: email | id
  unset:                     # in EVERY document of the collection: clear these fields where they hold the user
    paid_by: email           # email | id
```

| Part | Effect |
|---|---|
| `action: delete` | the documents whose `_meta.owner` is the user are removed |
| `action: anonymize` | on those documents, `remove` fields are removed, `replace` fields set, `_meta.owner` cleared |
| `pull`, `unset` | on **every** document of the collection, whoever owns it: the user's email or id is removed from the array / cleared from the field. Exactly one identifier per field (`email` or `id`) |

- **The developer chooses** per collection and per field what is kept, anonymized or deleted. The choice is in the versioned config, not made per user at the moment of erasing.
- **`pull` and `unset` match exactly.** Emails are stored normalized (lower case), so fields matched by `email` must hold that form (the docs say so; the expenses schema already enforces it). The email is read from the user record just before it is anonymized.
- **`_meta` is never rewritten.** `_meta.owner`, `created_by` and `updated_by` keep the user's id (the tombstone makes it a pseudonym), except that `anonymize` clears `_meta.owner` on the documents it applies to. History stays consistent and no pattern matching over `created_by` / `updated_by` is needed.

**Startup checks against `schema.json`:** every named field is declared; a required field can't be in `remove` unless it is also in `replace`, and can't be in `unset` at all (clearing it would make documents invalid: the error says to make it optional); every `replace` value is valid for its field's schema and the field is in no unique index; `pull` fields are arrays of strings without `minItems`; `unset` fields are strings. Erased documents therefore stay valid, so the job needs no validation bypass.

### 5.2 What an erase does

`DELETE /_admin/users/{id}` does the certain part in the request:

1. disables the user and deletes their sign-in methods, sessions, email tokens and email jobs;
2. turns the record into the **tombstone** (§1);
3. queues one **erase job** (`origin: backd:account.erase`) holding the user's id and original email, and answers `202` with the job id (`backd user delete` waits for it and prints the counts).

A worker then applies the policies of the collections that declare one, in batches:
- Updates are atomic per document (never read-then-write), bump `_meta.version` and `updated_at`, and record `backd:erase` in `updated_by`.
- Progress is saved per collection, so the job resumes where a stopped worker left it, and repeating it is safe.
- Provisioning creates the indexes the policies use: `_meta.owner` where there is an action, and one on each `pull` / `unset` field. They are part of `PROVISION_MODE=verify`.
- At the end the job clears the email from itself and writes **one audit record**, `user.erased`, with counts per database and collection (deleted, anonymized, pulled, cleared), never content.
- A failure (a database unreachable, a document that doesn't fit) is retried; when attempts run out the job ends **failed**, visible in the jobs list, and the audit record says it needs attention. Nothing is skipped silently. Repeating `DELETE` for an erased user with an unfinished erase job resumes it.

A realm with no policies erases the same way: the job has nothing to do and finishes at once.

### 5.3 The other stores

| Store | Holds | On erase |
|---|---|---|
| user record | email, roles, locale, networks, pending and previous email | tombstone |
| sign-in methods, sessions | password hash, hashed tokens | deleted |
| email tokens, the user's email jobs | the user id; addresses of an email change; custom emails' recipients and `data` | deleted |
| login and email counters | hashes of the address, TTL of at most a day | expire on their own |
| audit trail | `user:<id>` as actor or target, never addresses | left (`audit.retention`, 365 days by default); a pseudonym after the tombstone |
| function history, async jobs, idempotency records | actor `user:<id>`; a job's input and output; a function's console output | left to `log_retention`, `job_retention` and the idempotency TTL (7 days, 24 hours, 24 hours by default) |
| backups | everything at that moment | out of scope |

The docs state that function inputs and logs can hold personal data because the developer's code decides what it writes, say which settings shorten them, and tell developers that `realm.yaml` role seeds can list a person's email (their file, not backd's).

### 5.4 Report before erasing

`GET /_admin/users/{id}/owned`, `backd user owned --email …` and `admin.users.owned()` in the JS client answer "what would an erase do?", **only for collections that declare a policy**:

```json
{
  "user": { "id": "…", "status": "active | deactivated | erased" },
  "collections": [
    { "database": "main", "collection": "orders", "action": "anonymize", "owned": 12,
      "remove": ["phone"], "replace": ["buyer_name"] },
    { "database": "main", "collection": "groups", "action": null, "owned": 0,
      "pull": { "members": 3 }, "unset": { "paid_by": 0 } }
  ],
  "without_policy": ["main.digests", "main.events"]
}
```

Collections without a policy are listed by name, not counted (an erase leaves them alone, and counting them would need an owner index everywhere). The counts come from the queries the erase uses, so the preview and the result agree. It never returns content. For an erased user it answers the status `erased` with no counts.

### 5.5 Retention documentation

A docs page lists, per store, what it holds about a user and for how long (§5.3), plus the points above.

## 6. Endpoints (new or changed)

| Method and path | Purpose |
|---|---|
| `POST /_auth/signup` | + `locale`, `redirect_to`; `202` mode (§4.1) |
| `GET /_auth/verify-email`, `POST /_auth/verify-email` | hosted page / JSON redeem |
| `POST /_auth/verify-email/resend` | resend, uniform answer |
| `POST /_auth/reset-password/request` | request reset, uniform answer |
| `GET /_auth/reset-password`, `POST /_auth/reset-password` | hosted form / JSON redeem |
| `POST /_auth/email` | request email change |
| `GET/POST /_auth/confirm-email-change`, `GET/POST /_auth/revert-email-change` | hosted pages / JSON |
| `GET/POST /_auth/accept-invitation` | hosted page / JSON |
| `PATCH /_auth/me` | + `locale` |
| `POST /_admin/users/{id}/email` | admin email change |
| `DELETE /_auth/me` | now **deactivates** (§4.6) |
| `DELETE /_admin/users/{id}` | now **erases** (§5.2); `202` with the erase job |
| `GET /_admin/users/{id}/owned` | erasure report (§5.4) |
| `POST /_admin/invitations` | + `send`, `redirect_to` |

Hosted pages and JSON redeem endpoints follow [email-delivery.md §10](./email-delivery.md#10-links-and-hosted-pages-issue-42f). New codes: `403 email_not_verified`, `400 invalid_token`, `400 invalid_locale`, `400 invalid_redirect`.

## 7. Emails per event

| Event | Email | Condition |
|---|---|---|
| sign-up, new address | `verify-email` | `email:` configured |
| sign-up, registered address | `account-exists` | `require_verified_email: true` |
| email verified | `welcome` | `welcome_email: true` |
| reset requested (known address) | `reset-password` | `email:` configured |
| password changed, reset or set by admin | `password-changed` | `email:` configured |
| email change requested | `change-email` → new address | `allow_email_change: true` (or admin change) |
| email change confirmed | `email-changed` → old address | same |
| email change reverted | `reset-password` → restored address | same |
| invitation with `send: true` | `invitation` | `email:` configured |

## 8. Audit events

`user.email_changed` (self or admin), `user.email_change_reverted`, `user.password_reset`, `user.delete_account` (self-deactivation), `user.erased` (counts, or needs attention), `user.purged_unverified` (count), `invitation.sent`. Never addresses or tokens; user **ids** only.

## 9. Security considerations

- No sessions from links; links never carry sessions or redirect targets.
- Registration hidden only where it can be (no session at sign-up); documented elsewhere.
- **Pre-registration of someone else's address:** an attacker can create an unverified account with a victim's address. The real owner is protected by the reset flow (it revokes every session, verifies the address and invalidates the old password), by `account-exists` in verify mode, and by the optional purge of unverified accounts.
- Email change needs the current password and the new address; the old address can revert for 7 days and forces a reset.
- Password changes revoke sessions and notify the owner.
- Erasure is an administrator's action and irreversible; policies are explicit per collection and validated against the schema. A user's id stays in document metadata as a pseudonym: the tombstone holds no identity, and anything personal in a document is for its collection's policy to remove.

## 10. Documentation

- "Account lifecycle" guide: settings, each flow, emails per event, hosted pages vs own pages (GET vs POST).
- `collection.yaml` reference and an "Erasure" page: deactivation vs erasure stated plainly, **external clean-up (Stripe, mailing lists) is done before calling erase, in the developer's backend or runbook, while the data is still there (the hook is deferred)**, examples (orders anonymized, profiles deleted, shared documents with `pull` by email or id), and the retention page (§5.3, §5.5).
- `realm.yaml` reference: `account:`.
- JS client docs (4.7).
- `api/openapi.yaml` for every endpoint and code.

## 11. Acceptance criteria by issue

**4.3 — verification:** `require_verified_email`; users from bootstrap, the admin API and invitations born verified; `account.purge_unverified_after`; `202` sign-up mode; login gate after password check; resend uniform; hosted page and JSON; `welcome` after verification; no session issued; examples (`blog`, `expenses`) require `user.email_verified`. Tests for every branch.

**4.4 — reset:** uniform request answer and timing; throttling; hosted form with policy errors; JSON endpoint; all sessions revoked; address marked verified; `password-changed` on reset, change and admin set. Tests.

**4.5 — generic sign-up answer:** uniform `202` in `require_verified_email` realms; `account-exists` with limits; password hashed for registered addresses; `409` elsewhere, documented. Tests including timing similarity.

**4.6 — erasure:** `DELETE /_auth/me` deactivates; `DELETE /_admin/users/{id}` erases (tombstone with the anonymized unique email, other stores per §5.3); optional `collection.yaml` with startup checks against `schema.json` (collections without it untouched); `delete` / `anonymize` / `remove` / `replace`; `pull` / `unset` by `email` or `id`; indexes from the policies; resumable erase job (killed-worker test) with one audit record and a visible failure; the owned report for policy collections; docs: erasure page (with the external clean-up statement), retention page, openapi, CLI, JS client.

**4.7 — JS client:** sign-up `locale`/`redirect_to`; resend; reset request and redeem; verify redeem; email change, confirm, revert; accept invitation; `me.locale`; admin `owned`, email change, invitation `send`; example app "forgot password" and "change email". Unit and integration tests.

**4.8 — email change:** `allow_email_change`; password required; pending address; confirm with uniqueness re-check; other sessions revoked; `email-changed` with revert; revert restores, revokes all and sends reset; admin change; audit. Tests including revert after confirm and expired revert.

**4.9 — invitation emails:** `send: true`; hosted `accept-invitation` page and JSON; address marked verified; no session; existing manual invitations unchanged. Tests.

## 12. Alternatives considered

- **Verification never required / grace period:** chose a per-realm switch.
- **Sessions issued on verification:** rejected; would put a session token in a redirect URL.
- **Both emails at sign-up / welcome first:** chose verification first, welcome after.
- **Generic answer in every email realm:** would change `require_verified_email: false` into "sign in after sign-up"; rejected.
- **Emails immutable / admin-only changes:** chose opt-in self-service with a revert link.
- **Erasure settings in `rules.yaml` or `x-backd-erase` in `schema.json`:** chose `collection.yaml`.
- **A required policy for every collection / `keep` as an action:** replaced by an optional file, where no file means nothing happens (rejected the migration of every existing collection).
- **Deleting the user record and scrubbing the id from every document's `_meta`:** replaced by a tombstone; scrubbing `created_by` / `updated_by` needed pattern matching over composite strings and new indexes, and broke the history that purchases need.
- **Self-service erasure (a flag on `DELETE /_auth/me`, or an `account.self_erase` switch):** deferred; the developer's backend can offer the button.
- **An `account.on_delete` hook now:** deferred (§13).

## 13. Follow-ups

- 7.11 HttpOnly cookie mode could later let hosted pages sign users in safely.
- 7.15 soft delete lives in `collection.yaml`.
- **Erasure after deactivation in the background** (`account.erase_after`, off by default) and an opt-in self-service erase.
- **The `account.on_delete` hook** (an internal async function for external clean-up). When it comes it runs **before** the policies, with `{ user_id, email }`, retried per its `retry`, and the policies wait for it: after them the data it needs may be gone, and a failing hook ends the erase job visibly, resumable by repeating `DELETE`. It becomes necessary when erasure becomes automatic, because nobody is then there to clean up first.
