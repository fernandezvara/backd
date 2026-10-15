---
title: "Change feed"
description: "How backd reads MongoDB's change stream for realtime and triggers: when it reads, what it never reads, what it costs and what to size."
icon: "bolt"
weight: 417
toc: true
---

Realtime subscriptions and function triggers are built on one piece: the **change feed**, which reads MongoDB's [change stream](https://www.mongodb.com/docs/manual/changeStreams/). This page describes the feed as it is built today. The features on top of it, the subscription endpoint and triggers, are planned and are marked as such.

## When it reads

| Setting | Default | Meaning |
|---|---|---|
| `BACKD_EVENTS` | `true` | This instance may read the change stream for realtime subscribers. `false` on API replicas keeps stream work off request-serving instances. |
| `BACKD_TRIGGERS` | `true` | This instance may hold the lease of the trigger dispatcher (planned). |

Turning the setting on reads nothing by itself. An instance opens its stream **when its first subscriber arrives**, from that moment on, and closes it shortly after the last one leaves. An instance without subscribers does not connect for it and does not scan MongoDB's change history, however many replicas you run.

## What it watches, and what it never reads

- **Collections that opt in.** A collection is watched when its `collection.yaml` says [`realtime: true`](../../configuration/config-dir/#realtime). The set comes from the configuration in force, not from who subscribes; a reload that changes it makes the stream continue from where it was, so nothing is skipped.
- **backd's own collections, reduced where they are read.** To close an open stream the moment someone's access changes, the feed also hears about sessions, users, API keys, organizations, memberships and teams. For these, MongoDB itself cuts every event down to the document's key and the **names** of the fields that changed, before it reaches `backd`: password hashes, second-factor secrets and token hashes never leave the database, and are never in a log. Updates that only touch a session's last-use time, or an API key's, are dropped there too.

{{< hint style="tip" title="Already done for you" >}}
Events are never logged, and a change that can't be decoded is reported by its collection and operation only.
{{< /hint >}}

## What it costs

Opting a collection in is a decision with a price, so it is per collection and never implicit. Measured on one developer machine against a single-node MongoDB 8 (the ratios are the point, not the absolute numbers):

| Cost | What was measured |
|---|---|
| **Writes** to an opted-in collection | Pre-change images add little to documents up to 16 KiB (within the run-to-run noise). With documents of 256 KiB and eight concurrent writers, throughput fell by about 40% and the slowest writes took several times longer. |
| **Storage** | Every update or delete keeps the previous document as well: replacing 3,000 documents of 16 KiB stored 47 MiB more, one extra copy per change, held for as long as the change history is kept. |
| **Reading** | One stream drained 40,000 to 75,000 changes per second with nothing else writing, and about 9,500 per second while a hundred unrelated writes passed for every watched one: the server scans the whole deployment's history, so a stream costs as much as the cluster's total write rate. |
| **Several streams** | Eight streams reading the same backlog together delivered more than three times the total of one, each taking about twice as long. |

{{< hint style="warning" >}}
Pre-change images need MongoDB 6.0 or later, are part of what [`backd provision`](../deploying/) will set up for opted-in collections, and can't be switched off by a restart: keep `realtime: true` to collections that need it, and away from very large documents that change often.
{{< /hint >}}

## Sizing the history

A stream that falls behind can only catch up while MongoDB still holds the history it needs: its **oplog**. A consumer that is offline longer than the oplog window loses its position and continues from now, which `backd` reports as a gap (`changefeed_reopens_total{reason="history_lost"}`). Size the oplog for your write volume and the longest outage you want to survive.

## What is planned

- **Subscriptions** (`POST /v1/{realm}/_realtime`), with per-subscriber rules and immediate revocation.
- **Triggers**, a function run after a document changes, on a dispatcher with a durable position, and the `backd provision` step that enables pre-change images.

Their [design](https://github.com/fernandezvara/backd/blob/main/design/triggers-and-realtime.md) is in the repository. Until they land, the feed has no subscribers and the only visible effects are the two settings, the `realtime:` key and the [metrics](../metrics/).
