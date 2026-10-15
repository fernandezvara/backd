---
title: "Aggregations"
description: "Summarize documents for dashboards and counters with GET _aggregate: group by declared fields and date buckets in a time zone, count, sum, average, minimum, maximum and distinct counts, with the read rules applied first."
icon: "monitoring"
weight: 325
toc: true
---

A dashboard, a counter or a report needs summaries: orders per status, revenue per month, how many people did something. Without help that is a function per summary, or every document fetched and added up in the browser. `GET …/_aggregate` does it in the database, over **only the documents the caller may read**, with a fixed pipeline: the client picks *declared* fields, buckets and metrics, never database syntax.

## Declare what can be summarized

A collection is opt-in: [`aggregate:` in `collection.yaml`](../../configuration/config-dir/#aggregations) lists the fields a request may group by and measure. A collection without it answers `400 aggregate_not_enabled`.

```yaml
aggregate:
  group_by: [status, category, _meta.owner, _meta.created_at, paid_at]
  metrics: [total, quantity, paid_at]
```

## Ask for a summary

```http
GET /v1/shop/main/orders/_aggregate
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
    {
      "key": { "category": "shoes", "paid_at:month": "2026-09" },
      "count": 412,
      "sum_total": "18234.5",
      "avg_total": "44.2584951456310679611650485436893",
      "count_distinct__meta.owner": 230
    }
  ],
  "total_groups": 24
}
```

| Parameter | Meaning |
|---|---|
| `where` | The [same filter](../querying/) as a list's, with its caps, and its [`$search`](../querying/#full-text-search) (with `language=`). The read rule is applied **before** grouping, so a total never includes a document a list would hide. `?org=` and `?team=` work as on lists. |
| `group_by` | Up to 3 declared fields, separated by commas. A date field takes a bucket: `paid_at:day`, `:week` (ISO 8601, Monday first), `:month`, `:year`. Without `group_by` the answer is one group with an empty key: a counter. |
| `tz` | The IANA zone the buckets are cut in (`Europe/Madrid`), `UTC` by default. A sale at 23:30 UTC on the 30th is already the 1st in Madrid in summer; the bucket follows the zone, including daylight saving changes. |
| `metrics` | `count` (the default), `count_distinct:<field>`, `sum:<field>`, `avg:<field>`, `min:<field>`, `max:<field>`; at most 10. |
| `order_by` | A key or a result name, `-` for descending: `-count`, `sum_total`, `paid_at:month`. Without it, the keys ascending. Ties are broken by the keys, so pages of the same request are the same. |
| `limit` | Groups returned: 1 to the collection's maximum (1,000 by default); 100 when not given. `total_groups` always says how many there were. |

### The metrics

| Metric | Fields | What it does |
|---|---|---|
| `count` | none | documents in the group |
| `count_distinct:<f>` | a `group_by` field | exact number of different values (null and missing aren't values); past 10,000 it answers `422` |
| `sum:<f>`, `avg:<f>` | numeric `metrics` fields | missing and `null` are ignored; the sum of nothing is `"0"`, the average of nothing `null` |
| `min:<f>`, `max:<f>` | numbers and dates of `metrics` | the value as stored (dates as RFC 3339 text) |

Result names are `<metric>_<field>`: `sum_total`, `count_distinct__meta.owner`.

- **Decimals are exact.** `sum` and `avg` are strings with the digits of the numbers added (`"18234.5"`, not `18234.499999999996`); an average that doesn't end is cut at 34 significant digits. Counts are integers.
- **Keys are text for buckets** (`2026-09-14`, `2026-W38`, `2026-09`, `2026`), the field's own value otherwise, and `null` for documents where the field is missing or `null`: they form a group of their own.
- **Empty buckets aren't filled in**; a chart that needs them fills them on the client.

## Cost, limits and errors

Every matching document is read once, so the cost is the number of documents `where` selects, not the number of groups. The server bounds it:

- **Time:** 5 seconds by default (`BACKD_AGGREGATE_TIME`; a collection's `aggregate.limits.time` can lower it).
- **No disk:** an aggregation that needs more memory than the database allows is refused instead of spilling.
- **Groups:** at most 1,000 by default (`BACKD_AGGREGATE_MAX_GROUPS`, lowered per collection with `max_groups`).
- **One at a time** per caller and collection on an instance: a second one while the first runs is `429` with `Retry-After`.

| Status | `code` | When |
|---|---|---|
| 400 | `aggregate_not_enabled` | the collection declares no `aggregate:` |
| 400 | `invalid_aggregate` | an unknown or undeclared field, a bad bucket, metric, `order_by`, `limit` or `tz`; the details name each parameter |
| 422 | `aggregate_too_expensive` | past the time limit, out of memory, or too many distinct values: narrow `where`, group by fewer fields, or add an index that starts with a `group_by` field |
| 429 | `too_many_requests` | another aggregation of the collection is running for this caller |
| 401, 403 | | as for a list: nobody who can't read the collection learns anything |

`backd` warns at startup about `group_by` fields that no index leads with, and names the index to add. [`backd_aggregations_total` and `backd_aggregation_duration_seconds`](../../operations/metrics/) count and time them per collection.

## Caching

The answer has a weak `ETag` over its content and is `private, no-cache`: send it back in `If-None-Match` and an unchanged summary is a `304` with no body. The aggregation still runs (the validator is a hash of the result), so this saves bytes, not database work; for a dashboard that is loaded on every page, keep a counter a function maintains instead.
