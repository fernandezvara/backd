---
title: "Tutorial"
description: "Build Shelf, a team asset library, one feature per chapter — every chapter ends in a working app."
icon: "school"
weight: 200
toc: true
---

This tutorial builds **Shelf**, a team asset library: members save links and notes, curators publish them, public links share them, a digest emails the news. Each chapter adds one `backd` feature because the app needs it, and ends in a working state with a checklist of what now works.

You do **not** need a `backd` checkout: the app, the client and the released containers all download from this site. You need only **Docker** and a text editor.

## The starter

Create a folder and fetch the stack and the app:

```sh
mkdir shelf && cd shelf
mkdir -p app client config

# The stack: released backd images + MongoDB + executor + egress + nginx.
curl -fsSLO https://fernandezvara.github.io/backd/tutorial/compose.yaml
curl -fsSLO https://fernandezvara.github.io/backd/tutorial/nginx.conf

# The app snapshot (step 0: static HTML you will make live, view by view).
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

`docker compose up -d` then runs backd at `http://localhost:8080`: nginx serves `app/` at `/`, proxies `/v1` to the API and serves `client/` at `/example/client/`, exactly like the app's importmap expects. `config/` is where you write the realm — chapter by chapter.

{{< hint info >}}
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
| 6 | Reaching the outside | `preview` and the `network:` allowlist |
| 7 | Functions calling functions | `notify` as an internal function, `ctx.call` |
| 8 | Work that takes time | `digest` as an async job, `retry:` |
| 9 | On a schedule | cron schedules, `cleanup` |
| 10 | Email for real | delivery function, templates, the Mailbox |
| 11 | Being called by the internet | the `import` webhook |
| 12 | Running it | API keys, audit feed, the hardening checklist |

Later chapters (not written yet) add file management: uploads, thumbnails, share links to files — the schema already reserves `assets.file` for them.

## How a chapter reads

Each chapter is a diff on the state the previous one left: the files to create or change (quoted from the same files the repository runs and tests), the commands to run, and a **"you should see"** list at the end — if every line holds, the chapter worked. When a command surprises you, the troubleshooting notes point at the log line that explains it.
