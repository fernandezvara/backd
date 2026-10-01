---
title: "Function logs"
description: "See what your deployed functions did: invocation history, console logs, retention, and shipping logs to a long-term system."
icon: "description"
weight: 563
toc: true
aliases: ["/docs/configuration/function-logs/"]
---

Every function call — `sync`, `webhook`, and once an [async](../jobs/) job finishes — is recorded in `<realm>___system.invocations`: what happened, not what was sent or returned. `backd` is a short debugging window, not a log archive — plan to ship logs elsewhere for anything you need to keep.

## What's recorded

- The function (`<database>/<name>`), who called it, its mode, status and duration.
- The function's own console output (`console.log`, `console.error`, …), already masked of any [declared secrets'](../secrets/) exact values.
- The request id, so a record can be matched to `backd`'s own JSON log line for the same call.
- **Never** the input or output. If you need to reproduce what a specific call did, log what matters yourself with `console.log` inside the function.

```json
{
  "id": "d3c9ljp8hc2g00b6s1m0",
  "at": "2026-09-29T12:00:03.500Z",
  "function": "orders/checkout",
  "actor": "user:d3c9lg38hc2g00b6s1lg",
  "mode": "sync",
  "status": "ok",
  "code": null,
  "duration_ms": 118,
  "request_id": "darf9mq5mh5g00aq9df0",
  "job_id": null,
  "logs": [{ "level": "log", "line": "charging card for 1250" }]
}
```

`status` is one of `ok`, `function_error`, `timeout`, `memory`, `cpu`, `crash`, `output_too_large`, `busy` or `bundle` — the same reasons a call can end that show up in `backd`'s own log and in the [error table](../calling/#calling-a-function). `code` is the function's own error code (`ctx.error`'s second argument) when `status` is `function_error`. For an [async](../jobs/) call, `mode` is `async` and `job_id` names the job; the record is written once the job finishes, not when it's queued.

## Retention: a debugging window, not a log archive

Records expire after `functions.log_retention` in `realm.yaml` (default **7 days**):

```yaml
functions:
  log_retention: 7d
```

- **Short retention** (the default) keeps `<realm>___system.invocations` small and treats it as what it is: a window for debugging what just happened, not a system of record. This needs `auth: enabled` — there's no system database to hold it otherwise, the same restriction [secrets](../secrets/) and [rate limits](../calling/#rate-limits) already have.
- **Long-term retention is not `backd`'s job.** The function's console output already goes to `backd`'s own JSON log output (`"msg":"function log"`, tagged with the request id, function and realm) at the moment it happens, independently of the database record — ship *that* to whatever log system you already run (a log aggregator, cloud logging, etc.) for anything you need to keep longer than a debugging window.
- **Choosing a value** is a sizing and privacy trade-off: longer retention means a bigger `invocations` collection (bounded by call volume × `functions.log_retention`, since each function log is capped at 64 KiB), and means function output sits in your database for longer — for functions whose logs could include anything sensitive, shorter is safer.

## Reading it

```sh
backd functions history --function shop/orders/checkout --since 24h
# TIME                      STATUS  CODE          DURATION  ACTOR         REQUEST-ID
# 2026-09-29T12:00:03.500Z  ok      -             118ms     user:d3c9...  darf9mq5...
# 2026-09-29T11:58:41.200Z  function_error  out_of_stock  9ms  user:a1b2...  dbrq2n80...

backd functions logs --function shop/orders/checkout --request-id darf9mq5mh5g00aq9df0
# 2026-09-29T12:00:03.500Z darf9mq5mh5g00aq9df0 [log] charging card for 1250
```

- `backd functions history --function <realm>/<database>/<name>` lists recent calls: status, error code, duration, actor, request id — never the logs themselves.
- `backd functions logs --function <realm>/<database>/<name>` prints the console output of recent calls; `--request-id` narrows to one specific call (the same id `backd functions invoke`, `backd`'s own log, and a caller's `X-Request-ID`/`request_id` in an error all share).
- Both accept `--since` (an RFC 3339 time, or a duration back from now such as `24h` or `7d`), `--limit` (default 50), and `--json` for one JSON object per line — pipe that to another tool, or into a log shipper for anything you want kept past `functions.log_retention`.
- Both use the realm's admin API (`GET /v1/{realm}/_admin/invocations`), the same as [`backd audit`](../../auth/audit/); see [Command-line administration](../../auth/cli/).
