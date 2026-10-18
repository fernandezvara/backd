---
title: "21. Authors with references"
description: "Show who wrote each asset with one reference declared in collection.yaml, a profiles collection keyed by owner, and expand=author(name,avatar) on the list: the cost headers before and after selecting fields, a profile nobody can read, and writes that keep the asset's own ETag."
weight: 410
toc: true
---

The gallery shows titles, but not **who** saved them. The name lives somewhere else: the account (email, password, roles) is private, and the *directory* of members is business data. The usual fix is one more request per card, or copying the name into every asset and keeping it up to date. A [reference](../../build/configuration/config-dir/#references) is the third way: the asset says where its author is, and a read can bring the author along.

## A profile per person

A profile is what other members see of a person. It belongs to its owner (`_meta.owner`, set by backd) and there is at most one per person, which a **unique index** on `_meta.owner` guarantees:

{{< example-file path="shelf/main/profiles/schema.json" >}}

```json
[
  { "fields": ["_meta.owner"], "unique": true }
]
```

Add them as `config/shelf/main/profiles/schema.json` and `indexes.json`. Its rules are short: every signed-in member reads every profile, and only the owner changes their own.

{{< example-file path="shelf/main/profiles/collection.yaml" >}}

A second `create` by the same person is a `409`: the index says so, not a rule that has to count.

## Declare the reference

An asset's author is the person who owns it, so the source is `_meta.owner` and the target is the profile whose own `_meta.owner` is the same user. Add this at the end of `config/shelf/main/assets/collection.yaml`:

```yaml
references:
  author:
    field: _meta.owner
    collection: profiles
    key: _meta.owner
```

`backd` checks it when it starts: the collection exists, the source is something a reference can read, and `key` has a unique index (a `key` that could match two profiles is refused, with the file and the fix). Nothing is stored for the reference, and nothing changes for requests that don't ask.

## Ask for it

A read says which references it wants, and which fields of them:

```http
GET /v1/shelf/main/assets/<id>?expand=author
```

```json
{
  "id": "…", "title": "The backd handbook", "kind": "link",
  "_meta": { "owner": "…", "version": 1, … },
  "_expanded": {
    "author": { "id": "…", "name": "Ana", "avatar": "🦊", "bio": "…", "_meta": { … } }
  }
}
```

Without parentheses the whole profile comes back, as a direct read would return it. A gallery card needs two fields, and asking for two keeps the response small:

```http
GET /v1/shelf/main/assets?expand=author(name,avatar)&limit=9
```

The app does it in one line, with the builder that checks the form:

```js
import { expand } from 'backd-js'

const page = await assets.list({ where, orderBy, limit: 9, expand: expand(['author', ['name', 'avatar']]) })
page.items[0]._expanded?.author   // { id, name, avatar } or null
```

## What it costs

The answer says what the expansion did:

```text
X-Backd-Expand-Queries: 1     one query on profiles, for the whole page
X-Backd-Expand-Docs: 9        nine authors placed
```

A page of nine cards costs **one** extra query, not nine. Those numbers don't change with field selection: choosing `name,avatar` makes the response smaller, not the lookup cheaper. What makes it expensive is asking for more paths, more levels or a `many` reference with long arrays; nothing is expanded unless asked, and the server refuses past [its limits](../../build/api/documents/#expanding-references).

## A profile nobody can read

`profiles` reads need a sign-in. The public gallery of chapter 1 has no signed-in visitor, so there the author is `null`, and so it is for an author who never made a profile or whose profile was erased. The three look identical on purpose: a reader can't use the field to find out which of them it was. The card just doesn't print a name.

## Writes are the asset's own

`ETag` of an expanded read is still the asset's version, so `If-Match` works with it. Expanded responses are never cached or answered `304`, and a write never expands: sending back what you read (`_expanded` included) changes nothing, because `_expanded` is ignored like `_meta`.

`where` and `order_by` use the asset's own fields: `where={"author.name":"Ana"}` is a `400` that says so. To find the assets of one person, filter on `_meta.owner`.

## You should see

- A second profile for the same member is refused (`409`).
- `expand=author` returns the profile in `_expanded.author`; the asset and its `ETag` are as before, and the response is `private, no-cache`.
- With `author(name)` only `id` and `name` come back; `X-Backd-Expand-Queries` is `1` for one asset and for a whole page.
- An unknown reference is `400 invalid_expand` that names it; `where` on `author.name` is `400 invalid_query`.
- The gallery cards show *by Ana*; signed out they show no author.

Read [Expanding references](../../build/api/documents/#expanding-references) for the limits and [the client](../../build/clients/js/#expanding-references) for typed `_expanded`.
