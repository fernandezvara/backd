# Design: query language — aggregations and full-text search

- **Issues:** features 21.1–21.8 (milestone `21-query-language`)
- **Status:** decided
- **Related designs:** the query translator and rules pushdown (architecture §3.4), [conditional-get.md](./conditional-get.md) (`ETag`s), [field-encryption.md](./field-encryption.md) (encrypted fields excluded), [realtime.md](./realtime.md) (`$search` not allowed in subscriptions), [organizations.md](./organizations.md)

## 1. The idea

Two extensions to backd's query language, built the same way as `where`: a small declared vocabulary, translated by backd through an allowlist, never MongoDB syntax from clients.

- **Aggregations:** `GET …/_aggregate` groups and summarizes documents (count, unique count, sum, average, minimum, maximum), with date buckets in the business's time zone. Dashboards and counters no longer need a function per summary.
- **Full-text search:** text indexes declared in `indexes.json`, and a `$search` operator in `where` with relevance sorting and languages from the realm's locales. Unlike `$contains`, it uses an index.

Both apply **read rules pushed down**, so they never reveal anything a list wouldn't. Both are **declared in config**, so cost is designed rather than discovered, and both come with best-practice documentation.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Aggregation shape | `GET …/{collection}/_aggregate` with `where`, `group_by`, `metrics`, `order_by`, `limit`; a fixed internal pipeline |
| Declaration | `aggregate:` in `collection.yaml` with `group_by` and `metrics` field lists; startup checks; missing-index warnings |
| Metrics | `count`, `count_distinct`, `sum`, `avg`, `min`, `max` |
| Dates | buckets `:day`, `:week` (ISO), `:month`, `:year` with `tz`; text keys |
| Limits | 5 s, no disk use, 1,000 groups, one concurrent request per caller and collection |
| Text indexes | one per collection in `indexes.json`, with weights, `default_language` and `language_field` |
| `$search` | top level of `where` only; words, phrases, exclusions; `order_by=_relevance`; `_meta.score` |
| Languages | per document via `language_field`; per request from `language=`, `user.locale` or the index default; mapped from realm locales |
| Security | rules pushed down; encrypted fields refused; nothing beyond what lists show |

## 3. Aggregations

### 3.1 Declaring (issue 21.1)

```yaml
# collection.yaml of "orders"
aggregate:
  group_by: [status, category, _meta.org, _meta.owner, _meta.created_at, paid_at]
  metrics: [total, quantity, paid_at]
  limits:                       # optional, can only lower instance defaults
    time: 3s
    max_groups: 500
```

- **Opt-in:** collections without `aggregate:` answer `400 aggregate_not_enabled`.
- **`group_by` fields:** strings, booleans, numbers, dates, `_meta.org`, `_meta.owner` and `_meta.created_at`. Date fields can be bucketed.
- **`metrics` fields:** numbers, plus dates for `min` and `max`.
- **Startup checks:** fields exist in `schema.json` with suitable types, and encrypted fields are refused.
- **Startup warning:** names `group_by` fields with no supporting index in `indexes.json`, and suggests the index.

### 3.2 Requesting (issue 21.2)

```
GET /v1/{realm}/{db}/orders/_aggregate
    ?where={"status":{"$in":["paid","shipped"]}}
    &group_by=category,paid_at:month
    &tz=Europe/Madrid
    &metrics=count,sum:total,avg:total,count_distinct:_meta.owner
    &order_by=-sum_total
    &limit=50
```

```json
{
  "groups": [
    { "key": { "category": "shoes", "paid_at:month": "2026-09" },
      "count": 412, "sum_total": "18234.50", "avg_total": "44.26", "count_distinct__meta.owner": 230 }
  ],
  "total_groups": 24
}
```

- **`where`:** the same syntax, allowlist, caps and `$search` (§4) as lists. The read rule is pushed down **before** grouping, so totals include only documents the caller could list.
- **`group_by`:** up to 3 declared fields, with date buckets `:day`, `:week` (ISO, Monday), `:month`, `:year`. Without `group_by` the result is a single group with an empty key, for counters and KPIs.
- **`tz`:** an IANA zone (default UTC), validated. Bucket keys are text: `2026-09-14`, `2026-W38`, `2026-09`, `2026`.
- **`metrics`:**

  | Metric | Fields | Behavior |
  |---|---|---|
  | `count` | — | always allowed |
  | `count_distinct:<f>` | a `group_by` field | exact; above 10,000 distinct values fails with `aggregate_too_expensive` |
  | `sum:<f>`, `avg:<f>` | numeric metrics | missing and `null` ignored; `avg` of nothing is `null` |
  | `min:<f>`, `max:<f>` | numeric or date metrics | |

  Result names are `<metric>_<field>`. Decimals stay exact and are returned as lists return them.
- **Missing values:** a missing or `null` group value forms its own `null` group. Empty buckets are **not** filled in; the JS client has a helper for charts.
- **`order_by`:** any group key or result name (`-count`, `sum_total`, `paid_at:month`). `limit` is at most `max_groups` (1,000 by default). `total_groups` is always returned.
- **Built internally** as `$match` (where plus rules), `$group`, `$sort`, `$limit` with `maxTimeMS` and `allowDiskUse: false`. Clients never send stages.

### 3.3 Limits and errors

- **Expensive requests:** past the time limit, or needing to spill to disk, they answer `422 aggregate_too_expensive` with a hint (narrow `where`, add an index, fewer groups).
- **Validation:** unknown or undeclared fields, unknown metrics or buckets, and `tz` errors answer `400 invalid_aggregate`.
- **Metrics** per collection: count, duration and failures by reason.

### 3.4 Caching

- **`ETag`:** aggregate responses carry a weak `ETag` over the result, as lists do ([conditional-get.md](./conditional-get.md) §4), and are `private, no-cache`.
- **Hot dashboards:** precomputed counters maintained by functions remain the better tool, and the docs show both.

## 4. Full-text search

### 4.1 Declaring (issue 21.3)

```json
// indexes.json
{
  "text": {
    "fields": { "title": 10, "tags": 5, "body": 1 },
    "default_language": "english",
    "language_field": "lang"
  }
}
```

- **One text index per collection** (a MongoDB rule). Fields must be strings or arrays of strings in `schema.json`. Encrypted fields are refused.
- **`default_language`** must be one MongoDB supports, or `none`.
- **`language_field`** is optional. It must be declared as a string, and holds **realm locale codes** (`es`, `en`, `es-MX`). backd maps them to MongoDB's language names when indexing, storing the mapped value in a hidden field used as MongoDB's language field. Unsupported languages are indexed without stemming.
- **Rebuilds:** changing the declaration rebuilds the index during provisioning, like other indexes. The startup log reports it, and searches during the rebuild answer `503 index_building`.

### 4.2 Searching (issue 21.4)

```
GET /v1/{realm}/{db}/articles?where={"$search":"red \"running shoes\" -kids","status":"published"}
    &order_by=_relevance&limit=20
```

- **Placement:** `$search` is allowed **only at the top level** of `where`, ANDed with the other conditions and the pushed-down read rule. Inside `$or` or nested groups it's rejected with `400 invalid_query` explaining why (a MongoDB limit).
- **Syntax:** words (matching any), quoted phrases, and `-excluded` words. It's a plain string passed to MongoDB's text search, never parsed as operators. At most 256 characters and 20 terms.
- **Language:** `language=` if given, else the caller's `user.locale`, else `Accept-Language` for anonymous callers, else the index default. Mapped from locale codes the same way as indexing.
- **Relevance:**
  - `order_by=_relevance` sorts by MongoDB's text score (ties by id), and `_meta.score` is included in each document;
  - other `order_by` values work as usual with the search acting as a filter;
  - relevance pages use offsets, capped at the first 1,000 results, and cursors stay for other sorts.
- **Combinations:**
  - `$search` works in `_aggregate`'s `where` (for example "results per category" next to a search box);
  - expansion and conditional GET work as for any list;
  - realtime subscriptions refuse `$search` (`400 invalid_query`), because relevance can't be evaluated per event;
  - collections without a text index answer `400 search_not_enabled`.
- **`$contains`** stays for exact substrings on small collections. The docs explain when to use which.

## 5. Functions and JavaScript client (issue 21.5)

- **Functions:**
  - `ctx.db.collection("orders").aggregate({ where, group_by, metrics, tz, order_by, limit })` with the caller's rules;
  - `ctx.admin.db` aggregates with admin access;
  - `$search` works in `list`.
- **JS client:**
  - `client.collection("orders").aggregate({…})`, returning typed groups;
  - `search(text, { where, language, limit })` as a helper over `list` with `order_by=_relevance`;
  - `fillBuckets(groups, { field, unit, from, to })` fills empty date buckets for charts;
  - typed errors (`aggregate_not_enabled`, `aggregate_too_expensive`, `search_not_enabled`, `index_building`).
- **`@backd/functions-testing`** supports aggregation in its in-memory store, and search as a simple word match, documented as an approximation.

## 6. Shelf (issue 21.6)

- **Search:** items get a text index on title and description with weights, and a search box sorted by relevance in the user's language.
- **Dashboard:** a team dashboard shows items added per member per month, using `group_by=_meta.owner,_meta.created_at:month` with the team's time zone.
- **Cost:** the chapter shows the missing-index warning and fixes it.
- **Tests:** Shelf code and tests are updated, and earlier chapters still pass.

## 7. Security and performance

- **Nothing beyond lists:** read rules are pushed down before grouping and searching, and aggregates cover only documents the caller can read.
- **No MongoDB syntax from clients:** fixed pipelines, a search string that is never parsed as operators, and an allowlisted `where`.
- **Encrypted fields** can't be grouped, measured or text-indexed.
- **Bounded cost:** declared fields, index warnings, time limits, no disk use, group caps, and search length and term caps.
- **Review and load tests** (21.8): expensive aggregations at scale, abusive search strings, limits under load.

## 8. Documentation (issue 21.7)

- **Aggregation best practices:**
  - what a request costs (every matching document is scanned);
  - indexes that help (on `where` fields first, then `group_by`);
  - narrowing `where` (dates, organization);
  - choosing buckets and time zones;
  - caching with `ETag`s;
  - when to switch to counters precomputed by functions (dashboards loaded on every page, very large collections);
  - reading the warnings and errors.
- **Search guide:**
  - declaring the text index and weights;
  - languages and stemming, stop words, mixed-language collections;
  - the search syntax;
  - relevance and paging;
  - `$search` versus `$contains`;
  - what it doesn't do (typo tolerance, facets, an external engine as a possible later option).
- **References:** `collection.yaml` and `indexes.json`, OpenAPI for `_aggregate` and `$search`, functions and client.

## 9. Issues

| id | Issue | Depends on |
|---|---|---|
| 21.1 | Declaring aggregations | — |
| 21.2 | The aggregate endpoint | 21.1, 20.2 |
| 21.3 | Text indexes | — |
| 21.4 | `$search` and relevance | 21.3, 21.2 |
| 21.5 | Aggregation and search in functions and the JavaScript client | 21.4 |
| 21.6 | Shelf search and dashboard | 21.5, 14.10 |
| 21.7 | Aggregation and search documentation | 21.6 |
| 21.8 | Review and load tests | 21.7 |

**When to build:** after reference expansion (15), since both extend the read path and the translator. Order: 13 → 19 consents → 20 → 14 → 15 → **21** → 18 → 17 → 19 personal export → 16, with push in parallel.

## 10. Alternatives considered

- **A JSON pipeline subset, or only `count` by one field:** rejected for a declarative endpoint.
- **Aggregation open on every collection and field, or on any field of opted-in collections:** rejected for declared fields.
- **Only `count`, `sum` and `avg`, or percentiles:** rejected; percentiles need MongoDB 7.0+ and are approximate.
- **An external search engine, or Atlas Search:** rejected for MongoDB text indexes; an external engine can come later behind `$search`.
- **One language per collection, or no stemming:** rejected for languages per document and per request.

## 11. Follow-ups

- Precomputed counters declared in config, kept up to date by backd.
- Percentiles and medians when MongoDB 7.0 is the minimum.
- An external search engine behind `$search` (typo tolerance, facets).
- Skipping aggregate queries when nothing changed, with realtime's change tracking.
- Admin UI: aggregate explorer and search in the data browser.
