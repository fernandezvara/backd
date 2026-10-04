---
title: "1. An empty shelf"
description: "The shelf realm, the assets collection, dates stored as dates, and a public gallery with filters and cursor paging."
weight: 210
toc: true
---

A collection, a schema, and a page that lists documents. No users yet: anyone on localhost can read and write, which is exactly what you want while the shape of the data settles.

{{< hint warning >}}
`auth: disabled` means anyone who can reach backd can read and modify its data. Keep this stack on localhost — chapter 2 turns authentication on and [the checklist](../../operations/checklist/) is what you run before anything faces the internet.
{{< /hint >}}

## The realm

A **realm** is one application's world: its users, roles and settings. A **database** inside it groups collections (`main` here), and a **collection** holds documents — the vocabulary the URLs use: `/v1/{realm}/{database}/{collection}`. They are directories under `config/`. Create `config/shelf/realm.yaml`:

```yaml
auth: disabled
```

That is the whole file for now — every key is optional. Chapter 2 turns authentication on and creates the first accounts; the repository's finished `realm.yaml` is the *last* chapter's state, so don't copy it yet.

## The collections

Create the directories (`mkdir -p config/shelf/main/assets config/shelf/main/shares`) — a collection is a directory inside a database: `config/shelf/main/assets/`. `schema.json` says what a document may contain:

{{< example-file path="shelf/main/assets/schema.json" >}}

Two things worth noticing:

- **`x-backd-store: "date"`** on `published_at`: the API still accepts and returns RFC 3339 strings, but MongoDB stores a real date, so filters and sorting compare instants ([Dates](../../configuration/config-dir/#dates)).
- **`file`** is declared but unused — it reserves the field for the file-management chapters.

`shares` holds the public links (chapter 5 creates them):

{{< example-file path="shelf/main/shares/schema.json" >}}

And the indexes — `indexes.json` per collection, `-` means descending:

{{< example-file path="shelf/main/assets/indexes.json" >}}

{{< example-file path="shelf/main/shares/indexes.json" >}}

The unique index on `token` makes a duplicate share slug a `409 conflict` instead of a data bug. (`created_by` in the shares schema is filled by the `share` function of chapter 5; nothing writes it yet.)

{{< tutorial-files "main/assets/schema.json main/assets/indexes.json main/shares/schema.json main/shares/indexes.json" >}}

## Start the stack

Everything is in place under `config/`, so bring the stack up from the starter folder (`compose.yaml`, `nginx.conf`, `app/`, `client/`, `config/` — see [the overview](../)). The first run pulls the images and takes a minute or two:

```sh
docker compose up -d
curl -s http://localhost:8080/readyz
```

`readyz` answers once MongoDB has become a replica set and backd is serving; until then it refuses the connection or answers `503`, so repeat it every few seconds. `docker compose logs -f backd` follows the API. If port 8080 is taken, change the left side of `"127.0.0.1:8080:80"` in `compose.yaml` and use that port in every URL.

backd reads `config/` **at startup**, so every change to a file under `config/` needs `docker compose restart backd` (chapter 5 adds one more command for functions). A typo fails startup and the log names the file and line — `docker compose logs backd` shows it.

## Try the API by hand

```sh
curl -s http://localhost:8080/v1/shelf/main/assets \
  -H 'Content-Type: application/json' \
  -d '{"title":"The backd handbook","kind":"link","url":"https://fernandezvara.github.io/backd/","tags":["docs","onboarding"],"published_at":"2026-09-12T09:00:00Z"}'

curl -s 'http://localhost:8080/v1/shelf/main/assets?order_by=-published_at&limit=5'
curl -s -G http://localhost:8080/v1/shelf/main/assets --data-urlencode 'where={"tags":"docs"}'
```

Repeat the first `curl` with other titles and `"tags":["docs"]` (four or five documents is enough) so the gallery has something to show; the page size in the app is 9, so paging needs more: the second `curl` shows `items`, `has_more` and `next_cursor` — the cursor the app will follow ([Querying](../../api/querying/)).

## The gallery

The app you downloaded is the **finished Shelf app**, written once for all twelve chapters: a gallery, a form, my assets, notifications, an Admin view and an account page. You don't edit it; each chapter gives the *backend* what one more part of it needs, and that part comes alive. Until then the part reports an error (the Admin view says it needs a signed-in admin, notifications find no collection) — expected, not a mistake of yours.

What this chapter's backend already lights up, in `app/app.js`:

```js
const backd = createClient({ url: window.location.origin, realm: 'shelf' })
const assets = backd.db('main').collection('assets')
```

One Alpine component lists assets with `where`/`orderBy`/`after` and creates them from the form. The tag input re-fetches on change (`{"tags": tag}` matches array membership), the sort select switches between `-published_at`, `published_at` and `title`, and the pager is a **Load more** button that passes `next_cursor` as `after` — the cursor keeps the list stable while documents change under it. The source is [in the repository](https://github.com/fernandezvara/backd/tree/main/clients/js/examples/shelf/app.js), and it reads top to bottom if you want to see how each chapter's feature is called.

Open `http://localhost:8080`: while the realm has `auth: disabled`, the gallery is open to everyone and shows what you created with `curl`.

## You should see

- `http://localhost:8080` shows the gallery with the assets you created by curl.
- (The **New asset** form lives in *My assets*, which needs an account — chapter 2 — and saves drafts, which the gallery doesn't list; the *Share* buttons need chapter 5. Notifications and Admin wait for their chapters too.)
- Typing `docs` in the tag filter shows only matching assets; picking **Title A–Z** re-sorts.
- With more than 9 published assets, **Load more** appends the next page.
- A document that breaks the schema (e.g. `{"kind":"video"}`) is refused with `400 validation_error` and field-level errors — try it with curl.

Next: [chapter 2](../02-members/) turns authentication on and the gallery gains owners.
