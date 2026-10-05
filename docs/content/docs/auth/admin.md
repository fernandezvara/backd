---
title: "Admin API"
description: "Manage a realm's users and roles from server-side services with an API key."
icon: "admin_panel_settings"
weight: 550
toc: true
---

The admin API manages the users and API keys of a realm. [`backd user`](../users/) and [`backd apikey`](../api-keys/) are command-line clients of it (see [Command-line administration](../cli/)), and server-side services can call it directly. It lives under `/v1/{realm}/_admin` in every realm with `auth: enabled`.

Every request needs one of the realm's [API keys](../api-keys/) with the `admin` role, acting as itself, or the session of a user holding one of the realm's [admin roles](../../configuration/realm/#roles):

| Credential | Answer |
|---|---|
| API key with the `admin` role | Allowed |
| Session of a user holding an admin role (`admin: true` in `realm.yaml`) | Allowed |
| Session of a user whose admin role opens only some [areas](#admin-rights) | Allowed for those areas; `403 forbidden` naming the missing one for the rest |
| API key with the `data` role | `403 forbidden` |
| None, or an invalid key | `401 unauthenticated` |
| Session of a user without an admin role | `403 forbidden` |
| `X-Backd-On-Behalf-Of` with any credential | `400 invalid_header`: admin calls never act as another user |

{{< hint danger >}}
Never call these endpoints from a browser or a mobile app with an API key: the key would be exposed, and an `admin` key can manage every user of the realm.
{{< /hint >}}

Requests from outside the realm's `admin.allowed_networks`, or outside an admin user's own `admin_networks`, answer `404 not_found` (see [network restrictions](../../configuration/realm/#network-restrictions)).

The [Admin UI](../admin-ui/) is a web interface to this API, for administrators who prefer a browser.

## Admin rights

An admin role can open the whole admin API (`admin: true`), only some of its areas (`admin: [users, invitations]`), or all of it **to read** (`admin: read`), so that the person who invites teammates doesn't also hold the secrets and the keys, and developers can look without being able to change anything. A user's roles add up, and an admin API key always opens every area.

| Area | Endpoints |
|---|---|
| `users` | `/users` and everything under `/users/{id}`: reading, disabling, erasing, passwords, addresses, roles and networks |
| `invitations` | `/invitations` |
| `apikeys` | `/apikeys` |
| `secrets` | `/secrets` |
| `audit` | `GET /audit` |
| `functions` | `POST /jobs/{id}/cancel` | none | `200` with the job, now `done` with the result `cancelled`: a queued, retrying or running function job ends at once and a worker running it stops the run. `409` when it has already finished or isn't a function's job. Audited. See [Cancelling and re-running a job](../../functions/jobs/#cancelling-and-re-running-a-job) |
| `POST /jobs/{id}/rerun` | none | `202` with a new job (`rerun_of` names the original) for a finished function job: the same function, input and caller. `409` while it hasn't finished, or when it isn't a function's job or its function no longer exists. Audited |
| `POST /functions/{database}/{name}/invoke`, `GET /invocations`, `GET /jobs`, and `POST /jobs/{id}/cancel` and `…/rerun`; and reading any job through the data API |
| `data` | the admin data route (documents past their collections' rules) |
| `config` | `GET /config`, the read-only view of the realm's configuration |

### The read-only level

`admin: read` reads every area and changes none: every `GET` under `/_admin` answers, every other method answers `403` ("can read the area but not change it"). Two areas hold personal data, so a realm opts in: **users** and **data** are read-only-visible only with `admin.read_access.users` and `admin.read_access.data` set to `true` in [`realm.yaml`](../../configuration/realm/#keys). API keys are listed (never the keys) and secrets by name (never a value). A list may add `read` to a few writable areas: `admin: [read, secrets]` reads everything and changes secrets. The same rules apply to every credential: network restrictions, session limits for administrators, and the audit trail of refusals.

`GET /whoami` tells a client what the signed-in administrator may do (`level`: `full`, `read` or `custom`; the areas it may `write` and `read`; `read_access`), so an interface offers only what the server would allow.

A request for an area the roles don't open answers `403 forbidden` with the missing area in the message, and is recorded in the [audit trail](../audit/) as `admin.refused` with the reason, the method and the path.

An administrator can't use these endpoints to end up holding more than they started with:

- **A role is granted or taken away only by someone who holds everything it opens,** reading included: giving `admin: read` (every area to read) takes a caller who can read every area. `support` (`users`, `invitations`) can give `support` or a role that is no admin role, but not `staff` (`admin: true`), and not `keeper` (`apikeys`, `secrets`) either.
- **A user who holds admin rights the caller doesn't can be read, and nothing more:** changing their password or address, disabling, erasing, re-roling them or setting their networks answers `403`, since each would be a way to become them.
- **Admin API keys are created and revoked only by full administrators** (`admin: true` or an admin key), because such a key opens every area. Data keys follow the `apikeys` area.

Keep one role with `admin: true`, held by the people who recover the realm: `backd bootstrap` needs it for the first administrator, and startup warns when a realm has none.

## Endpoints

Paths are relative to `/v1/{realm}/_admin`. Bodies are JSON (`Content-Type: application/json`) and unknown fields are rejected.

| Method and path | Body | Success |
|---|---|---|
| `GET /users` | none; query `limit` (1–100, default 20), `skip` or `after`, `email` (exact), `q` (search: plain text, at most 254 characters) | `200` with a page of users, sorted by email, read from the database a page at a time; `next_cursor` (the last email) while `has_more`: send it as `after` for the next page |
| `POST /users` | `{"email", "password"?}` | `201` with the user |
| `GET /users/{id}` | none | `200` with the user |
| `PATCH /users/{id}` | `{"email_verified"?: bool, "disabled"?: bool}` | `200` with the user |
| `DELETE /users/{id}` | none | `202` with the erase job: [**erases** the user](../erasure/) (irreversible) |
| `GET /users/{id}/sessions` | none | `200` with the user's unexpired sessions, newest first (`id`, `created_at`, `last_used_at`, `expires_at`; never a token) |
| `DELETE /users/{id}/sessions/{session_id}` | none | `204`; the session's token stops working at once; audited as `session.revoke`. `404` for an unknown session |
| `POST /users/{id}/password` | `{"password"}` | `204` |
| `GET /users/{id}/owned` | none | `200` with [what erasing the user would do](#previewing-an-erase) |
| `POST /users/{id}/email` | `{"email"}` | `200` with the user; [changes the address](#changing-a-users-email) |
| `PUT /users/{id}/roles/{role}` | none | `200` with the user |
| `DELETE /users/{id}/roles/{role}` | none | `200` with the user |
| `PUT /users/{id}/networks` | `{"admin_networks": [...], "login_networks": [...]}` | `200` with the user |
| `GET /whoami` | none | `200` with what this credential may do: `level`, the areas it may `write` and `read`, and `read_access` (see [the read-only level](#the-read-only-level)) |
| `GET /config` | none | `200` with the realm's [configuration](#configuration), read-only |
| `POST /invitations` | `{"email"?, "expires_in"?, "send"?, "redirect_to"?, "locale"?}` | `201` with the invitation and its token, or `sent: true` and no token when it is [emailed](#emailing-an-invitation) |
| `GET /invitations` | none | `200` with unexpired, unused invitations |
| `DELETE /invitations/{id}` | none | `204` |
| `GET /apikeys` | none | `200` with the realm's API keys (never the keys themselves) |
| `POST /apikeys` | `{"name", "role"?, "expires_in"?, "networks"?, "scopes"?}` | `201` with the key, shown only here |
| `DELETE /apikeys/{name}` | none | `204`; the key stops working at once |
| `GET /secrets` | none | `200` with the realm's [secrets](../../functions/secrets/)' metadata (never their values) |
| `PUT /secrets/{name}` | `{"value", "database"?}` | `204`; creates or replaces the value |
| `DELETE /secrets/{name}` | none; query `database`? | `204` |
| `GET /audit` | none; query `action`, `actor`, `target`, `since`, `until` (RFC 3339), `limit`, `skip` | `200` with a page of [audit records](../audit/), newest first |
| `GET /jobs` | none; query `function` (`<database>/<name>`), `status` (`queued`, `running`, `done`), `origin` (such as `function:main/ship`, the emails that function sent), `scheduled` (`true` for cron runs), `since`, `until` (RFC 3339, on the creation time), `limit`, `skip` | `200` with a page of [jobs](../../functions/jobs/), newest first: `id`, `function`, `status`, `scheduled`, `origin`, `email_kind` (the kind of an email job, never its recipients), `attempts`, `created_at`, `completed_at` and `result` (`status`, `code`, `duration_ms`), never a job's input or output. `backd functions jobs` is its CLI |
| `POST /functions/{database}/{name}/invoke` | `{"input"?, "as"?}`; header `Idempotency-Key`? | Runs any function by hand, internal ones included, as the user `as` (an email) or with no user; answers like `_func` (`200` with the output, or `202` with a job). Audited. See [Internal functions](../../functions/internal/#running-a-function-by-hand) |
| `GET /invocations` | none; query `function`, `request_id`, `since`, `until` (RFC 3339), `limit`, `skip` | `200` with a page of [function invocation records](../../functions/logs/), newest first |

A user looks like this:

```json
{
  "id": "dars2ql90v600434mf0g",
  "email": "ada@example.com",
  "email_verified": true,
  "roles": ["editor"],
  "locale": "en",
  "disabled": false,
  "erased_at": null,
  "admin_networks": [],
  "login_networks": ["10.20.0.0/16"],
  "created_at": "2026-09-26T12:58:18.838Z",
  "updated_at": "2026-09-26T13:02:41.120Z"
}
```

### Finding a user by email

`GET /users?email=ada@example.com` returns the user with exactly that email (case doesn't matter), or an empty list. Services use it to turn an email into a user id, for example to add someone to a document's member list. Signed-in users can't look other users up, so browsers can't use this to discover which emails are registered.

### Behavior

- **Create:** the email must be valid and not registered (`409 email_taken`). The user starts with a verified address: an administrator vouches for it. Without `password`, the user can't sign in with a password until one is set. Roles seeded in `realm.yaml` for that email are applied.
- **Update:** only `email_verified` and `disabled` can change. Disabling a user ends all their sessions and blocks login. Sending `email` answers `400`: an address changes through [its own call](#changing-a-users-email), which sends the notices a change needs.
- **Password:** must follow the realm's [password policy](../users/#password-policy); all of the user's sessions end, and in a realm with [email](../../functions/email/) the user gets a `password-changed` message.
- **Roles:** only roles declared in `realm.yaml` (`400` otherwise). Like `backd user add-role`, assignments that `realm.yaml` doesn't list live only in the database, and removing a seeded one only lasts until the next startup.
- **Delete is an erase:** the user becomes a tombstone at once (the id stays, the email becomes `erased-<id>@erased.invalid`, and the sessions, sign-in methods, email tokens and queued emails are deleted), and a worker applies the collections' policies. The answer is the erase job; repeating the call for a user whose job failed resumes it, and for an erased user answers `409 already_erased`. To keep the data, disable the user instead. Everything else on an erased user answers `409 user_erased`. See [Deleting and erasing users](../erasure/).
- Unknown user ids answer `404 not_found`.

### Network restrictions

`PUT /users/{id}/networks` replaces the user's [network restrictions](../../configuration/realm/#network-restrictions): `admin_networks` (their admin API requests; within the realm's `admin.allowed_networks`, or `400`) and `login_networks` (their login and session). Both lists are required; empty lists remove the restriction. For users whose networks `realm.yaml` sets, `realm.yaml` wins again at the next startup.

### API keys

The same operations as [`backd apikey`](../api-keys/), over HTTP:

```sh
curl -X POST https://localhost:8443/v1/blog/_admin/apikeys \
  -H "Authorization: Bearer $BACKD_API_KEY" -H 'Content-Type: application/json' \
  -d '{"name": "billing-2026q4", "role": "data", "expires_in": "90d", "networks": ["203.0.113.0/24"], "scopes": ["read:main/posts"]}'
```

```json
{
  "name": "billing-2026q4",
  "role": "data",
  "prefix": "bdk_7VeQ1h",
  "networks": ["203.0.113.0/24"],
  "scopes": ["read:main/posts"],
  "created_at": "2026-09-27T10:00:00.000Z",
  "last_used_at": null,
  "expires_at": "2026-12-26T10:00:00.000Z",
  "key": "bdk_7VeQ1hN0m2k…"
}
```

- `key` is shown only in this response; `backd` stores just its hash.
- `role` is `data` (default) or `admin`; `expires_in` takes days or Go durations, and without it the key never expires; `networks` [pins the key](../api-keys/#network-restrictions); `scopes` (up to 32 grants such as `read:main/posts`) [limit what a data key reaches](../api-keys/#scopes) and must name databases, collections and functions the realm has.
- A name that's already used answers `409 conflict`. `GET /apikeys` lists name, role, first characters, scopes (empty: everything), networks, creation, last use and expiry.

### Secrets

Functions read secrets you set here as `ctx.secrets`, decrypted (see [Functions → Secrets](../../functions/secrets/)). `backd secret set|list|delete` are command-line clients of these endpoints:

```sh
curl -X PUT https://localhost:8443/v1/blog/_admin/secrets/STRIPE_KEY \
  -H "Authorization: Bearer $BACKD_API_KEY" -H 'Content-Type: application/json' \
  -d '{"value": "sk_live_…", "database": "shop"}'
```

```json
{"items": [
  {"database": "shop", "name": "STRIPE_KEY", "created_at": "2026-09-28T10:00:00.000Z", "updated_at": "2026-09-28T10:00:00.000Z", "updated_by": "key:publisher"}
]}
```

- Without `database`, the secret is in the **realm scope** (`realm.NAME` in `function.yaml`); with it, that **database's scope** (`NAME`, readable only by functions of that database — never another's, even in the same realm).
- `value` must not be empty; `name` must be upper-case letters, digits and `_`, starting with a letter (`400` otherwise). Setting an existing scope and name replaces its value; `GET /secrets` never includes it, only who last set it and when.
- A change reaches running functions within about a minute (decrypted values are cached briefly); deleting one makes a function that declares it answer `500 secret_missing` again.
- Answers `500` (`internal_error`) if the server has no `BACKD_SECRETS_KEY` configured: an operator problem, not something a request can fix.

### Previewing an erase

`GET /users/{id}/owned` (`backd user owned --email …`, `admin.users.owned(id)` in the JavaScript client) answers what an erase would do to a user's data, **only for the collections that declare a policy** in [`collection.yaml`](../../configuration/config-dir/#collectionyaml):

```json
{
  "user": { "id": "dars2ql90v600434mf0g", "status": "active" },
  "collections": [
    { "database": "main", "collection": "orders", "action": "anonymize", "owned": 12,
      "remove": ["phone"], "replace": ["buyer_name"] },
    { "database": "main", "collection": "groups", "action": null, "owned": 0,
      "pull": { "members": 3 }, "unset": { "paid_by": 0 } }
  ],
  "without_policy": ["main.digests", "main.events"]
}
```

- `owned` counts the documents the user owns (`_meta.owner`); `pull` and `unset` count, per field, the documents that hold the user (by email or id, as the policy says). `status` is `active` or `deactivated`.
- The collections **without a policy** are listed by name and not counted: an erase leaves them alone.
- It returns counts and field names, never document content. The counts use indexes that `backd provision` creates from the policies.

### Changing a user's email

`POST /users/{id}/email` with `{"email": "new@example.com"}` changes the address **at once**, in any realm with [`email`](../../functions/email/) (`404` without), whatever `account.allow_email_change` says: the administrator vouches for the address, so it counts as verified. The user's sessions end, the old address is sent `email-changed` with a link to undo the change for 7 days (the undo restores the address, ends every session, makes the current password unusable and sends a password reset link to it), and the new address is told. An address another user has answers `409 email_taken`. Audited as `user.email_changed` with `by: admin`.

### Configuration

`GET /v1/{realm}/_admin/config` shows what this instance runs for the realm, read from the files it loaded, so an operator can check what is live without logging into the host: the realm's settings (defaults applied), every database's collections (schema, indexes, rules, the `collection.yaml` policy) and functions (`function.yaml`: mode, limits, `calls`, `network`, secrets **by name only**), the email templates and hosted pages found on disk, the **configuration fingerprint** (the same `/readyz` shows) and a list of warnings (authentication disabled, no administrator role, an admin API reachable from any network, open sign-up, the development delivery function). Each item carries the file it came from, relative to the config directory. Seeded user emails show only to callers who may read users. It needs the `config` [area](#admin-rights), which a read-only administrator has.

Nothing here can be changed through the API: configuration is a reviewed, versioned artifact, and a change goes through your repository and a deploy.

### Data

`/v1/{realm}/_admin/data/{database}/{collection}[/{id}]` (and `/data/{database}/_batch`) serve the [data routes](../../api/documents/)' operations to an administrator **past the collection's rules**: list, get, create, `PUT`, `PATCH`, `DELETE` (soft or hard as the collection decides, with `?purge=true` and `…/restore` for a [soft-deleting](../../configuration/config-dir/#soft-delete) one), and the `deleted` and `where` parameters of the data routes. Everything else is the same: the schema validates every write, `_meta.version`, `ETag` and `If-Match` work, errors have the same shape. It needs the `data` [area](#admin-rights): `admin: true` and an admin key have it, an area list has it only if it names `data`, and a read-only administrator reads only when `admin.read_access.data` is `true` (every change answers `403`).

- A document created here has **no owner** (`_meta.owner` is `null`): the administrator didn't create it for themselves. `created_by` and `updated_by` record who did, `user:<id>` or `key:<name>`.
- **Writes are audited**, never their content: `data.create`, `data.update`, `data.delete`, `data.restore` and `data.purge`, with the actor and the target `doc:<database>/<collection>/<id>`. Reads are not in the audit trail; they carry the actor in the access log.
- The normal data routes are unchanged: an administrator using an app is bound by its rules there.
- With `BACKD_ADMIN_API=false` the route doesn't exist (`404`).

### Invitations

In a realm with `signup: invite`, people can only sign up with an invitation. A service creates one and delivers its token, for example by email:

```sh
curl -X POST https://localhost:8443/v1/blog/_admin/invitations \
  -H "Authorization: Bearer $BACKD_API_KEY" -H 'Content-Type: application/json' \
  -d '{"email": "ada@example.com", "expires_in": "3d"}'
```

```json
{
  "id": "dasb1l190v6000bbojk0",
  "token": "bdi_ZkN0x3Qm…",
  "email": "ada@example.com",
  "created_by": "key:publisher",
  "created_at": "2026-09-26T16:40:00.000Z",
  "expires_at": "2026-09-29T16:40:00.000Z"
}
```

- `token` is shown only in this response; `backd` stores just its hash. The invitee sends it as `invitation` to [`POST /_auth/signup`](../sessions/#sign-up).
- `email` is optional. With it, only that email can sign up with the invitation; without it, anyone holding the token can.
- `expires_in` takes days (`7d`) or Go durations (`12h`): default 7 days, from 1 minute to 90 days.
- Each invitation works once. `GET /invitations` lists those not used or expired yet (never their tokens); `DELETE /invitations/{id}` revokes one.
- Expired and used invitations are deleted automatically.

#### Emailing an invitation

In a realm with [`email`](../../functions/email/), `"send": true` (with an `email`) makes `backd` email the invitation instead of returning a token to deliver yourself:

```sh
curl -X POST https://localhost:8443/v1/blog/_admin/invitations \
  -H "Authorization: Bearer $BACKD_API_KEY" -H 'Content-Type: application/json' \
  -d '{"email": "ada@example.com", "send": true, "redirect_to": "https://app.example.com/welcome", "locale": "es"}'
```

The answer has `"sent": true` and **no token**: nobody holds one. The message carries a link to a page of `backd` (the realm's `pages/accept-invitation`, showing the invited address) where the person chooses a password, or to your own page with [`email.links.invitation`](../../functions/email/#links-and-pages), which posts `{token, password, locale?}` to [`POST /_auth/accept-invitation`](../sessions/#accepting-an-invitation). The link works as long as the invitation does (`expires_in`) and once. `redirect_to` (within `email.allowed_redirects`) and `locale` (the email's language) only apply with `send`. A revoked invitation's link stops working. The email limits apply: past them the answer is `429` and no invitation is left behind.

The access log records the caller as `key:<name>`.
