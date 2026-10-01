---
title: "Running in production"
description: "The executor, workers and egress: how to run them."
icon: "monitor_heart"
weight: 563
toc: true
---

## What runs where

Functions add up to four kinds of process. All are the same `backd` binary (the executor's image adds Deno), each in its own role:

| Process | Command | Needed for | How many |
|---|---|---|---|
| API | `backd serve` | calling functions over HTTP | as many as you like, all with the same config |
| Executor | `backd executor` | running any function | one or more, reachable only from `backd` and workers |
| Worker | `backd worker`, or `backd serve --with-worker` | [async jobs](../jobs/) and [cron schedules](../cron/) | one or more; each job and each scheduled run is done by exactly one |
| Egress | `backd egress` | functions that declare [`network`](../network/) | one or more; the executor's only way out |

{{< hint note >}}
**`backd executor` runs only on Linux**: it enforces each function's CPU time, memory and process group with Linux features, and on Windows or macOS it refuses to start and says so, rather than run functions without limits. Use the `backd-executor` container image (or WSL2 on Windows). `serve`, `worker`, `egress` and the command line run on any system, pointing `BACKD_EXECUTOR_URL` at an executor.
{{< /hint >}}

The smallest useful deployment is `backd serve --with-worker` plus an executor (add egress once a function declares `network`). The [production reference](../../operations/production/#functions-executor-egress-and-worker) runs them as separate services with the executor on a network of its own, and the project template's `compose.yaml` runs the same set for local development.

## Sizing

- **Executor:** `EXECUTOR_MAX_PROCESSES` functions run at once (default 64), each using its `memory` plus about 130 MiB; beyond that calls answer `503` (a busy executor leaves async jobs queued for retry). Set the container's memory limit for that many.
- **Workers:** `WORKER_CONCURRENCY` jobs per worker process (default 10), and the executor's process limit above bounds the total. Long jobs hold a slot for their whole run: size for the jobs that run at the same time, not the ones per day.
- **`backd`:** each function's `concurrency` (default 10) and the realm's `functions.max_concurrency` cap `sync` calls per instance; per-caller [`rate_limit`](../calling/#rate-limits) is shared across instances.

## What to watch

- **Startup warnings** name functions that declare a secret with no value, functions callable anonymously with no `rate_limit`, scheduled functions when `serve` runs without `--with-worker`, and an executor that isn't configured or doesn't answer.
- **`"msg":"function called"` log lines** carry each call's function, status and duration: alert on the rate of statuses other than `ok`.
- **Queue health:** `backd functions jobs --realm <realm> --status queued` (or the admin API) that keeps growing, or an old `queued` job, means no worker is keeping up.
- **Schedules:** the newest run of every scheduled function is recent and `ok` (see [Checking that a run happened](../cron/#checking-that-a-run-happened-and-finished)).
- **Retention:** `functions.log_retention` and `functions.job_retention` in `realm.yaml` decide how long history, logs and finished jobs are kept.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `503 unavailable` on every function call | The executor isn't configured or isn't reachable | Set `BACKD_EXECUTOR_URL` and `BACKD_EXECUTOR_TOKEN` (the same on both sides); check the network between them |
| `503` in bursts, `Retry-After: 1` | The executor is at `EXECUTOR_MAX_PROCESSES`, or a `webhook` function is at its concurrency limit | Raise the limit or add executors; make slow work `async` |
| `429 too_many_requests` | A function's `concurrency` on that instance, or the caller's `rate_limit` | Retry after `Retry-After`; tune the limits |
| `500 secret_missing` | The function declares a secret that isn't set | `backd secret set --realm … --name …` (`--database …` for a database secret) |
| `500 function_failed` | The function threw something other than `ctx.error`, crashed, or exceeded memory | `backd functions logs`; catch client errors and use [`relay`](../writing/#errors) |
| `504 function_timeout` | It ran longer than its `timeout` | Raise `timeout` (up to 60 s), or make it `async` |
| `backd` refuses to start: functions are not built | Missing, stale or modified bundles | `backd functions build`, then redeploy ([Building](../testing/#building)) |
| `--with-worker requires BACKD_EXECUTOR_URL` | A worker needs the executor | Set `BACKD_EXECUTOR_URL`/`BACKD_EXECUTOR_TOKEN` |
| A job stays `queued` | No worker is running | [When a job doesn't finish](../jobs/#when-a-job-doesnt-finish) |
| A scheduled run is missing | No worker was up within five minutes of its time | [Scheduled functions](../cron/#how-runs-are-created) |
| Calls to an outside host fail | The host isn't in `network`, or `backd egress` isn't reachable | [Outbound network](../network/) |

## Running the executor

Functions run in the **executor**, `backd executor`: a supervisor that starts one Deno process per call and stops it at its deadline. It runs in its own container, from the `backd-executor` image (the official Deno image plus `backd`; build it from `docker/executor/Dockerfile`). It never reads `CONFIG_DIR` or MongoDB: it fetches each function's bundle from `backd` by its hash, and functions reach data only through `backd`.

`backd` talks to the executor, and the executor and functions talk back to `backd` on an **internal listener** that must never be published:

{{< hint danger >}}
The executor and the internal listener must **never be published** to the internet, or reachable from anything but `backd`, the workers and `backd egress`. The executor runs whatever it is given; its token and network placement are what stop others from using it.
{{< /hint >}}

| Variable | On | Meaning |
|---|---|---|
| `BACKD_EXECUTOR_URL` | `backd serve` | Where the executor listens, e.g. `http://executor:9100`. Without it, calls to functions answer `503` (startup warns) |
| `BACKD_EXECUTOR_TOKEN` | both | A shared secret, 32 characters or more: `backd` calls the executor with it, and the executor fetches bundles with it |
| `BACKD_INTERNAL_ADDR` | `backd serve` | The internal listener's address (default `:8081`), served when functions exist and the executor is configured |
| `BACKD_CALLBACK_URL` | `backd serve` | The internal listener as the executor and functions reach it, e.g. `http://backd:8081` |
| `BACKD_URL` | `backd serve`, `backd worker` | `backd`'s public address, e.g. `https://api.example.com`: the base of the links in [emails](../email/). A realm's `email.public_url` overrides it; startup fails when a realm sends email and neither is set |
| `BACKD_CALLBACK_KEY` | `backd serve` | A secret, 32 characters or more, signing the short-lived credentials functions call back with. Every `backd` instance of a deployment needs the same one; the executor never gets it |
| `EXECUTOR_ADDR` | executor | Listen address (default `:9100`) |
| `EXECUTOR_DIR` | executor | Writable directory for the bundle cache (the image uses `/tmp/backd-executor`: mount a tmpfs) |
| `EXECUTOR_MAX_PROCESSES` | executor | Functions running at once (default 64); beyond it, calls answer `503` |
| `EXECUTOR_PROXY` | executor | `backd egress`'s base URL, e.g. `http://egress:3128`. Without it, function processes get no outbound network beyond `--allow-net` on this host |
| `BACKD_EGRESS_KEY` | executor, egress | A shared secret, 32 characters or more, required with `EXECUTOR_PROXY`: the executor signs each invocation's egress credentials with it, egress verifies them |
| `EGRESS_ADDR` | egress | Listen address (default `:3128`) |
| `EGRESS_ALLOW_PRIVATE` | egress | Comma-separated `host:port` exceptions to the private-address block; only `backd`'s internal listener belongs here |
| `WORKER_CONCURRENCY` | `backd worker`, `backd serve --with-worker` | [Async jobs](../jobs/) one worker process runs at once (default 10) |

The internal listener serves only bundles (to the executor) and the data routes, for functions calling back with their short-lived credentials; sessions and API keys aren't accepted there, and those credentials aren't accepted on the public listener.

Run the executor as a non-root user, with a read-only root filesystem, no capabilities and a tmpfs for `EXECUTOR_DIR`. Size its memory limit for `EXECUTOR_MAX_PROCESSES` functions (each uses its `memory` plus about 130 MiB), and its process limit for about 5 per running function.

### Running the worker

**`backd worker`** claims and runs [async jobs](../jobs/) — same binary and image as `serve`, a different role. It needs `BACKD_EXECUTOR_URL` and `BACKD_EXECUTOR_TOKEN` (it calls the executor directly, the same way `serve` does for `sync` calls) and `BACKD_CALLBACK_URL`/`BACKD_CALLBACK_KEY` (functions it runs still reach data through `backd`'s API, via whichever instance's internal listener `BACKD_CALLBACK_URL` points at — the worker doesn't need to run one itself). It also needs `BACKD_SECRETS_KEY` if any async function declares secrets, and reads `CONFIG_DIR`/`MONGO_URI` like `serve`.

Run it as its own deployment (scale workers independently of API instances) with `backd worker`, or fold it into an API instance with `backd serve --with-worker` for a smaller deployment — both drive the same claim loop, so several workers (or several `--with-worker` instances) safely share one queue: each job is claimed by exactly one of them at a time.

Scheduled functions ([cron](../cron/)) run only while at least one worker is running.
