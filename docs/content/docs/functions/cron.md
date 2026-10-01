---
title: "Scheduled functions"
description: "Run a function on a cron schedule, with no caller: when to use it, the expression syntax, what a run can do, and how to check that it ran and finished."
icon: "history"
weight: 556
toc: true
---

Add `schedule` to an `async` function and it runs by itself, with no caller: nightly cleanups, daily digests, hourly syncs, monthly reports.

{{< example-file path="workshop/main/_functions/nightly_cleanup/function.yaml" >}}

{{< example-file path="workshop/main/_functions/nightly_cleanup/index.ts" >}}

Each run is an ordinary [async job](../jobs/): created on schedule instead of by a request, run by a worker, stored, recorded in the [history](../logs/), and read back like any job. Everything on the [jobs page](../jobs/) applies, including at-least-once delivery.

## When to use cron

Use a scheduled function when the work must happen **because time passed**, whether or not anybody asks: expiring old data, aggregating a day's activity, syncing with another system, sending a reminder.

| Instead of cron, use… | When |
|---|---|
| An async job started by the client | A person or app action starts the work (an export button) |
| A `sync` function | The caller needs the answer now |
| A webhook | Another service tells you when something happened |
| An external scheduler (system cron, CI, a cloud scheduler) calling a function with an API key | You need per-timezone schedules, more than minute resolution, non-overlap guarantees, or a schedule that changes without redeploying |

## Schedule expressions

Five fields, `minute hour day-of-month month day-of-week`, in **UTC**:

| Field | Values | Also |
|---|---|---|
| minute | `0`–`59` | |
| hour | `0`–`23` | |
| day of month | `1`–`31` | |
| month | `1`–`12` | `jan`–`dec` |
| day of week | `0`–`6` (Sunday is `0`, and `7`) | `sun`–`sat` |

Each field takes `*`, a number, a range (`9-17`), a list (`1,15`) or a step (`*/15`, `9-17/2`). When both day fields are restricted a time matches if *either* does, as in classic cron. `@hourly`, `@daily`, `@weekly`, `@monthly` and `@yearly` are shortcuts. A wrong expression stops `backd` at startup and names the file.

| Schedule | Runs |
|---|---|
| `*/15 * * * *` | every 15 minutes |
| `@hourly` (`0 * * * *`) | at the start of every hour |
| `0 3 * * *` | every day at 03:00 UTC |
| `@daily` (`0 0 * * *`) | every day at 00:00 UTC |
| `30 6 * * mon-fri` | weekdays at 06:30 UTC |
| `0 9 1 * *` | on the 1st of the month at 09:00 UTC |
| `0 0 * * 0` | Sundays at 00:00 UTC |

{{< hint note >}}
Schedules are **always UTC**, with no time zones and no seconds. If you need 09:00 in Madrid, write the UTC hour that is right now, and revisit it when the clocks change.
{{< /hint >}}

## Another example: a daily digest

`daily_digest` writes one document per UTC day. It runs at 00:00 and summarizes the day before; an API key can also call it by hand for any day, which is how you backfill after an outage:

{{< example-file path="workshop/main/_functions/daily_digest/function.yaml" >}}

{{< example-file path="workshop/main/_functions/daily_digest/index.ts" >}}

```sh
# Rebuild the digest of one day by hand (an API key; the run acts as the function)
curl -X POST https://api.example.com/v1/workshop/main/_func/daily_digest \
  -H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' -d '{"day": "2026-09-28"}'
```

## What a scheduled run is

- **It acts as the function.** There is no caller: `ctx.input` is empty (`null`), `ctx.user` is `null`, `ctx.db` acts as an anonymous caller (which, subject to the rules, can do little), and **`ctx.admin.db` is always available**, with full access recorded as `func:<realm>/<database>/<name>`, whether or not `admin: true` is set. Review a scheduled function as if it declared `admin: true`. (A call made *by hand* needs `admin: true` to get `ctx.admin`, which is why `daily_digest` sets it.)
- **It is an async function.** `mode: async`, so `timeout` runs up to 24 hours (15 minutes by default) and the [async limits](../jobs/#storage-and-retention) apply. `schedule` needs `auth: enabled`.
- **Secrets and network work as usual**: declare what it needs.
- **It can still be called by hand** through its `invoke` rule, like any async function; that run has a caller and its own input. Administrators can also [run it by hand](../internal/#running-a-function-by-hand) as its schedule would run it.

## A worker must be running

{{< hint warning >}}
Runs go through the async queue, so schedules fire **only while at least one [worker](../running/#running-the-worker) is running**: `backd worker` as its own deployment, or `backd serve --with-worker` folded into an API instance. `serve` without `--with-worker` logs a reminder per scheduled function, because it can't know about a separate worker.
{{< /hint >}}

```yaml
# compose.yaml: a dedicated worker, next to the API (env as in the production reference)
  worker:
    image: ghcr.io/fernandezvara/backd:v0.3.0
    command: ["worker"]
    environment:
      CONFIG_DIR: /config
      MONGO_URI: mongodb://mongo:27017/?replicaSet=rs0
      BACKD_EXECUTOR_URL: http://executor:9100
      BACKD_EXECUTOR_TOKEN: ${BACKD_EXECUTOR_TOKEN}
      BACKD_CALLBACK_URL: http://backd:8081
      BACKD_CALLBACK_KEY: ${BACKD_CALLBACK_KEY}
      BACKD_SECRETS_KEY: ${BACKD_SECRETS_KEY}   # only if a function declares secrets
```

## How runs are created

- **Exactly one run per scheduled time, across any number of workers, with no leader.** Every worker checks the schedules every 15 seconds and tries to create the job of a due run. The job's id comes from the function and the scheduled minute, `cron_<database>_<function>_<yyyymmddhhmm>` (UTC), and the database accepts only one job with that id: one worker wins, the rest find it exists. Adding or losing workers needs no configuration.
- **Missed runs are skipped, not replayed.** A run is created only within 5 minutes after its scheduled time. If every worker was down for longer, that run doesn't happen. A run that *was* created but whose worker died is picked up by another when its lease expires, like any job.
- **Changing a schedule** means editing `function.yaml`, rebuilding and redeploying (the config fingerprint changes, so `PROVISION_MODE=verify` instances need the new config). To stop a schedule, remove the `schedule` key the same way. There is no runtime pause.

{{< hint warning >}}
**Runs can overlap.** Nothing stops the 03:00 run from still going at 03:05 (or 04:00, for an hourly schedule): each scheduled time is its own job. Keep `timeout` shorter than the interval, and make the function safe to run twice at once: work in small batches with a natural key (as `nightly_cleanup` deletes only what is already past its cutoff), or write a marker document first and skip if it exists.
{{< /hint >}}

## Checking that a run happened and finished

Every run is a job with a predictable id, so you can look at any of them:

```sh
# Recent runs of one function, newest first: state, tries and outcome
backd functions jobs --function workshop/main/nightly_cleanup --scheduled --since 7d
# CREATED                   FUNCTION              STATUS  RESULT  CODE  DURATION  TRIES  SCHEDULED  ID
# 2026-09-30T03:00:05.512Z  main/nightly_cleanup  done    ok      -     412ms     1      true       cron_main_nightly_cleanup_202609300300

# One run in full (output included), with an API key, from the id above
curl https://api.example.com/v1/workshop/main/_jobs/cron_main_nightly_cleanup_202609300300 -H "Authorization: Bearer $API_KEY"
# → {"status": "done", "result": {"status": "ok", "output": {"deleted": 3, "cutoff": "2026-08-31T03:00:05.210Z"}, "duration_ms": 412}, …}

# What it printed
backd functions logs --function workshop/main/nightly_cleanup --since 24h
# 2026-09-30T03:00:05.900Z cron [log] deleted 3 draft orders older than 30 days
```

- **A run that should have happened is missing** when no worker was up within five minutes of its scheduled time. Look for a gap in `backd functions jobs --scheduled`, and check the workers' logs.
- **A run is `queued`/`running` for too long**: same causes as any [job that doesn't finish](../jobs/#when-a-job-doesnt-finish).
- **A run failed**: `result.status` isn't `ok`, and the history and logs say why.
- The same list is available to programs: `GET /v1/{realm}/_admin/jobs?function=main/nightly_cleanup&scheduled=true` or `client.admin.jobs.list({ function: 'main/nightly_cleanup', scheduled: true })`.

`backd` has no built-in alerting. To be told when a schedule breaks, run a small check from wherever you already monitor things (an API key with the `admin` role) and alert when the newest run of a function is older than expected or isn't `ok`:

```js
const { items } = await backd.admin.jobs.list({ function: 'main/nightly_cleanup', scheduled: true, limit: 1 })
const last = items[0]
const stale = !last || Date.now() - Date.parse(last.created_at) > 26 * 3600_000
const failed = last?.status === 'done' && last.result.status !== 'ok'
if (stale || failed) alert(`nightly_cleanup: ${stale ? 'no recent run' : last.result.code}`)
```

## Testing a schedule

- **Call the function by hand** first: `backd functions invoke --function workshop/main/nightly_cleanup` (a call made by hand has a caller, so the function needs `admin: true` to get `ctx.admin`; it answers with a job to look at, as any async call does) exercises the code without waiting for the clock.
- **Use a fast schedule locally**: `schedule: "* * * * *"` in the [local stack](../quickstart/) (its `backd` runs a worker) shows a run every minute; put the real schedule back before you commit.
- **Unit-test the logic**: cron changes nothing about how the function's code is tested ([Build, dev and test](../testing/#unit-testing-a-functions-logic)).
