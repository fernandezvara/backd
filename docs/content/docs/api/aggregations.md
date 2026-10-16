---
title: "Aggregations"
description: "Summarize documents for dashboards and counters with GET _aggregate: group by declared fields and date buckets in a time zone, count, sum, average, minimum, maximum and distinct counts, with the read rules applied first."
icon: "chart-line"
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

## Best practices

An aggregation is the most expensive read there is, so most of using it well is deciding **how much of the collection one request reads**.

### What it costs

Every document `where` selects is read once, whatever the number of groups: grouping 200,000 orders into 12 months reads 200,000 documents. The database does it in one pass, but a pass over a large part of a large collection takes time and memory, and `backd` bounds both (5 seconds, no spilling to disk, one request at a time per caller). So:

- **Narrow `where` first.** A dashboard for this quarter, this team or this customer reads a small slice; "everything, grouped by month" reads it all. Date ranges and `_meta.org` are the usual narrowing, and a [text search](../search/) works too.
- **Group by few things.** Each extra key multiplies the groups, not the documents read, but the groups are what the database holds in memory and what the response carries (up to 1,000).
- **Ask only for the metrics you show.** `count_distinct` keeps every distinct value of a group in memory until it counts them; past 10,000 per group it fails on purpose.

### Indexes that help

- **`where` fields first.** The documents selected are found through an index on the fields of `where` (for example `{"fields": ["_meta.org", "-_meta.created_at"]}` for "this team, recent first"), exactly as for a list.
- **Then `group_by` fields.** `backd` warns at startup about each declared `group_by` field that no index starts with, and names the line to add to `indexes.json`. The automatic index on `_meta.created_at` serves date buckets of the creation time.
- An index doesn't make grouping read fewer documents: it finds them faster and in a useful order. Reading fewer is what a narrower `where` is for.

### Buckets and time zones

- **Cut in the zone people live in.** A day or a month begins at midnight *somewhere*: pass the business's `tz`, and keep it the same everywhere the totals are compared, or a sale at 23:30 UTC lands in different months on different screens. Daylight saving changes are handled; a day of 23 or 25 hours is still one bucket.
- **Weeks are ISO weeks** (Monday first, week 1 holds January 4th), so `2026-12-31` can belong to `2027-W01`. For calendar weeks, group by `day` and add them up on the client.
- **Pick the coarsest unit that answers the question**: a year of days is 365 groups.
- **Empty buckets aren't returned.** Charts fill them with `fillBuckets` in the [JavaScript client](../../clients/js/#aggregations-and-search).

### Caching

The answer has a weak `ETag`, so an unchanged summary costs a `304` with no body, but **the aggregation still runs** to compute the validator. For a dashboard that many people load all day, this is the wrong tool: precompute.

- **Counters kept by a function** (or by the code that makes the change) turn "count the orders" into reading one document. They cost an update per change, and they can drift if a path forgets to maintain them: keep the aggregation as the audit.
- **Let a client cache it for a while** (your own `max-age` on an endpoint of yours, or the client's memory) when a figure a few minutes old is fine.
- **Don't aggregate on every page view** of a public page: put the result in a document a scheduled function refreshes.

### Reading the warnings and errors

| You see | It means | What to do |
|---|---|---|
| a startup warning `group_by "x" has no index that starts with it` | grouping by `x` reads documents in no useful order | add `{"fields": ["x"]}` (or a compound index that also serves your `where`) to `indexes.json` and provision |
| `422 aggregate_too_expensive` | past the time limit, out of memory, or too many distinct values | narrow `where`, group by fewer fields, drop `count_distinct`, add the index; if it is a report, run it off-peak from a function |
| `429` with `Retry-After: 1` | another aggregation of yours on this collection is still running | wait for it; don't fire them in parallel (the client can send them one after the other) |
| `400 invalid_aggregate` | a field, bucket, metric or `tz` that isn't valid or declared | the `details` name the parameter and what is allowed |
| `400 aggregate_not_enabled` | the collection declares no `aggregate:` | declare the fields in `collection.yaml` |
| `backd_aggregation_duration_seconds` rising | aggregations are getting slower as data grows | look at which collection ([metrics](../../operations/metrics/)), narrow it or precompute |

## Caching

The answer has a weak `ETag` over its content and is `private, no-cache`: send it back in `If-None-Match` and an unchanged summary is a `304` with no body. The aggregation still runs (the validator is a hash of the result), so this saves bytes, not database work; for a dashboard that is loaded on every page, keep a counter a function maintains instead.
