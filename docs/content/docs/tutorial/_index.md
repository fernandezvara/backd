---
title: "Tutorial"
description: "Build Shelf, a team asset library, one feature per chapter — every chapter ends in a working app."
icon: "school"
weight: 200
toc: true
---

This tutorial builds **Shelf**, a team asset library: members save links and notes, curators publish them, public links share them, a digest emails the news. Each chapter adds one `backd` feature because the app needs it, and ends in a working state with a checklist of what now works.

You do **not** need a `backd` checkout: the app, the client and the released containers all download from this site. You need only **Docker** and a text editor.

## Words used here

- A **realm** is one application's world — its users, roles, sessions and settings (`shelf`). A **database** groups collections inside it (`main`), and a **collection** holds documents (`assets`). The URLs say it: `/v1/{realm}/{database}/{collection}`.
- A **function** is a small TypeScript program backd runs in a sandbox when something calls it: a person, another function, a clock or the internet.
- A **rule** is an expression in `rules.yaml` that decides whether a read or write is allowed. A **`HOLE` comment** (chapter 3) marks the place where a rule can't say what you mean and a function has to take over.
- An **operator** is the administrator of the realm; a **curator** is a member who may publish; a **member** is everyone else.

## What you need

- **Docker** with Compose v2 (`docker compose version` prints a version) and free ports **8080** (the app and the API); the first start downloads about 1 GB of images.
- A text editor and a terminal. **curl** for the API examples; **Node 20 or newer** only for chapter 11's feed script (optional).
- This tutorial's stack is a *separate* thing from the backd documentation you are reading: the docs may be at `https://localhost:8443` or on GitHub Pages, the tutorial's app is always **`http://localhost:8080`**.

To start over at any point: `docker compose down -v` (deletes the database), then `up -d` again.

## The starter

Create a folder and fetch the stack and the app:

```sh
mkdir shelf && cd shelf
mkdir -p app client config/shelf

# The stack: released backd images + MongoDB + executor + egress + nginx.
curl -fsSLO https://fernandezvara.github.io/backd/tutorial/compose.yaml
curl -fsSLO https://fernandezvara.github.io/backd/tutorial/nginx.conf

# The finished Shelf app: each chapter makes one more part of it work.
for f in index.html app.js style.css; do
  curl -fsSL "https://fernandezvara.github.io/backd/tutorial/app/$f" -o "app/$f"
done
mkdir app/lib
for f in pico.min.css alpine.min.js; do
  curl -fsSL "https://fernandezvara.github.io/backd/tutorial/app/lib/$f" -o "app/lib/$f"
done

# The JavaScript client the app imports (backd-js, as source).
for f in admin auth client data errors functions index storage; do
  curl -fsSL "https://fernandezvara.github.io/backd/tutorial/client/$f.js" -o "client/$f.js"
done
```

Once chapter 1 has written the first config, `docker compose up -d` runs backd at `http://localhost:8080`: nginx serves `app/` at `/`, proxies `/v1` to the API and serves `client/` at `/example/client/`, exactly like the app's importmap expects. `config/` is where you write the realm — chapter by chapter.

{{< hint note >}}
Working on a `backd` checkout instead? The same files live in the repository: `examples/config/shelf/` is the realm's final state, `clients/js/examples/shelf/` the app, and `make example` serves it at `/example/shelf/` next to the other examples.
{{< /hint >}}

## The chapters

| # | Chapter | You add |
|---|---|---|
| 1 | [An empty shelf](01-empty-shelf/) | the `shelf` realm, the `assets` and `shares` collections, dates, indexes, a public gallery with filters and cursor paging |
| 2 | [Members](02-members/) | sign-up, sessions, roles, the account page |
| 3 | [Who may touch what](03-rules/) | `rules.yaml`, ownership, optimistic concurrency |
| 4 | [Invitations](04-invitations/) | inviting teammates by email |
| 5 | [The first function](05-first-function/) | `publish`, `admin: true`, idempotency, share links |
| 6 | [Reaching the outside](06-network/) | `preview` and the `network:` allowlist |
| 7 | [Functions calling functions](07-calls/) | `notify` as an internal function, `ctx.call` |
| 8 | [Work that takes time](08-async/) | `digest` as an async job, `retry:` |
| 9 | [On a schedule](09-schedules/) | cron schedules, `cleanup` |
| 10 | [Email for real](10-email/) | delivery function, templates, the Mailbox |
| 11 | [Being called by the internet](11-webhooks/) | the `import` webhook |
| 12 | [Running it](12-running/) | API keys, audit feed, the hardening checklist |

Later chapters (not written yet) add file management: uploads, thumbnails, share links to files — the schema already reserves `assets.file` for them.

## The three accounts

From chapter 2 on you use three accounts, all created in chapter 2 while sign-up is open: `operator@shelf.example` (the administrator), `curator@shelf.example` (publishes) and one of your own (a member). Chapter 4 closes sign-up, so create them **before** it — the lock-out section there is the way back if you didn't.

## How a chapter reads

Each chapter is a diff on the state the previous one left: the files to create or change (quoted from the same files the repository runs and tests; a **fetch** block lists them as a download loop when you'd rather not type), the commands to run, and a **"you should see"** list at the end — if every line holds, the chapter worked. When a command surprises you, the troubleshooting notes point at the log line that explains it.
