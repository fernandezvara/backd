---
title: "Realm settings (realm.yaml)"
description: "Configure authentication, sign-up, sessions, passwords, CORS and roles for a realm."
icon: "domain"
weight: 215
toc: true
---

Every realm directory must contain a `realm.yaml`. It holds the settings that apply to the whole realm: its users, how they sign in, and which browser origins may call it.

```
$CONFIG_DIR/<realm>/realm.yaml
```

If a realm has no `realm.yaml`, `backd` refuses to start and names the realm. Create a commented starter file with:

```sh
CONFIG_DIR=./config backd template realm --realm <realm>
```

## Example

```yaml
auth: enabled
signup: invite
sessions:
  idle_timeout: 14d
  max_lifetime: 60d
password:
  min_length: 14
cors:
  origins:
    - https://app.example.com
    - http://localhost:5173
roles:
  admin:
    description: Manages users and invitations
    admin: true
    users:
      - ops@example.com
      - email: lead@example.com
  editor:
    description: Can publish articles
```

An empty file, or one with only comments, is valid: every key has a default.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `auth` | `enabled` | `enabled` or `disabled`. With `enabled`, data routes need credentials (see [who can access data](../../auth/api-keys/#who-can-access-data)) and provisioning creates the realm's [system database](../../operations/#realm-system-databases). A realm with `disabled` has no users, keys or access rules, so anyone who can reach `backd` can read and modify its data; `backd` logs a warning for it at startup. |
| `signup` | `closed` | Who can create accounts: `open` (anyone), `invite` (needs an [invitation](../../auth/admin/#invitations)), `closed` (only operators, with the CLI or the admin API). |
| `sessions.idle_timeout` | `30d` | A session expires after this long without use. |
| `sessions.max_lifetime` | `90d` | A session never lives longer than this. Must be at least `idle_timeout`. |
| `sessions.cookie.enabled` | `false` | `true`: browser apps can keep a session in an HttpOnly cookie, when their login asks for one. Needs explicit `cors.origins` (no `*`). See [Session cookies](../../auth/sessions/#session-cookies) |
| `sessions.cookie.same_site` | `lax` | `lax`, `strict` or `none` (an app on another site than the API) |
| `sessions.admin_idle_timeout` | `idle_timeout` | The idle timeout for users who hold an [admin role](#roles). Can only shorten `idle_timeout`. |
| `sessions.admin_max_lifetime` | `max_lifetime` | The longest an admin's session lives. Can only shorten `max_lifetime`; must be at least `admin_idle_timeout`. |
| `password.min_length` | `12` | Minimum password length, from 8 to 128. |
| `login_throttle.account_threshold` | `5` | Failed logins an email address gets before each new attempt must wait (1 or more). See [Brute-force protection](../../auth/sessions/#brute-force-protection). |
| `login_throttle.ip_threshold` | `50` | The same per client address (an IPv6 `/64`), higher because a network can hide many people. |
| `login_throttle.window` | `15m` | How long a failure counts: a counter expires this long after its last failure. At least `1m`. |
| `login_throttle.max_delay` | `15m` | The longest anyone waits; the wait starts at 1 second and doubles with each failure. At least `1s`, and not more than `window`. |
| `cors.origins` | none (CORS off) | Browser origins allowed to call this realm, as `scheme://host[:port]`. `*` allows any origin and must be the only entry. See [CORS](#cors). |
| `roles` | none | Roles that access rules can check. See below. |
| `audit.retention` | `365d` | How long [audit records](../../auth/audit/) are kept, at least `1d`. |
| `admin.read_access.users` | `false` | `true`: read-only administrators (`admin: read`) can also read users (emails, roles, sessions). Users hold personal data, so a realm opts in. |
| `admin.read_access.data` | `false` | `true`: read-only administrators can read documents through the admin data route. |
| `admin.allowed_networks` | none (any network) | IP addresses or CIDR networks, IPv4 or IPv6, allowed to use the [admin API](../../auth/admin/). See [Network restrictions](#network-restrictions). |
| `email` | none (no email) | How the realm sends email: the delivery function, the sender, backd's public address for links, languages, the pages the links open, where users go afterwards, and limits. backd never sends mail itself. See [Email](../../functions/email/) |
| `account.require_verified_email` | `false` | `true`: no session until the address is verified. See [Account](#account) |
| `account.allow_email_change` | `false` | `true`: users may [change their own address](../../auth/sessions/#changing-the-email-address) |
| `account.welcome_email` | `false` | `true`: send the `welcome` email once the address is verified |
| `account.purge_unverified_after` | off | Delete accounts that never verified after this long (at least `1m`; never accounts with roles) |
| `account.tokens.verify_email` | `48h` | How long the verification link works |
| `account.tokens.reset_password` | `1h` | How long a [password reset](../../auth/sessions/#password-reset) link works |
| `account.tokens.change_email` | `24h` | How long the confirmation link of an [email change](../../auth/sessions/#changing-the-email-address) works |
| `account.tokens.revert_email_change` | `7d` | How long the link to undo one works |
| `functions.max_concurrency` | none (unlimited) | Caps how many of the realm's [functions](../../functions/calling/#concurrency-limits) may run at once **on this `backd` instance**; with several instances, up to that many times this value run at once. |
| `functions.log_retention` | `7d` | How long [function invocation records](../../functions/logs/) are kept, at least `1h`. |
| `functions.job_retention` | `24h` | How long an [async job](../../functions/jobs/)'s result is kept once done, at least `1h`. |

Durations are a whole number of days (`30d`) or Go durations (`12h`, `90m`), with a minimum of one minute.

`signup`, `sessions`, `password`, `login_throttle`, `roles`, `admin`, `audit`, `email`, `account`, `functions.log_retention` and `functions.job_retention` only apply when `auth` is `enabled`. Setting them in a realm with `auth: disabled` is an error, so a realm can't carry settings that do nothing. `cors` and `functions.max_concurrency` apply either way.

## CORS

Browsers only let a web page call `backd` from another origin if the realm allows that origin. List the origins of your web apps under `cors.origins`:

```yaml
cors:
  origins:
    - https://app.example.com
    - http://localhost:5173
```

- The list applies to every route of the realm: data, `/_auth` and `/_admin`.
- Preflight requests (`OPTIONS`) from a listed origin get `204` with the allowed methods (`GET`, `POST`, `PUT`, `PATCH`, `DELETE`) and request headers (`Authorization`, `Content-Type`, `If-Match`, `Idempotency-Key`, `Prefer`, `X-Request-ID`). The answer may be cached for 10 minutes.
- Every response under `/v1/` carries `Vary: Origin`, so caches keep answers for different origins apart (see [Caching](../../api/#caching)).
- Responses to listed origins, errors included, carry `Access-Control-Allow-Origin` and expose `ETag`, `Idempotent-Replayed`, `Location`, `Preference-Applied`, `Retry-After`, `WWW-Authenticate` and `X-Request-ID`.
- Credentials travel in the `Authorization` header, so responses don't allow credentials, except in a realm with [session cookies](../../auth/sessions/#session-cookies) (`sessions.cookie`): there, responses to the listed origins carry `Access-Control-Allow-Credentials: true`, and a wildcard origin is refused.
- `X-Backd-On-Behalf-Of` isn't allowed from browsers: only server-side services with API keys may use it.
- `*` allows any origin. That's reasonable for public, read-only data; for apps with users, list the origins explicitly.

{{< hint warning >}}
`cors.origins: "*"` in a realm with users lets any website's scripts call your API with a token they can get their hands on. For apps with sign-in, list the exact origins, `scheme://host[:port]`, and nothing else.
{{< /hint >}}

CORS only tells browsers which pages may read responses. It isn't access control: requests from other origins, and from anything that isn't a browser, still go through [authentication and access rules](../../auth/).

## Account

```yaml
account:
  require_verified_email: false
  welcome_email: false
  allow_email_change: false
  purge_unverified_after: 30d
  tokens:
    verify_email: 48h
    reset_password: 1h
    change_email: 24h
    revert_email_change: 7d
```

Each of `require_verified_email`, `welcome_email`, `allow_email_change` and `purge_unverified_after` needs the [`email`](../../functions/email/) section: without one, startup fails naming the key. What they do is described in [Email verification](../../auth/sessions/#email-verification).

## Roles

Each key under `roles` declares a role name. Role names follow the same rules as realm names: lowercase letters and digits, optionally separated by single `-` or `_`.

- `description` is optional and only for readers.
- `admin` makes it an **admin role**: sessions of users holding it can use the [admin API](../../auth/admin/), as admin API keys can. `admin: true` opens every area of it to change; `admin: read` is a **read-only level** for developers (every area to read, users and data only if `admin.read_access` grants them, nothing to change); a list opens only those areas: `admin: [users, invitations]`, and may include `read`: `admin: [read, secrets]` reads everything and changes secrets. The areas are `users`, `invitations`, `apikeys`, `secrets`, `audit`, `functions` and `data`; a user's roles add up. See [Admin rights](../../auth/admin/#admin-rights) for what each opens and the rules that stop an administrator from handing out more than they hold. [`backd bootstrap`](../../auth/cli/#the-first-administrator) gives a realm's first administrator a role with `admin: true`. Several roles can be admin roles. At startup, `backd` warns about a realm that declares no admin role, one with no role that has `admin: true`, or whose admin roles nobody holds (in `realm.yaml` or the database).
- `users` lists the users who get the role: plain email addresses, or objects with an `email` key (`- email: lead@example.com`). These are *seed assignments*: they are applied by `backd provision` and every time `backd serve` starts, and to a listed email as soon as that user signs up or is created. They add the role and never remove it.

{{< hint style="tip" title="Best practice" >}}
Keep the assignments that matter (your administrators, staff roles) in `realm.yaml`, in version control: then the realm can be rebuilt on new infrastructure with the same roles. Assignments made with the CLI or the admin API live only in the database.
{{< /hint >}}

Other assignments can be made later with [`backd user add-role`](../../auth/users/#roles) or the admin API, without editing the file; those live only in the database. At startup, `backd` logs how many users have assignments that `realm.yaml` doesn't list, and warns about users holding roles that `realm.yaml` no longer declares.

[Access rules](../../auth/rules/#roles) check roles with `hasRole(user, 'admin')` or `'admin' in user.roles`. Changes to a user's roles apply from their next request.

Emails are trimmed and lowercased, and each can appear only once per role.

## Network restrictions

Three settings limit where people may act from. Client addresses come from [`TRUSTED_PROXIES`](../../operations/#client-addresses-behind-a-proxy), so every one of these depends on it being exact: behind a proxy without it, every request seems to come from the proxy.

```yaml
admin:
  allowed_networks: [10.20.0.0/16, "2001:db8:20::/48"]
roles:
  ops:
    admin: true
    users:
      - email: ada@example.com
        admin_networks: [10.20.1.0/24]     # her admin API requests
        login_networks: [10.20.0.0/16]     # her login and her session
      - bob@example.com                    # no restrictions of his own
```

| Setting | Limits | Outside it |
|---|---|---|
| `admin.allowed_networks` | Every admin API request, with an admin key or an admin session | `404 not_found`, as if the admin API didn't exist; credentials aren't even checked. Logged as a `WARN` with the client address |
| `admin_networks` (in a role's user entry) | That user's admin API requests. Must lie within `admin.allowed_networks`, or `backd` doesn't start | `404 not_found`, logged |
| `login_networks` (in a role's user entry) | That user's login, and every request made with their session | Login answers `401 invalid_credentials`, as for a wrong password, and counts as a failed attempt; requests with the session answer `401 unauthenticated`. The session isn't revoked: it works again from an allowed network |

- An empty or missing list means no restriction. At startup, `backd` logs (at `info`) each realm whose admin API is reachable from any network.
- A user's networks set in `realm.yaml` are applied like role seeds, at startup and when the user is created, and they win over changes made elsewhere. Networks set for users `realm.yaml` doesn't cover live only in the database; startup logs how many users have them.
- An email listed in several roles must have the same networks everywhere.
- [API keys](../../auth/api-keys/#network-restrictions) can be limited too, when they're created.

## Validation

`realm.yaml` is read strictly. `backd` refuses to start, naming the file, when:

- the realm has no `realm.yaml`;
- the YAML is invalid or has an unknown key (a typo such as `idle_timout` is an error, not ignored);
- a value is out of range or not one of the allowed values;
- a user setting is present while `auth` is `disabled`;
- a role name or email address is invalid, or an email is listed twice for a role;
- a network is invalid, a user's `admin_networks` isn't within `admin.allowed_networks`, or an email has different networks in two roles.
