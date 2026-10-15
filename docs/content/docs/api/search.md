---
title: "Full-text search"
description: "A guide to searching with a text index: declaring fields and weights, languages and stemming, the search syntax, relevance and paging, $search versus $contains, limits and what it doesn't do."
icon: "search"
weight: 328
toc: true
---

`$contains` looks for a substring and reads every document. **Full-text search** looks for *words*, through an index: it finds `running` in "He was running late" and "runs", ranks documents by how well they match, and stays fast on collections that `$contains` would crawl through. It is declared per collection, in `indexes.json`, and used with `$search` in `where`: the [reference](../querying/#full-text-search) has the request; this page is how to use it well.

## Declare the index

```json
[
  { "text": {
      "fields": { "title": 10, "tags": 5, "body": 1 },
      "default_language": "english",
      "language_field": "lang"
  } }
]
```

- **Pick the fields people search.** Titles, names, tags, summaries and bodies; not ids, codes or numbers (use `where` on those). Fields are strings or arrays of strings, up to 30.
- **Weights say what matters.** A word in a weight-10 title counts ten times a word in a weight-1 body, so "handbook" in a title beats "handbook" mentioned in passing. Start with 10 / 5 / 1 for title / keywords / body and adjust by looking at results.
- **One index per collection**, a limit of the database: every searchable field goes in the same entry.
- **Changing it rebuilds it.** Different fields, weights or languages replace the index at the next provisioning, with a warning in the log; searches answer `503 index_building` meanwhile (with `Retry-After`; the JavaScript client retries 503s when its `retry` option is on). On a large collection do it at a quiet time.

## Languages and stemming

Stemming reduces words to a root, so `corriendo` finds `correr` and `shoes` finds `shoe`; stop words (`the`, `de`, `und`) are ignored. Both depend on the language, in two places:

- **When a document is indexed**, in `language_field`'s language if the collection declares one (a string field holding a locale code: `es`, `en-GB`, `pt_BR`), else in `default_language`. Locale codes are mapped to the database's languages (danish, dutch, english, finnish, french, german, hungarian, italian, norwegian, portuguese, romanian, russian, spanish, swedish, turkish); a locale with no language of its own (Chinese, Arabic…) is indexed without stemming, and its words match exactly.
- **When a search is made**, in `language=` if given, else the **user's locale**, else the first language of `Accept-Language` for anonymous callers, else the index's default.

A collection with documents in several languages should declare `language_field` and have its writers set it. A search is stemmed in one language: it finds the Spanish documents' Spanish roots when asked in Spanish, and the English ones' when asked in English. To find both, search twice.

## The syntax

```
red "running shoes" -kids
```

- **Words**: a document matches if it has **any** of them (and ranks higher for having more).
- **`"quoted phrases"`**: the words together, in that order.
- **`-excluded`**: a document with that word is left out.
- It is only words: operators, wildcards and regular expressions have no meaning, and at most 256 bytes and 10 terms are read (a phrase is one). A user can type anything.

## Relevance and paging

- `order_by=_relevance` (alone) puts the best match first, ties by `id`, and each document gets `_meta.score`. The score is only comparable inside one result.
- Any other `order_by` makes the search a filter: "matches, newest first".
- Relevance pages with `skip` and stops at the **first 1,000 results**: nobody reads past page 50, and the database would score everything to give it. If users reach the limit, ask for a narrower `where` or a more specific search, not a deeper page.
- `count=true` counts every match, which on a broad search reads them all: use it when you show the number.

## `$search` or `$contains`?

| | `$search` | `$contains` / `$icontains` |
|---|---|---|
| Finds | words, phrases, stems (`runs` ≈ `running`) | any substring (`runn`, `-2026-`, `a@b`) |
| Needs | a text index | nothing |
| Cost | uses the index | reads the whole collection |
| Ranks | yes, by weights | no |
| Good for | prose: titles, bodies, descriptions | codes, ids, short fields, small collections |

Use `$contains` for a few thousand documents and for fragments of identifiers. Use `$search` when people look for things by describing them.

## Combine it

- **With `where`**: `{"$search": "handbook", "status": "published", "_meta.org": "…"}`; the other conditions narrow the documents before ranking.
- **With rules**: the read rule is applied first, so a search never returns what a list wouldn't.
- **With an aggregation**: [`$search` in `_aggregate`](../aggregations/) answers "how many results per category" for the facet next to the search box.
- **With `expand`**: `?expand=author(name)` on the search results works as on any list.
- **From functions and the client**: `search(text, { where, language, limit })` in [`backd-js`](../../clients/js/#aggregations-and-search) and `ctx.db`.

## What it doesn't do

- **No typo tolerance** (`runnng` finds nothing) and **no partial words** (`runn` doesn't find `running`; use `$contains` for that).
- **No facets or highlighting**, and no synonyms.
- **One language per query.**
- **An external search engine** (typo tolerance, facets, synonyms) can come behind `$search` later without changing the request.
