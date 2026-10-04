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
- A text editor and a terminal with a **POSIX shell** (macOS, Linux, or WSL on Windows — the examples use `curl`, `sed` and `$VARIABLES`). **Node 20 or newer** only for chapter 11's feed script (optional).
- This tutorial's stack is a *separate* thing from the backd documentation you are reading: the docs may be at `https://localhost:8443` or on GitHub Pages, the tutorial's app is always **`http://localhost:8080`**.

To start over at any point: `docker compose down -v` (deletes the database), then `up -d` again.

## What runs, and what you edit

`docker compose up -d` starts five containers. You only ever edit files on your side of them:

| Container | What it does | You touch |
|---|---|---|
| `backd` | the API: documents, sign-in, rules, functions' entry point, jobs and schedules (`serve --with-worker`) | `config/` (mounted read-only) |
| `mongo` | MongoDB, a one-node replica set | nothing — its data lives in a volume (`down -v` deletes it) |
| `executor` | runs each function call in its own Deno process | nothing |
| `egress` | the only way out for functions that declare `network:` (chapter 6) | nothing |
| `nginx` | serves the app at `/` and the client at `/example/client/`, proxies `/v1` to backd — one origin for both | `app/` if you change the app |
| `functions-build` | a one-shot job that bundles the functions' TypeScript before backd starts | run again after editing a function |

## Conventions

- `<angle brackets>` in a command are values you substitute. `$TOKEN` is a session token from `POST /_auth/login` (chapter 3 shows how); `$OP` is the operator's (chapter 4).
- A `config/` path is relative to your starter folder: `config/shelf/main/assets/schema.json` is the `assets` collection of the `main` database in the `shelf` realm.
- Rules, `realm.yaml` and schemas are read **at startup**: `docker compose restart backd` after changing them. Function code (and its `function.yaml`) is bundled by `functions-build`: run it, then restart.

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
| 4 | [Invitations](04-invitations/) | closing sign-up; one-time invitation links |
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

Each chapter is a diff on the state the previous one left: the files to create or change (quoted from the same files the repository runs and tests; a **fetch** block lists them as a download loop when you'd rather not type), the commands to run, and a **"you should see"** list at the end — if every line holds, the chapter worked. 
## When something is off

- `curl -s http://localhost:8080/readyz` answers once MongoDB is a replica set and backd serves; for a minute after `up` it may not. `docker compose ps` shows which container is down.
- `docker compose logs backd` is the first place to look. A bad config file fails startup and the log names the file and line; every request is logged with its `request_id`, which also appears in every error body.
- An error body is `{"error": {"code", "message", "details"}}`, and the status says what kind: **401** not signed in or the session ended (log in again; sessions last 30 days); **403** signed in but a rule or an `invoke` expression refuses it; **404** the thing doesn't exist *or you may not read it*; **400** invalid input, with the failing fields in `details`; **409** a unique index or an already-used value; **412** the document changed since you read it; **503** a function call while the executor is still starting.
- A function change that does nothing: run `docker compose run --rm functions-build` (it prints one `bundled …` line per function; a TypeScript error stops here) and `docker compose restart backd`; `docker compose logs backd` shows the function's `console.log` lines and, when a call fails, its error.
- Start over: `docker compose down -v`, then `up -d` — and sign up the three accounts again (chapter 2).
