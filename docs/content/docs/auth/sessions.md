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
- Sessions end immediately on logout, when revoked, when the password changes (other sessions only), when the user is disabled or deleted, or when an operator sets a new password with `backd user set-password`.

## Endpoints

All request bodies are JSON objects sent with `Content-Type: application/json`. Unknown fields are rejected.

| Method and path | Needs a session | Body | Success |
|---|---|---|---|
| `POST /_auth/signup` | no | `{"email", "password", "invitation"?}` | `201` with a session |
| `POST /_auth/login` | no | `{"email", "password"}` | `200` with a session |
| `POST /_auth/logout` | yes | none | `204`; ends this session |
| `POST /_auth/logout-all` | yes | none | `204`; ends all of the user's sessions, including this one |
| `GET /_auth/me` | yes | none | `200` with the user |
| `DELETE /_auth/me` | yes | `{"password"}` | `204`; deletes the account |
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
- An email that is already registered returns `409 email_taken`.
- The new user is signed in at once: the response carries a session.

### Login

```sh
curl -X POST https://localhost:8443/v1/blog/_auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}'
```

Any failure returns the same `401 invalid_credentials`: unknown email, wrong password, a user without a password, or a disabled user. Repeated failures are slowed down (see [brute-force protection](#brute-force-protection)). Unknown emails take the same time to answer as known ones, so neither the response nor its timing reveals whether an email is registered.

A user with [`login_networks`](../../configuration/realm/#network-restrictions) can only log in from those networks: elsewhere, the right password answers `401 invalid_credentials` too, and counts as a failure. Their session also only works from those networks.

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

Behind a reverse proxy or load balancer, set [`TRUSTED_PROXIES`](../../operations/#client-addresses-behind-a-proxy) so that `backd` counts real client addresses rather than the proxy's.

### The current user

`GET /_auth/me` returns the signed-in user:

```json
{"id": "dars2ql90v600434mf0g", "email": "ada@example.com", "email_verified": false, "roles": [], "created_at": "2026-09-26T12:58:18.838Z"}
```

`DELETE /_auth/me` with `{"password": "…"}` deletes the account, its sign-in methods and sessions. Documents the user owns are kept. A wrong password returns `401 invalid_credentials`.

### Change password

`POST /_auth/password` with `{"current_password": "…", "new_password": "…"}` replaces the password. The current session stays valid and every other session ends. A wrong current password returns `401 invalid_credentials`; a new password that breaks the policy returns `400 validation_error` naming `new_password`.

There is no self-service password reset yet, because `backd` doesn't send email. An operator can set a new password with [`backd user set-password`](../users/).

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
| 403 | `forbidden` | Sign-up is closed in this realm, or needs a valid invitation |
| 404 | `not_found` | Unknown realm, realm with `auth: disabled`, or a session id that isn't yours |
| 409 | `email_taken` | Sign-up with a registered email |
| 429 | `too_many_requests` | Too many failed logins or password checks; wait `Retry-After` seconds |
| 503 | `unavailable` | Too many password operations at once; retry after the `Retry-After` delay |

`401 unauthenticated` responses include a `WWW-Authenticate: Bearer realm="<realm>"` header, with `error="invalid_token"` when a token was sent but not accepted.
