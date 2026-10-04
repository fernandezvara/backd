---
title: "Sign-up, login and sessions"
description: "The /_auth endpoints: sign up, log in, manage sessions, change or delete the account."
icon: "key"
weight: 520
toc: true
---

What a signed-in user may do with data is decided by each collection's [access rules](../rules/). Server-side services use [API keys](../api-keys/) instead.

Every realm with `auth: enabled` has its authentication endpoints under `/v1/{realm}/_auth`. A realm that doesn't exist or has `auth: disabled` answers `404 not_found` on all of them.

## Sessions and tokens

Signing up or logging in returns a **session token**:

```json
{
  "token": "bds_mxamh1_afbN3BMzfXqDWhxYpXKF58FhWQ4r9eaLc9ew",
  "token_type": "Bearer",
  "session_id": "dars2ql90v6000bbohr0",
  "expires_at": "2026-10-26T12:58:18.988Z",
  "user": {
    "id": "dars2ql90v600434mf0g",
    "email": "ada@example.com",
    "email_verified": false,
    "roles": [],
    "locale": "en",
    "created_at": "2026-09-26T12:58:18.838Z"
  }
}
```

Send it on every request that needs a signed-in user:

```
Authorization: Bearer bds_mxamh1_afbN3BMzfXqDWhxYpXKF58FhWQ4r9eaLc9ew
```

- Tokens are random (256 bits) and start with `bds_`. `backd` stores only a SHA-256 hash of each token, never the token itself, and never logs it.
- A token belongs to one realm. It is recognized on every database of that realm and unknown (`401`) in any other realm.
- Tokens are only accepted in the `Authorization` header. There are no cookies, so there is nothing to protect against cross-site request forgery.
- Browser apps on another origin need that origin listed in the realm's [CORS settings](../../configuration/realm/#cors).

{{< hint warning >}}
A token stored where any script on the page can read it (`localStorage`, a global variable) is stolen by any cross-site-scripting bug. Keep it in memory (the [JavaScript client](../../clients/js/#token-storage) does by default), send a strict Content-Security-Policy, and never put tokens in URLs or logs.
{{< /hint >}}
- A session **expires after `sessions.idle_timeout` without use** (default 30 days), and **never lives longer than `sessions.max_lifetime`** (default 90 days), both set in [`realm.yaml`](../../configuration/realm/). Each use pushes the idle expiry forward, recorded at most once a minute. `expires_at` in responses reflects the latest value.
- **Admins can have shorter sessions.** A session of a user who holds an [admin role](../../configuration/realm/#roles) uses `sessions.admin_idle_timeout` and `sessions.admin_max_lifetime` when they are set (for example `1h` and `12h`), and they can only shorten the regular limits. The limits follow the roles the user holds now: granting an admin role shortens their existing sessions from the next request, and taking it away restores the regular limits for sessions started afterwards. They apply to the command line too (`backd login` sessions end sooner, and you log in again); API keys have their own [expiry](../api-keys/).
- Sessions end immediately on logout, when revoked, when the password changes (other sessions only), when the user is disabled or deleted, or when an operator sets a new password with `backd user set-password`.

## Endpoints

All request bodies are JSON objects sent with `Content-Type: application/json`. Unknown fields are rejected.

| Method and path | Needs a session | Body | Success |
|---|---|---|---|
| `POST /_auth/signup` | no | `{"email", "password", "invitation"?, "locale"?, "redirect_to"?}` | `201` with a session, or `202` without one when the realm [requires a verified email](#email-verification) |
| `POST /_auth/login` | no | `{"email", "password"}` | `200` with a session |
| `POST /_auth/verify-email` | no | `{"token"}` | `204`; [verifies the address](#email-verification) |
| `POST /_auth/verify-email/resend` | no | `{"email", "redirect_to"?}` | `202`, whatever the account |
| `POST /_auth/reset-password/request` | no | `{"email", "redirect_to"?}` | `202`, whatever the account; [password reset](#password-reset) |
| `POST /_auth/reset-password` | no | `{"token", "password"}` | `204`; sets the password and ends every session |
| `POST /_auth/logout` | yes | none | `204`; ends this session |
| `POST /_auth/logout-all` | yes | none | `204`; ends all of the user's sessions, including this one |
| `GET /_auth/me` | yes | none | `200` with the user |
| `PATCH /_auth/me` | yes | `{"locale"?}` | `200` with the user; changes the user's [language](#language) |
| `DELETE /_auth/me` | yes | `{"password"}` | `204`; **deactivates** the account (see [Deleting and erasing users](../erasure/)) |
| `POST /_auth/email` | yes | `{"new_email", "password", "redirect_to"?}` | `202`; asks to [change the address](#changing-the-email-address), only in realms with `account.allow_email_change` |
| `POST /_auth/confirm-email-change`, `POST /_auth/revert-email-change` | no | `{"token"}` | `204`; the links of an [email change](#changing-the-email-address) |
| `POST /_auth/accept-invitation` | no | `{"token", "password", "locale"?}` | `204`; [accepts an emailed invitation](#accepting-an-invitation) |
| `POST /_auth/password` | yes | `{"current_password", "new_password"}` | `204`; ends all other sessions |
| `GET /_auth/sessions` | yes | none | `200` with the user's sessions |
| `DELETE /_auth/sessions/{id}` | yes | none | `204`; ends one of the user's sessions |

Paths are relative to `/v1/{realm}`.

### Sign-up

```sh
curl -X POST https://localhost:8443/v1/blog/_auth/signup \
  -H 'Content-Type: application/json' \
  -d '{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}'
```

- Realms with `signup: open` accept anyone. Realms with `signup: invite` need an `invitation` token created with the [admin API](../admin/#invitations); each works once, and one bound to an email only works for that email. Realms with `signup: closed` refuse. Refusals answer `403 forbidden`; wrong, used, expired and mismatched invitations all get the same answer.
- If sign-up fails for another reason (weak password, email already registered), the invitation isn't used up.
- The email must be valid, and the password must follow the realm's [password policy](../users/#password-policy). Failures return `400 validation_error` with `details` naming `email` or `password`.
- An email that is already registered returns `409 email_taken`, except in a realm that [requires verified addresses](#email-verification), where it gets the same answer as a new one (see below).
- The new user is signed in at once: the response carries a session, unless the realm [requires a verified email](#email-verification) (then `202`, no session). In a realm with [`email`](../../functions/email/), sign-up also sends the verification email. `redirect_to` is where the page after that link may send the user; it must be within the realm's `email.allowed_redirects` (`400 invalid_redirect`).

### Login

```sh
curl -X POST https://localhost:8443/v1/blog/_auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}'
```

Any failure returns the same `401 invalid_credentials`: unknown email, wrong password, a user without a password, or a disabled user. Repeated failures are slowed down (see [brute-force protection](#brute-force-protection)). Unknown emails take the same time to answer as known ones, so neither the response nor its timing reveals whether an email is registered.

A user with [`login_networks`](../../configuration/realm/#network-restrictions) can only log in from those networks: elsewhere, the right password answers `401 invalid_credentials` too, and counts as a failure. Their session also only works from those networks.

### Email verification

In a realm with [`email`](../../functions/email/), sign-up sends a `verify-email` message whose link opens a page of `backd` with one button. Pressing it sets `email_verified` to `true` on the user, whom [rules](../rules/) see as `user.email_verified`. Opening the link only shows the button, so mail scanners can't use it up. **Verifying never starts a session**: the user signs in as usual. Apps with their own pages send the token to `POST /_auth/verify-email` instead (`204`, or `400 invalid_token` for any token that is expired, used or unknown), and point the link at them with [`email.links`](../../functions/email/#links-and-pages).

- **`POST /_auth/verify-email/resend`** with `{"email"}` sends the message again. It answers `202` whatever the account is (unknown, disabled or already verified), and the [email limits](../../functions/email/#limits) count the same in every case, so it can't be used to learn who is registered.
- **Who is verified from the start:** users created by `backd bootstrap`, by the [admin API](../admin/) or `backd user create`, and users who sign up with an [invitation](../admin/#invitations) bound to their address. Users who sign up themselves, and those whose invitation isn't bound to an address, are not.
- **Requiring it.** With `account.require_verified_email: true` in [`realm.yaml`](../../configuration/realm/#account), sign-up answers `202 {"status": "verification_required"}` without a session, and a login with the **right password** for an unverified address answers `403 email_not_verified`. A wrong password still answers `401 invalid_credentials`, so the answer reveals nothing to someone who doesn't know the password. Users of an existing realm who are unverified when you switch this on are locked out until they use resend or [reset their password](#password-reset). In this mode **sign-up doesn't reveal who is registered**: an address that is already registered gets the same `202` as a new one, and its owner receives an `account-exists` email ("someone tried to sign up with your address; sign in, or reset your password") instead of a verification link. The password is hashed in both cases, so the time taken doesn't tell them apart either, and the email limits stay silent. A realm that hands out a session at sign-up can't do this: the answer for a new address has to be a session, so a registered address has to be refused, and `409 email_taken` tells anyone who asks. If that matters to you, require verified addresses; if it doesn't, the `409` is what lets your app say "this email is taken".
- **Welcome.** `account.welcome_email: true` sends the `welcome` message once the address is verified.
- **Lifetime.** The link works for `account.tokens.verify_email` (48 hours by default).
- **Squatters.** Anyone can sign up with someone else's address and leave it unverified. `account.purge_unverified_after` (off by default) lets a worker delete accounts that never verified after that time (except those with roles), recorded in the [audit trail](../audit/) as `user.purged_unverified` with a count. Documents such accounts created are kept, as with any deleted user.

### Brute-force protection

Failed attempts are counted per account and per client address. Past a threshold, `backd` answers `429 too_many_requests` with a `Retry-After` header (in seconds) without even checking the password:

| Counter | Threshold | Then |
|---|---|---|
| Per email address | 5 failures within 15 minutes | wait 1 second, doubling with each further failure, up to 15 minutes |
| Per client address (IPv4 address, or IPv6 `/64` network) | 50 failures within 15 minutes | the same growing wait |

- There is no lockout: after the wait, the right password always works. Nobody can lock another user out permanently.
- Attempts refused with `429` don't count, so waiting as told is always enough.
- A successful login clears the email's counter, but not the address's.
- Unregistered emails are counted like registered ones, so the answers don't reveal which emails exist.
- Wrong current passwords in `POST /_auth/password` and `DELETE /_auth/me` count against the account too, so a stolen session can't be used to guess the password.
- Counters are stored in the realm's system database, so all `backd` instances share them. They are deleted automatically 15 minutes after the last failure.
- Password checks for one account run **one at a time across all instances**: each takes a short lock kept in the system database (which expires by itself after 30 seconds if its instance dies). Parallel attempts can't all pass before a failure is counted, so many instances allow no more guesses than one. If the lock stays busy for 5 seconds the attempt gets `429` with `Retry-After: 1`.

Behind a reverse proxy or load balancer, set [`TRUSTED_PROXIES`](../../operations/#client-addresses-behind-a-proxy) so that `backd` counts real client addresses rather than the proxy's.

### The current user

`GET /_auth/me` returns the signed-in user:

```json
{"id": "dars2ql90v600434mf0g", "email": "ada@example.com", "email_verified": false, "roles": [], "locale": "en", "created_at": "2026-09-26T12:58:18.838Z"}
```

### Language

Every user has a `locale`, the language of the emails they receive. A realm lists the languages it supports in [`email.locales`](../../functions/email/#languages) (English only when it sends no email), with `email.default_locale` as the fallback.

- **At sign-up** `backd` picks the best listed language for what was asked, silently and without ever failing: the `locale` in the body (such as `es` or `es-MX`), else the browser's `Accept-Language`, matched exactly (`es-MX`), then by language (`es`), else the realm's default. A page sends `locale: navigator.language` to use the browser's language; a browser's `Accept-Language` header is used when the body has none.
- **To change it**, `PATCH /_auth/me` with `{"locale": "es"}`: the value must be a listed language (case is ignored). Anything else answers `400 invalid_locale` with the allowed languages in `details`: an explicit change is never mapped silently.
- Users who signed up before languages existed use the realm's default.

`DELETE /_auth/me` with `{"password": "…"}` **deactivates** the account: it is disabled and every session ends, and all data is kept. A wrong password returns `401 invalid_credentials`. Deactivating is not erasing: erasing a user's data is an administrator's action (see [Deleting and erasing users](../erasure/)), and until then the account's email stays registered.

### Change password

`POST /_auth/password` with `{"current_password": "…", "new_password": "…"}` replaces the password. The current session stays valid and every other session ends. A wrong current password returns `401 invalid_credentials`; a new password that breaks the policy returns `400 validation_error` naming `new_password`.

In a realm with [`email`](../../functions/email/), the owner gets a `password-changed` email after this, after a [reset](#password-reset) and after an administrator sets a password ([`backd user set-password`](../users/)), so a change they didn't make doesn't go unnoticed.

### Password reset

In a realm with `email`, a user who forgot the password asks for a link:

```sh
curl -X POST https://localhost:8443/v1/blog/_auth/reset-password/request \
  -H 'Content-Type: application/json' -d '{"email": "ada@example.com"}'
```

The answer is always `202 {"status": "accepted"}`: for a registered address, an unknown one and a disabled account alike, so it can't be used to find out who is registered. Only an enabled account gets the `reset-password` message. `redirect_to` (within the realm's `email.allowed_redirects`) says where the page after the reset may send the user.

- **The link** opens a page of `backd` with a form for the new password, twice. Opening it never uses the token up. The form shows the password policy's reason on the same page, and a refused password leaves the link usable. Apps with their own pages point the link at them with [`email.links`](../../functions/email/#links-and-pages) and post `{"token", "password"}` to `POST /_auth/reset-password` (`204`, or `400 invalid_token` for an expired, used or unknown token, `400 validation_error` for a password the policy refuses).
- **What a reset does:** sets the password, **ends every session of the user**, marks the address **verified** (the token proves they read that mailbox, and it ends a squatter's access), and sends `password-changed`. It never starts a session: the user logs in afterwards. A token works once and for `account.tokens.reset_password` (1 hour by default).
- **Limits.** Requests are counted per address (5 per 15 minutes) and per client address (30 per 15 minutes), for every address alike: past either, the answer is `429 too_many_requests` with `Retry-After`. The [email limits](../../functions/email/#limits) apply on top and stay silent. A realm without `email` answers `404`: there, an operator sets passwords with [`backd user set-password`](../users/).

### Changing the email address

In a realm with [`email`](../../functions/email/) and `account.allow_email_change: true` (off by default: then the route doesn't exist, `404`), a signed-in user changes their address:

```sh
curl -X POST https://localhost:8443/v1/blog/_auth/email \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"new_email": "ada.new@example.com", "password": "dev-p4ssw0rd!"}'
```

It needs the session **and the current password** (a wrong one counts against the account like a failed login). The answer is `202` whether or not the new address is free, so it can't be used to find out who is registered; nothing changes yet.

- **Confirmation.** A link goes to the **new** address (`confirm-email-change`, valid `account.tokens.change_email`, 24 hours by default). The hosted page names the address and has one button; opening the link never uses it up. Pressing it changes the address (uniqueness is checked again: an address registered meanwhile fails like any bad link), marks it verified, **ends every session** and starts none. A newer request replaces an older, unconfirmed one.
- **Undo.** The **old** address receives `email-changed` with a link valid `account.tokens.revert_email_change` (7 days by default). Using it restores the old address, ends every session, makes the current password unusable (whoever changed the address may know it) and sends a password reset link to the restored address. It works once, and only if no other change happened since.
- Apps with their own pages post `{"token"}` to `POST /_auth/confirm-email-change` and `POST /_auth/revert-email-change` (`204`, or `400 invalid_token`), and point the links at them with [`email.links`](../../functions/email/#links-and-pages) (`change_email`, `revert_email_change`).
- Audited as `user.email_changed` and `user.email_change_reverted`. An administrator can change an address without the user's involvement: see the [admin API](../admin/#changing-a-users-email).

### Accepting an invitation

An invitation [sent by email](../admin/#emailing-an-invitation) opens a page where the invited person chooses a password. Posting the form (or `{"token", "password", "locale"?}` to `POST /_auth/accept-invitation`, `204`) creates the account for the invited address, **already verified** (only its owner received the link), and uses up the invitation. No session is issued: the user logs in. A password the policy refuses leaves the link usable; a revoked or expired invitation, or an address registered meanwhile, is `400 invalid_token`. It works in any realm with `email`, whatever its `signup` mode.

### Sessions

`GET /_auth/sessions` lists the user's active sessions, newest first:

```json
{
  "items": [
    {"id": "dars2ql90v6000bbohr0", "created_at": "2026-09-26T12:58:18.988Z", "last_used_at": "2026-09-26T12:58:18.988Z", "expires_at": "2026-10-26T12:58:18.988Z", "current": true}
  ]
}
```

`DELETE /_auth/sessions/{id}` ends one of them, for example a lost device. Ids of other users' sessions return `404 not_found`.

## Errors

| HTTP | `code` | When |
|---|---|---|
| 400 | `validation_error` | Missing, wrongly typed or unknown fields; invalid email; password breaks the policy |
| 401 | `unauthenticated` | No `Authorization: Bearer` header, or the token is unknown, expired, revoked, or its user is disabled |
| 401 | `invalid_credentials` | Wrong email or password, or wrong current password |
| 400 | `invalid_redirect` | `redirect_to` isn't within the realm's `email.allowed_redirects` |
| 400 | `invalid_token` | An email link's token is unknown, expired, used or for another purpose (JSON endpoints) |
| 403 | `forbidden` | Sign-up is closed in this realm, or needs a valid invitation |
| 403 | `email_not_verified` | The password is right but the realm requires a verified address |
| 404 | `not_found` | Unknown realm, realm with `auth: disabled`, or a session id that isn't yours |
| 409 | `email_taken` | Sign-up with a registered email, in a realm that issues a session at sign-up |
| 429 | `too_many_requests` | Too many failed logins or password checks; wait `Retry-After` seconds |
| 503 | `unavailable` | Too many password operations at once; retry after the `Retry-After` delay |

`401 unauthenticated` responses include a `WWW-Authenticate: Bearer realm="<realm>"` header, with `error="invalid_token"` when a token was sent but not accepted.
