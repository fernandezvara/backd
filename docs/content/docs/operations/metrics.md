---
title: "Metrics"
description: "Prometheus metrics from every backd process on a private port: how to turn them on, what they hold, and why the port must never be public."
icon: "monitoring"
weight: 415
toc: true
---

Every `backd` process can tell [Prometheus](https://prometheus.io/) how it is doing: requests, errors, database latency and, as the next steps land, functions, jobs and email. It is **off by default**.

## Turning it on

| Variable | Default | Meaning |
|---|---|---|
| `METRICS_ADDR` | empty (off) | Where this process serves `/metrics`, for example `:9090`. A listener of its own: it can't be `HTTP_ADDR` or `BACKD_INTERNAL_ADDR`. |
| `METRICS_TOKEN` | empty | At least 32 characters. When set, every request needs `Authorization: Bearer <token>`. Needs `METRICS_ADDR`. |

`backd serve`, `backd worker`, `backd executor` and `backd egress` each serve their own `/metrics` (the executor and egress read the same two variables); a process that runs both (`serve --with-worker`) serves one. The listener answers `GET /metrics` and nothing else, and it is a separate server: whatever happens to it never touches the API.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: backd
    static_configs:
      - targets: ["backd:9090"]
    authorization:            # only with METRICS_TOKEN
      credentials: "<the token>"
```

## The port must be private

The metrics hold no user ids, emails, addresses, tokens, key names, documents or request paths: labels come from fixed lists and from your configuration (route patterns, realm, database and function names). Still, they tell an attacker which realms and routes exist, how busy and how healthy the service is, and when it is slow. **Keep the port on a private network**: never publish it, never route it through the public proxy (the [production reference](../production/) answers `404` for `/metrics` at the edge, and its test checks it), and set `METRICS_TOKEN` when anything but Prometheus can reach the network it is on.

## What is measured

All names start with `backd_`; durations are in seconds.

| Metric | Labels | Meaning |
|---|---|---|
| `http_requests_total` | `method`, `route`, `status` | Requests answered. `route` is the router's pattern (`/v1/{realm}/{database}/{collection}`), never the real path; a request no route matched is `unmatched`. A method other than the common ones is `OTHER`. |
| `http_request_duration_seconds` | `method`, `route` | Time to answer (histogram). |
| `http_in_flight_requests` | | Requests being answered now. |
| `http_refusals_total` | `status` | Answers `401`, `403`, `429` and `503`. |
| `mongodb_operation_duration_seconds` | `operation`, `outcome` | Time MongoDB took to run each command (histogram). `operation` is the command (`find`, `insert`, `update`, `aggregate`, …, or `other`); `outcome` is `ok`, `error` or `timeout`. Measured by the driver for every database call, whatever part of backd made it. |
| `mongodb_up` | | `1` while the last check (every 15 seconds) of MongoDB worked, `0` when it failed. |
| `sessions_created_total` | `realm` | Sessions started by sign-up and login. |
| `sessions_ended_total` | `realm`, `reason` | Sessions ended on purpose: `logout`, `logout_all`, `revoked`, `password_changed`, `password_reset`, `password_set`, `email_changed`, `email_reverted`, `disabled`, `deactivated`, `erased` or `deleted`. Sessions that expire on their own are not counted. |
| `rate_limited_total` | `realm`, `scope` | Times a limit stopped someone: `login` (the throttle), `function` (a function's `rate_limit`) and `email_function`, `email_invocation`, `email_ip`, `email_recipient` (the [email limits](../../functions/email/)). |
| `function_invocations_total` | `realm`, `function`, `mode`, `status` | Function runs that ended, sync and async, with the executor's status (`ok`, `error`, `timeout`, …). |
| `function_duration_seconds` | `realm`, `function` | How long they took (histogram). |
| `function_refusals_total` | `realm`, `function`, `reason` | Calls refused before running: `rate_limit`, `concurrency`, `idempotency_conflict`, `idempotency_reused`. |
| `function_idempotent_replays_total` | `realm`, `function` | Calls answered from a stored result because their `Idempotency-Key` was used before. |
| `executor_errors_total` | `kind` | Calls that failed because the executor couldn't be used (`unavailable`). |
| `jobs` | `realm`, `kind`, `state` | Jobs not done yet: `kind` is `function`, `schedule`, `email` or `erase`; `state` is `queued` (a worker can claim it), `running` (a worker holds its lease) or `waiting` (for a retry's delay). Refreshed every 15 seconds in the background, never when you scrape. |
| `jobs_oldest_wait_seconds` | `realm`, `kind` | Age of the longest-waiting queued job: the number to alert on when workers fall behind. |
| `jobs_refreshed_timestamp_seconds` | | When the job gauges were last refreshed; `0` means never. |
| `jobs_completed_total` | `realm`, `kind`, `status` | Jobs a worker finished. Email and erase outcomes are here: `kind="email"` and `kind="erase"`, `status="ok"` or the failure's status. |
| `jobs_retried_total` | `realm`, `kind` | Failed attempts queued again. |
| `jobs_expired_leases_total` | `realm`, `kind` | Jobs claimed again because the worker holding them stopped answering. |
| `erase_needs_attention` | `realm` | Erase jobs that used up their attempts and need an administrator (repeat the delete to resume). |
| `metrics_refresh_errors_total` | | Times refreshing the job gauges failed; the last values stay. |
| `executor_runs_total` | `status` | The executor's runs by status (`busy`: refused at its process limit). Served by `backd executor`. |
| `executor_run_duration_seconds` | | How long they took (histogram). |
| `executor_running` | | Function processes running now. |
| `egress_requests_total` | `outcome` | Requests to the egress proxy: `allowed`, `denied_credentials`, `denied_host` or `denied_address`. Host names are never labels. Served by `backd egress`. |
| `build_info` | `version`, `commit` | Always `1`. |

The Go runtime (`go_*`) and process (`process_*`) metrics are included.
