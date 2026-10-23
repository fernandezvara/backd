# Design: reference expansion

- **Issues:** features 15.1–15.7 (milestone `15-reference-expansion`)
- **Status:** decided
- **Related designs:** [organizations.md](./organizations.md) (organization rules on expanded documents), the rules pushdown and property tests (roadmap phases 1–3)

## 1. The idea

Almost every app has relations: a post's author, an invoice's customer, an item's tags. Today each relation costs another request and joining on the client. With `?expand=author`, a read returns the referenced documents next to the result:

- **in one request,**
- **with one batched query per expanded path for the whole page,**
- **with every expanded document passing its own collection's rules.**

Principles:

- **Explicit and declared:** only references declared in `collection.yaml` can be expanded, and only when the request asks. Nothing is ever expanded by default, including by the JS client.
- **Additive:** the document stays exactly as `schema.json` describes it, and expansions go in `_expanded`.
- **Rules everywhere:** an expanded document is visible only if the caller could read it directly. Unreadable and missing look the same.
- **Bounded and visible cost:** caps on depth, paths, arrays and totals, cost headers in responses, and a cost guide in the docs.
- **Never across realms.**
- **Not a join engine:** `where` and `sort` stay on the collection's own fields.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Declaration | `references:` in `collection.yaml`, checked at startup |
| Matching | `field` (default: the reference name) to `key` in the target (default: `id`), which must be unique; `_meta.owner` and `_meta.created_by` can be sources |
| Scope | same database by default; another database of the same realm when declared; never across realms |
| Response | a server-owned `_expanded` section keyed by reference name; missing or unreadable is `null`; the document is unchanged |
| Request | `expand=author(name,avatar),tags(name),author.company` — explicit, with field selection |
| Limits | 2 levels, 5 paths, 100 ids per array (`truncated`), 2,000 documents per response; instance limits, lowered per collection |
| Queries | no filtering or sorting on referenced fields; documented alternatives |
| Caching | `ETag` stays the document's version; expanded responses `private, no-cache`; conditional GET only without `expand` |
| Cost | one batched query per path per level; headers report queries and documents; a cost guide |

## 3. Declaring references (issue 15.1)

```yaml
# collection.yaml of "posts"
references:
  category:                      # simplest form: field "category" holds a categories id
    collection: categories
  tags:
    collection: tags
    many: true                   # the field is an array of ids
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
| `field` | the reference name | source field in this collection; a declared field, `_meta.owner` or `_meta.created_by` |
| `collection` | required | target collection |
| `database` | this database | target database, in the same realm |
| `key` | `id` | target field that must equal the source value |
| `many` | `false` | the source is an array of values |
| `max_expand` | instance value (100) | lower cap for this array |

**Startup checks** fail with the file, the reference name and what to fix:
- the target database and collection exist in the same realm;
- the source field is declared in `schema.json` as a string, or as an array of strings with `many: true`, or is a supported `_meta` field;
- `key` is `id` or has a **unique** index in the target's `indexes.json`, so each value matches at most one document;
- reference names don't clash with declared fields other than their own source, or with each other;
- `max_expand` is not above the instance limit.

**Not checked:** values aren't checked to exist when documents are written. A reference to a deleted document simply expands to `null`. Referential integrity is out of scope.

## 4. Requesting expansion (issue 15.2)

```
GET /v1/{realm}/{db}/posts?expand=author(name,avatar),tags(name),category
GET /v1/{realm}/{db}/posts/{id}?expand=customer,customer.account_manager(name)
```

- **Paths:** a path is a reference name, or `name.nested` for the second level, where `nested` is a reference declared on the **target** collection.
- **Field selection:** `(f1,f2)` selects fields. The expanded document always includes `id`. `_meta` is included only when selected. Without parentheses the whole document is returned, as a direct read would.
- **Validation:** unknown references or fields (checked against the target's `schema.json`), a third level, or more than 5 paths answer `400 invalid_expand` with the offending part.
- **Explicit only:** no expansion happens unless `expand` is given.

## 5. Response

```json
{
  "id": "p_1",
  "title": "Hello",
  "author": null,
  "tags": ["t_1", "t_2", "t_3"],
  "_meta": { "owner": "u_9", "version": 4 },
  "_expanded": {
    "author": { "id": "pr_7", "name": "Ana", "avatar": "…" },
    "tags": [ { "id": "t_1", "name": "go" }, null, { "id": "t_3", "name": "db" } ]
  }
}
```

- **Placement:** `_expanded` holds one entry per requested path, keyed by reference name. Second-level results nest inside the expanded document's own `_expanded`.
- **`null`:** a reference that is missing, unreadable, or in an organization the caller doesn't count in becomes `null`, identical in every case. For `many`, positions are kept and each unavailable one is `null`.
- **Truncated arrays:** arrays longer than the cap expand only their first `max_expand` values, and the entry becomes `{ "items": [...], "truncated": true }` so clients can tell.
- **Empty sources:** a `null` or empty source gives `null` or `[]`.
- **Server-owned:** `_expanded` is ignored on writes, like `_meta`, so a client that PUTs back an expanded read changes nothing by accident.

## 6. Execution

For each path, per level:

1. Collect the source values from every document in the page, de-duplicated and capped.
2. Run **one** query on the target: `key in [values]`, combined with the target collection's `read` rule pushed down (including organization helpers) and the selected fields as a projection.
3. Map results back by key, putting `null` where nothing came back.

- **Rules:** rules see the same caller as the main read. Expansion never widens access: `ctx.admin.db` expands with admin access, and normal callers with their own.
- **Cost:** a list of 50 posts with `author,tags` costs the main query plus two queries. With `author.company`, three.
- **Total cap:** if the expanded documents exceed the total (2,000), the request fails with `400 expand_too_large` *before* returning partial data. The error suggests smaller pages or fewer paths.
- **Cost headers:** `X-Backd-Expand-Queries` and `X-Backd-Expand-Docs`. The query log records the paths, query count, document count and time spent expanding.
- **Timeouts:** expansion queries share the request's deadline.

## 7. Limits

| Limit | Default | Setting |
|---|---|---|
| depth | 2 | `BACKD_EXPAND_MAX_DEPTH` (max 2) |
| paths per request | 5 | `BACKD_EXPAND_MAX_PATHS` |
| ids per `many` field | 100 | `BACKD_EXPAND_MAX_ARRAY`; lower per reference with `max_expand` |
| expanded documents per response | 2,000 | `BACKD_EXPAND_MAX_DOCS` |

Instance values are logged at startup. Collections can only lower them.

## 8. Queries, caching and writes

- **No joins in queries:** `where` and `sort` accept only the collection's own fields. Expanded paths answer `400 invalid_query`, with the docs page linked in the message.
- **Caching:**
  - `ETag` is always the document's own version, so `If-Match` from an expanded read keeps working for writes;
  - responses with `expand` send `Cache-Control: private, no-cache` and are never cached publicly, even for anonymous readers;
  - conditional GET (backlog) applies only without `expand`.
- **Writes:** creates and updates don't expand their responses.

## 9. Functions, and why not `_batch` (issue 15.3)

- **`_batch`:** has no read operations (every operation writes), so there is nothing to expand there. A function that needs related documents reads with `ctx.db`, below. If `_batch` ever gains reads, they take `expand` with the same syntax and limits and the total cap applies per batch.
- **Functions:**
  - `ctx.db.collection("posts").get(id, { expand: "author(name)" })` and `.list({ where, expand })`;
  - `ctx.admin.db` expands with admin access, which the docs make explicit;
  - results have the same `_expanded` shape.
- **`@backd/functions-testing`:** expansion works with the in-memory store, applying declared references and rules.

## 10. JavaScript client (issue 15.4)

- **Option:** `expand` on `get` and `list`, as a string or a small builder: `expand(["author", ["name", "avatar"]], "tags")`.
- **Never automatic:** the client never adds `expand` by default or in helpers.
- **Types:** TypeScript types put expanded results in `_expanded` with `T | null`. `truncated` is surfaced.
- **Errors:** `invalid_expand` and `expand_too_large` as typed `BackdError`s.

## 11. Shelf (issue 15.5)

- **Profiles:** Shelf gains a `profiles` collection with a unique index on `_meta.owner`.
- **Items:** they declare an `author` reference from `_meta.owner` to `profiles`.
- **Lists:** they request `expand=author(name,avatar)`, showing the owner-to-profile recipe and field selection.
- **What the chapter shows:**
  - the cost headers before and after selecting fields;
  - what a reference to an unreadable profile looks like (`null`);
  - that writes keep using the item's own `ETag`.
- **Tests:** Shelf's tests cover the chapter, and earlier chapters still pass.

## 12. Security considerations

- **No probing:** missing, unreadable and other-organization references are identical `null`s, with no timing or error difference by design. Tests compare them.
- **No widening:** target rules are always applied, pushed down, with the main caller's identity. Admin expansion happens only through `ctx.admin.db`.
- **Only declared relations,** checked at startup, so clients can't join arbitrary collections.
- **Never across realms.**
- **Bounded cost:** caps on depth, paths, arrays and totals, and failure rather than partial results, so expansion can't be used to multiply load.
- **No shared caching of caller-dependent responses.**
- **Field selection** never reveals fields the target's rules wouldn't. Rules are evaluated on the whole document before projection; when a rule can't be fully pushed down, the expansion query fetches the whole document and the selected fields are applied after rule evaluation in memory.

## 13. Documentation (issue 15.6)

Documentation is a priority for this feature:

- **Reference page:**
  - `references:` with every key, `id` matching first, then `key` and `_meta` sources;
  - the startup errors and how to fix each one;
  - the `expand` syntax, the response shape and `null` semantics.
- **Owner-to-profile recipe,** step by step: the `profiles` collection, the unique index in `indexes.json`, the reference, the request, the result.
- **Cost guide:**
  - what each path costs and how page size and `many` multiply it;
  - how to stay cheap: ask only for what the screen shows, select fields, prefer one level, use smaller pages with `many`, cache rarely changing references by id on the client;
  - how to read the cost headers and the query log;
  - why nothing is expanded by default.
- **No joins in queries:** why `where` and `sort` don't follow references, and the two alternatives (filter by id after a lookup; copy fields kept up to date by a function, with the update cost explained).
- **Caching:** why expanded reads aren't cacheable, and why `ETag` and `If-Match` still refer to the document itself.
- **Updated references:** OpenAPI, the `collection.yaml` reference and the JS client reference.

## 14. Issues

| id | Issue | Depends on |
|---|---|---|
| 15.1 | Declare references in `collection.yaml` | — |
| 15.2 | Expand references in reads | 15.1 |
| 15.3 | Expansion in functions (`_batch` has no reads) | 15.2 |
| 15.4 | Expansion in the JavaScript client | 15.2 |
| 15.5 | Shelf: item authors through expansion | 15.4 |
| 15.6 | Reference expansion documentation | 15.5 |
| 15.7 | Security review of reference expansion | 15.3, 15.6 |

## 15. Alternatives considered

- **References in `schema.json` (`x-backd-ref`), or undeclared and chosen by the client:** rejected for `collection.yaml` with startup checks.
- **Replacing fields in place, or a JSON:API-style `included` list:** rejected for an additive `_expanded`.
- **One level only, or unlimited depth:** rejected for two levels with caps.
- **Same database only, or any database chosen per request:** rejected for declared databases within the realm.
- **Matching by id only, or a built-in public user view:** rejected for a declared `field` and `key`.
- **Whole documents only, or fixed fields per reference:** rejected for field selection per request.
- **Filtering and sorting through `$lookup`, or limited joins on unique fields:** rejected; expansion only decorates results.
- **A combined ETag, or keeping today's caching headers:** rejected for document `ETag` plus `private, no-cache`.

## 16. Follow-ups

- Admin UI: expand references in the data browser.
- Reverse expansion ("comments of this post") as counts or first-N lists, if apps ask; it's a different query shape with its own cost.
- Realtime subscriptions (backlog) will need to decide whether expansions are refreshed.
