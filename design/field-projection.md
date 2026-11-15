# Design: field projection (`fields`)

- **Issues:** #976–#979 (milestone 46-field-projection)
- **Status:** decided 2026-11-15 (limits and typing chosen by the maintainer)
- **Related designs:** [reference-expansion.md](./reference-expansion.md) (`expand=ref(a,b)`), [conditional-get.md](./conditional-get.md) (the representation tag), [field-encryption.md](./field-encryption.md)

## 1. The problem

A read returns the whole document. A list screen that shows a title and a date still downloads every body, which costs bandwidth on mobile and memory in every client. `expand=ref(a,b)` can select fields, but only of referenced documents.

## 2. The parameter

`fields=title,address.city,_meta.version` on:

- `GET` of a list and of one document;
- the responses of `POST`, `PUT` and `PATCH` (the write happens as always; `fields` only shapes the answer). Also on the restore of a soft-deleted document.

Rules:

- Comma-separated paths of declared fields, dotted for nested ones, as `where` and `order_by` write them. A path selects its whole subtree.
- `id` is always returned. `_meta` is returned only when named (`_meta`, or one of `_meta.created_at`, `_meta.updated_at`, `_meta.version`, `_meta.schema_version`, `_meta.owner`, …); without it the answer has no `_meta`.
- A path that is not declared answers `400 invalid_query` with the path in `details`, like `where`. At most 32 paths.
- Without `fields` nothing changes.

## 3. It is applied last

The stored document goes through everything it goes through today (read filter, soft delete, `reveal` of encrypted fields, file links, `expand`), and the projection is applied to the result. So:

- **Rules are unaffected.** The read rule filters in the database before anything is selected; `fields` never widens or narrows what a caller may read.
- **Hidden fields.** `reveal` only ever reveals; naming an encrypted field the caller may not reveal returns it as it would without `fields`. Projection cannot unhide a field.
- **Cursors and ordering.** The cursor is built from the stored document, so paging works whatever `fields` says. What the docs state: a client that shows `order_by` values or the cursor's fields must select them itself; they are not added.
- **`expand`** is computed from the stored document too, so `_expanded` appears even when the reference field is not selected. The docs say the source field itself shows only if selected.
- **Conditional reads.** The ETag already carries a representation tag for a partial view (`representationTag`); the selected paths go into it, so a cached full document is never matched by a partial request or the reverse. A list's weak ETag covers the projected page.

## 4. Types

A projected answer is not a `T`. The typed clients return an **untyped partial document** when `fields` is used: `Record<string, unknown>` with `id` (JavaScript), `dict[str, Any]` (Python), `Map<String, dynamic>` (Dart), `map[string]any` (Go). The docs say plainly to expect that, and recommend selecting only what you read. No new generated type is made: a `Pick`-style type per call would multiply the generated code for little. The type generators and the per-realm OpenAPI add the `fields` parameter; the OpenAPI response for a call with `fields` is the object schema without `required`.

## 5. Out of scope

Aggregations (they already choose their fields), realtime events (they carry keys), `_batch` responses, and pushing the projection into MongoDB. The last is an optimisation worth measuring with `make perf-queries` (a list of large documents with and without `fields`); it must not change a response.

## 6. Order of work

Server (parse, apply, ETag, write responses, OpenAPI, tests), then the four clients and `ctx.db` (the functions' database client), then docs: the querying page, the documents page, each client page, caching.
