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
