# Design: account lifecycle

- **Issues:** design 4.1 (#5) → features 4.3 (#11), 4.4 (#12), 4.5 (#13), 4.6 (#14), 4.7 (#15), 4.8, 4.9
- **Status:** decided
- **Related designs:** [email-delivery.md](./email-delivery.md) (templates, tokens, hosted pages, limits), [internal-functions.md](./internal-functions.md) (`account.on_delete` hook)

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
| Erasure | `collection.yaml` with `on_owner_delete` **required** in auth realms: `delete`, `anonymize`, `keep`, plus `pull` and `unset`; the user's id in `_meta.created_by` and `_meta.updated_by` is always replaced by `erased` |
| Erasure execution | account removed at once; data erased by a resumable job; optional `account.on_delete` internal function |
| Before deleting | an "owned" report in the admin API and CLI |

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
  on_delete: notifications/cleanup   # optional; <database>/<function>, internal + async
```

**Startup checks:** any of `require_verified_email`, `welcome_email`, `allow_email_change` set to `true` without an `email:` section fails; `on_delete` must name an internal async function; durations must be positive; `purge_unverified_after` without an `email:` section fails.

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
- **Purging squatters:** with `account.purge_unverified_after`, a worker periodically deletes accounts that never verified after that time (they own nothing yet, so it reuses the erasure path); audited as `user.purged_unverified` with a count, never addresses.
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

- Self-delete (`DELETE /_auth/me` with password) and admin delete remove the user, identities and sessions **immediately**, then start the erasure job (§5).

## 5. Erasure of personal data (issue 4.6)

### 5.1 Policy per collection

New file `collection.yaml` next to `schema.json`, parsed strictly. It's the home for collection-level data settings (future soft delete, 7.15, goes here too). **Required for every collection in an auth realm**; startup fails naming the collections without one. `backd template database` always writes it with comments.

```yaml
on_owner_delete:
  action: anonymize            # delete | anonymize | keep
  remove: [phone, address]     # anonymize: fields removed
  replace:                     # anonymize: fields replaced
    name: "Deleted user"
  pull: [members]              # any action: remove the user's id from these arrays in EVERY document
  unset: [paid_by]             # any action: clear these single-value fields in EVERY document where they hold the user's id
```

| Action | Effect on documents whose `_meta.owner` is the user |
|---|---|
| `delete` | removed |
| `anonymize` | `remove` fields deleted, `replace` fields set, `_meta.owner` cleared |
| `keep` | untouched (`_meta.owner` keeps the old id) |

**Always, whatever the policy:** in every document of every collection of the realm, the user's id in `_meta.created_by` and `_meta.updated_by` is replaced by `erased`, so no id remains in document metadata. (`_meta.owner` follows the action above.)

**Startup checks against `schema.json`:** a required field can't be in `remove` or `unset` unless it's also in `replace`; every `replace` value must be valid for its field's schema; every `remove`, `replace`, `unset` and `pull` field must be declared; `pull` fields must be arrays of strings and `unset` fields single values (strings). Anonymized documents therefore stay valid.

### 5.2 Execution

- A job (`origin: backd:account.erase`) runs on a worker over every database of the realm, collection by collection, in batches; safe to repeat; resumed if a worker dies. Provisioning creates the indexes it needs (`_meta.owner`, `_meta.created_by`, `_meta.updated_by`), so large collections aren't scanned.
- Writes are system writes recorded as `backd:erase` in `updated_by`.
- Afterwards, if `account.on_delete` is set, that internal async function is called with `{ user_id }` (no user in `ctx`), for clean-up backd can't know about (e.g. deleting the customer at Stripe).
- **Audit:** one record `user.erased` with counts per database and collection (deleted, anonymized, kept, pulled, unset, metadata scrubbed), never content.

### 5.3 Report before deleting

`GET /_admin/users/{id}/owned` and `backd user owned --email …`: counts per database and collection of owned documents, documents where the user appears in a `pull` or `unset` field, documents that carry the id in `_meta.created_by` or `_meta.updated_by`, plus what each policy will do.

### 5.4 Retention documentation

A docs page listing, per store, what it holds about a user and for how long: login attempts and counters (TTL), sessions, audit records (`audit.retention`), invocation history and logs (`log_retention`), jobs (`job_retention`), tokens (until expiry), backups.

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
| `GET /_admin/users/{id}/owned` | erasure report |
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

`user.email_changed` (self or admin), `user.email_change_reverted`, `user.password_reset`, `user.deleted`, `user.erased` (counts), `user.purged_unverified` (count), `invitation.sent`. Never addresses or tokens; user **ids** only.

## 9. Security considerations

- No sessions from links; links never carry sessions or redirect targets.
- Registration hidden only where it can be (no session at sign-up); documented elsewhere.
- **Pre-registration of someone else's address:** an attacker can create an unverified account with a victim's address. The real owner is protected by the reset flow (it revokes every session, verifies the address and invalidates the old password), by `account-exists` in verify mode, and by the optional purge of unverified accounts.
- Email change needs the current password and the new address; the old address can revert for 7 days and forces a reset.
- Password changes revoke sessions and notify the owner.
- Erasure policies are explicit per collection and validated against the schema; no user id remains in document metadata.

## 10. Documentation

- "Account lifecycle" guide: settings, each flow, emails per event, hosted pages vs own pages (GET vs POST).
- `collection.yaml` reference and an "Erasure" page with examples (orders kept but anonymized, profiles deleted, shared documents with `pull`), plus the retention page (§5.4).
- `realm.yaml` reference: `account:`.
- JS client docs (4.7).
- `api/openapi.yaml` for every endpoint and code.

## 11. Acceptance criteria by issue

**4.3 — verification:** `require_verified_email`; users from bootstrap, the admin API and invitations born verified; `account.purge_unverified_after`; `202` sign-up mode; login gate after password check; resend uniform; hosted page and JSON; `welcome` after verification; no session issued; examples (`blog`, `expenses`) require `user.email_verified`. Tests for every branch.

**4.4 — reset:** uniform request answer and timing; throttling; hosted form with policy errors; JSON endpoint; all sessions revoked; address marked verified; `password-changed` on reset, change and admin set. Tests.

**4.5 — generic sign-up answer:** uniform `202` in `require_verified_email` realms; `account-exists` with limits; password hashed for registered addresses; `409` elsewhere, documented. Tests including timing similarity.

**4.6 — erasure:** required `collection.yaml` (startup failure tested); actions and schema checks; `pull` and `unset`; `_meta.created_by` / `updated_by` scrubbed everywhere; resumable job (killed worker test); `on_delete` hook; owned report; audit with counts; retention page.

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
- **Default `keep` or `delete` for undeclared collections:** chose a required policy.

## 13. Follow-ups

- 7.11 HttpOnly cookie mode could later let hosted pages sign users in safely.
- 7.15 soft delete lives in `collection.yaml`.
