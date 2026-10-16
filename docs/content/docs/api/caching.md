---
title: "Caching and conditional GET"
description: "ETags, If-None-Match and 304, what the server still does, public caching of anonymous reads, and when to poll."
icon: "arrows-clockwise"
weight: 330
toc: true
---

An app that reads the same documents again and again can ask the server "has this changed?" instead of downloading it again. `backd` answers with `304 Not Modified` and no body when nothing changed. This page explains what that guarantees, how to do it by hand, and how a collection can let a CDN serve its public reads.

## How a revalidation works

1. A read of a document or a list returns an `ETag`.
2. The next read of the same URL sends it back in `If-None-Match`.
3. If what the caller would receive is the same, the answer is `304` with the same `ETag`, `Cache-Control` and `Vary`, and no body. Otherwise it is a normal `200` with the new `ETag`.

```http
GET /v1/shop/main/orders/ord_1
→ 200  ETag: "3"

GET /v1/shop/main/orders/ord_1
If-None-Match: "3"
→ 304  ETag: "3"
```

### What the server still does

A `304` saves the transfer, not the work. On every read `backd` still runs the query and evaluates the [read rule](../../auth/rules/) for the caller, and only then compares tags. That is why it is safe:

- A document the caller can no longer read answers **`404`**, never a `304` that would confirm it exists.
- Two callers with different views of a document never share a tag: the tag describes what *this* caller receives.
- A document that was edited, added, removed, hidden or revealed changes the tag.

### The tags

| Response | `ETag` | Notes |
|---|---|---|
| A document | `"<version>"`, for example `"3"` | The document's `_meta.version`. A response that differs from the stored document for the caller (hidden fields) adds `.r` and 8 hex characters, such as `"3.r1a2b3c4d"`. |
| A list | `W/"<hash>"` | Weak: a hash over the id, version and view of every returned document, `has_more`, the cursor and the total. One tag covers the whole page. |
| With `?file_links=true` | the version, for `If-Match` | Never `304`: the links are made per response and expire, so the answer carries `Cache-Control: private, no-store`. |

### `If-Match` and `If-None-Match`

They look alike and do opposite jobs:

| Header | On | Meaning |
|---|---|---|
| `If-None-Match` | reads | "Send it only if it is not still this version" → `304` when it is |
| `If-Match` | writes | "Write only if it is still this version" → `412` when it isn't |

The same `ETag` value goes in both. `If-Match` looks only at the version, so a tag with a representation suffix still guards a write. See [Concurrency and `If-Match`](../documents/#concurrency-and-if-match).

{{< hint style="note" >}}
`304` is cheap on the wire but not free on the server: the query runs each time. To avoid even that, use realtime subscriptions instead of polling (see [Polling or realtime](#polling-or-realtime)).
{{< /hint >}}

## In the JavaScript client

The [client](../../clients/js/#revalidation-cache) does all of this for you: it keeps the last body and `ETag` per URL in a bounded in-memory cache and sends `If-None-Match` automatically. `cache: false` turns it off for a client or a call, signing in or out empties it, and writes drop the document's entry.

## By hand over HTTP

For a mobile app or a script, keep the `ETag` next to the body and send it back. Use a cache per signed-in user, and drop it on sign-out; a `304` is only meaningful for the same caller.

```http
GET /v1/shop/main/orders?limit=20
Authorization: Bearer …
If-None-Match: W/"9f1c2a…"
```

```http
HTTP/1.1 304 Not Modified
ETag: W/"9f1c2a…"
Cache-Control: private, no-cache
Vary: Origin, Authorization, Cookie
```

Browsers' own HTTP cache does this for `GET`s by itself because responses are `private, no-cache`: it may keep them but must revalidate every time. A `fetch` from a web app needs no code for it.

## Public caching

By default no shared cache stores a response: `Cache-Control: private, no-cache`. For content anyone may read (published posts, a product catalog) a collection can opt in, so a CDN serves it without reaching `backd`:

```yaml
# collection.yaml
cache:
  public: true
  max_age: 60       # seconds, at most 3600
rules:
  read: document.published == true
```

- **Who gets it:** only requests with no `Authorization` header and no cookie, in the default view. They answer `Cache-Control: public, max-age=60` with the `ETag`; the same documents for a signed-in caller or an API key stay private. `Vary: Origin, Authorization, Cookie` keeps the two apart in caches.
- **What is refused at startup:** `public: true` without a `max_age` of 1 to 3600, in a collection with no read rule, with one anonymous callers can never satisfy (`user != nil`, `hasRole(…)`), or one that depends on `now`.
- **Never public:** the trash view, `?file_links=true`, and anything authenticated.

### CDN setup

- Forward `Authorization` and `Cookie` to the origin, and **key the cache on them (or on `Vary`)**: without that a CDN could serve an anonymous copy to a signed-in user, who would see less, or the other way around.
- Honor `Cache-Control` from the origin; don't override `max-age` upward, don't strip `Vary`.
- Let the CDN revalidate with `If-None-Match`: a revalidation costs one `304`.

{{< hint style="warning" >}}
A document that stops being anonymously readable (unpublished, deleted, rule changed) can still be served by a CDN until its `max_age` runs out, and there is no way to take it back from caches you don't control. Choose `max_age` as the longest a withdrawn document may stay visible.
{{< /hint >}}

## Polling or realtime

Conditional GET makes **polling** cheap: a client that asks every few seconds pays a query and a tiny `304` when nothing changed. It suits dashboards, lists that rarely change, and clients that can't hold a connection open.

Pushing changes as they happen is a different tool, planned as realtime subscriptions, and is the better fit when updates must show up in under a second or the data changes constantly. Realtime subscriptions are not implemented yet; until then, poll with `If-None-Match`.
