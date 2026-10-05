---
title: "Admin UI"
description: "The web interface for operating a realm: switching it on, who sees what, and where to serve it."
icon: "dashboard"
weight: 552
toc: true
---

The admin UI is a web interface for realm administrators and developers. It is a client of the [Admin API](../admin/) and nothing more: it can do nothing the CLI can't, and every call it makes is authenticated, network-checked and audited by the server like any other. This page is the start of its guide; each area of the interface (users, keys, secrets, audit, functions, data, configuration) is added to it as the area ships.

The interface is bundled in the `backd` binary and the release images. Nothing is loaded from anywhere else: no CDN, font, icon or script from another origin.

## Switching it on

It is off by default. Set `BACKD_ADMIN_UI=true` on the instance that should serve it:

```sh
docker run -e BACKD_ADMIN_UI=true … ghcr.io/fernandezvara/backd:vX.Y.Z serve
```

Open `https://<your backd>/_ui/`, enter the realm's name and sign in with an administrator's email and password. The address carries the realm, `/_ui/r/<realm>/…`, so a link opens the same realm for whoever follows it; the realm is always shown at the top of the page, because an administrator works in one realm at a time.

The UI needs the admin API on the same instance: `BACKD_ADMIN_UI=true` with `BACKD_ADMIN_API=false` is a startup error. A binary built from source without the UI (`go build` without running `make ui` first) starts, logs a warning, and answers `404` at `/_ui/`.

| Variable | Default | Purpose |
|---|---|---|
| `BACKD_ADMIN_UI` | `false` | Serves the interface at `/_ui/` |
| `BACKD_ADMIN_UI_IDLE` | `30m` | Signs the administrator out, and revokes the session on the server, after this long without activity |

## Who sees what

Signing in needs a user with one of the realm's [admin roles](../admin/#admin-rights); a user without one is refused, and their session is ended at once. After sign-in the UI asks the server what the account may do (`GET /_admin/whoami`) and shows only that: a read-only administrator sees no buttons that change anything, and `admin.read_access` decides whether they see users and data. The server enforces every permission anyway; the interface only avoids offering what would be refused.

The overview page lists the level (full, read-only or custom) and the areas the account may read and change.

## Users and invitations

The **Users** page lists the realm's users, a page at a time, and searches by email as you type. Opening a user shows their status, whether the address is verified, the roles they hold, their networks and their active sessions (never a token). With the `users` area an administrator can also:

- **create** a user, with or without a password (without one they can't sign in until a password is set);
- **disable** or **enable** them (disabling ends their sessions), mark the address **verified**, **set a password** (which ends their sessions) and **change the email** (offered only when the realm sends email, because both addresses are told);
- **add and remove roles**, from the roles `realm.yaml` declares. An assignment that `realm.yaml` doesn't list for that user is marked *only in the database*: a rebuilt realm wouldn't have it. Reading which roles are declared and seeded needs the `config` area; without it the role is typed by name and the server checks it;
- **revoke a session**: its token stops working at once;
- **delete** the user: the dialog first shows what they own in every collection with a [policy](../erasure/), and asks for the user's email to be typed before it erases. Erasing is irreversible; disabling keeps the data.

**Invitations** lists the pending ones (who they are for, who created them, when they expire). Creating one shows its token once, to copy; *Email an invitation* has backd send it (offered only when the realm sends email); *Revoke* asks for the word to be typed.

A read-only level sees the Users page only when the realm's `admin.read_access.users` grants it, and then without any control that changes something. The same holds for every area: a control appears only when `whoami` says the level may use it.

## API keys, secrets and audit

**API keys** lists every key with its role, prefix, scopes, networks, expiry and last use, never the key. *Create API key* takes a name, a role (`data` or `admin`), an optional expiry (`90d`, `12h`), networks and scopes; the key is shown **once**, in a dialog, and is gone from the page when you close it. *Revoke* asks for the key's name to be typed. An admin key can use the whole admin API, so create one only for a server you trust, and remember the [escalation rules](../admin/#admin-rights): you can't hand out more than you hold.

**Secrets** lists the names, scopes (the realm or one database), when each was last set and by whom. Values are **write-only**, as in the API: the form takes a value, sends it and forgets it, and nothing in the interface can show it again. Setting a name again replaces the value; deleting asks for the name to be typed, and a function that declares the secret answers `secret_missing` until it is set again. Functions only see secrets when the instance has `BACKD_SECRETS_KEY`.

**Audit** shows the [audit trail](../audit/) newest first, fifty records a page, filtered by action, actor, target and a time range. Each record's details, request id and client address open under *Details*, as plain text; an actor or a target that is a user links to the user's page when the level may read users.

A read-only level reads all three without the buttons that create, revoke, set or delete; a custom level that lacks an area doesn't get its page at all.

## Functions

The **Functions** area has three pages.

- **Definitions** lists, per database, every function with its mode, schedule, flags (internal, admin only, email delivery, dev only), limits (timeout, memory, maximum output, concurrency), `calls`, `secrets` and `network`, retry and rate limit, `invoke` rule, and the `function.yaml` it comes from. It reads the realm's configuration, so a level without the `config` area sees a note instead.
- **History** is the [invocation history](../../functions/logs/): when, which function, mode, status and code, duration, origin and actor, with the request id, job and parent invocation and the function's log lines under *Details*. Logs hold whatever functions print, so the page says so and shows them as plain text only.
- **Jobs** lists async and scheduled [jobs](../../functions/jobs/) with their state, origin, attempts and result, filtered by function, status, origin, scheduled runs and time. Inputs and outputs of jobs are never listed.

An administrator with the `functions` area can **run a function by hand**, internal ones included: choose it, give an input as JSON and optionally an email to run as (without one there is no user). A sync function's output is shown on the page; an async one answers with its job, which the Jobs page follows. The function's `invoke` rule and rate limit don't apply, and every run is [audited](../audit/). Read-only levels read all three pages and get no way to run anything. Cancelling and re-running a job will join once the platform has them.

## Configuration

The **Configuration** area shows what this instance runs for the realm, read from the files it loaded through `GET /_admin/config`. It is read-only by design: configuration is a reviewed, versioned artifact, so the interface never edits it. Change the files, review and deploy them; *Reload* fetches what the instance runs now.

- **Overview:** the realm, its `realm.yaml` file, the **config fingerprint** (it changes whenever any configuration file changes, so comparing it across instances shows whether they run the same files) and the **startup warnings** (an admin API open to any network, open sign-up, no administrators and the like).
- **Settings:** every realm setting with its default applied, one row per setting with its dotted path, and the roles with their description, admin access and seeded users. Secrets appear as names only, never values, and the seeded emails show only to a level that may read users.
- **Collections:** for each database and collection, the JSON Schema (in a scrollable block, however large), the indexes, each operation's access rule and which rule file it came from, and the collection policy (soft delete, what happens when an owner is erased). Every item names its source file, relative to the configuration directory.
- **Templates:** the email templates and hosted pages found on disk, by kind and language.

The functions' definitions are on the Functions page. Reading the configuration needs the `config` area, which every read-only level has.

## Sessions

- **The token lives in memory.** Reloading the page signs out. *Keep me signed in in this tab* stores the session in the tab's `sessionStorage` until the tab closes; the UI never uses `localStorage` for it. Leave it off on shared computers.
- **Idle timeout.** After `BACKD_ADMIN_UI_IDLE` without a click, key press or scroll the UI signs out and revokes the session on the server, so a token someone copied stops working too.
- **Sign out revokes.** *Sign out* ends the session on the server, not only in the page.
- **Administrator sessions only.** API keys are never entered in a browser.

## Security

The page is served with a strict `Content-Security-Policy`: scripts and styles only from `/_ui/` itself, no `unsafe-inline`, no `unsafe-eval`, requests only to the same origin (`connect-src 'self'`) and `frame-ancestors 'none'`, plus `X-Content-Type-Options`, `Referrer-Policy` and the other usual headers. Hashed assets are cached for a year; `index.html` is never cached, so a new release reaches users at once. Everything the UI shows from your data (documents, log lines, file names, user agents) is rendered as text, never as markup.

## Public and internal instances

What isn't served can't be attacked. Serve the UI from the instance that has the admin API, and keep that instance off the public internet or behind the network restrictions of [Production deployment](../../operations/production/#admin-api):

- the **public** `backd`: `BACKD_ADMIN_API=false`, no `BACKD_ADMIN_UI`: it serves neither `/_admin` nor `/_ui/`;
- the **internal** `backd-admin`: `BACKD_ADMIN_API=true` and `BACKD_ADMIN_UI=true`, reachable only from the networks that administer the realm.

The UI talks to the origin it was loaded from, so the reverse proxy in front of the internal instance sends both `/_ui/` and `/v1/` to it.

## Static hosting

Every release also attaches the same build as `admin-ui_vX.Y.Z.tar.gz`, for serving from a web server of your own instead of from `backd`. Serve it under `/_ui/` of an origin that also proxies `/v1/` to the instance with the admin API, and add the security headers above yourself: the interface calls only its own origin.

## Trying it locally

`make example` serves the interface at `https://localhost:8443/_ui/`. The `adminui` realm has one user for each level (`admin@adminui.example`, `viewer@adminui.example` and `support@adminui.example`); they gain their roles when they sign up, with the development password `dev-p4ssw0rd!`.
