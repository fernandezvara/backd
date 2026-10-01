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
| API key with the `data` role | `403 forbidden` |
| None, or an invalid key | `401 unauthenticated` |
| Session of a user without an admin role | `403 forbidden` |
| `X-Backd-On-Behalf-Of` with any credential | `400 invalid_header`: admin calls never act as another user |

{{< hint danger >}}
Never call these endpoints from a browser or a mobile app with an API key: the key would be exposed, and an `admin` key can manage every user of the realm.
{{< /hint >}}

Requests from outside the realm's `admin.allowed_networks`, or outside an admin user's own `admin_networks`, answer `404 not_found` (see [network restrictions](../../configuration/realm/#network-restrictions)).

## Endpoints

Paths are relative to `/v1/{realm}/_admin`. Bodies are JSON (`Content-Type: application/json`) and unknown fields are rejected.

| Method and path | Body | Success |
|---|---|---|
| `GET /users` | none; query `limit` (1–100, default 20), `skip`, `email` | `200` with a page of users |
| `POST /users` | `{"email", "password"?}` | `201` with the user |
| `GET /users/{id}` | none | `200` with the user |
| `PATCH /users/{id}` | `{"email_verified"?: bool, "disabled"?: bool}` | `200` with the user |
| `DELETE /users/{id}` | none | `204` |
| `POST /users/{id}/password` | `{"password"}` | `204` |
| `PUT /users/{id}/roles/{role}` | none | `200` with the user |
| `DELETE /users/{id}/roles/{role}` | none | `200` with the user |
| `PUT /users/{id}/networks` | `{"admin_networks": [...], "login_networks": [...]}` | `200` with the user |
| `POST /invitations` | `{"email"?, "expires_in"?}` | `201` with the invitation and its token |
| `GET /invitations` | none | `200` with unexpired, unused invitations |
| `DELETE /invitations/{id}` | none | `204` |
| `GET /apikeys` | none | `200` with the realm's API keys (never the keys themselves) |
| `POST /apikeys` | `{"name", "role"?, "expires_in"?, "networks"?}` | `201` with the key, shown only here |
| `DELETE /apikeys/{name}` | none | `204`; the key stops working at once |
| `GET /secrets` | none | `200` with the realm's [secrets](../../functions/secrets/)' metadata (never their values) |
| `PUT /secrets/{name}` | `{"value", "database"?}` | `204`; creates or replaces the value |
| `DELETE /secrets/{name}` | none; query `database`? | `204` |
| `GET /audit` | none; query `action`, `actor`, `target`, `since`, `until` (RFC 3339), `limit`, `skip` | `200` with a page of [audit records](../audit/), newest first |
| `GET /jobs` | none; query `function` (`<database>/<name>`), `status` (`queued`, `running`, `done`), `scheduled` (`true` for cron runs), `since`, `until` (RFC 3339, on the creation time), `limit`, `skip` | `200` with a page of [jobs](../../functions/jobs/), newest first: `id`, `function`, `status`, `scheduled`, `attempts`, `created_at`, `completed_at` and `result` (`status`, `code`, `duration_ms`), never a job's input or output. `backd functions jobs` is its CLI |
| `POST /functions/{database}/{name}/invoke` | `{"input"?, "as"?}`; header `Idempotency-Key`? | Runs any function by hand, internal ones included, as the user `as` (an email) or with no user; answers like `_func` (`200` with the output, or `202` with a job). Audited. See [Internal functions](../../functions/internal/#running-a-function-by-hand) |
| `GET /invocations` | none; query `function`, `request_id`, `since`, `until` (RFC 3339), `limit`, `skip` | `200` with a page of [function invocation records](../../functions/logs/), newest first |

A user looks like this:

```json
{
  "id": "dars2ql90v600434mf0g",
  "email": "ada@example.com",
  "email_verified": false,
  "roles": ["editor"],
  "disabled": false,
  "admin_networks": [],
  "login_networks": ["10.20.0.0/16"],
  "created_at": "2026-09-26T12:58:18.838Z",
  "updated_at": "2026-09-26T13:02:41.120Z"
}
```

### Finding a user by email

`GET /users?email=ada@example.com` returns the user with exactly that email (case doesn't matter), or an empty list. Services use it to turn an email into a user id, for example to add someone to a document's member list. Signed-in users can't look other users up, so browsers can't use this to discover which emails are registered.

### Behavior

- **Create:** the email must be valid and not registered (`409 email_taken`). Without `password`, the user can't sign in with a password until one is set. Roles seeded in `realm.yaml` for that email are applied.
- **Update:** only `email_verified` and `disabled` can change. Disabling a user ends all their sessions and blocks login. Sending `email` answers `400`: emails can't be changed through `backd`.
- **Password:** must follow the realm's [password policy](../users/#password-policy); all of the user's sessions end.
- **Roles:** only roles declared in `realm.yaml` (`400` otherwise). Like `backd user add-role`, assignments that `realm.yaml` doesn't list live only in the database, and removing a seeded one only lasts until the next startup.
- **Delete:** removes the user, their sign-in methods and sessions. Documents they own are kept.
- Unknown user ids answer `404 not_found`.

### Network restrictions

`PUT /users/{id}/networks` replaces the user's [network restrictions](../../configuration/realm/#network-restrictions): `admin_networks` (their admin API requests; within the realm's `admin.allowed_networks`, or `400`) and `login_networks` (their login and session). Both lists are required; empty lists remove the restriction. For users whose networks `realm.yaml` sets, `realm.yaml` wins again at the next startup.

### API keys

The same operations as [`backd apikey`](../api-keys/), over HTTP:

```sh
curl -X POST https://localhost:8443/v1/blog/_admin/apikeys \
  -H "Authorization: Bearer $BACKD_API_KEY" -H 'Content-Type: application/json' \
  -d '{"name": "billing-2026q4", "role": "data", "expires_in": "90d", "networks": ["203.0.113.0/24"]}'
```

```json
{
  "name": "billing-2026q4",
  "role": "data",
  "prefix": "bdk_7VeQ1h",
  "networks": ["203.0.113.0/24"],
  "created_at": "2026-09-27T10:00:00.000Z",
  "last_used_at": null,
  "expires_at": "2026-12-26T10:00:00.000Z",
  "key": "bdk_7VeQ1hN0m2k…"
}
```

- `key` is shown only in this response; `backd` stores just its hash.
- `role` is `data` (default) or `admin`; `expires_in` takes days or Go durations, and without it the key never expires; `networks` [pins the key](../api-keys/#network-restrictions).
- A name that's already used answers `409 conflict`. `GET /apikeys` lists name, role, first characters, networks, creation, last use and expiry.

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

The access log records the caller as `key:<name>`.
