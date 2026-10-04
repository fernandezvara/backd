---
title: "API keys"
description: "Give server-side services access to a realm's data with API keys."
icon: "vpn_key"
weight: 530
toc: true
---

API keys let server-side services (a backend, a worker, a script) call a realm's data routes. A key belongs to one realm and gives **full access to all of that realm's data**, in every database. 
{{< hint danger >}}
A key is a password with no user behind it: it bypasses every [access rule](../rules/). **Never ship one to a browser or a mobile app**, where every visitor can read it. Use it only from servers you control, keep it in a secret store, and give it an expiry.
{{< /hint >}}

## Roles

Each key has a role, set when it's created:

| Role | Can |
|---|---|
| `data` (default) | Data routes with full access, whatever the rules say; [act on behalf of a user](#acting-on-behalf-of-a-user) |
| `admin` | Everything `data` can, plus the [admin API](../admin/): users, roles and invitations |

A `data` key on the admin API gets `403 forbidden`.

{{< hint style="tip" title="Best practice" >}}
Give services `data` keys, and keep `admin` keys for management tooling: a key leaked from an ordinary service then can't manage users or roles.
{{< /hint >}}

## Managing keys

Keys are managed by the realm's administrators, with `backd apikey` (after [`backd login`](../cli/#logging-in), or with an admin key in `BACKD_API_KEY`) or over HTTP with the [admin API](../admin/#api-keys):

| Command | Effect |
|---|---|
| `backd apikey create --realm <realm> --name <name>` | Create a `data` key that never expires, and print it (with a warning: prefer `--expires`) |
| `backd apikey create --realm <realm> --name <name> --role admin` | Create an `admin` key, for the [admin API](../admin/) |
| `backd apikey create --realm <realm> --name <name> --networks 10.0.0.0/8,192.0.2.7` | Create a key that works only from those networks |
| `backd apikey create --realm <realm> --name <name> --scopes read:blog/posts,call:main/contact` | Create a key that reaches only what the [scopes](#scopes) grant |
| `backd apikey create --realm <realm> --name <name> --expires 90d` | Create a key that stops working after 90 days (days or Go durations such as `12h`) |
| `backd apikey list --realm <realm>` | List keys: name, role, first characters, scopes, networks, creation, last use and expiry |
| `backd apikey revoke --realm <realm> --name <name>` | Delete the key; it stops working at once |

```sh
backd login --realm blog --url https://api.example.com
backd apikey create --realm blog --name publisher --expires 90d
# bdk_7VeQ1hN0m2k…            ← standard output: the key
# API key "publisher" (role data) created. Store it now as a secret: it can't be shown again. It expires at …

backd apikey list --realm blog
# NAME       ROLE  KEY          SCOPES  NETWORKS  CREATED               LAST USED  EXPIRES
# publisher  data  bdk_7VeQ1h…  all     -         2026-09-26T15:22:52Z  -          2026-12-25T15:22:52Z
```

- The key is printed **once**, on standard output, so scripts can capture it.
- Names follow the same rules as realm names and are unique within the realm.
- Keys start with `bdk_` (session tokens start with `bds_`), which helps secret scanners find leaked keys.
- The last use is recorded at most once a minute.
- To rotate a key without downtime, create a new one, deploy it, then revoke the old one.

{{< hint note >}}
The key is shown **once**, when it is created. `backd` keeps only a SHA-256 hash and can't show it again: if you lose it, revoke it and create a new one.
{{< /hint >}}

## Expiry and rotation policy

A leaked key gives full access to the realm until it's revoked or expires, so give every key an expiry and rotate it on a schedule:

1. **Create keys with `--expires`.** Use 90 days or less for production services, and hours (`--expires 12h`) for one-off scripts and migrations. `backd apikey create` warns when a key has no expiry.
2. **Name keys by service and period**, such as `billing-2026q4`, so the old and new keys can live side by side during a rotation.
3. **Rotate before expiry:** create the replacement, deploy it, check with `backd apikey list` that the old key's `LAST USED` has stopped advancing, then revoke it.
4. **Revoke at once** when a key may have leaked, or when the service using it is retired or its operator leaves.
5. **Keep one key per service**, never a shared one, so revoking one doesn't break the others.

At startup, `backd serve` helps enforce this. For each realm it logs:

- a `WARN` line listing keys that never expire;
- a `WARN` line listing keys that expire within 14 days;
- an `INFO` line listing expired keys that are still stored (they are already refused; revoke them to tidy up).

The lines include key names only, never keys or hashes. `backd` checks only at startup, so for long-running services, schedule `backd apikey list` (for example weekly) and alert on the `EXPIRES` column.

## Scopes

A key without scopes reaches everything its role allows. **Scopes narrow a `data` key to what it was made for**, so a leaked key exposes that and nothing else. They are set when the key is created, with `--scopes` (comma-separated) or `scopes` in the [admin API](../admin/#api-keys), and `apikey list` shows them (`all` when there are none).

A grant is `read`, `write` or `call`, optionally followed by a target:

| Grant | Gives |
|---|---|
| `read` | reading (list and get) every collection |
| `read:blog` | reading the collections of the database `blog` |
| `write:blog/posts` | creating, updating and deleting in the collection `blog/posts` (including [batches](../../api/documents/#batch-writes)); `write` doesn't include reading |
| `call` | calling every [function](../../functions/calling/) |
| `call:main` | calling the functions of the database `main` |
| `call:main/export` | calling that function, and reading the [jobs](../../functions/jobs/) of that function |

```sh
# A website's key: reads the published posts, receives the contact form, nothing else.
backd apikey create --realm blog --name website --expires 90d \
  --scopes read:main/posts,call:main/contact_form
```

- **Anything not granted answers `403 forbidden`**, naming the target. On the data routes, in batches, on function calls and on job reads, whatever the access rules say (keys bypass the rules, scopes don't).
- **Functions are separate on purpose.** A function can do more than the key that calls it (it has its own admin access), so a scoped key calls a function only with a `call` grant. While the function runs, the data calls it makes *as the caller* stay within the key's scopes too.
- **Acting on behalf of a user** doesn't widen a key: both the user's rules and the key's scopes apply.
- Scopes are for `data` keys. An `admin` key reaches the admin API, which scopes don't cover, so creating one with scopes is refused; finer administrator rights are a separate feature.
- A grant must name something the realm has: a typo (`read:blogs`) is refused when the key is created, not discovered when it fails.
- Keys made before scopes existed have none, and keep working as before.

## Network restrictions

A key created with `--networks` works only from those IP addresses or CIDR networks, IPv4 or IPv6. From anywhere else it answers `401 unauthenticated`, as an invalid key would, and the attempt is logged as a `WARN` with the key's name and the client address. Client addresses come from [`TRUSTED_PROXIES`](../../operations/#client-addresses-behind-a-proxy), so set it exactly. Use this for services with fixed addresses, so a leaked key is useless elsewhere.

## Using a key

Send the key as a Bearer credential:

```sh
curl https://localhost:8443/v1/blog/main/posts -H "Authorization: Bearer $BACKD_API_KEY"
```

The access log records the request's `actor` as `key:<name>`, never the key itself.

## Acting on behalf of a user

A service can make a request as one of the realm's users by adding their id:

```sh
curl https://localhost:8443/v1/blog/main/posts \
  -H "Authorization: Bearer $BACKD_API_KEY" \
  -H "X-Backd-On-Behalf-Of: dars2ql90v600434mf0g"
```

- The user's [access rules](../rules/) apply, exactly as if they had signed in. The key's full access doesn't.
- Documents it creates are owned by the user: `_meta.owner` is their id, and `_meta.created_by` / `updated_by` are `user:<id>`.
- The access log records the actor as `key:<name> as user:<id>`, so the service stays visible.
- Only API keys may send the header. With a session or without credentials it answers `400 invalid_header`, as does an id that isn't a user of the realm. A disabled user answers `403 forbidden`.

Use it when a backend handles a request for a user and should be bound by the same rules, for example a server-rendered page or a webhook acting for an account.

## Who can access data

In a realm with `auth: enabled`, data routes (`/v1/{realm}/{database}/{collection}…`) decide by caller:

| Caller | Result |
|---|---|
| Valid API key of the realm | Full access, whatever the rules say |
| API key with `X-Backd-On-Behalf-Of` | What the access rules allow that user |
| Signed-in user (session token) | What the collection's [access rules](../rules/) allow |
| No credentials | What the access rules allow anonymous callers (`user == nil`) |
| Unknown, expired or revoked key or token, or one from another realm | `401 unauthenticated` |

A collection without `rules.yaml` is only reachable with API keys.

Realms with `auth: disabled` ignore credentials entirely: everyone has full access.
