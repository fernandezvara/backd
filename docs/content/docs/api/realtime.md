---
title: "Realtime"
description: "Subscribe to a collection's changes over one server-sent event stream: the request, the events, resuming, limits and how to start from a known state."
icon: "bolt"
weight: 332
toc: true
---

A collection that declares [`realtime: true`](../../configuration/config-dir/#realtime) can be followed live. A client opens **one stream** for the whole realm with `POST /v1/{realm}/_realtime`, names what it follows, and receives the changes the caller may read as server-sent events.

```http
POST /v1/shop/_realtime
Authorization: Bearer <session or API key>
Content-Type: application/json

{
  "subscriptions": [
    { "id": "mine",  "db": "main", "collection": "orders", "where": { "status": "open" } },
    { "id": "order", "db": "main", "collection": "orders", "doc": "o_17" }
  ]
}
```

Read it with `fetch` and a streaming body: `EventSource` can't send an `Authorization` header, and sessions never go in URLs.

## Events

```
event: ready
data: {"subscriptions":["mine","order"]}

id: eyJw…
event: created
data: {"sub":"mine","doc":{"id":"o_9","status":"open","_meta":{"version":1}}}

event: removed
data: {"sub":"mine","id":"o_9","version":3}

event: reset
data: {"subs":["mine"],"reason":"slow_consumer"}

: heartbeat
```

| Event | Meaning |
|---|---|
| `ready` | The stream is open. Always first. |
| `created` | A document entered the subscription: it was created, or an update made it match `where` and the read rule. |
| `updated` | A document that was in the subscription changed. |
| `removed` | A document left it: it was deleted, or an update made it invisible to this caller. Carries only the `id` and the last `version`. |
| `reset` | Reload these subscriptions with normal reads (see below). |
| `: heartbeat` | A comment every 25 seconds so proxies keep the connection open. |

- **Documents are what a read returns** for this caller: the same fields, `_meta.version` included, no `_expanded`.
- **Strangers learn nothing.** A change to a document the caller couldn't read before or after it produces no event at all, not even an id.
- **`reset` reasons.** `history_expired` (the change history no longer reaches the stream, or a resume token is too old), `slow_consumer` (the client didn't keep up and its buffer was dropped), `preimage_missing` (a change arrived without the image needed to tell who could see it, so nothing is guessed), `config_changed` (the configuration the subscription depended on changed: the subscription is gone and must be made again; a collection that lost `realtime:` answers `realtime_not_enabled`) and `access_changed` (reserved for access changes).

## Subscriptions

| Field | |
|---|---|
| `id` | Names the subscription in events: unique in the request, at most 64 characters. |
| `db`, `collection` | What to follow. The collection must declare `realtime: true`. |
| `where` | Optional filter, in the syntax of a list's `where`, limited to comparisons, `$in`, `$nin`, `$between`, `$isNull` and `$and`/`$or`. `$search`, `$like` and the text operators answer `400 invalid_query` when subscribing. |
| `doc` | Follow one document by id instead of a filter. |

`order_by`, `limit`, `page` and `expand` are not part of a stream: ordering and loading belong to the client.

## Start from a known state

1. Open the stream and wait for `ready`.
2. Load each subscription's data with normal list or get calls, keeping the events that arrive meanwhile.
3. Apply the buffered events, then the live ones, **ignoring any event whose `_meta.version` is not newer** than the document you hold. Replays and races are then harmless.

## Resuming

The last event of every change carries an SSE `id`: a **resume token**. After a disconnect, open a stream again with `resume_from` (or the `Last-Event-ID` header) set to the last token you saw, and the stream continues after it.

- A token is signed and bound to the realm and the caller: another caller's, another realm's or a forged one is `400 invalid_resume_token`.
- Replayed events are evaluated with the caller's **current** access, so a resume can never show more than a list would.
- If the history no longer reaches the token (MongoDB's oplog moved on, or the token is more than a day old) the stream says `reset` with `history_expired` for every subscription: reload them.
- The token isn't tied to the set of subscriptions: you may add or drop subscriptions when you resume; reconcile new ones by version.

## Limits

| Limit | Default | Answer |
|---|---|---|
| Subscriptions per stream | 20 | `400 too_many_subscriptions` |
| Open streams per caller (a user or key; anonymous: per address) | 5 | `429`, `Retry-After` |
| Streams opened per caller | one per second | `429`, `Retry-After: 1` |
| Open streams per instance | 10,000 | `429` |
| Buffer per stream | 256 KiB or 1,000 events | `reset` with `slow_consumer` |
| Buffers of all streams on an instance | `BACKD_REALTIME_MEMORY` | the largest buffers are reset |

An instance and a realm set them with the [`BACKD_REALTIME_*` variables](../../configuration/) and, lowering only, the [`realtime:` section of `realm.yaml`](../../configuration/realm/#realtime). A stream never stalls the change feed: a client that can't keep up is reset, not waited for.

{{< hint style="note" >}}
A stream is a long request. Put nothing in front of it that buffers or times out responses (for nginx: `proxy_buffering off`, a generous `proxy_read_timeout`; backd sends `X-Accel-Buffering: no`).
{{< /hint >}}

## What's planned

Rules per subscriber are evaluated today by re-checking each change in memory; closing a stream the moment a session or role changes (`access_changed`, `session_ended`, `reauth`), the JavaScript client's `subscribe()` and the Shelf chapter follow in this milestone.
