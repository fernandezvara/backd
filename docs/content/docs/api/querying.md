---
title: "Querying"
description: "Filter with where, sort with order_by, paginate and count."
icon: "search"
weight: 320
toc: true
---

The list endpoint, `GET /v1/{realm}/{database}/{collection}`, accepts these query parameters:

| Parameter | Default | Meaning |
|---|---|---|
| `where` | — | JSON object with filter conditions |
| `order_by` | `id` | Comma-separated fields; prefix a field with `-` for descending |
| `limit` | `20` | Page size, `1`–`100` |
| `skip` | `0` | Number of documents to skip |
| `after` | none | The `next_cursor` of the previous page: continues the list after it (see [Paging with a cursor](#paging-with-a-cursor)). Can't be combined with `skip` |
| `count` | `false` | `true` adds `total`, the number of documents matching `where` |

Each parameter may appear at most once. Any other parameter returns `400 invalid_query`.

```http
GET /v1/shop/orders/items?where={"status":"active","price":{"$between":[10,20]}}&order_by=-_meta.created_at&limit=10&count=true
```

```json
{ "items": [ ... ], "limit": 10, "skip": 0, "has_more": true, "next_cursor": "…", "total": 134 }
```

The example is shown unencoded for readability.

{{< hint warning >}}
Clients must URL-encode `where`. Sent raw, characters such as `+` (a space in a query string), `&` or `#` change the query silently, and you get wrong results instead of an error. Use your HTTP library's query builder, or the JavaScript client, which does it for you.
{{< /hint >}}

## `where`

- Top-level keys are field paths. Use dot notation for nested fields, for example `address.city`.
- Multiple keys, and multiple operators on one field, are combined with AND. Use [`$or`](#or-and-and) for alternatives.
- A bare value is shorthand for `$eq`: `{"status": "active"}` is the same as `{"status": {"$eq": "active"}}`.

```json
{
  "status": "active",
  "price": { "$gte": 10, "$lt": 50 },
  "name": { "$istartsWith": "wid" },
  "discount": { "$isNull": true }
}
```

### Operators

| Group | Operator | Value | Matches when the field… |
|---|---|---|---|
| Comparison | `$eq`, `$ne` | value | equals / doesn't equal the value |
| | `$gt`, `$gte`, `$lt`, `$lte` | number or string | is greater / less than the value |
| Sets & ranges | `$in`, `$nin` | array | is / isn't one of the values |
| | `$between` | `[min, max]` | is within the range, inclusive |
| Null | `$isNull` | `true` / `false` | is null or missing / has a non-null value |
| | `$notNull` | `true` / `false` | the opposite of `$isNull` |
| Text | `$like`, `$ilike` | pattern | matches the whole pattern: `%` is any run of characters, `_` is exactly one character |
| | `$startsWith`, `$istartsWith` | string | starts with the value |
| | `$endsWith`, `$iendsWith` | string | ends with the value |
| | `$contains`, `$icontains` | string | contains the value |

- Operators prefixed with `i` are case-insensitive. All other text matching is case-sensitive.
- Text operators apply only to string fields.
- All characters in text values are literal, except `%` and `_` in `$like` / `$ilike`. There is no escape character for them.
- `$ne`, `$nin` and `$eq: null` also match documents where the field is missing, as does `$isNull: true`.

**Array fields**

- A scalar value matches if **any element** matches. For example, `{"tags": "sale"}` finds documents whose `tags` contain `"sale"`.
- An array value matches the whole array exactly, including order.
- Properties of objects inside arrays can be queried by dot path, for example `lines.sku`.

### `$or` and `$and`

`$or` matches documents that match **any** of the objects in its array; `$and` matches documents that match **all** of them. Each object in the array is a `where` object of its own, with the same fields, operators and checks:

```json
{
  "published": true,
  "$or": [
    { "title": { "$icontains": "go" } },
    { "body": { "$icontains": "go" } }
  ]
}
```

This finds published documents whose title **or** body contains "go". Keys next to `$or` still combine with AND.

- Groups can be nested, for example an `$and` inside an `$or`, up to 4 levels deep.
- The array must not be empty, and neither may its objects.
- `$and` is rarely needed, because keys already combine with AND. It's useful inside an `$or` branch, or to put two alternatives side by side: `{"$and": [{"$or": [...]}, {"$or": [...]}]}`.
- The [limits](#limits) apply to the whole `where`: conditions in every group count towards the 32.
- In realms with access rules, `$or` only narrows what the caller may read. It can never widen it, because the [read rule](../../auth/rules/#read-rules-are-database-filters) is always applied on top.
- Other keys starting with `$` (such as `$nor`, `$not` or `$where`) are rejected.

Errors inside groups name their position, for example `where.$or.1.body.$icontains`.

### Fields and types

Every field in `where` and `order_by` must be one of:

- declared in the collection's `schema.json`;
- `id`;
- `_meta.created_at`, `_meta.updated_at` or `_meta.version`;
- `_meta.owner`, `_meta.created_by` or `_meta.updated_by` (strings; `_meta.owner` can be `null`). Only documents in realms with `auth: enabled` have them; elsewhere these fields are always missing.

Values are checked against the field's schema type:

- a string for an `integer` field is rejected;
- `1.5` for an `integer` field is rejected;
- `null` is only accepted for fields whose type includes `null` (use `$isNull` otherwise);
- `_meta.created_at` and `_meta.updated_at` take RFC3339 timestamps and are compared as dates, for example `{"_meta.created_at": {"$gte": "2026-09-01T00:00:00Z"}}`.

### Limits

| Limit | Value |
|---|---|
| Size of `where` | 4096 bytes |
| Conditions (operators) in `where` | 32 |
| Length of a text operator value | 256 characters |
| Wildcards (`%`, `_`) in a `$like` / `$ilike` pattern | 4 |
| Fields in `order_by` | 8 |
| Nesting of `$or` / `$and` | 4 levels |

## Errors

Invalid queries return `400 invalid_query`, with one detail per problem:

```json
{
  "error": {
    "code": "invalid_query",
    "message": "invalid query parameters",
    "details": [
      { "path": "where.age.$gt", "reason": "must be of type integer" },
      { "path": "where.secret", "reason": "unknown field" },
      { "path": "order_by.nope", "reason": "unknown field" }
    ],
    "request_id": "d2fk1a30h2ipb32i2hh0"
  }
}
```

## Ordering and pagination

- `order_by=-age,name` sorts by `age` descending, then `name` ascending.
- `id` ascending is always added as a final tiebreaker unless `order_by` already includes `id`, so pages are stable. Since ids are time-sortable, the default order is creation order.
- `has_more` is `true` when documents exist beyond this page.
- `skip` pages by offset, so deep pages get slower and can shift if data changes between requests. A [cursor](#paging-with-a-cursor) doesn't have those problems.

### Paging with a cursor

When `has_more` is `true`, the answer has a `next_cursor`. Send it as `after`, with the same `where` and `order_by`, to get the next page:

```http
GET /v1/shop/orders/items?order_by=-price,name&limit=50
GET /v1/shop/orders/items?order_by=-price,name&limit=50&after=eyJvIjoi…
```

- A cursor is a position, not a reading of the document it came from: it keeps working if that document is edited or deleted meanwhile, so a loop that processes and deletes documents is safe, and documents added or removed elsewhere never shift a page. Each page costs the same however far you are, if the order is [indexed](../../configuration/config-dir/#indexesjson).
- **Treat it as opaque.** For the default order (`id` ascending) it is just the `id` of the last document, so `after=<id>` continues after any document you know. For any other order it is an encoded position that only continues the same `order_by`: another `order_by`, a made-up value or `skip` answers `400 invalid_query` on `after`.
- Equal values and missing or `null` values are handled: documents with the same sort value are told apart by `id`, and `null` or missing values sort first, as in a normal list.
- It follows `order_by` fields that hold one kind of scalar value (strings, numbers, booleans, dates, ids). For fields that hold arrays, objects or several types there is no `next_cursor`, and `after` is refused: page those with `skip`.
- It never shows more than the first page would: `where` and the [read rules](../../auth/rules/) apply to everything after the cursor. `count=true` still counts the whole list, not what is left.
- The `audit`, `jobs` and `invocations` lists of the admin API still page with `skip`.

{{< hint style="tip" title="Best practice" >}}
Filter first, page second. A narrow `where` plus a small `limit` is fast; walking thousands of pages with `skip` is not; use a cursor. Every field you filter or sort by should be covered by an [index](../../configuration/config-dir/#indexesjson), or MongoDB scans the whole collection. To read everything, use the client's `iterate()`.
{{< /hint >}}
