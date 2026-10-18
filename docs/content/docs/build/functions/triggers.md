---
title: "Triggers"
description: "Run a function when documents change: the input, the identity it runs as, idempotent handlers, transitions, chains, ordering, cost and how to debug."
icon: "lightning"
weight: 559
toc: true
---

A **trigger** runs an [internal, async function](../internal/) when documents of a collection are created, updated or removed, whoever made the change and from wherever. It is declared in the collection's `collection.yaml` ([the keys](../../configuration/config-dir/#triggers)); this page is about writing the function and living with the result. A worked pipeline is [chapter 23](../../../tutorial/23-pipelines/) of the tutorial.

```yaml
triggers:
  on_paid:
    events: [updated]
    when: "before.status != 'paid' && document.status == 'paid'"
    function: main/on-paid
    fields: [total]
    session: true
```

## The input

```js
export default defineFunction("main/on-paid", async (ctx) => {
  const { id, event, doc, version, changed, before, after, by, depth, root, at, truncated } = ctx.input
})
```

`id` is the change's id (the same on every replay), `doc` is `{ id, version }`, `changed` the top-level fields that differ, `before`/`after` are present only for the paths in `fields`, `by` only with `session: true`, and `truncated` when the bodies were left out for size (over 256 KiB). The [config reference](../../configuration/config-dir/#triggers) has the full table; `fakeTrigger` in the [testing helpers](../testing/) builds it exactly.

## The function runs as itself

- `ctx.user` is `null`: an event has no caller to act as.
- `ctx.admin.db` is always there, as in a scheduled run, and **`admin: true` is not needed**: the realm declared the trigger, so its function is trusted with the realm's data. Treat a trigger function as the most privileged code of the realm; `backd triggers status` lists every trigger with its function and the note *runs with admin data access*, so the grant can be reviewed in one place.
- `secrets:` and `network:` remain the function's own declarations. `admin: true` in `function.yaml` still governs calling the same function over HTTP or with `ctx.call`.
- `by` is **information, never identity**: the user in `_meta.updated_by`. It is absent for API keys, functions, backd's own writes and an erased account's documents, and on a removal it is the last writer, not necessarily whoever deleted. Grant nothing because of it.

## Write it to be run twice

A change is delivered **at least once**: after a restart or a handoff between instances the same change can be read again. The job's id is made from the change, so the queue collapses the repeats it sees, but a job can still fail after a side effect and be retried. So:

- Deduplicate on `ctx.input.id` where the effect is outside the database (an email, a webhook): record it in a document with that id as its key.
- Read where the document stands *now* before acting, as [`publish-submission`](../../../tutorial/23-pipelines/) does, and do nothing already done.
- An error raised on purpose with `ctx.error` is final; any other error retries when the function declares [`retry:`](../jobs/), and is final otherwise.

## Transitions, not states

`when` sees `document`, `before`, `changed()`, `now` and `appConfig(...)`, never `user` or `data`. Write the **transition**: `before.status != 'paid' && document.status == 'paid'` fires once when the value is reached; writing the same value again fires nothing, and the function's own write can't loop through it. A removal has no `document`, so a condition that names it never matches one: read `before`.

## Chains and loops

A trigger's function may write documents that fire other triggers: that is how a pipeline is built and how a bug becomes a loop. Every write a trigger run makes, directly or through `ctx.call`, is stamped `_meta.chain = { root, depth }`; the events it causes carry `root` and `depth + 1`. The stamp can't be set by a client or dropped by a function.

- A chained event deeper than `triggers.max_chain` (16 by default) is not queued, and a trigger's chained events are limited to `max_chained_per_minute` (60). Both are logged and counted (`trigger_dropped_total{reason}`).
- Events nobody chained are never dropped for being many: importing 10,000 documents runs 10,000 jobs. The only ceiling is `max_backlog` per realm, past which events are recorded as [gaps](../../../operate/change-feed/#seeing-and-repairing-what-the-dispatcher-did).

## Ordering

Without `ordered: true`, a document changed three times quickly can have its three runs at once and in any order: compare `version` with the document's current one. With it, one document's runs go one at a time in the order of the changes; other documents are not held back, and a document that changes faster than its function runs builds a queue of its own (`jobs_serialized_waiting`).

## What it costs

Triggers are paid per **change to a collection that declares one**, not per subscriber:

| Cost | Where it falls |
|---|---|
| Pre-change images | Every update or delete of a triggered collection keeps the previous document in MongoDB for as long as the change history is kept: storage and some write time, noticeable with large documents that change often. Measured numbers are on [the change feed page](../../../operate/change-feed/#what-it-costs). |
| `when` | Evaluated once per change by the dispatcher, in memory, before any job is queued. A change that does not match costs only that evaluation. Narrow `events` and keep conditions simple. |
| `fields` | The job input size. Without it the job carries ids only; `["*"]` copies whole documents into every job, kept as long as invocation records (`functions.log_retention`, 7 days). Prefer a few paths, or read the document in the function. |
| The stream | One reader per deployment reads the whole deployment's history, so its cost follows the cluster's total write rate, and all trigger and realtime consumers share one resume window (the oplog). |
| Hot documents | `ordered: true` trades throughput on one document for order. |

## Debug a trigger

```sh
backd functions history --function main/on-paid      # runs: origin trigger:<db>/<collection>/<trigger>, event_id, result
backd triggers status                                # lag, backlog, open gaps, the lease
backd triggers gaps                                  # what could not be queued, and why
```

Trigger runs are ordinary invocations: `origin` is `trigger:<db>/<collection>/<trigger>`, they expire with `functions.log_retention`, and `POST /_admin/functions/{db}/{name}/invoke` runs the function by hand with an input you supply (it does not fire the trigger).

## The built-in hooks are not triggers

`account.on_signup` and the `organizations.hooks.*` of `realm.yaml` are separate paths with their own inputs, and they stay on the write path: their inputs (the provider's profile, the invitation, the computed role change) cannot be rebuilt from a change. What changed is that they are exact: a membership or team change and its hook are **one MongoDB transaction**, and the hook job has an id made of the event, so a retry is one job. `on_signup` is queued right after the account exists and retried; a crash between creating an account and queuing it can lose that one call, so reconcile from the users list if you depend on it. No configuration changed and every field of the inputs is the same. Users can't declare triggers on backd's own collections.

After restoring MongoDB from a backup the dispatcher's stored position is no longer valid: a gap is recorded and it continues from now. Run `backd triggers rescan` for the interval if it matters.

{{< hint style="note" >}}
Triggers add no routes of their own beyond the [admin routes](../../../operate/change-feed/#seeing-and-repairing-what-the-dispatcher-did) for status and gaps, and they need a realm with authentication enabled and a running [worker](../../../operate/functions-runtime/) with `BACKD_EXECUTOR_URL`, like any async job.
{{< /hint >}}
