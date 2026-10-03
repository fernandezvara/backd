---
title: "Async jobs"
description: "Run work in the background with mode: async, and know when it finished: polling, the JavaScript client, the CLI, and completion patterns inside the function."
icon: "receipt_long"
weight: 555
toc: true
---

`mode: async` runs a function in the background instead of within the request. The call answers `202` at once with a **job**, and a [worker](../running/#running-the-worker) runs the function later, on its own schedule and for as long as its `timeout` allows.

Use it when the work:

- takes more than a few seconds (a `sync` call is capped at 60 s, and a caller shouldn't be made to hold a request open for anything close to that);
- must survive the caller going away (a browser tab closed, a mobile app suspended);
- can be retried safely if something goes wrong;
- fans out (a batch of emails, a report over many documents, a slow third-party call).

Use a `sync` function when the caller needs the answer to continue and it's quick. Use [cron](../cron/) when nobody asks for the work: it just has to happen on a schedule.

The cookbook's `export_orders` builds a CSV report of the caller's orders. As the caller it can read only their orders, and it writes the report as a document they own:

{{< example-file path="workshop/main/_functions/export_orders/function.yaml" >}}

{{< example-file path="workshop/main/_functions/export_orders/index.ts" >}}

## Starting a job

```sh
curl -X POST https://api.example.com/v1/workshop/main/_func/export_orders \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}'
# → 202 {"id": "d3c9ljp8hc2g00b6s1m0", "function": "main/export_orders", "status": "queued",
#         "created_at": "2026-09-30T09:12:41.002Z", "result": null}
# Location: /v1/workshop/main/_jobs/d3c9ljp8hc2g00b6s1m0
```

The call only enqueues the job: `400`, `401`, `403` and `429` (a [rate limit](../calling/#rate-limits)) still apply at that point, exactly as for `sync`, and the function never runs inline. Keep the `id` (or the `Location` header): it is how you find out what happened.

{{< hint warning >}}
A job only runs while a [worker](../running/#running-the-worker) is running (`backd worker`, or `backd serve --with-worker`). Without one it stays `queued` forever, and nothing else tells you: check for it in every environment, including local development.
{{< /hint >}}

## The life of a job

```
   POST _func/…          worker claims it             the function ends
 ───────────────▶ queued ───────────────▶ running ───────────────────▶ done
                                              │                          (result: ok, function_error, timeout, …)
                                              └─ worker lost, executor down or busy: the lease expires
                                                 (timeout + 30 s) and another claim retries it: `attempts` grows
```

| Status | Means |
|---|---|
| `queued` | Stored, not yet claimed. If it stays here, no worker is running (see [below](#when-a-job-doesnt-finish)). A job whose attempt failed and is waiting to be [retried](#retrying-a-failed-job) is `queued` too, with `next_attempt_at` set |
| `running` | A worker claimed it. It holds a *lease* for the function's `timeout` plus 30 seconds; if the worker dies, or the executor is unreachable or busy, the lease simply expires and the job is claimed again |
| `done` | The function ended, one way or another. Look at `result` to see how |

## Retrying a failed job

By default a job runs once: if the function throws, times out or crashes, the job ends as `done` with that result. Add `retry` to `function.yaml` and `backd` tries again after a growing wait:

```yaml
mode: async
retry:
  attempts: 5      # total attempts, the first included (default: 1, no retry; at most 20)
  backoff: 1m      # the wait after the first failure; it doubles after each one (default 1m)
  max_backoff: 1h  # the longest single wait (default 1h)
```

With these values the waits are 1m, 2m, 4m and 8m. `retry` works for every `async` function, including [scheduled](../cron/) runs, and needs `mode: async`; the waits of one job may add up to at most 24 hours.

- **What is retried:** an attempt that threw, ran out of time or memory, or whose process died. **What isn't:** an error the function chose with `ctx.error(...)` (it is an answer, not a fault) and an output over `max_output` (it would only be over it again). Those end the job at once.
- **A retry is the same job**: same id, same input. Each attempt is its own record in the [invocation history](../logs/), all with the job's `job_id`. While a retry is waiting, the job is `queued` with `attempts` so far and `next_attempt_at`.
- **After the last attempt** the job is `done` with the last attempt's result: look at `result.status` (it isn't `ok`), as for any failed job. The log of the worker names the attempts used up.
- **A lost worker is not a failed attempt.** If a worker dies mid-run, the lease expires and the job is claimed again without using up one of the `retry` attempts.

A retry runs the function again, so it must be safe to repeat ([below](#designing-jobs-that-are-safe-to-repeat)): the first attempt may have done part of its work before it failed.

## Knowing when a job finished

`backd` doesn't push a notification when a job ends; you ask, or the function tells you. Pick what fits:

### Poll the job

`GET /v1/{realm}/{database}/_jobs/{id}` answers with the job's `status` and, once `done`, its `result`:

```sh
curl https://api.example.com/v1/workshop/main/_jobs/d3c9ljp8hc2g00b6s1m0 -H "Authorization: Bearer $TOKEN"
# → 200 {"id": "…", "function": "main/export_orders", "status": "done", "created_at": "…",
#         "result": {"status": "ok", "output": {"report_id": "d3c9lpr8hc2g00b6s1n0", "rows": 42, "reused": false}, "duration_ms": 118}}
```

A shell loop that waits for it:

```sh
until [ "$(curl -s "$API/v1/workshop/main/_jobs/$ID" -H "Authorization: Bearer $TOKEN" | jq -r .status)" = done ]; do
  sleep 2
done
curl -s "$API/v1/workshop/main/_jobs/$ID" -H "Authorization: Bearer $TOKEN" | jq .result
```

Poll every second or two, with a limit on how long you are willing to wait. The `result` has the same shape a `sync` call's answer would have:

| `result.status` | Meaning | Other fields |
|---|---|---|
| `ok` | The function returned | `output` (whatever it returned), `duration_ms` |
| `function_error` | It threw `ctx.error(...)` | `http_status`, `code`, `message`, `details` |
| `timeout` | It didn't finish within its `timeout`; it was stopped | `http_status: 504`, `code: function_timeout` |
| `output_too_large` | Its output exceeds `max_output` | `code: invalid_output` |
| anything else (`memory`, `cpu`, `crash`, …) | It failed | `http_status: 500`, `code: function_failed`; the stack trace is in `backd`'s log |

### Wait in the JavaScript client

```js
import { JobTimeoutError } from 'backd-js'

const job = await backd.db('main').fn('export_orders', {})   // returns at once with a Job handle
await job.status()                                           // 'queued', 'running' or 'done'

try {
  const out = await job.wait({ pollIntervalMs: 1000, timeoutMs: 60_000 })
  console.log(out.report_id, out.rows)
} catch (err) {
  if (err instanceof JobTimeoutError) {
    // not finished yet: it keeps running; check again later with job.wait() or job.status()
  } else {
    throw err   // the job itself failed: the same error a direct call would have thrown
  }
}
```

`wait()` resolves with the output when the job succeeds, and **throws** when it ends any other way, so it reads like a `sync` call. See [the client](../../clients/js/#functions).

### List jobs (administrators)

To see *many* jobs at once, or a job you didn't start, an administrator uses the admin API, `backd functions jobs`, or `client.admin.jobs.list()`:

```sh
backd functions jobs --function workshop/main/export_orders --status done --since 24h
# CREATED                   FUNCTION            STATUS  RESULT  CODE  DURATION  TRIES  NEXT ATTEMPT  SCHEDULED  ID
# 2026-09-30T09:12:41.002Z  main/export_orders  done    ok      -     118ms     1      -             false      d3c9ljp8hc2g00b6s1m0

backd functions jobs --realm workshop --status running    # what is being worked on right now, in every function
```

It shows each job's state, how many times a worker started it (`TRIES` above 1 means a worker was lost, the executor was unavailable, or the function is being [retried](#retrying-a-failed-job); `NEXT ATTEMPT` says when) and how it ended, but never its input or output. It needs an admin API key or a session of a user with an admin role ([the admin API](../../auth/admin/)).

### Have the function say so

{{< hint style="tip" title="Best practice" >}}
For anything user-facing, don't make every client poll a job endpoint: let the job leave its result where the app already looks.
{{< /hint >}}

- **Write the result to a collection.** `export_orders` creates a `reports` document; the app lists `reports` (or reads one by id) with its normal access rules, and a report *existing* means the job finished. Add a `status` field (`working`, `done`, `failed`) if the app should also show progress.
- **Call a URL.** A last step that calls your own service, or a chat or push service, with the job's outcome. Declare the host under [`network`](../network/) and any key under [`secrets`](../secrets/):

  ```yaml
  network: [hooks.example.com]
  secrets: [HOOK_TOKEN]
  ```

  ```ts
  // the job's last step: tell someone. A failed notification must not lose the result, so catch it.
  try {
    await fetch("https://hooks.example.com/job-finished", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${ctx.secrets.HOOK_TOKEN}` },
      body: JSON.stringify({ report_id: report.id, requested_by: ctx.user!.email }),
    });
  } catch (err) {
    console.error("notification failed", String(err));
  }
  ```

  A job runs **at least once**, so the receiver may be told twice: make it ignore a repeat.
- **Send an email** the same way, through your mail provider's API.

## When a job doesn't finish

| You see | Most likely | What to do |
|---|---|---|
| `status: "queued"` for longer than a moment | No worker is running, or it can't reach the executor | Run `backd worker`, or `backd serve --with-worker` (needs `BACKD_EXECUTOR_URL`); check its log |
| `status: "running"` for longer than `timeout` + 30 s | A worker was lost, or the executor is down or busy: the lease has to expire before a retry | Wait for the lease; look at `backd functions jobs` (`TRIES`) and the executor's health |
| `done`, but `result.status` isn't `ok` | The function failed | `backd functions logs --function … --request-id …`; the [history](../logs/) shows the status and code |
| `404` from `_jobs/{id}` | It isn't yours (only the caller who enqueued a job, an API key, or a user with an admin role can read it), the id is wrong, the database in the URL is wrong, or it expired | Use the `Location` header exactly; check [retention](#storage-and-retention) |
| Two runs of the same job in the log | At-least-once delivery after a lost worker | Make the function safe to repeat, as below |

## Designing jobs that are safe to repeat

{{< hint warning >}}
A job is delivered **at least once, not exactly once**. A worker leases a job for its `timeout` plus a margin; if the worker dies mid-run, the lease expires and another worker (or the same one, restarted) runs it again. So a function can, rarely, run twice for the same job, after its own side effects already happened but before its result was stored.
{{< /hint >}}

Write functions so a second run does no harm:

- **Derive a key from the job** and check it first. `export_orders` uses the request id of the call that queued the job, which is the same on every attempt: the second attempt finds the report the first wrote and returns it (`"reused": true`) instead of writing another.
- **Use natural keys and unique indexes** (a `day`, an `order_id`) so a repeated insert fails cleanly.
- **Send an idempotency key to the outside service** for external side effects (a charge, an email), for example Stripe's `Idempotency-Key`.
- `backd`'s own [`Idempotency-Key`](../calling/#idempotency) on the enqueue request protects against a different problem: a *client's* retry queuing a second job. Send the same key again and you get the first job back. It can't help a job that is already running when a worker is lost.

## Storage and retention

{{< hint warning >}}
A job's **input and result are stored** in MongoDB until the job expires. Never put a secret in a job's input; declare it as a [secret](../secrets/) and read `ctx.secrets`.
{{< /hint >}}

- **Jobs are stored** in the realm's system database: one of two deliberate exceptions to "`backd` never stores a function's input or output" (the other is idempotency), because there is nowhere else to keep them until you read them.
- **Only the caller who enqueued a job, any API key, or a user holding an admin role can read it back.** Anonymous callers get `401`; anyone else gets `404`, the same as an unknown id, so a job's existence isn't revealed. A [scheduled](../cron/) run has no caller, so only API keys read it.
- **Retention:** a finished job is kept for `functions.job_retention` in `realm.yaml` (default 24 hours, minimum 1 hour). A job that never finishes is removed after 48 hours regardless. Async functions need `auth: enabled`, like `secrets` and `rate_limit`.
- **Limits:** `max_output` is capped at 15 MiB for async functions (a result is one MongoDB document); `timeout` up to 24 hours (15 minutes by default).
- **Concurrency:** a function's `concurrency` and `functions.max_concurrency` don't apply to jobs (they protect `serve`'s own executor calls). `WORKER_CONCURRENCY` (default 10 per worker process) and the executor's `EXECUTOR_MAX_PROCESSES` bound async load: a busy executor just leaves the job queued to be retried.
- **Secrets and the bundle** are resolved when the job actually runs, not when it was queued, so a job that waited always sees the current values.
- Every run (its result too) is recorded in the [invocation history](../logs/) with `mode: "async"` and the job's id.
