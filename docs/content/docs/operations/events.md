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
Pre-change images are enabled per collection by `backd provision`, and a restart never switches them off: keep `realtime: true` to collections that need it, and away from very large documents that change often.
{{< /hint >}}

## Requirements

- **MongoDB 6.0 or later**, as a replica set. A configuration with a realtime collection on an older server stops at startup, naming the requirement and the collections: without the previous document, a removal could not be told to the right subscribers, and there is no degraded mode.
- **Pre-change images** on every realtime collection. `backd provision` (and `PROVISION_MODE=apply`, the default) enables them with `collMod`, which needs that privilege like creating an index does. With `PROVISION_MODE=verify` an instance **refuses to start** when a realtime collection lacks them, naming the collection and the command to run.
- **A MongoDB user that may read a change stream of the deployment.** The stream is opened on the whole deployment and `backd` filters it, and MongoDB checks the scope of the stream, not its filter: the user in `MONGO_URI` needs the `changeStream` and `find` actions on every database, for example the built-in `readAnyDatabase` role. An instance that serves realtime collections tries this at startup and stops with a message naming the role if the user lacks it. Clusters where `backd`'s user is confined to its own databases can't use realtime yet.

## Disabling pre-change images

Dropping `realtime:` from a collection leaves its images in place, so a rolling deploy with an older configuration never loses them; `backd` only warns that they still cost storage and writes. To remove them:

```sh
backd provision --disable-preimages          # lists what it would remove and stops
backd provision --disable-preimages --yes    # removes them
```

Removing them **deletes the images stored so far for good**. It only touches configured collections that no longer need them.

## The trigger dispatcher

When a collection declares [`triggers:`](../../configuration/config-dir/#triggers), one instance of the deployment dispatches them. Instances with `BACKD_TRIGGERS=true` (the default) compete for a **lease**, a document in MongoDB's `backd___deployment` database; the holder reads the change stream and queues one async job for each change a trigger matches. Nothing competes while no trigger is declared.

- **At least once, without duplicates.** The holder stores the position it has reached only after the jobs before it are queued. After a crash or a handoff the next holder starts from that position and reads some changes again; each job's id is derived from the change and the trigger, so the queue collapses the repeats (`trigger_jobs_deduplicated_total`). A function still has to be written to tolerate being run twice, because a job can fail after it was queued and be retried.
- **One leader at a time.** The lease has a time to live (15 seconds) and is renewed by the holder; an instance that was paused or partitioned and lost it can no longer move the position, because the position is stored with the lease and checked against it.
- **A worker runs the jobs.** The dispatcher only queues. Jobs run while at least one [worker](../../functions/running/) is up; with none, they wait in the queue and `trigger_dispatch_lag_seconds` stays low while the jobs' age grows (see `jobs_oldest_wait_seconds`).
- **Gaps are recorded, never silent.** If the dispatcher was away longer than the oplog reaches, the changes in between are gone from the stream: it records a gap (`window`, with the interval) in `backd___deployment.trigger_gaps`, logs an error and goes on from now. A change that arrives without the image a condition needs (`before`, `changed()`) is skipped and recorded (`preimage_missing`, with the document ids). Both are counted in `trigger_gaps_total`.
- **Several deployments, one cluster.** Give each its own [`BACKD_DEPLOYMENT_ID`](../../configuration/) so their dispatchers don't share a lease.

- **Chains and overload.** A trigger run's writes are stamped, so the events they cause carry a depth: past `max_chain`, or past `max_chained_per_minute` for one trigger, they are dropped and counted (`trigger_dropped_total{reason}`); events nobody chained are never dropped for being many. A realm with more than `max_backlog` unfinished trigger jobs (`trigger_backlog`) records further events as a `backlog` gap instead of queuing them, so the jobs collection stays bounded and other realms keep flowing. Alert well below the ceiling.
- **A new index.** The dispatcher counts a realm's unfinished trigger jobs by origin and status; `backd provision` creates that index in each realm's `jobs` collection (instances with `PROVISION_MODE=verify` ask for it).

`ordered` triggers are planned.

## Sizing the history

A stream that falls behind can only catch up while MongoDB still holds the history it needs: its **oplog**. A consumer that is offline longer than the oplog window loses its position and continues from now, which `backd` reports as a gap (`changefeed_reopens_total{reason="history_lost"}`). Size the oplog for your write volume and the longest outage you want to survive.

## What is planned

- **Subscriptions** (`POST /v1/{realm}/_realtime`), with per-subscriber rules and immediate revocation.
- **The rest of the trigger contract**: document bodies in the job (`fields`), who wrote (`by`), chain limits, ordered runs, `backd triggers status` and `rescan`.

Their [design](https://github.com/fernandezvara/backd/blob/main/design/triggers-and-realtime.md) is in the repository. Until they land, the feed has no subscribers and the only visible effects are the two settings, the `realtime:` key and the [metrics](../metrics/).
