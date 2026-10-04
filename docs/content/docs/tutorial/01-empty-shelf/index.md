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

## Bring the stack up

From the starter folder (`compose.yaml`, `nginx.conf`, `app/`, `client/`, `config/` — see [the overview](../)):

```sh
docker compose up -d
curl -s http://localhost:8080/readyz
```

`readyz` answers once MongoDB has become a replica set and backd is serving. `docker compose logs -f backd` follows the API.

## The realm

Realms, databases and collections are directories under `config/`. Create `config/shelf/realm.yaml`:

{{< example-file path="shelf/realm.yaml" >}}

`auth: disabled` is the whole file for now — every key is optional.

## The collections

A collection is a directory inside a database: `config/shelf/main/assets/`. `schema.json` says what a document may contain:

{{< example-file path="shelf/main/assets/schema.json" >}}

Two things worth noticing:

- **`x-backd-store: "date"`** on `published_at`: the API still accepts and returns RFC 3339 strings, but MongoDB stores a real date, so filters and sorting compare instants ([Dates](../../configuration/config-dir/#dates)).
- **`file`** is declared but unused — it reserves the field for the file-management chapters.

`shares` holds the public links (chapter 5 creates them):

{{< example-file path="shelf/main/shares/schema.json" >}}

And the indexes — `indexes.json` per collection, `-` means descending:

{{< example-file path="shelf/main/assets/indexes.json" >}}

{{< example-file path="shelf/main/shares/indexes.json" >}}

The unique index on `token` makes a duplicate share slug a `409 conflict` instead of a data bug.

## Apply the config

backd reads `config/` at startup:

```sh
docker compose restart backd
curl -s http://localhost:8080/readyz
```

A typo fails startup and the log names the file and line — `docker compose logs backd` shows it.

## Try the API by hand

```sh
curl -s http://localhost:8080/v1/shelf/main/assets \
  -H 'Content-Type: application/json' \
  -d '{"title":"The backd handbook","kind":"link","url":"https://fernandezvara.github.io/backd/","tags":["docs","onboarding"],"published_at":"2026-09-12T09:00:00Z"}'

curl -s 'http://localhost:8080/v1/shelf/main/assets?order_by=-published_at&limit=5'
curl -s 'http://localhost:8080/v1/shelf/main/assets?where={"tags":"docs"}'
```

Add a few more documents so the gallery has something to page: the second `curl` shows `items`, `has_more` and `next_cursor` — the cursor the app will follow ([Querying](../../api/querying/)).

## Wire the gallery

The snapshot you downloaded is static: every view is placeholder markup. This chapter replaces the gallery and the new-asset form with live bindings. In `app/index.html` the `<head>` gains three lines — the importmap for `backd-js`, the app's module, and Alpine:

```html
<script type="importmap">{ "imports": { "backd-js": "/example/client/index.js" } }</script>
<script type="module" src="app.js"></script>
<script defer src="lib/alpine.min.js"></script>
```

`app/app.js` is the whole chapter's JavaScript: one Alpine component that lists assets with `where`/`orderBy`/`after`, and creates them from the form:

```js
const backd = createClient({ url: window.location.origin, realm: 'shelf' })
const assets = backd.db('main').collection('assets')
```

The gallery card template becomes a `x-for` over `assets`, the tag input binds `tag` and re-fetches on change (`{"tags": tag}` matches array membership), the sort select switches between `-published_at`, `published_at` and `title`, and the pager is a **Load more** button that passes `next_cursor` as `after` — the cursor keeps the list stable while documents change under it. The finished file is what [the repo keeps](https://github.com/fernandezvara/backd/tree/main/clients/js/examples/shelf/app.js); copy it if you get stuck.

## You should see

- `http://localhost:8080` shows the gallery with the assets you created by curl.
- **New asset** adds a card to the list; the date reads today's.
- Typing `docs` in the tag filter shows only matching assets; picking **Title A–Z** re-sorts.
- With more than a page of assets, **Load more** appends the next page.
- A document that breaks the schema (e.g. `{"kind":"video"}`) is refused with `422` and field-level errors — try it with curl.

Next: [chapter 2](../02-members/) turns authentication on and the gallery gains owners.
