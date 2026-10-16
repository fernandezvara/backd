---
title: "Change feed"
description: "How backd reads MongoDB's change stream for realtime and triggers: when it reads, what it never reads, what it costs and what to size."
icon: "bolt"
weight: 417
toc: true
---

Realtime subscriptions and function triggers are built on one piece: the **change feed**, which reads MongoDB's [change stream](https://www.mongodb.com/docs/manual/changeStreams/). [Realtime subscriptions](../../api/realtime/) and triggers are built on it.

## When it reads

| Setting | Default | Meaning |
|---|---|---|
| `BACKD_EVENTS` | `true` | This instance may read the change stream for realtime subscribers. `false` on API replicas keeps stream work off request-serving instances. |
| `BACKD_TRIGGERS` | `true` | This instance may hold the lease of the [trigger dispatcher](#the-trigger-dispatcher). Run `false` on instances that should only serve requests. |

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

- **Ordered triggers.** A trigger with `ordered: true` gives its jobs a key (the trigger and the document) and an order (the time of the change in the database's history, the same wherever it is read); a worker takes only the oldest unfinished job of a key. `jobs_serialized_waiting` shows how many wait behind another; a document that changes faster than its function runs builds a queue of its own. `backd provision` adds the index this needs (`serial_key`, `serial_order`) to each realm's `jobs`.

## Seeing and repairing what the dispatcher did

```sh
backd triggers status --realm shop          # lease, position age, triggers, backlog, open gaps
backd triggers gaps --realm shop [--all]    # what could not be turned into jobs
backd triggers resolve --realm shop --id <gap>
backd triggers rescan --realm shop --database main --collection orders --updated-since 6h
```

`status`, `gaps` and `resolve` use the realm's [admin API](../../auth/admin/) (`GET /_admin/triggers/status`, `GET /_admin/triggers/gaps`, `POST /_admin/triggers/gaps/{id}/resolve`) with the `functions` area, as the session of `backd login` or an admin API key. `status` lists every trigger with its function and the note **runs with admin data access**: a trigger's function always has `ctx.admin.db`, so that is the one place to review who holds it. The lease holder's name is not shown.

A **gap** says what the dispatcher could not do and for which collection and trigger. After you have dealt with it, `resolve` closes it (audited); an unresolved one stays in `status` and `trigger_gaps_total`.

**`rescan`** re-emits what a gap lost. It runs where you run the command, with `CONFIG_DIR` and `MONGO_URI` like [`backd provision`](../#provisioning), not through the API, because it may read a whole collection:

- It reads the documents of one collection written from `--updated-since` (an RFC 3339 time, or `6h`, `7d`) until `--until` (default now), and queues an `updated` event for each to the triggers that can be evaluated from a document alone.
- It is **not a replay**: a document changed twice in the range is read once, as it is now; deletions can't be found; a trigger whose condition needs `before` or `changed()`, or that does not listen to `updated`, is reported and skipped.
- Its jobs have ids of their own and `rescan` in their input, so handlers see them as new work: they must be idempotent anyway.
- Over 10,000 documents it asks for `--confirm`. Without an index starting with `_meta.updated_at`, reading the range is a scan of the collection: run it when that is acceptable.
- It is audited (`triggers.rescan`).

## Operating checklist

- The MongoDB user needs the [change stream privilege](#requirements); `backd provision` enables pre-change images and creates the jobs indexes, and instances in `verify` mode refuse to start without them.
- Triggers need a running [worker](../../functions/running/) and `BACKD_EXECUTOR_URL` like any async job.
- Alert on `trigger_gaps_total` (reasons `window`, `backlog`, `preimage_missing`), on `trigger_backlog` approaching `max_backlog`, and on `trigger_dispatch_lag_seconds`.
- After restoring MongoDB from a backup the stored position is invalid: a gap is recorded and the dispatcher continues from now. `backd triggers rescan` re-emits what matters.

## Realtime in production

Streams are long requests and the change feed reads the whole deployment's history, so a few choices matter:

- **Dedicated instances.** Set `BACKD_EVENTS=false` on instances that only serve requests and route `POST /v1/{realm}/_realtime` to instances with `BACKD_EVENTS=true`. A request to an instance with events off is answered `503 realtime_unavailable`. Each events-enabled instance holds one stream of its own, whatever the number of clients, so adding instances adds streams: a handful is enough.
- **The proxy.** A stream must not be buffered or timed out like a response: turn response buffering off for the route (`proxy_buffering off` in nginx; backd also sends `X-Accel-Buffering: no`) and set the idle/read timeout well above the heartbeat (`BACKD_REALTIME_HEARTBEAT`, 25 seconds by default). Streams that cross a load balancer reconnect, with a resume token any instance can verify.
- **Instance limits.** Every open stream costs a connection and a buffer (256 KiB at most): the defaults (`BACKD_REALTIME_MAX_STREAMS` 10,000 and `BACKD_REALTIME_MEMORY` 256 MiB for all buffers) are a ceiling, not a target; measure before raising them. Over the memory budget, the largest buffers are reset rather than the instance slowed.
- **The MongoDB role.** The stream is opened on the whole deployment: the user in `MONGO_URI` needs the [change stream privilege](#requirements) on every database, and startup says so if it lacks it.
- **The resume window.** A client that's away longer than MongoDB's oplog covers reloads instead of resuming ([sizing the history](#sizing-the-history)); the longer the oplog, the cheaper reconnections after a deploy or an outage.
- **What to watch:** `realtime_streams`, `realtime_resets_total` (by reason: `slow_consumer` and `preimage_missing` are the ones to chase), `realtime_subscription_checks_total{kind="scanned"}` and `changefeed_lag_seconds`; the [metrics page](../metrics/) lists them.

## Sizing the history

A stream that falls behind can only catch up while MongoDB still holds the history it needs: its **oplog**. A consumer that is offline longer than the oplog window loses its position and continues from now, which `backd` reports as a gap (`changefeed_reopens_total{reason="history_lost"}`). Size the oplog for your write volume and the longest outage you want to survive.
