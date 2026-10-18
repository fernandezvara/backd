---
title: "Realtime"
description: "Subscribe to a collection's changes over one server-sent event stream: the request, the events, resuming, limits and how to start from a known state."
icon: "lightning"
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

## Live queries, not a change log

A stream is a **live query**: it tells a client how *its own view* of a collection changes, not what happened to the collection. That is why events are per subscriber:

- A document that becomes readable to you (a teammate assigns it to you, an update makes it match your `where`) arrives as `created`, even though it was only updated.
- A document that stops matching arrives as `removed`, even though it still exists. `removed` means *deleted or no longer visible to you*, never "deleted for everyone".
- A change to a document you never could read produces nothing: you can't learn that it exists.

If you need an audit of everything that happened, use [triggers](../../functions/triggers/) or the [audit trail](../../../operate/administration/audit/); a stream shows state.

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
| `session_ended` | The session, user or API key behind the stream is gone (signed out, revoked, deleted, disabled). The stream closes; sign in again. |
| `reauth` | The credential's lifetime is over. The stream closes; reconnect with a fresh credential and the last resume token. |
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

## Handling `reset`

`reset` says the stream can no longer promise the client has the right state for some subscriptions. The remedy is always the same: **read those subscriptions again**. The JavaScript client does it for you, with a random wait so that many clients reset by one cause don't read at once.

| `reason` | What happened | What to do |
|---|---|---|
| `slow_consumer` | The client didn't read fast enough and its buffer was dropped. | Reload. If it repeats, the client is too slow or the subscription too wide: narrow `where`. |
| `history_expired` | The change history no longer reaches the resume token (the client was away longer than MongoDB's oplog covers, or the token is over a day old). | Reload; consider [sizing the oplog](../../../operate/change-feed/#sizing-the-history). |
| `preimage_missing` | A change arrived without the document as it was, so who could see it can't be told. Nothing is guessed, and nobody is told the id. | Reload. Repeated resets mean [pre-change images](../../../operate/change-feed/#requirements) are missing or expiring too early. |
| `access_changed` | The caller's access to the subscription changed. | Reload; the list may now be empty, or a `403`. |
| `config_changed` | The collection's rules or its `realtime:` opt-in changed. The subscription is gone from the stream. | Subscribe again, then load. `realtime_not_enabled` means it was switched off. |

## Resuming

The last event of every change carries an SSE `id`: a **resume token**. After a disconnect, open a stream again with `resume_from` (or the `Last-Event-ID` header) set to the last token you saw, and the stream continues after it.

- A token is signed and bound to the realm, the caller and what their access depended on when it was issued: after a role, membership or team change a token is not replayed (the stream says `reset` with `history_expired`: reload). Another caller's, another realm's or a forged token is `400 invalid_resume_token`.
- Replayed events are evaluated with the caller's **current** access, so a resume can never show more than a list would.
- If the history no longer reaches the token (MongoDB's oplog moved on, or the token is more than a day old) the stream says `reset` with `history_expired` for every subscription: reload them.
- The token isn't tied to the set of subscriptions: you may add or drop subscriptions when you resume; reconcile new ones by version.

## Errors

| Answer | `code` | Meaning, and what to do |
|---|---|---|
| `400` | `invalid_query` | A subscription is wrong (unknown collection, a `where` operator a stream can't use, duplicate ids). `details` name it. |
| `400` | `realtime_not_enabled` | The collection doesn't declare `realtime: true`. |
| `400` | `too_many_subscriptions` | Over the per-stream limit: close views, or subscribe with a broader filter. |
| `400` | `invalid_resume_token` | The token is forged, from another realm or caller, or was signed with a key that has been rotated. Drop it and reload. |
| `401` | `unauthenticated` | No valid credential: sign in. |
| `403` | `forbidden` | The caller can read nothing of that collection. `details` name the subscription. |
| `429` | `too_many_requests` | Too many streams or too fast; honor `Retry-After`. |
| `503` | `realtime_unavailable` | This instance serves no realtime (`BACKD_EVENTS=false`): route streams to one that does. |

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

## What a stream costs

The cost has three parts, and only the first depends on your subscriptions:

- **Checks per change.** Each change is evaluated against the subscriptions it can matter to; the guide below shows how filters keep that small.
- **Pre-change images.** A realtime collection keeps the previous version of every updated or deleted document in MongoDB's change log, for as long as the log is kept: storage and some write time that grow with document size and change rate ([measured numbers](../../../operate/change-feed/#what-it-costs)). Don't mark very large, hot collections `realtime: true`.
- **The read of the log.** One stream per instance reads the whole deployment's changes, so it costs as much as the cluster's write rate, whatever you subscribe to.


Every change to a realtime collection is checked once against the subscriptions it can matter to, with the caller's `where` and read rule evaluated on the document before and after the change. Subscriptions are filed by the **equalities their filter can only be visible under**, so a change finds its subscribers without asking all of them:

- A subscription to one document (`doc`) is found by its id.
- A `where` (combined with the read rule) with an equality or `$in` on a field, such as `_meta.org`, `_meta.team` or `status`, is filed under that value. `inOrg`, `hasOrgRole`, `inTeam` and `hasTeamRole` rules become `_meta.org`/`_meta.team` memberships per caller, so they are filed like plain equalities.
- A subscription with no such equality is **checked on every change** of its collection on that instance.

{{< hint style="tip" title="Best practice" >}}
Subscribe with an equality on a field that groups your data (`where: { "_meta.org": "o_1" }`, a project id, a room). Without one, the checks per change grow with the number of subscribers; `backd_realtime_subscription_checks_total{kind="scanned"}` shows it.
{{< /hint >}}

## When access changes

A stream is tied to the session, user or API key that opened it, and to the roles, memberships and teams its rules depend on.

- **Sign-out, revocation, deletion or disabling** of the session, user or key: `session_ended` and the stream closes, within the time it takes the change to reach the instance. Reconnecting with the same credential fails with `401`.
- **Roles, organization memberships, team roles, organization claims or `require_mfa`**: the stream reads the caller's access again. If the effective access to a subscription differs, that subscription gets `reset` with `access_changed` and follows the new access from then on; if it is the same, nothing is sent. Changes of an organization are coalesced into at most one reload per stream every quarter second.
- **The app configuration:** a stream whose rules read it (`appConfig(...)`) reloads when the configuration changes. **Time:** access that ends by the clock (a role's window) has no event, so open streams read their access again every five minutes.
- **Credential lifetime**: a stream never outlives its session or key: at its expiry the stream says `reauth` and closes.

A rule that depends on the clock (`now`) is a different filter at each reload, so such a stream may get a `reset` it didn't need: it is harmless and costs one reload of that subscription.

The JavaScript client does all of the above for you: see [live lists](../../clients/js/#live-lists).
