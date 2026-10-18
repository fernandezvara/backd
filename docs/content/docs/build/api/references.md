---
title: "References and expansion"
description: "Declare references between collections, return the referenced documents with expand, and understand what it costs: syntax, response shape, null semantics, limits, startup errors, caching and the alternatives to joins."
icon: "link"
weight: 360
toc: true
---

Almost every app has relations: a post's author, an invoice's customer, an item's tags. Without help, each relation is another request and a join on the client. **Reference expansion** returns the referenced documents next to the result, in the same request, with one batched query per path for the whole page, and with every expanded document passing **its own collection's rules**.

Four principles hold everywhere:

- **Declared and explicit.** Only references declared in `collection.yaml` can be expanded, and only when the request asks. Nothing is expanded by default, and the [JavaScript client](../../clients/js/#expanding-references) never adds it by itself.
- **Additive.** The document is exactly what `schema.json` says; expansions go in `_expanded`.
- **Rules everywhere.** A referenced document is returned only if the caller could read it directly. Unreadable and missing look the same.
- **Bounded, visible cost.** Limits on depth, paths, arrays and totals, and headers that say what a request cost.

## Declaring a reference

```yaml
# collection.yaml of "posts"
references:
  category:                      # the field "category" holds the id of a categories document
    collection: categories
  tags:
    collection: tags
    many: true                   # the field is an array of ids
    max_expand: 20
  author:                        # the owner's profile
    field: _meta.owner
    collection: profiles
    key: _meta.owner             # profiles has a unique index on _meta.owner
  customer:
    collection: customers
    database: crm                # another database of the same realm
```

| Key | Default | Meaning |
|---|---|---|
| `collection` | required | the collection the values point to |
| `field` | the reference's name | where the value is in this collection: a declared string field (an array of strings with `many`), a dotted path of one, `_meta.owner` or `_meta.created_by` |
| `database` | this database | the database of the target; always in the same realm |
| `key` | `id` | the field of the target that equals the value |
| `many` | `false` | the source is an array of values |
| `max_expand` | the instance's value | follow at most this many ids of the array; only with `many`, and only lower than `BACKD_EXPAND_MAX_ARRAY` |

**How a value finds its document.** The value of `field` is compared with `key` in the target: first `id` (the default), otherwise the field named by `key`, which can be a declared field or `_meta.owner` / `_meta.created_by`. Each value must match at most one document, so a `key` other than `id` needs a **unique index** of its own in the target's `indexes.json` (one field; soft-delete's extra key doesn't matter).

**Never across realms.** A reference reaches another database of its own realm, never another realm.

**What isn't checked.** Values aren't checked when documents are written: there is no referential integrity. A reference to a deleted or never-created document expands to `null`.

### Startup errors

`backd` checks every reference when it starts (and `backd config check`, a dev reload and the admin config check do too). Each error names the file, the reference and what to fix:

| The message says | Fix |
|---|---|
| `` `collection` is required `` | name the target collection |
| `the name may use letters, digits and _…` | rename it: it appears in `expand=` paths, where `.`, `(`, `)` and `,` are syntax |
| `database "x" is not a database of realm "r"` | the target database must exist in the same realm; it lists the ones that do |
| `collection "x" does not exist in database "d"` | fix the name; it lists the collections |
| `field "x" is not declared in schema.json` | declare the source as a string (an array of strings with `many`), or point `field` at one that is |
| `field "x" must be declared as a string` / `…an array whose items are strings` | the source's type and `many` disagree |
| `field "x" is inside an array` | a reference reads one value, or one array of values, of its own |
| `field _meta.owner holds one id: remove many: true` | `_meta.owner` and `_meta.created_by` are single values |
| `field _meta.… can't be a source` | only `_meta.owner` and `_meta.created_by` are |
| `key "k" of db/coll needs a unique index of its own` | add `{"fields": ["k"], "unique": true}` to the target's `indexes.json` |
| `the name is also a field of the schema` | a reference may share its name only with its own `field` |
| `max_expand only applies with many: true` / `must be at least 1` | fix the value |
| `max_expand: N is above this instance's limit` | lower it, or raise `BACKD_EXPAND_MAX_ARRAY` |

## Requesting expansion

```http
GET /v1/{realm}/{db}/posts?expand=author(name,avatar),tags(name),category
GET /v1/{realm}/{db}/posts/{id}?expand=customer,customer.account_manager(name)
```

- **Entries** are separated by commas. An entry is a reference name, or `name.nested` for the second level, where `nested` is a reference declared on the **target** collection.
- **Field selection** `(f1,f2)` lists the fields to return; dotted paths (`address.city`) work. The `id` is always included and `_meta` only when selected. Without parentheses the whole document comes back, as a direct read would return it.
- **The way to a second level** (`customer.account_manager(name)`) returns the first level whole unless it is also listed with its own selection (`customer(name),customer.account_manager(name)`).
- **Validation.** An unknown reference or field, a third level, too many entries, an entry listed twice or a malformed one is `400 invalid_expand`, and the message names the part.

### The response

```json
{
  "id": "p_1",
  "title": "Hello",
  "tags": ["t_1", "t_2", "t_3"],
  "_meta": { "owner": "u_9", "version": 4 },
  "_expanded": {
    "author": { "id": "pr_7", "name": "Ana", "avatar": "ana.png" },
    "tags": [ { "id": "t_1", "name": "go" }, null, { "id": "t_3", "name": "db" } ]
  }
}
```

- `_expanded` has one entry per requested reference, by name; a second level is inside the expanded document's own `_expanded`.
- **`null` is the same answer for three different things:** the reference points at nothing, at a document the caller's rules don't let them read, or at one in an organization they don't count in. There is no way to tell them apart, on purpose: expansion must not be a way to probe for documents.
- **In a `many` reference** the positions are kept, and each unavailable value is `null`.
- **Long arrays** expand only their first values up to the limit, and the entry becomes `{ "items": [...], "truncated": true }`, so a client can tell.
- **An empty source** gives `null` (or `[]` for `many`).
- **Server-owned.** `_expanded` is ignored on writes, like `_meta`, so sending back an expanded read changes nothing.

## What it costs

For each path, at each level, backd collects the source values of the whole page (without duplicates), runs **one** query on the target with the target's read rule pushed down, and puts the results back by key.

| You ask | Queries |
|---|---|
| a page of 50 posts with `author` | the page + 1 |
| …with `author,tags` | the page + 2 |
| …with `author.company` | the page + 2 (one per level) |

- **The number of documents** multiplies with page size and with `many`: 100 posts with 20 tags each is up to 2,000 expanded documents. Past [the limits](#limits) the request is refused before any partial data.
- **Field selection** makes the response smaller, not the lookup cheaper: the rule is evaluated on the whole document and the selected fields are chosen afterwards.
- **The cost headers** `X-Backd-Expand-Queries` and `X-Backd-Expand-Docs` come with every expanded read, and the access log records the paths, the counts and the time spent expanding (`expand`, `expand_queries`, `expand_docs`, `expand_ms`): use them to find the request that costs too much.
- **Staying cheap:** ask for what the screen shows; select fields; prefer one level; use smaller pages with `many` references; cache rarely changing references (categories, a profile) by id on the client and expand only what's missing.
- **Why nothing is expanded by default:** every path costs a query and some response size for every caller of that route. The caller that needs the author asks for it; the rest never pay.

### Limits

| Limit | Default | Setting |
|---|---|---|
| levels | 2 | `BACKD_EXPAND_MAX_DEPTH` (1 or 2) |
| entries per request | 5 | `BACKD_EXPAND_MAX_PATHS` |
| ids of one `many` reference | 100 | `BACKD_EXPAND_MAX_ARRAY`; lower per reference with `max_expand` |
| expanded documents in a response | 2,000 | `BACKD_EXPAND_MAX_DOCS` |

The values are logged at startup. A collection can only lower them. Past the last one the answer is `400 expand_too_large`, before any partial data, and it says to ask for fewer entries or a smaller page.

## Recipe: the owner's profile

The account (email, password, roles) is private; what other members see of a person belongs in a **profile**. To show an author on every post:

1. **A `profiles` collection**, with one document per user, owned by that user (it is created by them, so `_meta.owner` is theirs). Its read rule says who sees profiles:

   ```yaml
   # profiles/collection.yaml
   rules:
     read: user != nil
     create: user != nil
     update: user != nil && document._meta.owner == user.id
   ```

2. **A unique index** on `_meta.owner`, so a person has at most one and the reference matches at most one document:

   ```json
   [ { "fields": ["_meta.owner"], "unique": true } ]
   ```

3. **The reference**, on the posts:

   ```yaml
   references:
     author: { field: _meta.owner, collection: profiles, key: _meta.owner }
   ```

4. **The request**: `GET …/posts?expand=author(name,avatar)`.
5. **The result**: each post has `_expanded.author` with `id`, `name` and `avatar`, or `null` for a visitor who can't read profiles, or an author who never made one.

The [Shelf tutorial](../../../tutorial/21-authors/) builds exactly this.

## Caching and writes

- **`ETag` stays the document's version**, so `If-Match` from an expanded read keeps working for writes.
- **Expanded responses are `private, no-cache`**, never `304` and never cached publicly, even for anonymous readers: what they hold depends on the caller and on other documents. [Conditional GET](../caching/) applies only to reads without `expand`.
- **Writes never expand** their responses; `expand` on one is ignored.
- **`_batch`** only writes, so it has nothing to expand. Functions do: [`ctx.db` expands as the caller and `ctx.admin.db` with admin access](../../functions/writing/#expanding-references).

## No joins in queries

`where` and `order_by` use the collection's own fields. A path through a reference (`where={"author.name": "Ana"}`) is `400 invalid_query`: expansion decorates results, it is not a join engine. Two ways to answer "posts by authors named Ana":

- **Look up, then filter by id.** List the profiles with that name, then list the posts with `where={"_meta.owner": {"$in": [ids…]}}`. Two requests, both cheap and cacheable.
- **Copy the field.** Keep `author_name` on the post, written by a [function](../../functions/) when the profile changes. Reads and filters become free; every change of a profile now costs updates to its posts, and the copy can lag.

## Rules and security

- **The target's `read` rule applies** to every expanded document, with the main request's caller: an administrator's API key sees what keys see, a user what their rules allow, and **an API key's scopes narrow it** too (a key that can't read `profiles` gets `null` authors).
- **Rules see the whole document** before the selected fields are chosen, so a selection can't reveal what a direct read wouldn't.
- **Organizations.** A target in organizations applies its `inOrg` rule as for any read: a document of an organization the caller isn't in is `null`.
- **Only declared relations**, checked at startup; clients can't join arbitrary collections.
