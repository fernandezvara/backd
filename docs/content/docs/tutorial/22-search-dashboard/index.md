---
title: "22. Search and a team dashboard"
description: "Search Shelf's assets with a weighted text index, best match first and in the user's language, and show how many pins each member added per month in the team's time zone with one declared aggregation: the missing-index warning and its fix."
weight: 420
toc: true
---

Shelf's gallery filters by tag. It can't answer "where is that handbook about onboarding?", and a team can't see who is pinning what. Two features of the query language fix that, both declared in config so their cost is designed:

- a **text index** and `$search`, for words in prose, ranked by relevance;
- an **aggregation**, for counts and summaries that a dashboard shows without a function per number.

## A text index on assets

Add an entry to `config/shelf/main/assets/indexes.json`. Its fields are weighted: a word in the title counts ten times a word in the body.

```json
{ "text": { "fields": { "title": 10, "tags": 5, "body": 1 }, "default_language": "english" } }
```

{{< example-file path="shelf/main/assets/indexes.json" >}}

`backd provision` (or the start of `backd` itself) creates it. Declaring it later is the same as changing it: the index is **rebuilt**, the startup log says so, and a search made meanwhile answers `503 index_building` for a few seconds.

## Search, best match first

```js
const page = await assets.search('onboarding "style guide" -draft', { where: { published_at: { $ne: null } }, limit: 9 })
page.items[0]._meta.score        // the relevance
```

- Words match any of them, a quoted phrase must appear as written, and `-draft` leaves out what has it.
- Results come **best first** (`order_by=_relevance`), ties by `id`. Pages go by `skip`, up to the first 1,000 results.
- The query is stemmed in the **user's language**: their locale, else the browser's `Accept-Language`, else the index's `default_language`. A Spanish reader's `corriendo` finds `correr`. To search in another language, pass `language: 'es'`.
- The read rules apply first, so a search shows exactly what a list would.

The gallery's search box is that call, and clearing it brings the list back:

```js
const page = this.q.trim()
  ? await assets.search(this.q.trim(), { ...common, where: this.where(), skip: this.assets.length })
  : await assets.list({ ...common, where: this.where(), orderBy: this.sort, after: this.after })
```

`$search` is for prose. `$contains` (the tag filter) stays right for exact fragments of short fields.

## Declare what a dashboard may count

A team's dashboard shows **pins per member per month**. A collection says which fields can be summarized; add this at the end of `config/shelf/main/pins/collection.yaml`:

```yaml
aggregate:
  group_by: [author, _meta.created_at]
```

Restart `backd` and read its log:

```text
WARN shelf/main/pins: aggregate.group_by "author" has no index that starts with it, so grouping by it scans every matching document: add {"fields": ["author"]} to indexes.json
```

`backd config check` prints the same line. Nothing is wrong yet: pins are few. But an aggregation reads every pin the request selects, so a team with a hundred thousand of them would feel it. The warning is there so the index is a decision, not a surprise. Create `config/shelf/main/pins/indexes.json`:

```json
[
  { "fields": ["author"] }
]
```

and the warning is gone. (`_meta.created_at` needed none: every collection already has an index that starts with it.)

## A time zone for the team

A month starts at midnight **somewhere**. A pin made at 23:30 UTC on March 31st belongs to April for a team in Auckland. The team's zone is a claim, next to its plan, that only an operator writes; `team-plan` now takes it too:

{{< example-file path="shelf/main/_functions/team-plan/index.ts" >}}

`setClaims` replaces all the claims, so the function reads the team's first and keeps the plan when only the zone changes. An unknown zone is `400 invalid_timezone`.

## The dashboard

```js
const org = await backd.orgs.get(team)
const tz = org.claims.timezone ?? 'UTC'
const { groups } = await pins.inOrg(team).aggregate({
  groupBy: ['author', '_meta.created_at:month'],
  metrics: ['count'],
  tz,
  orderBy: ['-_meta.created_at:month', '-count'],
})
// groups[0] → { key: { author: 'u_9', '_meta.created_at:month': '2026-09' }, count: 12 }
```

- `_meta.created_at:month` is a **bucket**: `day`, `week` (ISO), `month` or `year`, cut in `tz`. Keys are text (`2026-09`).
- The read rule runs before grouping: a member counts only the pins of their team; someone in no team is refused (`403`, as for a list) and a member of another team counts nothing of this one, and `aggregate` on a collection with no `aggregate:` (try `shares`) is `400 aggregate_not_enabled`.
- Months with no pins aren't returned. For a chart, `fillBuckets(groups, { field: '_meta.created_at', unit: 'month', from: '2026-01', to: '2026-12' })` adds them with `count: 0`.
- **A dashboard loaded on every page** is a better fit for a counter a function keeps up to date than for an aggregation per visit; see [Aggregations](../../build/api/aggregations/#cost-limits-and-errors).

## You should see

- A search for a word in a title and in another asset's body returns the title first, with `_meta.score`; `-word` and `"a phrase"` behave as written; a `$search` inside `$or` is `400`.
- Declaring `aggregate:` without the index logs the warning; adding `indexes.json` silences it.
- After `team-plan` gives the team a `timezone`, the Teams view shows *Pins per member per month* in that zone, and an outsider's aggregate is refused or empty.

Read [Full-text search](../../build/api/querying/#full-text-search), [Aggregations](../../build/api/aggregations/) and [the client](../../build/clients/js/#aggregations-and-search).
